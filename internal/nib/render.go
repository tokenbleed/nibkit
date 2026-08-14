package nib

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Out is where every emitter writes. The CLI points it at stdout (or the
// pager pipe); the MCP server redirects it per call to capture output.
var Out io.Writer = os.Stdout

// Blob is one discovered nib: its display label, raw bytes and parsed archive.
type Blob struct {
	Label string
	data  []byte
	Arc   *Archive
	err   error
}

// ParseAll parses every blob's raw bytes in place, recording per-blob parse
// status. Returns true if any blob failed.
func ParseAll(blobs []Blob) bool {
	hadErr := false
	for i := range blobs {
		arc, perr := Parse(blobs[i].data)
		blobs[i].Arc = arc
		blobs[i].err = perr
		if perr != nil {
			hadErr = true
		}
	}
	return hadErr
}

func Discover(path string) ([]Blob, func(), error) {
	if strings.EqualFold(filepath.Ext(path), ".ipa") {
		appDir, cleanup, err := extractIPA(path)
		if err != nil {
			return nil, nil, err
		}
		blobs, werr := walkDir(appDir)
		if werr != nil {
			cleanup()
			return nil, nil, werr
		}
		return blobs, cleanup, nil
	}

	st, err := os.Stat(path)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %v", path, err)
	}
	if !st.IsDir() {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %v", path, err)
		}
		if !isNibData(data) {
			return nil, nil, fmt.Errorf("%s: not a NIBArchive or keyedarchive nib", path)
		}
		return []Blob{{Label: filepath.Base(path), data: data}}, nil, nil
	}
	blobs, err := walkDir(path)
	if err != nil {
		return nil, nil, err
	}
	return blobs, nil, nil
}

func walkDir(root string) ([]Blob, error) {
	var out []Blob
	werr := filepath.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			return nil
		}
		if strings.ToLower(filepath.Ext(p)) != ".nib" {
			return nil
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		if !isNibData(data) {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		out = append(out, Blob{Label: rel, data: data})
		return nil
	})
	if werr != nil {
		return nil, fmt.Errorf("%s: %v", root, werr)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no NIBArchive .nib files found")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out, nil
}

func extractIPA(path string) (string, func(), error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", nil, fmt.Errorf("open %s: %v", filepath.Base(path), err)
	}
	tmp, err := os.MkdirTemp("", "nibkit-ipa-*")
	if err != nil {
		zr.Close()
		return "", nil, err
	}
	cleanup := func() { os.RemoveAll(tmp) }
	root := filepath.Clean(tmp)
	// Zip-bomb guard: total decompressed budget = 4x the compressed size,
	// floor 256MB. A small utility ipa (<64MB) never legitimately decompresses
	// past ~5x its size, and big ipas get a proportional budget (a 500MB ipa
	// gets 2GB). A 10MB zip inflating to gigabytes is rejected after 40MB.
	var total int64
	budget := int64(256 << 20)
	if st, serr := os.Stat(path); serr == nil && st.Size()*4 > budget {
		budget = st.Size() * 4
	}
	for _, f := range zr.File {
		fp := filepath.Join(tmp, f.Name)
		if !strings.HasPrefix(filepath.Clean(fp)+string(os.PathSeparator), root+string(os.PathSeparator)) {
			continue // zip-slip: escapes tmp
		}
		if f.FileInfo().IsDir() {
			os.MkdirAll(fp, 0o755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
			zr.Close()
			cleanup()
			return "", nil, err
		}
		out, err := os.Create(fp)
		if err != nil {
			zr.Close()
			cleanup()
			return "", nil, err
		}
		rc, err := f.Open()
		if err != nil {
			out.Close()
			zr.Close()
			cleanup()
			return "", nil, err
		}
		n, err := io.CopyN(out, rc, budget-total)
		rc.Close()
		out.Close()
		total += n
		if err == nil && total >= budget {
			zr.Close()
			cleanup()
			return "", nil, fmt.Errorf("%s: zip decompresses beyond %dMB (zip bomb?)", filepath.Base(path), budget>>20)
		}
		if err != nil && err != io.EOF {
			zr.Close()
			cleanup()
			return "", nil, err
		}
	}
	zr.Close()
	matches, _ := filepath.Glob(filepath.Join(tmp, "Payload", "*.app"))
	if len(matches) == 0 {
		return tmp, cleanup, nil
	}
	return matches[0], cleanup, nil
}

