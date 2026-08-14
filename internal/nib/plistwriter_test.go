package nib

import (
	"encoding/binary"
)

// tVal is a binary-plist object for the synthetic keyedarchive builder.
// Supported leaf types: string, bool, int, data, UID; containers: array, dict.
type tVal struct {
	marker byte
	str    string
	b      bool
	i      int64
	raw    []byte
	uid    int
	refs   []int
	keys   []string // parallel to refs for dicts
}

func tStr(s string) tVal    { return tVal{marker: 0x50, str: s} }
func tInt(n int64) tVal     { return tVal{marker: 0x10, i: n} }
func tUID(n int) tVal       { return tVal{marker: 0x80, uid: n} }
func tArr(refs ...int) tVal { return tVal{marker: 0xA0, refs: refs} }
func tDict(keys []string, refs []int) tVal {
	return tVal{marker: 0xD0, keys: keys, refs: refs}
}

type plistWriter struct {
	objs []tVal
}

func (w *plistWriter) add(v tVal) int {
	w.objs = append(w.objs, v)
	return len(w.objs) - 1
}

// intern returns the index of a string object, adding one if absent.
func (w *plistWriter) intern(s string) int {
	for i, o := range w.objs {
		if o.marker>>4 == 0x5 && o.str == s {
			return i
		}
	}
	return w.add(tStr(s))
}

func (w *plistWriter) build() []byte {
	// pre-intern dict keys so nothing is appended during serialization
	for i := 0; i < len(w.objs); i++ {
		if w.objs[i].marker>>4 == 0xD {
			for _, k := range w.objs[i].keys {
				w.intern(k)
			}
		}
	}

	var body []byte
	offsets := make([]int, len(w.objs))
	refSize := 1
	for n := len(w.objs); n >= 256; n >>= 8 {
		refSize++
	}
	appendRef := func(idx int) {
		v := idx
		for i := refSize - 1; i >= 0; i-- {
			body = append(body, byte(v>>(8*i)))
		}
	}

	for i := 0; i < len(w.objs); i++ {
		o := w.objs[i]
		offsets[i] = 8 + len(body)
		m := o.marker
		n := len(o.refs)
		if o.marker>>4 == 0x5 || o.marker>>4 == 0x4 {
			if o.marker>>4 == 0x5 {
				n = len(o.str)
			} else {
				n = len(o.raw)
			}
		}
		if n >= 15 {
			m |= 0x0F
		} else {
			m |= byte(n)
		}
		body = append(body, m)
		if n >= 15 { // extended count: an int object follows
			body = append(body, 0x10, byte(n))
		}
		switch o.marker >> 4 {
		case 0x5:
			body = append(body, o.str...)
		case 0x4:
			body = append(body, o.raw...)
		case 0x1: // int, fixed 8 bytes
			var b [8]byte
			binary.BigEndian.PutUint64(b[:], uint64(o.i))
			body = append(body, b[:]...)
		case 0x0: // bool: marker byte alone
			if o.b {
				body[len(body)-1] = 0x09
			} else {
				body[len(body)-1] = 0x08
			}
		case 0x8: // uid, 1-byte size
			body = append(body, byte(o.uid))
		case 0xA:
			for _, r := range o.refs {
				appendRef(r)
			}
		case 0xD:
			for _, k := range o.keys {
				appendRef(w.intern(k))
			}
			for _, r := range o.refs {
				appendRef(r)
			}
		}
	}

	offSize := 1
	for end := len(body) + 8; end >= 256; end >>= 8 {
		offSize++
	}
	buf := []byte("bplist00")
	buf = append(buf, body...)
	tableOff := len(buf)
	for _, o := range offsets {
		v := o
		for i := offSize - 1; i >= 0; i-- {
			buf = append(buf, byte(v>>(8*i)))
		}
	}
	trailer := make([]byte, 32)
	trailer[6] = byte(offSize)
	trailer[7] = byte(refSize)
	binary.BigEndian.PutUint64(trailer[8:], uint64(len(w.objs)))
	binary.BigEndian.PutUint64(trailer[16:], 0) // root index
	binary.BigEndian.PutUint64(trailer[24:], uint64(tableOff))
	return append(buf, trailer...)
}
