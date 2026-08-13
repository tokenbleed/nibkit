package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ==================== shared table helpers ====================

// objProps maps an object's coder values to their keyed value structs.
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

// objString follows an NSString object reference and returns its NS.bytes text.
func (a *Archive) objString(idx int) string {
	for k, v := range a.objProps(idx) {
		if v.Type != tData || !strBytes[k] {
			continue
		}
		if b, ok := v.Payload.([]byte); ok {
			return cutNull(string(b))
		}
	}
	return ""
}

// objStringProp reads a named prop that holds an NSString object reference.
func (a *Archive) objStringProp(idx int, key string) string {
	v, ok := a.objProps(idx)[key]
	if !ok || v.Type != tObj {
		return ""
	}
	return a.objString(int(v.Payload.(uint32)))
}

// arrayRefs returns the tObj element indices of an NSArray object.
func (a *Archive) arrayRefs(idx int) []int {
	var out []int
	if idx < 0 || idx >= len(a.Objects) {
		return out
	}
	oe := a.Objects[idx]
	for i := oe.ValueStart; i < oe.ValueStart+oe.ValueCount && i < len(a.Values); i++ {
		if v := a.Values[i]; v.Type == tObj {
			out = append(out, int(v.Payload.(uint32)))
		}
	}
	return out
}