// ==================== shared render helpers ====================

func splitWiring(arc *Archive) (outs, acts []conn, ra []runtimeAttr) {
	outs, acts = []conn{}, []conn{}
	for _, c := range arc.Connections() {
		if c.Kind == "outlet" {
			outs = append(outs, c)
		} else {
			acts = append(acts, c)
		}
	}
	ra = arc.RuntimeAttributes()
	if ra == nil {
		ra = []runtimeAttr{}
	}
	return
}

func HeaderLine(label string, arc *Archive) string {
	fmtKind := fmt.Sprintf("format %d, coder %d", arc.Major, arc.Minor)
	if arc.Keyed {
		fmtKind = "NSKeyedArchive (pre-2012)"
	}
	return fmt.Sprintf("# %s  (%s)  %d objs / %d vals / %d keys / %d classes",
		label, fmtKind, len(arc.Objects), len(arc.Values), len(arc.Keys), len(arc.Classes))
}

// isNibData accepts both modern NIBArchive nibs and legacy keyedarchive nibs.
func isNibData(data []byte) bool {
	if len(data) >= 10 && string(data[:10]) == magic {
		return true
	}
	return len(data) >= 8 && string(data[:6]) == "bplist"
}

// startPager pipes stdout through $PAGER (default less -RFX) when stdout is a
// terminal, so long reports scroll/search cleanly. Piped output stays raw.
// Uses an os.Pipe so the write end is a real *os.File the printer can target.
func StartPager() func() {
	realStdout := os.Stdout
	fi, err := realStdout.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return func() {}
	}
	pager := firstNonEmpty(os.Getenv("NIBKIT_PAGER"), os.Getenv("PAGER"), "less -RFX")
	if pager == "cat" || pager == "" {
		return func() {}
	}
	r, w, err := os.Pipe()
	if err != nil {
		return func() {}
	}
	cmd := exec.Command("sh", "-c", pager)
	cmd.Stdin = r
	cmd.Stdout = realStdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		r.Close()
		w.Close()
		return func() {}
	}
	r.Close() // child holds the read end; parent only needs the write end
	os.Stdout = w
	Out = w
	return func() {
		os.Stdout = realStdout
		Out = realStdout
		w.Close()
		cmd.Wait()
	}
}

// renderTable prints aligned columns that shrink to the terminal width.
func renderTable(headers []string, rows [][]string) {
	// backstop: nib-controlled strings must never reach the terminal raw
	for _, r := range rows {
		for i := range r {
			r[i] = sanitize(r[i])
		}
	}
	rw := func(s string) int {
		n := 0
		for range s {
			n++
		}
		return n
	}
	nc := len(headers)
	width := make([]int, nc)
	for i, h := range headers {
		width[i] = rw(h)
	}
	for _, r := range rows {
		for i := 0; i < nc && i < len(r); i++ {
			if w := rw(r[i]); w > width[i] {
				width[i] = w
			}
		}
	}
	const minCol, gap, indent = 6, 2, 2
	maxW := termWidth() - indent - (nc-1)*gap
	total := 0
	for _, x := range width {
		total += x
	}
	// shrink columns to fit, but never the last (it holds findings/long text)
	for total > maxW {
		big := -1
		for i := 0; i < nc-1; i++ {
			if width[i] > minCol && (big < 0 || width[i] > width[big]) {
				big = i
			}
		}
		if big < 0 {
			break
		}
		width[big]--
		total--
	}
	cell := func(s string, w int, last bool) string {
		if rw(s) > w {
			if w <= 1 {
				return "…"
			}
			s = string([]rune(s)[:w-1]) + "…"
		}
		if last {
			return s
		}
		pad := w - rw(s)
		if pad < 0 {
			pad = 0
		}
		return s + strings.Repeat(" ", pad) + strings.Repeat(" ", gap)
	}
	emit := func(cells []string) {
		var b strings.Builder
		b.WriteString(strings.Repeat(" ", indent))
		for i := 0; i < nc; i++ {
			c := ""
			if i < len(cells) {
				c = cells[i]
			}
			b.WriteString(cell(c, width[i], i == nc-1))
		}
		fmt.Fprintln(Out, b.String())
	}
	emit(headers)
	for _, r := range rows {
		emit(r)
	}
}

