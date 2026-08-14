package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"unicode/utf16"
)

// legacy.go: pre-NIBArchive nib support. iOS apps shipped before ~2012 embed
// nibs as binary plists (bplist00) wrapping an NSKeyedArchive whose $top holds
// UINibObjectsKey / UINibConnectionsKey / UINibTopLevelObjectsKey. The plist is
// decoded and then synthesized into the same NIBArchive table structure
// (keys / classes / objects / values) so every command and emitter works
// unchanged: legacy connection objects even reuse UILabel / UISource /
// UIDestination and the UIRuntimeOutletConnection class name.

// ==================== binary plist parser ====================
// Containers keep child refs unresolved (positional indices) so the
// synthesizer below can build stable object-table slots.

type lUID struct{ n int } // plist UID: reference into $objects

type pArr struct{ refs []int }

type pDict struct {
	keys []string
	vals []int // refs, parallel to keys
}

type plist struct {
	objs    []any // decoded slot cache: nil, bool, int64, float64, string, []byte, lUID, pArr, pDict
	buf     []byte
	table   []int // slot -> offset
	refSize int
	depth   int // recursion guard for crafted inputs
}

func (p *plist) take(off, n int) []byte {
	if off < 0 || n < 0 || off+n > len(p.buf) {
		panic(errShort)
	}
	return p.buf[off : off+n]
}

func (p *plist) at(off int, n int) uint64 {
	b := p.take(off, n)
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}

func (p *plist) decode(idx int) any {
	if idx < 0 || idx >= len(p.objs) {
		panic(errShort)
	}
	if v := p.objs[idx]; v != nil {
		return v
	}
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > 512 {
		panic(errShort) // nesting deeper than any real keyedarchive
	}
	off := p.table[idx]
	b := p.take(off, 1)
	hdr, lo := int(b[0]), int(b[0]&0x0f)
	// length helper: ints/reals/uids store log2(size) in the low nibble;
	// strings/data/collections store the count itself, with lo == 0xF marking
	// the extended form where the real count follows as an int object.
	count, head := lo, 1
	if lo == 0xF {
		e := p.take(off+1, 1)
		if e[0]>>4 != 0x1 {
			panic(errShort)
		}
		count = int(p.at(off+2, 1<<int(e[0]&0x0f)))
		head = 2 + 1<<int(e[0]&0x0f)
	}
	var v any
	switch hdr >> 4 {
	case 0x0:
		switch lo {
		case 0x0:
			v = any(nil)
		case 0x8:
			v = false
		case 0x9:
			v = true
		default:
			v = ""
		}
	case 0x1: // int, big-endian
		raw := p.take(off+1, 1<<lo)
		var n int64
		for _, c := range raw {
			n = n<<8 | int64(c)
		}
		v = n
	case 0x2: // real
		if lo == 2 {
			v = math.Float64frombits(binary.BigEndian.Uint64(p.take(off+1, 8)))
		} else {
			v = float64(math.Float32frombits(binary.BigEndian.Uint32(p.take(off+1, 4))))
		}
	case 0x3: // date
		v = math.Float64frombits(binary.BigEndian.Uint64(p.take(off+1, 8)))
	case 0x4: // data
		v = append([]byte(nil), p.take(off+head, count)...)
	case 0x5: // ascii string
		v = string(p.take(off+head, count))
	case 0x6: // utf-16be string
		raw := p.take(off+head, 2*count)
		u := make([]uint16, count)
		for i := range u {
			u[i] = binary.BigEndian.Uint16(raw[2*i:])
		}
		v = string(utf16.Decode(u))
	case 0x8: // uid
		v = lUID{int(p.at(off+1, 1<<lo))}
	case 0xA, 0xC: // array / set
		refs := make([]int, count)
		base := off + head
		for i := range refs {
			refs[i] = int(p.at(base+i*p.refSize, p.refSize))
		}
		v = pArr{refs}
	case 0xD: // dict
		keys := make([]string, count)
		vals := make([]int, count)
		base := off + head
		vo := base + count*p.refSize
		for i := 0; i < count; i++ {
			kr := int(p.at(base+i*p.refSize, p.refSize))
			if ks, ok := p.decode(kr).(string); ok {
				keys[i] = ks
			}
			vals[i] = int(p.at(vo+i*p.refSize, p.refSize))
		}
		v = pDict{keys, vals}
	default:
		panic(fmt.Errorf("unknown plist object marker 0x%02x", hdr))
	}
	p.objs[idx] = v
	return v
}