// classLabel resolves an object index to a human label: the real custom class
// for a UIClassSwapper, the proxy identifier for a UIProxyObject, else the class.
func (a *Archive) classLabel(idx int) string {
	switch a.className(idx) {
	case "UIClassSwapper":
		if n := a.objStringProp(idx, "UIClassName"); n != "" {
			return n
		}
	case "UIProxyObject":
		if id := a.objStringProp(idx, "UIProxiedObjectIdentifier"); id != "" {
			return id + "(proxy)"
		}
	}
	return a.className(idx)
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// scalarString renders a coder value that may be a scalar or an object ref.
func (a *Archive) scalarString(key string, v value) string {
	if v.Type == tObj {
		ref := int(v.Payload.(uint32))
		if s := a.objString(ref); s != "" {
			return s
		}
		return "<" + a.className(ref) + ">"
	}
	return fmtScalar(decodeValue(key, v.Type, v.Payload))
}

// ==================== dump (tree) ====================

func (a *Archive) printTree(root interface{}, indent int) {
	pad := strings.Repeat("  ", indent)
	switch v := root.(type) {
	case *node:
		extra := ""
		if cl := firstNonEmpty(
			a.nodeStringProp(v, "UIClassName"),
			a.nodeStringProp(v, "UICustomClass"),
			a.nodeStringProp(v, "NSClassName"),
		); cl != "" && cl != v.Class {
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

// nodeStringProp finds a string-valued NSString property on a built graph node.
func (a *Archive) nodeStringProp(n *node, key string) string {
	for _, p := range n.Props {
		if p.Key != key {
			continue
		}
		if sub, ok := p.Value.(*node); ok {
			return nodeString(sub)
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

// ==================== strings ====================

type strItem struct {
	Source string `json:"source"`
	Value  string `json:"value"`
}

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

// ==================== wiring (outlets + actions) ====================

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

type conn struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Event       string `json:"event,omitempty"`
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
			c.Source = a.classLabel(int(v.Payload.(uint32)))
		}
		if v, ok := p["UIDestination"]; ok && v.Type == tObj {
			c.Destination = a.classLabel(int(v.Payload.(uint32)))
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

// ==================== runtime attributes ====================

type runtimeAttr struct {
	Object  string `json:"object"`
	KeyPath string `json:"keyPath"`
	Value   string `json:"value"`
}

func (a *Archive) runtimeAttributes() []runtimeAttr {
	var out []runtimeAttr
	for i := range a.Objects {
		if a.className(i) != "UINibKeyValuePair" {
			continue
		}
		p := a.objProps(i)
		kp := a.objStringProp(i, "UIKeyPath")
		if kp == "" {
			continue
		}
		owner := "?"
		if v, ok := p["UIObject"]; ok && v.Type == tObj {
			owner = a.classLabel(int(v.Payload.(uint32)))
		}
		val := ""
		if v, ok := p["UIValue"]; ok {
			val = a.scalarString("UIValue", v)
		}
		out = append(out, runtimeAttr{Object: owner, KeyPath: kp, Value: val})
	}
	return out
}

// ==================== custom classes ====================

type customClass struct {
	Class   string `json:"class"`
	Base    string `json:"base"`
	SceneID string `json:"sceneID,omitempty"`
	Module  string `json:"module,omitempty"`
}

func (a *Archive) customClasses() []customClass {
	seen := map[string]bool{}
	var out []customClass
	for i := range a.Objects {
		if a.className(i) != "UIClassSwapper" {
			continue
		}
		name := a.objStringProp(i, "UIClassName")
		base := a.objStringProp(i, "UIOriginalClassName")
		if name == "" || name == base || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, customClass{
			Class:   name,
			Base:    base,
			SceneID: a.objStringProp(i, "UIStoryboardIdentifier"),
			Module:  a.objStringProp(i, "UICustomModule"),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Class < out[j].Class })
	return out
}

// ==================== segues (storyboard navigation) ====================

type segue struct {
	SourceClass string            `json:"sourceClass"`
	SourceID    string            `json:"sourceSceneID,omitempty"`
	Kind        string            `json:"kind"`
	DestID      string            `json:"destinationID"`
	Identifier  string            `json:"identifier,omitempty"`
	Selector    string            `json:"selector,omitempty"`
	CustomClass string            `json:"customClass,omitempty"`
	Details     map[string]string `json:"details,omitempty"`
}

func segueKind(cls string) string {
	switch {
	case strings.Contains(cls, "Embed"):
		return "embed"
	case strings.Contains(cls, "Relationship"):
		return "relationship"
	case strings.Contains(cls, "Push"):
		return "push"
	case strings.Contains(cls, "Modal"):
		return "modal"
	case strings.Contains(cls, "Popover"):
		return "popover"
	case strings.Contains(cls, "Show"):
		return "show"
	case strings.Contains(cls, "Custom"):
		return "custom"
	default:
		return strings.TrimPrefix(strings.TrimPrefix(cls, "UIStoryboard"), "UI")
	}
}

func (a *Archive) segues() []segue {
	var out []segue
	for i := range a.Objects {
		if a.className(i) != "UIClassSwapper" {
			continue
		}
		v, ok := a.objProps(i)["UIStoryboardSegueTemplates"]
		if !ok || v.Type != tObj {
			continue
		}
		srcClass := a.objStringProp(i, "UIClassName")
		srcID := a.objStringProp(i, "UIStoryboardIdentifier")
		for _, sIdx := range a.arrayRefs(int(v.Payload.(uint32))) {
			out = append(out, a.extractSegue(srcClass, srcID, sIdx))
		}
	}
	return out
}

func (a *Archive) extractSegue(srcClass, srcID string, sIdx int) segue {
	cls := a.className(sIdx)
	s := segue{
		SourceClass: srcClass,
		SourceID:    srcID,
		Kind:        segueKind(cls),
		DestID:      a.objStringProp(sIdx, "UIDestinationViewControllerIdentifier"),
	}
	details := map[string]string{}
	for k, v := range a.objProps(sIdx) {
		if v.Type != tObj {
			continue
		}
		val := a.objString(int(v.Payload.(uint32)))
		if val == "" {
			continue
		}
		switch k {
		case "UIDestinationViewControllerIdentifier":
			// already captured as DestID
		case "UICustomPrepareForChildViewControllersSegueName", "UICustomSeguePerformSelectorName":
			if s.Selector == "" {
				s.Selector = val
			}
		case "UIIdentifier", "UICustomIdentifier":
			if s.Identifier == "" {
				s.Identifier = val
			}
		case "UISegueClassName", "UICustomClass":
			if s.CustomClass == "" {
				s.CustomClass = val
			}
		default:
			details[k] = val
		}
	}
	if len(details) > 0 {
		s.Details = details
	}
	return s
}
