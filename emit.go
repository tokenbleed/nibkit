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
	lo, hi := a.valueRange(oe)
	for i := lo; i < hi; i++ {
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

// objIntProp reads a named scalar prop as an int.
func (a *Archive) objIntProp(idx int, key string) (int, bool) {
	v, ok := a.objProps(idx)[key]
	if !ok {
		return 0, false
	}
	switch x := v.Payload.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	}
	return 0, false
}

// arrayRefs returns the tObj element indices of an NSArray object.
func (a *Archive) arrayRefs(idx int) []int {
	var out []int
	if idx < 0 || idx >= len(a.Objects) {
		return out
	}
	oe := a.Objects[idx]
	lo, hi := a.valueRange(oe)
	for i := lo; i < hi; i++ {
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
	case "NSCustomObject", "NSCustomView", "NSClassSwapper":
		if n := a.objStringProp(idx, "NSClassName"); n != "" {
			return n
		}
	}
	return a.className(idx)
}

// sceneID returns the storyboard scene identifier of an object if any.
func (a *Archive) sceneID(idx int) string {
	return a.objStringProp(idx, "UIStoryboardIdentifier")
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
	if root == nil {
		fmt.Printf("%s(no root object)\n", pad)
		return
	}
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
		if name != "UIRuntimeOutletConnection" && name != "UIRuntimeEventConnection" &&
			name != "NSNibOutletConnector" && name != "NSNibControlConnector" &&
			name != "NSNibBindingConnector" && name != "NSIBHelpConnector" {
			continue
		}
		p := a.objProps(i)
		c := conn{Kind: "outlet"}
		switch name {
		case "UIRuntimeEventConnection", "NSNibControlConnector":
			c.Kind = "action"
		case "NSNibBindingConnector":
			c.Kind = "binding"
		case "NSIBHelpConnector":
			c.Kind = "help"
		}
		// iOS keys are UILabel/UISource/UIDestination; macOS connectors use
		// NSLabel/NSSource/NSDestination with identical meaning.
		if v, ok := p["UILabel"]; ok && v.Type == tObj {
			c.Name = a.objString(int(v.Payload.(uint32)))
		}
		if v, ok := p["NSLabel"]; ok && v.Type == tObj {
			c.Name = a.objString(int(v.Payload.(uint32)))
		}
		if v, ok := p["NSMarker"]; ok && v.Type == tObj && c.Name == "" {
			c.Name = a.objString(int(v.Payload.(uint32)))
		}
		for _, key := range []string{"UISource", "NSSource"} {
			if v, ok := p[key]; ok && v.Type == tObj {
				c.Source = a.classLabel(int(v.Payload.(uint32)))
			}
		}
		for _, key := range []string{"UIDestination", "NSDestination"} {
			if v, ok := p[key]; ok && v.Type == tObj {
				c.Destination = a.classLabel(int(v.Payload.(uint32)))
			}
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
		switch a.className(i) {
		case "UINibKeyValuePair":
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
			out = append(out, runtimeAttr{owner, kp, val})
		case "NSIBUserDefinedRuntimeAttributesConnector":
			// macOS legacy: parallel arrays of key paths and values on one object
			p := a.objProps(i)
			owner := "?"
			if v, ok := p["NSObject"]; ok && v.Type == tObj {
				owner = a.classLabel(int(v.Payload.(uint32)))
			}
			var kps, vals []int
			if v, ok := p["NSKeyPaths"]; ok && v.Type == tObj {
				kps = a.arrayRefs(int(v.Payload.(uint32)))
			}
			if v, ok := p["NSValues"]; ok && v.Type == tObj {
				vals = a.arrayRefs(int(v.Payload.(uint32)))
			}
			for j, kIdx := range kps {
				val := ""
				if j < len(vals) {
					val = a.objScalar(vals[j])
				}
				out = append(out, runtimeAttr{owner, a.objString(kIdx), val})
			}
		}
	}
	return out
}

