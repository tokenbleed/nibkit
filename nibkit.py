#!/usr/bin/env python3
"""nibkit - parse compiled iOS/macOS Interface Builder archives.

Reads the UINibEncoder "NIBArchive" binary format (magic "NIBArchive",
formatVersion 1, coderVersion 9-11+, i.e. everything iOS 6 through current
Xcode). Also walks .storyboardc / .nib bundle directories. Stdlib only.

Subcommands:
  dump     tree view of the archived object graph (default)
  json     the object graph as JSON (for jq / IDA / scripts)
  strings  all string values, class names, and keys (endpoint/secret mining)
  info     header + table counts only (cheap fingerprint)
"""
from __future__ import annotations

import json
import struct
import sys
from dataclasses import dataclass, field
from pathlib import Path

MAGIC = b"NIBArchive"
HEADER = struct.Struct("<10s10I")  # magic + 10 uint32 LE

# coder value types (UINibCoderValueType, per mothersruin Archaeology / leaf456 spec)
INT8, INT16, INT32, INT64, TRUE, FALSE, FLOAT, DOUBLE, DATA, NIL, OBJ = range(11)
TYPE_NAMES = {
    INT8: "int8", INT16: "int16", INT32: "int32", INT64: "int64",
    TRUE: "bool", FALSE: "bool", FLOAT: "float", DOUBLE: "double",
    DATA: "data", NIL: "nil", OBJ: "ref",
}

# keys whose DATA payload is a 1-byte tag + N little-endian doubles (CGPoint/CGSize/CGRect)
GEO = {"UIBounds": 4, "UIFrame": 4, "UICenter": 2, "UIOrigin": 2, "UISize": 2,
       "UIContentOffset": 2, "UIContentInset": 4, "UIScrollEdgeInsets": 4,
       "UIShadowOffset": 2, "UITitleShadowOffset": 2}
# keys whose DATA payload is a UTF-8 string body
STR_BYTES = {"NS.bytes", "NS.string", "UIProxiedObjectIdentifier"}


class _R:
    """Tiny little-endian cursor over a bytes buffer."""
    __slots__ = ("m", "p")

    def __init__(self, m: bytes, p: int = 0):
        self.m = m
        self.p = p

    def i8(self):
        v = struct.unpack_from("<b", self.m, self.p)[0]; self.p += 1; return v
    def u8(self):
        v = self.m[self.p]; self.p += 1; return v
    def i16(self):
        v = struct.unpack_from("<h", self.m, self.p)[0]; self.p += 2; return v
    def i32(self):
        v = struct.unpack_from("<i", self.m, self.p)[0]; self.p += 4; return v
    def i64(self):
        v = struct.unpack_from("<q", self.m, self.p)[0]; self.p += 8; return v
    def f32(self):
        v = struct.unpack_from("<f", self.m, self.p)[0]; self.p += 4; return v
    def f64(self):
        v = struct.unpack_from("<d", self.m, self.p)[0]; self.p += 8; return v
    def u32(self):
        v = struct.unpack_from("<I", self.m, self.p)[0]; self.p += 4; return v
    def take(self, n):
        v = self.m[self.p:self.p + n]; self.p += n; return v
    def vint(self):
        """VInt32: 7 bits/byte LE, high bit SET marks the terminal byte."""
        r = s = 0
        while True:
            b = self.m[self.p]; self.p += 1
            r |= (b & 0x7f) << s
            s += 7
            if b & 0x80:
                break
        return r


@dataclass
class NibArchive:
    major: int
    minor: int  # coderVersion
    keys: list = field(default_factory=list)
    classes: list = field(default_factory=list)   # [(name, extras)]
    objects: list = field(default_factory=list)   # [(class_idx, vstart, vcount)]
    values: list = field(default_factory=list)    # [(key_idx, type, payload)]


