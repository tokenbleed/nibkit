package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ---------- dump (tree) ----------

func (a *Archive) printTree(root interface{}, indent int) {
	pad := strings.Repeat("  ", indent)
	switch v := root.(type) {
	case *node:
		extra := ""
		if cl := a.nodeStringProp(v, "UICustomClass"); cl != "" {
			extra = "  <" + cl + ">"
		} else if cl := a.nodeStringProp(v, "NSClassName"); cl != "" {
			extra = "  <" + cl + ">"
		}
		fmt.Printf("%s%s (#%d)%s\n", pad, v.Class, v.Idx, extra)
		for _, p := range v.Props {
			switch pv := p.Value.(type) {
			case *node:
				label := p.Key
				if s := nodeString(pv); s != "" {
					label = p.Key + ": \"" + s + "\""
				}
				fmt.Printf("%s  %s:\n", pad, label)
				a.printTree(pv, indent+2)
			case backref:
				fmt.Printf("%s  %s: -> backref #%d\n", pad, p.Key, pv.Idx)
			default:
				fmt.Printf("%s  %s = %s\n", pad, p.Key, fmtScalar(pv))
			}
		}
	case backref:
		fmt.Printf("%s-> backref #%d\n", pad, v.Idx)
	}
}

// nodeString returns the text of an NSString node, else "".
func nodeString(n *node) string {
	if n.Class != "NSString" {
		return ""
	}
	for _, p := range n.Props {
		if s, ok := p.Value.(string); ok && (p.Key == "NS.bytes" || p.Key == "NS.string") {
			return s
		}
	}
	return ""
}

// nodeStringProp finds a string-valued NSString property on a node.
func (a *Archive) nodeStringProp(n *node, key string) string {
	for _, p := range n.Props {
		if p.Key == key {
			if sub, ok := p.Value.(*node); ok {
				return nodeString(sub)
			}
		}
	}
	return ""
}

func fmtScalar(v interface{}) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case bool:
		if x {
			return "True"
		}
		return "False"
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case string:
		return strconv.Quote(x)
	case []float64:
		parts := make([]string, len(x))
		for i, f := range x {
			parts[i] = fmtScalar(f)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	default:
		return fmt.Sprintf("%v", x)
	}
}

// ---------- strings ----------

type strItem struct{ Source, Value string }

func (a *Archive) collectStrings() []strItem {
	var out []strItem
	seen := map[string]bool{}
	add := func(src, val string) {
		if val == "" || seen[val] {
			return
		}
		seen[val] = true
		out = append(out, strItem{src, val})
	}
	for _, c := range a.Classes {
		add("class", c.Name)
	}
	for _, v := range a.Values {
		if v.Type != tData {
			continue
		}
		key := ""
		if v.KeyIdx >= 0 && v.KeyIdx < len(a.Keys) {
			key = a.Keys[v.KeyIdx]
		}
		if strBytes[key] {
			add("value", cutNull(string(v.Payload.([]byte))))
		}
	}
	for _, k := range a.Keys {
		add("key", k)
	}
	return out
}

// ---------- wiring (outlets + actions) ----------

var eventBits = map[int]string{
	1 << 0:  "touchDown",
	1 << 1:  "touchDragInside",
	1 << 2:  "touchDragOutside",
	1 << 5:  "touchCancel",
	1 << 6:  "touchUpInside",
	1 << 7:  "touchUpOutside",
	1 << 12: "valueChanged",
	1 << 14: "primaryActionTriggered",
	1 << 16: "editingDidBegin",
	1 << 17: "editingChanged",
	1 << 18: "editingDidEnd",
	1 << 19: "editingDidEndOnExit",
}

func eventName(mask int) string {
	if n, ok := eventBits[mask]; ok {
		return n
	}
	var names []string
	for b, n := range eventBits {
		if mask&b != 0 {
			names = append(names, n)
		}
	}
	if len(names) > 0 {
		sort.Strings(names)
		return strings.Join(names, "|")
	}
	return fmt.Sprintf("0x%x", mask)
}

// objProps maps a connection object's keys to their values.
func (a *Archive) objProps(idx int) map[string]value {
	m := map[string]value{}
	if idx < 0 || idx >= len(a.Objects) {
		return m
	}
	oe := a.Objects[idx]
	for i := oe.ValueStart; i < oe.ValueStart+oe.ValueCount && i < len(a.Values); i++ {
		v := a.Values[i]
		key := "?"
		if v.KeyIdx >= 0 && v.KeyIdx < len(a.Keys) {
			key = a.Keys[v.KeyIdx]
		}
		m[key] = v
	}
	return m
}

// objString returns the NS.bytes text of an NSString-typed object.
func (a *Archive) objString(idx int) string {
	for k, v := range a.objProps(idx) {
		if v.Type == tData && strBytes[k] {
			return cutNull(string(v.Payload.([]byte)))
		}
	}
	return ""
}

func (a *Archive) sourceLabel(idx int) string {
	if v, ok := a.objProps(idx)["UIProxiedObjectIdentifier"]; ok && v.Type == tObj {
		if ident := a.objString(int(v.Payload.(uint32))); ident != "" {
			return ident + "(" + a.className(idx) + ")"
		}
	}
	return a.className(idx)
}

type conn struct {
	Kind, Name, Source, Destination string
	Event                           string
}

func (a *Archive) connections() []conn {
	var out []conn
	for i := range a.Objects {
		name := a.className(i)
		if name != "UIRuntimeOutletConnection" && name != "UIRuntimeEventConnection" {
			continue
		}
		p := a.objProps(i)
		c := conn{Kind: "outlet"}
		if name == "UIRuntimeEventConnection" {
			c.Kind = "action"
		}
		if v, ok := p["UILabel"]; ok && v.Type == tObj {
			c.Name = a.objString(int(v.Payload.(uint32)))
		}
		if v, ok := p["UISource"]; ok && v.Type == tObj {
			c.Source = a.sourceLabel(int(v.Payload.(uint32)))
		}
		if v, ok := p["UIDestination"]; ok && v.Type == tObj {
			c.Destination = a.className(int(v.Payload.(uint32)))
		}
		if c.Kind == "action" {
			if v, ok := p["UIEventMask"]; ok {
				switch x := v.Payload.(type) {
				case int:
					c.Event = eventName(x)
				case int64:
					c.Event = eventName(int(x))
				}
			}
		}
		out = append(out, c)
	}
	return out
}