// objScalar renders an object index as a short scalar string: string bodies,
// numbers and booleans, falling back to the class name.
func (a *Archive) objScalar(idx int) string {
	for k, v := range a.objProps(idx) {
		if v.Type == tData {
			if strBytes[k] {
				if b, ok := v.Payload.([]byte); ok {
					return cutNull(string(b))
				}
			}
			continue
		}
		if v.Type != tObj {
			if s := a.scalarString(k, v); s != "" {
				return s
			}
		}
	}
	return a.className(idx)
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
		var name, base string
		switch a.className(i) {
		case "UIClassSwapper":
			name = a.objStringProp(i, "UIClassName")
			base = a.objStringProp(i, "UIOriginalClassName")
		case "NSCustomObject", "NSCustomView", "NSClassSwapper":
			// macOS analog: the class name rides in NSClassName
			name = a.objStringProp(i, "NSClassName")
			if a.className(i) != "NSCustomObject" {
				base = a.objStringProp(i, "NSOriginalClassName")
			}
			if a.className(i) == "NSCustomView" && base == "" {
				base = "NSView"
			}
		default:
			continue
		}
		if name == "" || name == base || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, customClass{
			Class:   name,
			Base:    base,
			SceneID: a.sceneID(i),
			Module:  a.objStringProp(i, "UICustomModule"),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Class < out[j].Class })
	return out
}

// ==================== navigation graph (segues + containers) ====================

type navEdge struct {
	Kind        string            `json:"kind"` // show/push/modal/embed/relationship/custom/container
	SrcClass    string            `json:"sourceClass"`
	SrcID       string            `json:"sourceSceneID,omitempty"`
	DstClass    string            `json:"destinationClass,omitempty"`
	DstID       string            `json:"destinationID,omitempty"`
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
		return "segue"
	}
}

// navEdges returns the full navigation graph: storyboard segue templates plus
// container child arrays (tab bar tabs, navigation roots, etc.).
func (a *Archive) navEdges() []navEdge {
	var out []navEdge
	for i := range a.Objects {
		cn := a.className(i)
		// segue templates live under a UIClassSwapper's UIStoryboardSegueTemplates
		if cn == "UIClassSwapper" {
			if v, ok := a.objProps(i)["UIStoryboardSegueTemplates"]; ok && v.Type == tObj {
				srcClass := a.objStringProp(i, "UIClassName")
				srcID := a.sceneID(i)
				for _, sIdx := range a.arrayRefs(int(v.Payload.(uint32))) {
					out = append(out, a.extractSegue(srcClass, srcID, sIdx))
				}
			}
		}
		// container children: UIViewControllers / UIChildViewControllers arrays
		for _, k := range []string{"UIViewControllers", "UIChildViewControllers"} {
			v, ok := a.objProps(i)[k]
			if !ok || v.Type != tObj {
				continue
			}
			srcClass := a.classLabel(i)
			srcID := a.sceneID(i)
			for _, childIdx := range a.arrayRefs(int(v.Payload.(uint32))) {
				out = append(out, navEdge{
					Kind:     "container",
					SrcClass: srcClass,
					SrcID:    srcID,
					DstClass: a.classLabel(childIdx),
					DstID:    a.sceneID(childIdx),
				})
			}
		}
	}
	return dedupeEdges(out)
}

func edgeKey(e navEdge) string {
	return e.Kind + "|" + e.SrcClass + "|" + e.SrcID + "|" + e.DstClass + "|" + e.DstID
}

func dedupeEdges(in []navEdge) []navEdge {
	seen := map[string]bool{}
	var out []navEdge
	for _, e := range in {
		k := edgeKey(e)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, e)
	}
	return out
}

func (a *Archive) extractSegue(srcClass, srcID string, sIdx int) navEdge {
	cls := a.className(sIdx)
	e := navEdge{
		Kind:     segueKind(cls),
		SrcClass: srcClass,
		SrcID:    srcID,
		DstID:    a.objStringProp(sIdx, "UIDestinationViewControllerIdentifier"),
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
			// captured as DstID
		case "UICustomPrepareForChildViewControllersSegueName", "UICustomSeguePerformSelectorName":
			if e.Selector == "" {
				e.Selector = val
			}
		case "UIIdentifier", "UICustomIdentifier":
			if e.Identifier == "" {
				e.Identifier = val
			}
		case "UISegueClassName", "UICustomClass":
			if e.CustomClass == "" {
				e.CustomClass = val
			}
		default:
			details[k] = val
		}
	}
	if len(details) > 0 {
		e.Details = details
	}
	return e
}