// ==================== text output ====================

func EmitText(blobs []Blob, cmd string, hadErr bool) int {
	defer StartPager()()
	for _, b := range blobs {
		if b.err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", b.Label, b.err)
			continue
		}
		fmt.Fprintln(Out, HeaderLine(b.Label, b.Arc))
		switch cmd {
		case "info":
		case "tree":
			b.Arc.PrintTree(b.Arc.BuildGraph(0, map[int]bool{}), 0)
		case "wiring":
			printWiringText(b.Arc)
		case "classes":
			printClassesText(b.Arc)
		case "segues":
			printNavText(b.Arc)
		case "all":
			printClassesText(b.Arc)
			printWiringText(b.Arc)
			printNavText(b.Arc)
		}
	}
	if hadErr {
		return 1
	}
	return 0
}

func printWiringText(arc *Archive) {
	outlets, actions, attrs := splitWiring(arc)
	if len(outlets)+len(actions) > 0 {
		fmt.Fprintln(Out, "WIRING")
		var rows [][]string
		for _, c := range outlets {
			rows = append(rows, []string{"outlet", c.Name, c.Source, c.Destination})
		}
		for _, c := range actions {
			dest := c.Destination
			if c.Kind == "action" && c.Event != "" {
				dest += " [" + c.Event + "]"
			}
			rows = append(rows, []string{c.Kind, c.Name, c.Source, dest})
		}
		renderTable([]string{"TYPE", "SELECTOR/OUTLET", "SOURCE", "DESTINATION"}, rows)
	}
	if len(attrs) > 0 {
		fmt.Fprintln(Out, "RUNTIME ATTRS")
		var rows [][]string
		for _, a := range attrs {
			rows = append(rows, []string{a.Object, a.KeyPath, a.Value})
		}
		renderTable([]string{"OBJECT", "KEYPATH", "VALUE"}, rows)
	}
	if len(outlets)+len(actions) == 0 && len(attrs) == 0 {
		fmt.Fprintln(Out, "(no wiring)")
	}
}

func printClassesText(arc *Archive) {
	cs := arc.CustomClasses()
	fmt.Fprintln(Out, "CLASSES")
	if len(cs) == 0 {
		fmt.Fprintln(Out, "  (none)")
		return
	}
	var rows [][]string
	for _, c := range cs {
		rows = append(rows, []string{c.Class, c.Base, c.SceneID})
	}
	renderTable([]string{"CLASS", "BASE", "SCENE ID"}, rows)
}

func edgeSource(e navEdge) string {
	if e.SrcID != "" {
		return e.SrcID
	}
	return e.SrcClass
}

func edgeDest(e navEdge) string {
	return firstNonEmpty(e.DstID, e.DstClass, "-")
}

func printNavText(arc *Archive) {
	edges := arc.NavEdges()
	fmt.Fprintln(Out, "NAVIGATION")
	if len(edges) == 0 {
		fmt.Fprintln(Out, "  (none)")
		return
	}
	var rows [][]string
	for _, e := range edges {
		id := firstNonEmpty(e.Identifier, e.CustomClass, "-")
		rows = append(rows, []string{e.Kind, edgeSource(e), edgeDest(e), id, e.Selector})
	}
	renderTable([]string{"KIND", "SOURCE", "DESTINATION", "IDENTIFIER", "SELECTOR"}, rows)
	for _, e := range edges {
		for k, v := range e.Details {
			fmt.Fprintf(Out, "    %s = %s\n", k, v)
		}
	}
}

