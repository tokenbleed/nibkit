package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"
)

// geo: keys whose DATA payload is a 1-byte tag + N little-endian doubles.
var geo = map[string]int{
	"UIBounds": 4, "UIFrame": 4, "UICenter": 2, "UIOrigin": 2, "UISize": 2,
	"UIContentOffset": 2, "UIContentInset": 4, "UIScrollEdgeInsets": 4,
	"UIShadowOffset": 2, "UITitleShadowOffset": 2,
}

// strBytes: keys whose DATA payload is a null-terminated UTF-8 string body.
var strBytes = map[string]bool{
	"NS.bytes": true, "NS.string": true, "UIProxiedObjectIdentifier": true,
}

type backref struct {
	Idx int `json:"backref"`
}

type prop struct {
	Key   string      `json:"key"`
	Value interface{} `json:"value"`
}

type node struct {
	Idx   int    `json:"idx"`
	Class string `json:"class"`
	Props []prop `json:"props"`
}

// decodeValue renders a single coder value into a JSON-friendly Go value.
func decodeValue(key string, t int, pl interface{}) interface{} {
	if t != tData {
		return pl
	}
	data := pl.([]byte)
	if n, ok := geo[key]; ok && len(data) >= 1+n*8 {
		out := make([]float64, n)
		for i := 0; i < n; i++ {
			bits := binary.LittleEndian.Uint64(data[1+i*8:])
			out[i] = math.Float64frombits(bits)
		}
		return out
	}
	if strBytes[key] {
		return sanitize(cutNull(string(data)))
	}
	return bytesToHex(data)
}

func cutNull(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == 0 {
			return s[:i]
		}
	}
	return s
}

// sanitize neutralizes terminal control characters so nib-controlled
// strings cannot inject escape sequences (clipboard writes, beeps, cursor
// hijacks) into the terminal or pager. Hostile bytes stay visible, escaped
// as hex, so they remain useful for analysis.
func sanitize(s string) string {
	bad := func(b byte) bool {
		return (b < 0x20 && b != '\t' && b != '\n') || b == 0x7f
	}
	i := 0
	for ; i < len(s); i++ {
		if bad(s[i]) {
			break
		}
	}
	if i == len(s) {
		return s
	}
	var sb strings.Builder
	sb.WriteString(s[:i])
	for ; i < len(s); i++ {
		if bad(s[i]) {
			fmt.Fprintf(&sb, "\\x%02x", s[i])
		} else {
			sb.WriteByte(s[i])
		}
	}
	return sb.String()
}

// buildGraph resolves object[idx] into a nested node tree, following OBJ refs.
// A shared seen set expands each object once; later refs collapse to backref.
func (a *Archive) buildGraph(idx int, seen map[int]bool) interface{} {
	if idx < 0 || idx >= len(a.Objects) {
		return nil // dangling OBJ ref (fuzzed/corrupt archive)
	}
	if seen[idx] {
		return backref{idx}
	}
	seen[idx] = true
	oe := a.Objects[idx]
	name := "?"
	if oe.ClassIdx >= 0 && oe.ClassIdx < len(a.Classes) {
		name = a.Classes[oe.ClassIdx].Name
	}
	n := &node{Idx: idx, Class: name}
	lo, hi := a.valueRange(oe)
	for i := lo; i < hi; i++ {
		v := a.Values[i]
		key := "?"
		if v.KeyIdx >= 0 && v.KeyIdx < len(a.Keys) {
			key = a.Keys[v.KeyIdx]
		}
		var val interface{}
		if v.Type == tObj {
			val = a.buildGraph(int(v.Payload.(uint32)), seen)
		} else {
			val = decodeValue(key, v.Type, v.Payload)
		}
		n.Props = append(n.Props, prop{Key: key, Value: val})
	}
	return n
}
