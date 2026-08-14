package main

import (
	"archive/zip"
	"bufio"
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

const version = "1.2.0"

type blob struct {
	label string
	data  []byte
	arc   *Archive
	err   error
}

func main() {
	os.Exit(run(os.Args[1:]))
}

type cli struct {
	cmd        string
	paths      []string
	jsonOut    bool
	fridaOut   bool
	mermaidOut bool
}

func run(args []string) int {
	if len(args) == 0 && isTerminal() {
		return runInteractive()
	}
	c, err := parseArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		usage()
		return 2
	}

	var blobs []blob
	for _, p := range c.paths {
		bs, cleanup, derr := discover(p)
		if derr != nil {
			fmt.Fprintln(os.Stderr, derr)
			continue
		}
		if cleanup != nil {
			cleanup()
		}
		blobs = append(blobs, bs...)
	}
	if len(blobs) == 0 {
		if len(c.paths) > 1 {
			fmt.Fprintln(os.Stderr, "error: no NIBArchive .nib files found in input")
		}
		return 1
	}

	hadErr := false
	for i := range blobs {
		arc, perr := parse(blobs[i].data)
		blobs[i].arc = arc
		blobs[i].err = perr
		if perr != nil {
			hadErr = true
		}
	}

	if c.mermaidOut {
		emitMermaid(blobs)
		if hadErr {
			return 1
		}
		return 0
	}
	if c.fridaOut {
		emitFrida(os.Stdout, blobs)
		if hadErr {
			return 1
		}
		return 0
	}
	if c.jsonOut {
		return emitJSON(blobs, c.cmd, hadErr)
	}
	return emitText(blobs, c.cmd, hadErr)
}

// ==================== interactive mode ====================

const banner = `nibkit ` + version + ` - NIBArchive decompiler (.nib / .storyboardc / .app / .ipa)
.ipa files are extracted automatically. drop in a path and pick an action.

sample commands (this menu is optional; the CLI works directly):
  nibkit wiring Foo.app              outlets + @IBAction selectors + runtime attrs
  nibkit classes Foo.ipa             custom Interface Builder classes
  nibkit segues --mermaid Foo.app    navigation graph as Mermaid
  nibkit wiring --frida Foo.ipa      Frida hooks for @IBAction handlers
  type 'help' anywhere for the full command list.`

func isTerminal() bool {
	// True isatty on both ends: TIOCGWINSZ fails with ENOTTY on pipes, files,
	// and /dev/null (a char device), so the interactive menu only starts on a
	// real console (a winsize-less pty still counts).
	return isTTY(os.Stdin.Fd()) && isTTY(os.Stdout.Fd())
}

func runInteractive() int {
	fmt.Println(banner)
	r := bufio.NewReader(os.Stdin)
	for {
		fmt.Println()
		fmt.Println("Path to a .ipa / .app / .storyboardc / .nib / directory")
		fmt.Println("(drag one in, 'help' for commands, blank to quit):")
		fmt.Print("> ")
		line := cleanPath(readline(r))
		if line == "" {
			return 0
		}
		if isHelpWord(line) {
			printHelp()
			continue
		}
		blobs, cleanup, err := discover(line)
		if err != nil {
			fmt.Fprintln(os.Stderr, "  error:", err)
			continue
		}
		if cleanup != nil {
			cleanup()
		}
		var bad int
		for i := range blobs {
			arc, perr := parse(blobs[i].data)
			blobs[i].arc = arc
			blobs[i].err = perr
			if perr != nil {
				bad++
			}
		}
		fmt.Printf("\n  loaded %d NIBArchive file(s)", len(blobs)-bad)
		if bad > 0 {
			fmt.Printf(" (%d skipped)", bad)
		}
		fmt.Println()

		// action loop: every action returns here, so any number of actions can
		// run against the same bundle without re-answering prompts.
		for {
			fmt.Println()
			printMenu()
			fmt.Print("> ")
			choice := strings.ToLower(readline(r))
			if isHelpWord(choice) {
				printHelp()
				continue
			}
			if choice == "" || choice == "q" || choice == "quit" || choice == "exit" {
				return 0
			}
			if choice == "b" || choice == "back" {
				break // back to the path prompt
			}
			doAction(r, choice, blobs)
		}
	}
}

func isHelpWord(s string) bool {
	switch s {
	case "help", "commands", "h", "?":
		return true
	}
	return false
}

