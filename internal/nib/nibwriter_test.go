package nib

import (
	"encoding/binary"
	"math"
)

// nibWriter builds synthetic NIBArchive binaries. It mirrors the on-disk
// layout in archive.go: magic, ten u32 header words, then key, class, object
// and value tables addressed by offset. It exists so tests never depend on
// files lifted out of real applications.
type nibWriter struct {
	keys    []string
	classes []string
	objects []nibObj
}

type nibObj struct {
	cls  string
	vals []testValue
}

type testValue struct {
	key  string
	typ  int
	intv int64
	raw  []byte
	f64v float64
	refv uint32
}

func (w *nibWriter) key(k string) int {
	for i, e := range w.keys {
		if e == k {
			return i
		}
	}
	w.keys = append(w.keys, k)
	return len(w.keys) - 1
}

func (w *nibWriter) class(name string) int {
	for i, e := range w.classes {
		if e == name {
			return i
		}
	}
	w.classes = append(w.classes, name)
	return len(w.classes) - 1
}

// obj appends an object with the given class and values, returning its index.
func (w *nibWriter) obj(cls string, vals ...testValue) int {
	w.class(cls)
	w.objects = append(w.objects, nibObj{cls: cls, vals: vals})
	return len(w.objects) - 1
}

func kstr(key, s string) testValue       { return testValue{key: key, typ: tData, raw: []byte(s)} }
func kobj(key string, idx int) testValue { return testValue{key: key, typ: tObj, refv: uint32(idx)} }
func kint(key string, n int64) testValue { return testValue{key: key, typ: tInt32, intv: n} }

func putvint(out []byte, n int) []byte {
	for {
		b := byte(n & 0x7f)
		n >>= 7
		if n == 0 {
			return append(out, b|0x80)
		}
		out = append(out, b)
	}
}

func (w *nibWriter) build() []byte {
	// values first: serializing them interns every key/class the objects use,
	// so the key and class tables below are complete when written.
	var values, keys, classes, objects []byte
	for _, o := range w.objects {
		for _, v := range o.vals {
			values = putvint(values, w.key(v.key))
			values = append(values, byte(v.typ))
			switch v.typ {
			case tInt8, tInt16, tInt32:
				b := make([]byte, 4)
				binary.LittleEndian.PutUint32(b, uint32(int32(v.intv)))
				values = append(values, b...)
			case tInt64:
				b := make([]byte, 8)
				binary.LittleEndian.PutUint64(b, uint64(v.intv))
				values = append(values, b...)
			case tFloat:
				b := make([]byte, 4)
				binary.LittleEndian.PutUint32(b, math.Float32bits(float32(v.f64v)))
				values = append(values, b...)
			case tDouble:
				b := make([]byte, 8)
				binary.LittleEndian.PutUint64(b, math.Float64bits(v.f64v))
				values = append(values, b...)
			case tData:
				values = putvint(values, len(v.raw))
				values = append(values, v.raw...)
			case tObj:
				b := make([]byte, 4)
				binary.LittleEndian.PutUint32(b, v.refv)
				values = append(values, b...)
			}
		}
	}
	for _, k := range w.keys {
		keys = putvint(keys, len(k))
		keys = append(keys, k...)
	}
	for _, c := range w.classes {
		classes = putvint(classes, len(c))
		classes = putvint(classes, 0) // no extra u32s
		classes = append(classes, c...)
	}
	vstart := 0
	for _, o := range w.objects {
		objects = putvint(objects, w.class(o.cls))
		objects = putvint(objects, vstart)
		objects = putvint(objects, len(o.vals))
		vstart += len(o.vals)
	}

	buf := make([]byte, 50)
	copy(buf, magic)
	put32 := func(off, n int) { binary.LittleEndian.PutUint32(buf[off:], uint32(n)) }
	put32(10, 1)  // major (format version)
	put32(14, 10) // minor (coder version)
	put32(18, len(w.objects))
	put32(22, 50) // object table directly after the header
	put32(26, len(w.keys))
	put32(30, 50+len(objects))
	valCount := 0
	for _, o := range w.objects {
		valCount += len(o.vals)
	}
	put32(34, valCount)                               // valCount
	put32(38, 50+len(objects)+len(keys)+len(classes)) // valOff
	put32(42, len(w.classes))                         // clsCount
	put32(46, 50+len(objects)+len(keys))              // clsOff
	buf = append(buf, objects...)
	buf = append(buf, keys...)
	buf = append(buf, classes...)
	buf = append(buf, values...)
	return buf
}