def parse_nibarchive(buf: bytes) -> NibArchive:
    if len(buf) < HEADER.size:
        raise ValueError("file too short to be a NIBArchive")
    (magic, major, minor, ocount, ooff, kcount, koff,
     vcount, voff, ccount, coff) = HEADER.unpack_from(buf, 0)
    if magic != MAGIC:
        raise ValueError(f"not a NIBArchive (magic={magic!r})")

    r = _R(buf)

    r.p = koff
    keys = [r.take(r.vint()).decode("utf-8", "replace") for _ in range(kcount)]

    r.p = coff
    classes = []
    for _ in range(ccount):
        ln = r.vint()
        nextra = r.vint()
        extras = [r.u32() for _ in range(nextra)]
        name = r.take(ln).decode("utf-8", "replace").rstrip("\x00")
        classes.append((name, extras))

    r.p = ooff
    objects = [(r.vint(), r.vint(), r.vint()) for _ in range(ocount)]

    r.p = voff
    values = []
    for _ in range(vcount):
        ki = r.vint()
        t = r.u8()
        if t == INT8:
            pl = r.i8()
        elif t == INT16:
            pl = r.i16()
        elif t == INT32:
            pl = r.i32()
        elif t == INT64:
            pl = r.i64()
        elif t == FLOAT:
            pl = r.f32()
        elif t == DOUBLE:
            pl = r.f64()
        elif t == TRUE:
            pl = True
        elif t == FALSE:
            pl = False
        elif t == NIL:
            pl = None
        elif t == DATA:
            pl = r.take(r.vint())
        elif t == OBJ:
            pl = r.u32()
        else:
            raise ValueError(f"unknown coder value type {t} at value #{len(values)}")
        values.append((ki, t, pl))

    return NibArchive(major, minor, keys, classes, objects, values)


def _decode(key: str, t: int, pl):
    """Render a single coder value into a JSON-friendly Python value."""
    if t == DATA:
        if key in GEO and len(pl) >= 1 + GEO[key] * 8:  # 1-byte tag + N doubles
            return list(struct.unpack_from(f"<{GEO[key]}d", pl, 1))
        if key in STR_BYTES:
            return pl.split(b"\x00")[0].decode("utf-8", "replace")
        return pl.hex()
    return pl


def _get_prop(node, name):
    """Look up a property by name in a list-props node."""
    for k, v in node["props"]:
        if k == name:
            return v
    return None


def build_graph(nib: NibArchive, idx: int, seen: set | None = None):
    """Resolve object[idx] into a nested dict tree, following OBJ refs.

    Props are an ordered list of [key, value] pairs because NIBArchive coder
    values are positional and may repeat a key (e.g. every NSArray element is
    keyed by UINibEncoderEmptyKey). A shared `seen` set expands each object once
    and collapses later refs to {"backref": idx}.
    """
    if seen is None:
        seen = set()
    if idx in seen:
        return {"backref": idx}
    seen.add(idx)
    cls_idx, vstart, vcount = nib.objects[idx]
    name = nib.classes[cls_idx][0] if cls_idx < len(nib.classes) else f"?cls{cls_idx}"
    node = {"idx": idx, "class": name, "props": []}
    for i in range(vstart, vstart + vcount):
        ki, t, pl = nib.values[i]
        key = nib.keys[ki] if ki < len(nib.keys) else f"?key{ki}"
        if t == OBJ:
            node["props"].append([key, build_graph(nib, pl, seen)])
        else:
            node["props"].append([key, _decode(key, t, pl)])
    return node


def collect_strings(nib: NibArchive):
    """Yield (source, value) for every string-shaped thing in the archive."""
    seen = set()
    for name, _ in nib.classes:
        if name and name not in seen:
            seen.add(name); yield ("class", name)
    for i, (ki, t, pl) in enumerate(nib.values):
        if t == DATA:
            key = nib.keys[ki] if ki < len(nib.keys) else ""
            if key in STR_BYTES:
                s = pl.split(b"\x00")[0].decode("utf-8", "replace")
                if s and s not in seen:
                    seen.add(s); yield ("value", s)
    for k in nib.keys:
        if k and k not in seen:
            seen.add(k); yield ("key", k)


