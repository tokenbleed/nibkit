package nib

import (
	"bytes"
	"strings"
	"testing"
)

// ---------- NIBArchive (modern) ----------

func modernOutletNib() []byte {
	w := &nibWriter{}
	str := func(s string) int { return w.obj("NSString", kstr("NS.bytes", s)) }
	filesOwner := w.obj("UIProxyObject", kobj("UIProxiedObjectIdentifier", str("IBFilesOwner")))
	window := w.obj("UIWindow")
	delegate := w.obj("UIClassSwapper",
		kobj("UIClassName", str("AppAppDelegate")),
		kobj("UIOriginalClassName", str("UIResponder")),
	)
	w.obj("UIRuntimeOutletConnection",
		kobj("UILabel", str("window")),
		kobj("UISource", delegate),
		kobj("UIDestination", window),
	)
	w.obj("UIRuntimeEventConnection",
		kobj("UILabel", str("didTapButton:")),
		kobj("UISource", filesOwner),
		kobj("UIDestination", delegate),
		kint("UIEventMask", 64),
	)
	w.obj("UINibKeyValuePair",
		kobj("UIKeyPath", str("secretTag")),
		kobj("UIObject", window),
		kobj("UIValue", str("admin-flag")),
	)
	return w.build()
}

func TestParseModernWiring(t *testing.T) {
	a, err := Parse(modernOutletNib())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if a.Keyed {
		t.Fatal("flagged as keyedarchive")
	}
	conns := a.Connections()
	if len(conns) != 2 {
		t.Fatalf("want 2 connections, got %d", len(conns))
	}
	if conns[0].Kind != "outlet" || conns[0].Name != "window" ||
		conns[0].Source != "AppAppDelegate" || conns[0].Destination != "UIWindow" {
		t.Errorf("outlet mis-parsed: %+v", conns[0])
	}
	if conns[1].Kind != "action" || conns[1].Name != "didTapButton:" ||
		conns[1].Source != "IBFilesOwner(proxy)" {
		t.Errorf("action mis-parsed: %+v", conns[1])
	}
	attrs := a.RuntimeAttributes()
	if len(attrs) != 1 || attrs[0].KeyPath != "secretTag" || attrs[0].Value != "admin-flag" {
		t.Errorf("runtime attrs mis-parsed: %+v", attrs)
	}
	cc := a.CustomClasses()
	if len(cc) != 1 || cc[0].Class != "AppAppDelegate" || cc[0].Base != "UIResponder" {
		t.Errorf("custom classes mis-parsed: %+v", cc)
	}
}

// ---------- keyedarchive (legacy iOS + macOS) ----------

func TestParseLegacyIOS(t *testing.T) {
	a, err := Parse(buildIOSLegacy())
	if err != nil {
		t.Fatalf("parse legacy iOS: %v", err)
	}
	if !a.Keyed {
		t.Fatal("not flagged as keyedarchive")
	}
	conns := a.Connections()
	if len(conns) != 1 {
		t.Fatalf("want 1 connection, got %d: %+v", len(conns), conns)
	}
	c := conns[0]
	if c.Kind != "outlet" || c.Name != "window" || c.Source != "GameAppDelegate" || c.Destination != "UIWindow" {
		t.Errorf("legacy outlet mis-parsed: %+v", c)
	}
	cc := a.CustomClasses()
	if len(cc) != 1 || cc[0].Class != "GameAppDelegate" || cc[0].Base != "UIResponder" {
		t.Errorf("legacy classes mis-parsed: %+v", cc)
	}
}

func buildIOSLegacy() []byte {
	w := &plistWriter{}
	w.add(tDict(nil, nil))
	w.add(tStr("$null"))
	filesOwnerName := w.add(tStr("IBFilesOwner"))
	w.add(tDict([]string{"$class", "UIProxiedObjectIdentifier"},
		[]int{w.add(tDict([]string{"$classname"}, []int{w.add(tStr("UIProxyObject"))})), w.add(tUID(filesOwnerName - 1))}))
	window := w.add(tDict([]string{"$class"},
		[]int{w.add(tDict([]string{"$classname"}, []int{w.add(tStr("UIWindow"))}))}))
	swapperName := w.add(tStr("GameAppDelegate"))
	swapperBase := w.add(tStr("UIResponder"))
	swapper := w.add(tDict([]string{"$class", "UIClassName", "UIOriginalClassName"},
		[]int{w.add(tDict([]string{"$classname"}, []int{w.add(tStr("UIClassSwapper"))})),
			w.add(tUID(swapperName - 1)), w.add(tUID(swapperBase - 1))}))
	label := w.add(tStr("window"))
	conn := w.add(tDict([]string{"$class", "UILabel", "UISource", "UIDestination"},
		[]int{w.add(tDict([]string{"$classname"}, []int{w.add(tStr("UIRuntimeOutletConnection"))})),
			w.add(tUID(label - 1)), w.add(tUID(swapper - 1)), w.add(tUID(window - 1))}))
	connsDoc := w.add(tDict([]string{"$class", "NS.objects"},
		[]int{w.add(tDict([]string{"$classname"}, []int{w.add(tStr("NSMutableArray"))})), w.add(tArr(conn))}))
	// assemble root/top/objects
	topRefs := []int{w.add(tUID(connsDoc - 1))}
	topIdx := w.add(tDict([]string{"UINibConnectionsKey"}, topRefs))
	arr := []int{1}
	for i := 2; i < len(w.objs); i++ {
		arr = append(arr, i)
	}
	arrIdx := w.add(tArr(arr...))
	verIdx := w.add(tInt(100000))
	archIdx := w.add(tStr("NSKeyedArchiver"))
	w.objs[0] = tDict([]string{"$version", "$archiver", "$top", "$objects"},
		[]int{verIdx, archIdx, topIdx, arrIdx})
	return w.build()
}