func printHelp() {
	fmt.Println(`
interactive actions (number or word):
  1 tree      object graph with resolved classes
  2 wiring    outlets + @IBAction selectors + runtime attributes
  3 classes   custom Interface Builder classes
  4 nav       segue + container navigation graph
  5 frida     @IBAction hooks -> hooks.js
  6 json      export structured data
  7 mermaid   navigation graph as Mermaid (renders on GitHub)
  b back      load a different file      q quit

CLI commands (same features, scriptable):
  nibkit [tree] <path>              object tree + header (aliases: dump)
  nibkit info <path>                header counts only
  nibkit wiring <path>              outlets/actions/runtime attrs
  nibkit classes <path>             custom IB classes
  nibkit segues <path>              navigation graph
  nibkit all <path>                 classes + wiring + navigation
  nibkit wiring --frida <path>      write Frida hook stubs to stdout
  nibkit segues --mermaid <path>    Mermaid flowchart
  nibkit -J <cmd> <path>            JSON (object per nib, array when many)
paths: .ipa (auto-extracted) / .app / .storyboardc / .nib / directory
flags: NIBKIT_PAGER / PAGER override the pager (cat disables).`)
}

func printMenu() {
	fmt.Println(`actions (help = full command list, b = different file, q = quit):
  1  tree      object graph
  2  wiring    outlets + @IBAction selectors + runtime attributes
  3  classes   custom Interface Builder classes
  4  nav       segue + container navigation graph
  5  frida     @IBAction hooks -> hooks.js
  6  json      export structured data
  7  mermaid   navigation graph as Mermaid (renders on GitHub)`)
}

func doAction(r *bufio.Reader, choice string, blobs []blob) {
	switch choice {
	case "1", "tree", "dump":
		emitText(blobs, "tree", false)
	case "2", "wiring":
		emitText(blobs, "wiring", false)
	case "3", "classes":
		emitText(blobs, "classes", false)
	case "4", "nav", "segues":
		emitText(blobs, "segues", false)
	case "5", "frida":
		writeFrida(blobs)
	case "6", "json":
		emitJSON(blobs, "all", false)
	case "7", "mermaid":
		emitMermaid(blobs)
	default:
		fmt.Fprintln(os.Stderr, "  unknown action; pick 1-7, or help / b / q")
	}
}

func writeFrida(blobs []blob) {
	var n int
	for _, b := range blobs {
		if b.arc == nil {
			continue
		}
		for _, c := range b.arc.connections() {
			if c.Kind == "action" {
				n++
			}
		}
	}
	if n == 0 {
		fmt.Println("  no @IBAction connections found; nothing to hook")
		return
	}
	f, err := os.Create("hooks.js")
	if err != nil {
		fmt.Fprintln(os.Stderr, "  error:", err)
		return
	}
	emitFrida(f, blobs)
	f.Close()
	fmt.Printf("  wrote hooks.js (%d hook(s)); run: frida -U -f <bundle-id> -l hooks.js\n", n)
}

func readline(r *bufio.Reader) string {
	s, _ := r.ReadString('\n')
	return strings.TrimSpace(s)
}

func cleanPath(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\"'")
	s = strings.ReplaceAll(s, `\ `, " ")
	return s
}

// ==================== arg parsing (non-interactive) ====================

// realCmd maps a subcommand (or alias) to an internal command name.
var realCmd = map[string]struct {
	cmd   string
	isJos bool
}{
	"wiring":  {"wiring", false},
	"classes": {"classes", false},
	"segues":  {"segues", false},
	"all":     {"all", false},
	"dump":    {"tree", false},
	"tree":    {"tree", false},
	"info":    {"info", false},
	"json":    {"tree", true},
}

func parseArgs(args []string) (cli, error) {
	var c cli
	var pos []string
	for _, a := range args {
		switch a {
		case "-h", "--help":
			usage()
			os.Exit(0)
		case "-V", "--version":
			fmt.Println("nibkit " + version)
			os.Exit(0)
		case "-J", "--json":
			c.jsonOut = true
		case "--frida":
			c.fridaOut = true
		case "--mermaid":
			c.mermaidOut = true
		default:
			if strings.HasPrefix(a, "-") {
				return c, fmt.Errorf("unknown flag: %s", a)
			}
			pos = append(pos, a)
		}
	}
	if len(pos) == 0 {
		return c, fmt.Errorf("error: input path required")
	}
	if r, ok := realCmd[pos[0]]; ok {
		c.cmd = r.cmd
		if r.isJos {
			c.jsonOut = true
		}
		c.paths = pos[1:]
	} else {
		c.cmd = "tree"
		c.paths = pos
	}
	if len(c.paths) == 0 {
		return c, fmt.Errorf("error: input path required")
	}
	if c.fridaOut && c.cmd != "wiring" {
		return c, fmt.Errorf("error: --frida only applies to the 'wiring' command")
	}
	if c.mermaidOut && c.cmd != "segues" {
		return c, fmt.Errorf("error: --mermaid only applies to the 'segues' command")
	}
	return c, nil
}