// ==================== JSON output ====================

func EmitJSON(blobs []Blob, cmd string, hadErr bool) int {
	docs := make([]map[string]interface{}, 0, len(blobs))
	for _, b := range blobs {
		if b.err != nil {
			docs = append(docs, map[string]interface{}{"file": b.Label, "error": b.err.Error()})
			continue
		}
		doc := map[string]interface{}{
			"file":          b.Label,
			"formatVersion": b.Arc.Major,
			"coderVersion":  b.Arc.Minor,
		}
		switch cmd {
		case "info":
			doc["counts"] = map[string]int{
				"objects": len(b.Arc.Objects), "values": len(b.Arc.Values),
				"keys": len(b.Arc.Keys), "classes": len(b.Arc.Classes),
			}
		case "tree":
			doc["graph"] = b.Arc.BuildGraph(0, map[int]bool{})
		case "wiring", "all":
			outs, acts, ra := splitWiring(b.Arc)
			doc["outlets"] = outs
			doc["actions"] = acts
			doc["runtimeAttrs"] = ra
			if cmd == "wiring" {
				break
			}
			doc["classes"] = nonNilClasses(b.Arc)
			doc["navigation"] = nonNilNav(b.Arc)
		case "classes":
			doc["classes"] = nonNilClasses(b.Arc)
		case "segues":
			doc["navigation"] = nonNilNav(b.Arc)
		}
		docs = append(docs, doc)
	}
	var out interface{}
	if len(docs) == 1 {
		out = docs[0]
	} else {
		out = docs
	}
	enc := json.NewEncoder(Out)
	enc.SetIndent("", "  ")
	enc.Encode(out)
	if hadErr {
		return 1 // corrupt blobs are embedded as {"error":...}; still signal failure
	}
	return 0
}

func nonNilClasses(a *Archive) []customClass {
	c := a.CustomClasses()
	if c == nil {
		c = []customClass{}
	}
	return c
}
func nonNilNav(a *Archive) []navEdge {
	e := a.NavEdges()
	if e == nil {
		e = []navEdge{}
	}
	return e
}

// ==================== Mermaid navigation graph ====================

func EmitMermaid(blobs []Blob) {
	defer StartPager()()
	fmt.Fprintln(Out, "flowchart LR")
	ids := map[string]string{}
	node := func(label string) string {
		if id, ok := ids[label]; ok {
			return id
		}
		id := "n" + strconv.Itoa(len(ids))
		ids[label] = id
		safe := strings.ReplaceAll(label, "\"", "'")
		fmt.Fprintf(Out, "  %s[\"%s\"]\n", id, safe)
		return id
	}
	for _, b := range blobs {
		if b.Arc == nil {
			continue
		}
		for _, e := range b.Arc.NavEdges() {
			src := node(edgeSource(e))
			dst := node(edgeDest(e))
			lbl := firstNonEmpty(e.Identifier, e.CustomClass, e.Kind)
			fmt.Fprintf(Out, "  %s -->|%s| %s\n", src, lbl, dst)
		}
	}
}

// ==================== Frida codegen ====================

type fridaHook struct {
	file, selector, source, dest, event string
}

func isPlaceholder(s string) bool {
	return strings.HasSuffix(s, "(proxy)") || strings.Contains(s, "Placeholder")
}

func resolveImpl(dest string, candidates []customClass) string {
	if !isPlaceholder(dest) {
		return dest
	}
	var vcs, others []string
	for _, c := range candidates {
		if strings.Contains(c.Base, "ViewController") {
			vcs = append(vcs, c.Class)
		} else {
			others = append(others, c.Class)
		}
	}
	if len(vcs) == 1 {
		return vcs[0]
	}
	if len(vcs) == 0 && len(others) == 1 {
		return others[0]
	}
	return ""
}