# --------------------------------------------------------------------------- #
# connection graph: outlets + actions (the pentest gold)
# --------------------------------------------------------------------------- #
_EVENT_BITS = {
    1 << 0: "touchDown", 1 << 1: "touchDragInside", 1 << 2: "touchDragOutside",
    1 << 5: "touchCancel", 1 << 6: "touchUpInside", 1 << 7: "touchUpOutside",
    1 << 12: "valueChanged", 1 << 14: "primaryActionTriggered",
    1 << 16: "editingDidBegin", 1 << 17: "editingChanged",
    1 << 18: "editingDidEnd", 1 << 19: "editingDidEndOnExit",
}


def _event_name(mask: int) -> str:
    if mask in _EVENT_BITS:
        return _EVENT_BITS[mask]
    names = [n for b, n in _EVENT_BITS.items() if mask & b]
    return "|".join(names) if names else f"0x{mask:x}"


def connections(nib: NibArchive):
    """Yield dicts describing every outlet/action connection in the archive."""
    idx_class = {i: (nib.classes[c][0] if c < len(nib.classes) else "?")
                 for i, (c, _, _) in enumerate(nib.objects)}

    def props(idx):
        _, vs, vc = nib.objects[idx]
        out = {}
        for k in range(vs, vs + vc):
            ki, t, pl = nib.values[k]
            key = nib.keys[ki] if ki < len(nib.keys) else f"?{ki}"
            out[key] = (t, pl)
        return out

    def obj_string(idx):
        for k, (t, pl) in props(idx).items():
            if t == DATA and k in STR_BYTES:
                return pl.split(b"\x00")[0].decode("utf-8", "replace")
        return None

    def source_label(idx):
        # UIProxyObject -> use its proxied identifier (e.g. IBFilesOwner), else class name
        p = props(idx)
        if "UIProxiedObjectIdentifier" in p:
            t, pl = p["UIProxiedObjectIdentifier"]
            if t == OBJ:
                ident = obj_string(pl)
                if ident:
                    return f"{ident}({idx_class.get(idx, '?')})"
        return idx_class.get(idx, "?")

    for i, (c, _, _) in enumerate(nib.objects):
        name = idx_class[i]
        if name not in ("UIRuntimeOutletConnection", "UIRuntimeEventConnection"):
            continue
        p = props(i)
        sel = None
        if "UILabel" in p:
            t, pl = p["UILabel"]
            if t == OBJ:
                sel = obj_string(pl)
        src = dst = ev = None
        if "UISource" in p:
            t, pl = p["UISource"]
            if t == OBJ:
                src = source_label(pl)
        if "UIDestination" in p:
            t, pl = p["UIDestination"]
            if t == OBJ:
                dst = idx_class.get(pl, "?")
        rec = {"kind": "action" if name == "UIRuntimeEventConnection" else "outlet",
               "name": sel, "source": src, "destination": dst}
        if name == "UIRuntimeEventConnection" and "UIEventMask" in p:
            rec["event"] = _event_name(p["UIEventMask"][1])
        yield rec


# --------------------------------------------------------------------------- #
# discovery: turn a path into a list of (label, bytes) archives to parse
# --------------------------------------------------------------------------- #
def discover(path: Path):
    """Return [(label, bytes)] for every NIBArchive blob under path."""
    out = []
    if path.is_dir():
        if path.suffix in (".nib", ".storyboardc"):
            for nib in sorted(path.rglob("*.nib")):
                if nib.is_file():
                    out.extend(discover(nib))
            if not out:
                raise SystemExit(f"{path}: no .nib files inside bundle")
        else:
            raise SystemExit(f"{path}: pass a .nib / .storyboardc, not a directory")
    else:
        buf = path.read_bytes()
        if buf[:10] == MAGIC:
            out.append((path.name, buf))
        else:
            raise SystemExit(f"{path}: not a NIBArchive (magic mismatch)")
    return out