func usage() {
	fmt.Fprint(os.Stderr, `nibkit - NIBArchive (.nib / .storyboardc / .app / .ipa) decompiler

USAGE
  nibkit                              # interactive menu (no args)
  nibkit [command] <path...> [flags]

COMMANDS
  (default)   object tree with header          (aliases: dump, tree)
  wiring      outlets, @IBAction selectors, runtime attributes
  classes     custom (UIClassSwapper) classes
  segues      navigation graph (segues + container children)
  all         classes + wiring + navigation in one report
  info        header counts only

INPUT
  path          a .ipa (auto-extracted), .nib file, .nib/.storyboardc/.app bundle,
                or any directory (recursively walked for NIBArchive .nib files).
                Multiple paths are aggregated.

FLAGS
  -J, --json     emit JSON (single object for one blob, array for many)
      --frida    generate Frida hook stubs from @IBAction wiring (wiring only)
      --mermaid  emit the navigation graph as a Mermaid flowchart (segues only)
  -V, --version  print version and exit
  -h, --help     show this help

EXAMPLES
  nibkit                              # interactive menu
  nibkit Foo.ipa                      # tree (ipa auto-extracted)
  nibkit segues Foo.app               # navigation graph
  nibkit segues --mermaid Foo.app     # Mermaid flowchart
  nibkit -J segues Foo.app | jq '.[] | .navigation'
`)
}

// ==================== discovery ====================

func discover(path string) ([]blob, func(), error) {
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
		if len(data) < 10 || string(data[:10]) != magic {
			return nil, nil, fmt.Errorf("%s: not a NIBArchive (magic mismatch)", path)
		}
		return []blob{{label: filepath.Base(path), data: data}}, nil, nil
	}
	blobs, err := walkDir(path)
	if err != nil {
		return nil, nil, err
	}
	return blobs, nil, nil
}

func walkDir(root string) ([]blob, error) {
	var out []blob
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
		if len(data) < 10 || string(data[:10]) != magic {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		out = append(out, blob{label: rel, data: data})
		return nil
	})
	if werr != nil {
		return nil, fmt.Errorf("%s: %v", root, werr)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no NIBArchive .nib files found")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].label < out[j].label })
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
	for _, c := range arc.connections() {
		if c.Kind == "outlet" {
			outs = append(outs, c)
		} else {
			acts = append(acts, c)
		}
	}
	ra = arc.runtimeAttributes()
	if ra == nil {
		ra = []runtimeAttr{}
	}
	return
}

func headerLine(label string, arc *Archive) string {
	return fmt.Sprintf("# %s  (format %d, coder %d)  %d objs / %d vals / %d keys / %d classes",
		label, arc.Major, arc.Minor, len(arc.Objects), len(arc.Values), len(arc.Keys), len(arc.Classes))
}

// startPager pipes stdout through $PAGER (default less -RFX) when stdout is a
// terminal, so long reports scroll/search cleanly. Piped output stays raw.
// Uses an os.Pipe so the write end is a real *os.File the printer can target.
func startPager() func() {
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
	return func() {
		os.Stdout = realStdout
		w.Close()
		cmd.Wait()
	}
}

// renderTable prints aligned columns that shrink to the terminal width.
func renderTable(headers []string, rows [][]string) {
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
		fmt.Println(b.String())
	}
	emit(headers)
	for _, r := range rows {
		emit(r)
	}
}

// ==================== text output ====================

func emitText(blobs []blob, cmd string, hadErr bool) int {
	defer startPager()()
	for _, b := range blobs {
		if b.err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", b.label, b.err)
			continue
		}
		fmt.Println(headerLine(b.label, b.arc))
		switch cmd {
		case "info":
		case "tree":
			b.arc.printTree(b.arc.buildGraph(0, map[int]bool{}), 0)
		case "wiring":
			printWiringText(b.arc)
		case "classes":
			printClassesText(b.arc)
		case "segues":
			printNavText(b.arc)
		case "all":
			printClassesText(b.arc)
			printWiringText(b.arc)
			printNavText(b.arc)
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
		fmt.Println("WIRING")
		var rows [][]string
		for _, c := range outlets {
			rows = append(rows, []string{"outlet", c.Name, c.Source, c.Destination})
		}
		for _, c := range actions {
			ev := c.Event
			if ev != "" {
				c.Destination += " [" + ev + "]"
			}
			rows = append(rows, []string{"action", c.Name, c.Source, c.Destination})
		}
		renderTable([]string{"TYPE", "SELECTOR/OUTLET", "SOURCE", "DESTINATION"}, rows)
	}
	if len(attrs) > 0 {
		fmt.Println("RUNTIME ATTRS")
		var rows [][]string
		for _, a := range attrs {
			rows = append(rows, []string{a.Object, a.KeyPath, a.Value})
		}
		renderTable([]string{"OBJECT", "KEYPATH", "VALUE"}, rows)
	}
	if len(outlets)+len(actions) == 0 && len(attrs) == 0 {
		fmt.Println("(no wiring)")
	}
}

