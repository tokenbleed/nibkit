# nibkit

Parse compiled iOS/macOS Interface Builder archives (`.nib`, `.storyboardc`)
for reverse engineering and pentest. Reads the `UINibEncoder` "NIBArchive"
binary format (magic `NIBArchive`, formatVersion 1, coderVersion 9-11+) that
current Xcode produces for iOS apps. Single static binary, no dependencies,
pure Go.

## Why

Compiled nibs are opaque binary. The dominant free tool, `xibdump`, hard-pins
the old `coderVersion == 9` and throws on modern nibs (Xcode 13+ ship
coderVersion 10), and has no way to extract selectors, outlets, or strings in a
pipeline-friendly form. `nibkit` parses the current format and is built around
the things you actually want during an engagement:

- the `@IBAction` selectors a view controller responds to
- the `@IBOutlet` names and their target classes
- runtime attributes (a classic place to hide flags / keys / feature toggles)
- every string value, for grepping after endpoints and secrets

## Install

```
go install nibkit@latest        # after publish
```

Build from source:

```
git clone <repo> && cd nibkit
go build -o nibkit .
cp nibkit /usr/local/bin/       # or anywhere on PATH
```

## Commands

```
nibkit info     <path>   header + table counts (cheap fingerprint)
nibkit dump     <path>   tree view of the archived object graph
nibkit json     <path>   the object graph as JSON (pipe into jq)
nibkit strings  <path>   all string values, class names, keys
nibkit wiring   <path>   outlet + action connections (selectors, sources, targets)
```

`<path>` is a flat compiled `.nib` file, or a `.storyboardc` / `.nib` bundle
directory (each scene nib is parsed in turn).

## Example

```
$ nibkit wiring Action.storyboardc
TYPE    SELECTOR / OUTLET       SOURCE                       DESTINATION
OUTLET  myButton                IBFilesOwner(UIProxyObject)  UIButton
OUTLET  nameField               IBFilesOwner(UIProxyObject)  UITextField
OUTLET  view                    IBFilesOwner(UIProxyObject)  UIView
ACTION  didTapButton:withEvent: UIButton                     UIProxyObject  [touchUpInside]

$ nibkit strings App.app/Base.lproj/Main.storyboardc | rg -i 'url|key|secret|http'
```

## Format notes

NIBArchive is a 50-byte header (`NIBArchive` magic + formatVersion +
coderVersion + four count/offset table pairs) followed by four tables: keys,
class names, objects, and coder values. Integers are a 7-bit little-endian
varint whose high bit marks the terminal byte. Geometry payloads (UIBounds,
UICenter, ...) are a 1-byte tag followed by little-endian doubles. nibkit
resolves object references into a tree, expanding each object once and emitting
back-references for repeats.

Value-type table: 0=int8, 1=int16, 2=int32, 3=int64, 4=true, 5=false, 6=float,
7=double, 8=data, 9=nil, 10=object-ref(u32).

Decompilation is structurally lossy: Interface Builder discards editing
metadata (constraint equations, layout-guide math, the original document
object IDs) at compile time. nibkit recovers the runtime object graph, the
wiring, and the strings, not a round-trippable XIB.

## Scope

- Modern iOS `NIBArchive` (UINibEncoder) format, the current default.
- `.storyboardc` / `.nib` bundle directories.

Not (yet) handled: legacy NeXTSTEP `.nib`, and macOS `NSKeyedArchive`
(`.nib` archives that are actually binary plists). The latter is rare on iOS.

## License

MIT.