func EmitFrida(w io.Writer, blobs []Blob) {
	// safeName gates what may be interpolated into generated JS: selectors,
	// class names and labels from a hostile nib must not reach hooks.js raw.
	safeName := func(s string) bool {
		if s == "" {
			return false
		}
		for i := 0; i < len(s); i++ {
			b := s[i]
			if b < 0x21 || b > 0x7e || b == 0x27 || b == 0x22 || b == 0x5c || b == 0x2f {
				return false
			}
		}
		return true
	}
	var hooks []fridaHook
	var candidates []customClass
	seen := map[string]bool{}
	for _, b := range blobs {
		if b.Arc == nil {
			continue
		}
		for _, c := range b.Arc.Connections() {
			if c.Kind == "action" && safeName(c.Name) && safeName(c.Source) && safeName(c.Destination) {
				hooks = append(hooks, fridaHook{b.Label, c.Name, c.Source, c.Destination, c.Event})
			}
		}
		for _, cc := range b.Arc.CustomClasses() {
			if !seen[cc.Class] && safeName(cc.Class) {
				seen[cc.Class] = true
				candidates = append(candidates, cc)
			}
		}
	}
	fmt.Fprintln(w, "// nibkit "+Version+" - generated Frida hooks for @IBAction handlers")
	fmt.Fprintln(w, "// attach each target-action selector on the implementing class")
	fmt.Fprintln(w, "// usage: frida -U -f <bundle-id> -l hooks.js")
	fmt.Fprintln(w)
	if len(hooks) == 0 {
		fmt.Fprintln(w, "// no @IBAction connections found in input")
		return
	}
	if len(candidates) > 0 {
		fmt.Fprintln(w, "// candidate implementing classes (from nibkit classes):")
		for _, c := range candidates {
			fmt.Fprintf(w, "//   %s (%s)\n", c.Class, c.Base)
		}
		fmt.Fprintln(w)
	}
	for _, h := range hooks {
		ev := h.event
		if ev != "" {
			ev = " [" + ev + "]"
		}
		impl := resolveImpl(h.dest, candidates)
		if impl != "" {
			fmt.Fprintf(w, "// %s%s  %s -> %s   (%s)\n", h.selector, ev, h.source, impl, h.file)
			fmt.Fprintf(w, "try { (function () {\n")
			fmt.Fprintf(w, "    var C = ObjC.classes[%q];\n", impl)
			fmt.Fprintf(w, "    if (C && C[%q]) {\n", "- "+h.selector)
			fmt.Fprintf(w, "        Interceptor.attach(C[%q].implementation, {\n", "- "+h.selector)
			fmt.Fprintf(w, "            onEnter: function (a) { console.log('[+] %s %s'); }\n", impl, h.selector)
			fmt.Fprintf(w, "        });\n")
			fmt.Fprintf(w, "    }\n")
			fmt.Fprintf(w, "})(); } catch (e) {}\n\n")
		} else {
			fmt.Fprintf(w, "// %s%s  %s -> <unresolved placeholder: %s>   (%s)\n", h.selector, ev, h.source, h.dest, h.file)
			fmt.Fprintf(w, "// TODO: set implementing class, then uncomment\n")
			fmt.Fprintf(w, "// try { (function () {\n")
			fmt.Fprintf(w, "//     var C = ObjC.classes[%q];\n", "<CLASS>")
			fmt.Fprintf(w, "//     if (C && C[%q]) {\n", "- "+h.selector)
			fmt.Fprintf(w, "//         Interceptor.attach(C[%q].implementation, {\n", "- "+h.selector)
			fmt.Fprintf(w, "//         onEnter: function (a) { console.log('[+] <CLASS> %s'); }\n", h.selector)
			fmt.Fprintf(w, "//         });\n")
			fmt.Fprintf(w, "//     }\n")
			fmt.Fprintf(w, "// })(); } catch (e) {}\n\n")
		}
	}
}
