package main

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
)

const magic = "NIBArchive"

// coder value types (UINibCoderValueType)
const (
	tInt8 = iota
	tInt16
	tInt32
	tInt64
	tTrue
	tFalse
	tFloat
	tDouble
	tData
	tNil
	tObj
)

type classEntry struct {
	Name   string
	Extras []uint32
}

type objectEntry struct {
	ClassIdx   int
	ValueStart int
	ValueCount int
}

// value: payload type depends on Type (int, int64, float64, bool, []byte, uint32)
type value struct {
	KeyIdx  int
	Type    int
	Payload interface{}
}

// Archive is a parsed NIBArchive.
type Archive struct {
	Major, Minor int
	Keys         []string
	Classes      []classEntry
	Objects      []objectEntry
	Values       []value
}

var errShort = fmt.Errorf("unexpected end of NIBArchive data")

// reader is a little-endian cursor that panics on short reads, so the parser
// stays compact; parse() recovers the panic into an error.
type reader struct {
	m   []byte
	pos int
}

func (r *reader) u8() byte {
	if r.pos >= len(r.m) {
		panic(errShort)
	}
	v := r.m[r.pos]
	r.pos++
	return v
}
func (r *reader) i8() int    { return int(int8(r.u8())) }
func (r *reader) i16() int   { v := r.u16(); return int(int16(v)) }
func (r *reader) i32() int   { v := r.u32(); return int(int32(v)) }
func (r *reader) i64() int64 { v := r.u64(); return int64(v) }
func (r *reader) u16() uint16 {
	if r.pos+2 > len(r.m) {
		panic(errShort)
	}
	v := binary.LittleEndian.Uint16(r.m[r.pos:])
	r.pos += 2
	return v
}
func (r *reader) u32() uint32 {
	if r.pos+4 > len(r.m) {
		panic(errShort)
	}
	v := binary.LittleEndian.Uint32(r.m[r.pos:])
	r.pos += 4
	return v
}
func (r *reader) u64() uint64 {
	if r.pos+8 > len(r.m) {
		panic(errShort)
	}
	v := binary.LittleEndian.Uint64(r.m[r.pos:])
	r.pos += 8
	return v
}
func (r *reader) f32() float64 { return float64(math.Float32frombits(r.u32())) }
func (r *reader) f64() float64 { return math.Float64frombits(r.u64()) }

func (r *reader) take(n int) []byte {
	if n < 0 || r.pos+n > len(r.m) {
		panic(errShort)
	}
	v := r.m[r.pos : r.pos+n]
	r.pos += n
	return v
}

// vint: 7-bit little-endian varint, high bit set on the terminal byte.
func (r *reader) vint() int {
	var result, shift int
	for {
		b := r.u8()
		result |= int(b&0x7f) << shift
		shift += 7
		if b&0x80 != 0 {
			break
		}
	}
	return result
}

// parse parses a NIBArchive blob, recovering reader panics into errors.
func parse(buf []byte) (a *Archive, err error) {
	defer func() {
		if e := recover(); e != nil {
			a = nil
			err = fmt.Errorf("%v", e)
		}
	}()
	a = parseArchive(buf)
	return a, nil
}

func parseArchive(buf []byte) *Archive {
	if len(buf) < 10+10*4 {
		panic(errShort)
	}
	if string(buf[:10]) != magic {
		panic(fmt.Errorf("not a NIBArchive (magic=%q)", string(buf[:10])))
	}
	r := &reader{m: buf, pos: 10}
	a := &Archive{
		Major: int(r.u32()),
		Minor: int(r.u32()),
	}
	objCount := int(r.u32())
	objOff := int(r.u32())
	keyCount := int(r.u32())
	keyOff := int(r.u32())
	valCount := int(r.u32())
	valOff := int(r.u32())
	clsCount := int(r.u32())
	clsOff := int(r.u32())

	// DoS guard: every table entry consumes at least one byte of input, so a
	// count larger than the buffer cannot occur in a valid file. Without this,
	// a 50-byte nib with objCount=0xFFFFFFFF asks the allocator for >100GB and
	// the runtime dies with an unrecoverable out-of-memory (no panic, no recover).
	if keyCount > len(buf) || clsCount > len(buf)/2 ||
		objCount > len(buf)/3 || valCount > len(buf)/2 {
		panic(errShort)
	}

	// keys
	r.pos = keyOff
	a.Keys = make([]string, keyCount)
	for i := range a.Keys {
		a.Keys[i] = string(r.take(r.vint()))
	}

	// class names
	r.pos = clsOff
	a.Classes = make([]classEntry, clsCount)
	for i := range a.Classes {
		ln := r.vint()
		nextra := r.vint()
		if nextra > len(buf)/4 { // each extra is 4 bytes; guard the alloc below
			panic(errShort)
		}
		extras := make([]uint32, nextra)
		for j := range extras {
			extras[j] = r.u32()
		}
		name := strings.TrimRight(string(r.take(ln)), "\x00")
		a.Classes[i] = classEntry{Name: name, Extras: extras}
	}

	// objects
	r.pos = objOff
	a.Objects = make([]objectEntry, objCount)
	for i := range a.Objects {
		a.Objects[i] = objectEntry{
			ClassIdx:   r.vint(),
			ValueStart: r.vint(),
			ValueCount: r.vint(),
		}
	}
	// coder values
	r.pos = valOff
	a.Values = make([]value, valCount)
	for i := range a.Values {
		ki := r.vint()
		t := int(r.u8())
		var pl interface{}
		switch t {
		case tInt8:
			pl = r.i8()
		case tInt16:
			pl = r.i16()
		case tInt32:
			pl = r.i32()
		case tInt64:
			pl = r.i64()
		case tTrue:
			pl = true
		case tFalse:
			pl = false
		case tNil:
			pl = nil
		case tFloat:
			pl = r.f32()
		case tDouble:
			pl = r.f64()
		case tData:
			pl = r.take(r.vint())
		case tObj:
			pl = r.u32()
		default:
			panic(fmt.Errorf("unknown coder value type %d at value #%d", t, i))
		}
		a.Values[i] = value{KeyIdx: ki, Type: t, Payload: pl}
	}
	return a
}

// valueRange returns the clamped [lo, hi) window into a.Values for an
// object's coder values. Fuzzed archives carry huge or negative ValueStart and
// ValueCount (including int-overflow wraps), so every consumer must go through
// this instead of trusting the raw fields.
func (a *Archive) valueRange(oe objectEntry) (int, int) {
	lo, hi := oe.ValueStart, oe.ValueStart+oe.ValueCount
	if lo < 0 {
		lo = 0
	}
	if hi > len(a.Values) || hi < lo { // hi < lo == overflow wrap
		hi = len(a.Values)
	}
	if lo > len(a.Values) {
		lo = len(a.Values)
	}
	return lo, hi
}

// className resolves an object index to its runtime class name.
func (a *Archive) className(idx int) string {
	if idx < 0 || idx >= len(a.Objects) {
		return "?"
	}
	c := a.Objects[idx].ClassIdx
	if c < 0 || c >= len(a.Classes) {
		return "?"
	}
	return a.Classes[c].Name
}

// bytesToHex is a small helper used by decodeValue.
func bytesToHex(b []byte) string { return hex.EncodeToString(b) }