# --------------------------------------------------------------------------- #
# CLI
# --------------------------------------------------------------------------- #
def _py(v):
    if isinstance(v, float):
        return f"{v:g}"
    if isinstance(v, list):
        return "[" + ", ".join(_py(x) for x in v) + "]"
    return repr(v)


def _str_value(node):
    """Inline the text of an NSString node for compact labels."""
    if isinstance(node, dict) and node.get("class") == "NSString":
        b = _get_prop(node, "NS.bytes") or _get_prop(node, "NS.string")
        if isinstance(b, str):
            return b
    return None


def _print_tree(node, indent=0):
    pad = "  " * indent
    if isinstance(node, dict) and "backref" in node:
        print(f"{pad}-> backref #{node['backref']}")
        return
    extra = ""
    cl = _str_value(_get_prop(node, "UICustomClass")) or _str_value(_get_prop(node, "NSClassName"))
    if cl:
        extra = f"  <{cl}>"
    print(f"{pad}{node['class']} (#{node['idx']}){extra}")
    for key, val in node["props"]:
        if isinstance(val, dict) and "class" in val:
            inline = _str_value(val)
            label = f'{key}: "{inline}"' if inline is not None else key
            print(f"{pad}  {label}:")
            _print_tree(val, indent + 2)
        elif isinstance(val, dict) and "backref" in val:
            print(f"{pad}  {key}: -> backref #{val['backref']}")
        else:
            print(f"{pad}  {key} = {_py(val)}")


def main(argv=None):
    import argparse
    ap = argparse.ArgumentParser(prog="nibkit", description="parse compiled iOS/macOS NIB archives")
    sub = ap.add_subparsers(dest="cmd", required=True)
    for c in ("dump", "json", "strings", "info", "wiring"):
        sp = sub.add_parser(c, help=HELP[c])
        sp.add_argument("path", help=".nib file or .storyboardc directory")
    args = ap.parse_args(argv)

    blobs = discover(Path(args.path))
    rc = 0
    for label, buf in blobs:
        try:
            nib = parse_nibarchive(buf)
        except Exception as e:
            print(f"{label}: {e}", file=sys.stderr)
            rc = 1
            continue

        if len(blobs) > 1 and args.cmd != "strings":
            print(f"\n===== {label} (coderVersion {nib.minor}) =====", file=sys.stderr)

        if args.cmd == "info":
            print(f"file:            {label}")
            print(f"formatVersion:   {nib.major}")
            print(f"coderVersion:    {nib.minor}")
            print(f"objects:         {len(nib.objects)}")
            print(f"coder values:    {len(nib.values)}")
            print(f"keys:            {len(nib.keys)}")
            print(f"class names:     {len(nib.classes)}")
        elif args.cmd == "json":
            print(json.dumps(build_graph(nib, 0), indent=2, default=str))
        elif args.cmd == "strings":
            if len(blobs) > 1:
                print(f"# {label}")
            for src, s in collect_strings(nib):
                print(f"{src}\t{s}")
        elif args.cmd == "wiring":
            rows = list(connections(nib))
            if not rows:
                continue
            print(f"{'TYPE':7} {'SELECTOR / OUTLET':26} {'SOURCE':28} {'DESTINATION'}")
            for r in rows:
                ev = f"  [{r['event']}]" if "event" in r else ""
                print(f"{r['kind'].upper():7} {r['name'] or '?':26} {r['source'] or '?':28} {r['destination'] or '?'}{ev}")
        else:  # dump
            _print_tree(build_graph(nib, 0))
    return rc


HELP = {
    "dump": "tree view of the archived object graph",
    "json": "object graph as JSON",
    "strings": "all string values / class names / keys (endpoint + secret mining)",
    "info": "header + table counts only",
    "wiring": "outlet + action connections (selectors, sources, destinations)",
}


if __name__ == "__main__":
    raise SystemExit(main())