func printClassesText(arc *Archive) {
	cs := arc.customClasses()
	fmt.Println("CLASSES")
	if len(cs) == 0 {
		fmt.Println("  (none)")
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
	edges := arc.navEdges()
	fmt.Println("NAVIGATION")
	if len(edges) == 0 {
		fmt.Println("  (none)")
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
			fmt.Printf("    %s = %s\n", k, v)
		}
	}
}

// ==================== JSON output ====================

func emitJSON(blobs []blob, cmd string, hadErr bool) int {
	docs := make([]map[string]interface{}, 0, len(blobs))
	for _, b := range blobs {
		if b.err != nil {
			docs = append(docs, map[string]interface{}{"file": b.label, "error": b.err.Error()})
			continue
		}
		doc := map[string]interface{}{
			"file":          b.label,
			"formatVersion": b.arc.Major,
			"coderVersion":  b.arc.Minor,
		}
		switch cmd {
		case "info":
			doc["counts"] = map[string]int{
				"objects": len(b.arc.Objects), "values": len(b.arc.Values),
				"keys": len(b.arc.Keys), "classes": len(b.arc.Classes),
			}
		case "tree":
			doc["graph"] = b.arc.buildGraph(0, map[int]bool{})
		case "wiring", "all":
			outs, acts, ra := splitWiring(b.arc)
			doc["outlets"] = outs
			doc["actions"] = acts
			doc["runtimeAttrs"] = ra
			if cmd == "wiring" {
				break
			}
			doc["classes"] = nonNilClasses(b.arc)
			doc["navigation"] = nonNilNav(b.arc)
		case "classes":
			doc["classes"] = nonNilClasses(b.arc)
		case "segues":
			doc["navigation"] = nonNilNav(b.arc)
		}
		docs = append(docs, doc)
	}
	var out interface{}
	if len(docs) == 1 {
		out = docs[0]
	} else {
		out = docs
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(out)
	if hadErr {
		return 1 // corrupt blobs are embedded as {"error":...}; still signal failure
	}
	return 0
}

func nonNilClasses(a *Archive) []customClass {
	c := a.customClasses()
	if c == nil {
		c = []customClass{}
	}
	return c
}
func nonNilNav(a *Archive) []navEdge {
	e := a.navEdges()
	if e == nil {
		e = []navEdge{}
	}
	return e
}

// ==================== Mermaid navigation graph ====================

func emitMermaid(blobs []blob) {
	defer startPager()()
	fmt.Println("flowchart LR")
	ids := map[string]string{}
	node := func(label string) string {
		if id, ok := ids[label]; ok {
			return id
		}
		id := "n" + strconv.Itoa(len(ids))
		ids[label] = id
		safe := strings.ReplaceAll(label, "\"", "'")
		fmt.Printf("  %s[\"%s\"]\n", id, safe)
		return id
	}
	for _, b := range blobs {
		if b.arc == nil {
			continue
		}
		for _, e := range b.arc.navEdges() {
			src := node(edgeSource(e))
			dst := node(edgeDest(e))
			lbl := firstNonEmpty(e.Identifier, e.CustomClass, e.Kind)
			fmt.Printf("  %s -->|%s| %s\n", src, lbl, dst)
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

func emitFrida(w io.Writer, blobs []blob) {
	var hooks []fridaHook
	var candidates []customClass
	seen := map[string]bool{}
	for _, b := range blobs {
		if b.arc == nil {
			continue
		}
		for _, c := range b.arc.connections() {
			if c.Kind == "action" {
				hooks = append(hooks, fridaHook{b.label, c.Name, c.Source, c.Destination, c.Event})
			}
		}
		for _, cc := range b.arc.customClasses() {
			if !seen[cc.Class] {
				seen[cc.Class] = true
				candidates = append(candidates, cc)
			}
		}
	}
	fmt.Fprintln(w, "// nibkit "+version+" - generated Frida hooks for @IBAction handlers")
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