func TestParseLegacyMacOS(t *testing.T) {
	w := &plistWriter{}
	w.add(tDict(nil, nil))
	w.add(tStr("$null"))
	ownerName := w.add(tStr("AppController"))
	owner := w.add(tDict([]string{"$class", "NSClassName"},
		[]int{w.add(tDict([]string{"$classname"}, []int{w.add(tStr("NSCustomObject"))})), w.add(tUID(ownerName - 1))}))
	field := w.add(tDict([]string{"$class"},
		[]int{w.add(tDict([]string{"$classname"}, []int{w.add(tStr("NSTextField"))}))}))
	label := w.add(tStr("textField"))
	conn := w.add(tDict([]string{"$class", "NSLabel", "NSSource", "NSDestination"},
		[]int{w.add(tDict([]string{"$classname"}, []int{w.add(tStr("NSNibOutletConnector"))})),
			w.add(tUID(label - 1)), w.add(tUID(owner - 1)), w.add(tUID(field - 1))}))
	connsArr := w.add(tArr(conn))
	doc := w.add(tDict([]string{"$class", "NSConnections"},
		[]int{w.add(tDict([]string{"$classname"}, []int{w.add(tStr("NSIBObjectData"))})), connsArr}))
	topIdx := w.add(tDict([]string{"IB.objectdata"}, []int{w.add(tUID(doc - 1))}))
	arr := []int{1}
	for i := 2; i < len(w.objs); i++ {
		arr = append(arr, i)
	}
	arrIdx := w.add(tArr(arr...))
	verIdx := w.add(tInt(100000))
	archIdx := w.add(tStr("NSKeyedArchiver"))
	w.objs[0] = tDict([]string{"$version", "$archiver", "$top", "$objects"},
		[]int{verIdx, archIdx, topIdx, arrIdx})
	buf := w.build()

	a, err := Parse(buf)
	if err != nil {
		t.Fatalf("parse legacy macOS: %v", err)
	}
	conns := a.Connections()
	if len(conns) != 1 {
		t.Fatalf("want 1 connection, got %d: %+v", len(conns), conns)
	}
	if conns[0].Kind != "outlet" || conns[0].Name != "textField" ||
		conns[0].Source != "AppController" || conns[0].Destination != "NSTextField" {
		t.Errorf("macOS outlet mis-parsed: %+v", conns[0])
	}
	cc := a.CustomClasses()
	if len(cc) != 1 || cc[0].Class != "AppController" {
		t.Errorf("macOS classes mis-parsed: %+v", cc)
	}
}

// ---------- rejection / robustness ----------

func TestParseRejectsGarbage(t *testing.T) {
	cases := [][]byte{
		nil,
		[]byte("garbage not a nib at all"),
		[]byte("NIBArchive"),           // header too short
		bytes.Repeat([]byte{0xFF}, 50), // magic mismatch + huge counts
		[]byte("bplist00"),             // truncated plist
		[]byte("bplist00" + strings.Repeat("\x00", 32)),
	}
	for _, c := range cases {
		a, err := Parse(c)
		if err == nil && a != nil {
			t.Errorf("accepted garbage: %q", c)
		}
	}
}

func TestParseTruncatedModern(t *testing.T) {
	full := modernOutletNib()
	for _, cut := range []int{1, 25, 49, 51, 60, len(full) - 1} {
		if cut >= len(full) {
			continue
		}
		a, err := Parse(full[:cut])
		if err == nil && a != nil {
			t.Errorf("accepted truncation at %d", cut)
		}
	}
}

// ---------- output hardening ----------

func TestSanitizeEscapesControlBytes(t *testing.T) {
	in := "a\x1b]52;c;pwned\x07b\tc\nd"
	want := "a\\x1b]52;c;pwned\\x07b\tc\nd"
	if got := sanitize(in); got != want {
		t.Errorf("sanitize: got %q want %q", got, want)
	}
	if got := sanitize("clean\x7f"); got != "clean\\x7f" {
		t.Errorf("DEL not escaped: %q", got)
	}
	if got := sanitize("plain"); got != "plain" {
		t.Errorf("clean string mutated: %q", got)
	}
}

func TestHostileNibEmitsNoRawEscape(t *testing.T) {
	w := &nibWriter{}
	esc := w.obj("NSString", kstr("NS.bytes", "\x1b]52;c;pwned\x07"))
	w.obj("UIRuntimeOutletConnection",
		kobj("UILabel", esc),
		kobj("UISource", 2),
		kobj("UIDestination", 2),
	)
	w.obj("UIWindow")
	a, err := Parse(w.build())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, c := range a.Connections() {
		if strings.ContainsRune(c.Name, 0x1b) {
			t.Errorf("raw ESC survived into connection name: %q", c.Name)
		}
	}
}

func TestFridaGateDropsHostileNames(t *testing.T) {
	w := &nibWriter{}
	w.obj("UIClassSwapper", kobj("UIClassName", w.obj("NSString", kstr("NS.bytes", "Evil'Quote"))))
	w.obj("UIRuntimeEventConnection",
		kobj("UILabel", w.obj("NSString", kstr("NS.bytes", "ok"))),
		kobj("UISource", 2),
		kobj("UIDestination", 2),
		kint("UIEventMask", 64),
	)
	w.obj("UIWindow")
	a, err := Parse(w.build())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	blobs := []Blob{{Label: "x.nib", data: w.build(), Arc: a}}
	var out bytes.Buffer
	EmitFrida(&out, blobs)
	s := out.String()
	if strings.Contains(s, "Evil'Quote") {
		t.Error("quote-bearing class name leaked into Frida output")
	}
}