func parseBPlist(buf []byte) *plist {
	if len(buf) < 8+32 || string(buf[:6]) != "bplist" {
		panic(fmt.Errorf("not a binary plist"))
	}
	t := buf[len(buf)-32:]
	offSize, refSize := int(t[6]), int(t[7])
	numObj := int(binary.BigEndian.Uint64(t[8:16]))
	tabOff := int(binary.BigEndian.Uint64(t[24:32]))
	if offSize < 1 || offSize > 8 || refSize < 1 || refSize > 8 ||
		numObj < 1 || numObj > len(buf) || // every object needs >= 1 byte
		tabOff < 0 || tabOff+numObj*offSize > len(buf) {
		panic(errShort)
	}
	p := &plist{objs: make([]any, numObj), buf: buf, refSize: refSize, table: make([]int, numObj)}
	for i := range p.table {
		p.table[i] = int(p.at(tabOff+i*offSize, offSize))
	}
	return p
}

// ==================== keyedarchive -> NIBArchive synthesis ====================

func parseLegacy(buf []byte) *Archive {
	p := parseBPlist(buf)
	root, ok := p.decode(0).(pDict)
	if !ok {
		panic(errShort)
	}
	var arch string
	var topDict pDict
	var objects pArr
	for i, k := range root.keys {
		switch k {
		case "$archiver":
			arch, _ = p.decode(root.vals[i]).(string)
		case "$top":
			topDict, _ = p.decode(root.vals[i]).(pDict)
		case "$objects":
			objects, _ = p.decode(root.vals[i]).(pArr)
		}
	}
	if arch != "NSKeyedArchiver" || objects.refs == nil {
		panic(fmt.Errorf("binary plist but not an NSKeyedArchive"))
	}
	uinib := false
	for _, k := range topDict.keys {
		if len(k) >= 5 && k[:5] == "UINib" {
			uinib = true
			break
		}
	}
	if !uinib {
		panic(fmt.Errorf("legacy Interface Builder nib (IB.objectdata) not supported yet"))
	}

	a := &Archive{Keyed: true}
	keyIdx := map[string]int{}
	clsIdx := map[string]int{}
	internKey := func(k string) int {
		if i, ok := keyIdx[k]; ok {
			return i
		}
		a.Keys = append(a.Keys, k)
		keyIdx[k] = len(a.Keys) - 1
		return len(a.Keys) - 1
	}
	internCls := func(name string) int {
		if i, ok := clsIdx[name]; ok {
			return i
		}
		a.Classes = append(a.Classes, classEntry{Name: name})
		clsIdx[name] = len(a.Classes) - 1
		return len(a.Classes) - 1
	}
	emitVal := func(key string, t int, pl any) int {
		a.Values = append(a.Values, value{KeyIdx: internKey(key), Type: t, Payload: pl})
		return len(a.Values) - 1
	}
	emitObj := func(cls string, start, count int) {
		a.Objects = append(a.Objects, objectEntry{ClassIdx: internCls(cls), ValueStart: start, ValueCount: count})
	}

	// slot i+1 in the synthesized table = $objects[i]; slot 0 is a synthetic
	// root exposing the $top keys, so the tree walk starts somewhere meaningful.
	a.Objects = make([]objectEntry, 0, len(objects.refs)+1)
	emitObj("IBDocument", 0, 0) // placeholder; values patched below
	rootStart := len(a.Values)
	for i, k := range topDict.keys {
		if u, ok := p.decode(topDict.vals[i]).(lUID); ok {
			emitVal(k, tObj, uint32(u.n+1))
		}
	}
	a.Objects[0].ValueStart = rootStart
	a.Objects[0].ValueCount = len(a.Values) - rootStart

	// plist table ref -> synthesized slot. $objects members map 1:1; values
	// only reachable inline (class-name strings etc.) get a fresh slot.
	refSlot := make([]int, len(p.objs))
	for i := range refSlot {
		refSlot[i] = -1
	}
	for i, ref := range objects.refs {
		refSlot[ref] = i + 1
	}
	// intVal writes an inline int picking the narrowest NIBArchive type.
	intVal := func(key string, n int64) {
		t := tInt64
		var pl any = n
		switch {
		case n >= math.MinInt8 && n <= math.MaxInt8:
			t, pl = tInt8, int(n)
		case n >= math.MinInt16 && n <= math.MaxInt16:
			t, pl = tInt16, int(n)
		case n >= math.MinInt32 && n <= math.MaxInt32:
			t, pl = tInt32, int(n)
		}
		emitVal(key, t, pl)
	}
	// slotFor synthesizes a slot for a plist ref that $objects does not
	// already cover. ONLY valid after the main $objects pass: appending during
	// it would shift slot numbers out from under the UID mapping.
	var slotFor func(ref int) int
	slotFor = func(ref int) int {
		if s := refSlot[ref]; s >= 0 {
			return s
		}
		slot := len(a.Objects)
		refSlot[ref] = slot // reserve before children: cycles collapse here
		start := len(a.Values)
		switch v := p.decode(ref).(type) {
		case string:
			emitVal("NS.bytes", tData, []byte(v))
			emitObj("NSString", start, len(a.Values)-start)
		case []byte:
			emitVal("NS.bytes", tData, v)
			emitObj("NSData", start, len(a.Values)-start)
		case pArr:
			for _, r := range v.refs {
				if u, ok := p.decode(r).(lUID); ok {
					emitVal("NS.objects", tObj, uint32(u.n+1))
				} else {
					emitVal("NS.objects", tObj, uint32(slotFor(r)))
				}
			}
			emitObj("NSMutableArray", start, len(a.Values)-start)
		case pDict:
			for i, k := range v.keys {
				if k == "" || k == "$class" {
					continue
				}
				if u, ok := p.decode(v.vals[i]).(lUID); ok {
					emitVal(k, tObj, uint32(u.n+1))
				} else {
					emitVal(k, tObj, uint32(slotFor(v.vals[i])))
				}
			}
			emitObj("NSDictionary", start, len(a.Values)-start)
		default:
			emitObj("NSNull", start, 0)
		}
		return slot
	}
	// deferred non-UID refs: value payloads patched once all $objects slots
	// exist, so slotFor appends after (never inside) the main table.
	var pend []int // value indexes, alternating with refs
	objRef := func(key string, ref int) {
		if u, ok := p.decode(ref).(lUID); ok {
			emitVal(key, tObj, uint32(u.n+1))
			return
		}
		pend = append(pend, emitVal(key, tObj, uint32(0)), ref)
	}
	// classOf resolves an object dict's real class: $class holds a UID into
	// $objects whose target dict carries $classname.
	classOf := func(v pDict) string {
		for i, k := range v.keys {
			if k != "$class" {
				continue
			}
			desc := p.decode(v.vals[i])
			if u, ok := desc.(lUID); ok {
				if u.n >= 0 && u.n < len(objects.refs) {
					desc = p.decode(objects.refs[u.n])
				}
			}
			if cd, ok := desc.(pDict); ok {
				for j, ck := range cd.keys {
					if ck == "$classname" {
						if n, ok := p.decode(cd.vals[j]).(string); ok && n != "" {
							return n
						}
					}
				}
			}
		}
		return "NSObject"
	}
	for _, ref := range objects.refs {
		start := len(a.Values)
		cls := "NSObject"
		switch v := p.decode(ref).(type) {
		case string:
			cls = "NSString"
			emitVal("NS.bytes", tData, []byte(v))
		case []byte:
			cls = "NSData"
			emitVal("NS.bytes", tData, v)
		case bool:
			cls = "NSNumber"
			if v {
				emitVal("UIValue", tTrue, true)
			} else {
				emitVal("UIValue", tFalse, false)
			}
		case int64:
			cls = "NSNumber"
			intVal("UIValue", v)
		case float64:
			cls = "NSNumber"
			emitVal("UIValue", tDouble, v)
		case lUID:
			cls = "NSNull"
			emitVal("UIValue", tObj, uint32(v.n+1))
		case pArr:
			cls = "NSMutableArray"
			for _, r := range v.refs {
				objRef("NS.objects", r)
			}
		case pDict:
			cls = classOf(v)
			for i, k := range v.keys {
				if k == "" || k == "$class" || k == "$classname" || k == "$classes" {
					continue
				}
				switch p.decode(v.vals[i]).(type) {
				case lUID:
					objRef(k, v.vals[i])
				case bool:
					if p.decode(v.vals[i]).(bool) {
						emitVal(k, tTrue, true)
					} else {
						emitVal(k, tFalse, false)
					}
				case int64:
					intVal(k, p.decode(v.vals[i]).(int64))
				case float64:
					emitVal(k, tDouble, p.decode(v.vals[i]).(float64))
				default:
					objRef(k, v.vals[i])
				}
			}
		case nil:
			cls = "NSNull"
		default:
			panic(fmt.Errorf("unsupported plist object %T", v))
		}
		emitObj(cls, start, len(a.Values)-start)
	}
	for i := 0; i+1 < len(pend); i += 2 {
		a.Values[pend[i]].Payload = uint32(slotFor(pend[i+1]))
	}
	return a
}
