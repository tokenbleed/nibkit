package main

import (
	"archive/zip"
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const version = "1.0.0"

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
	cmd      string
	paths    []string
	jsonOut  bool
	fridaOut bool
}

func run(args []string) int {
	// no args + interactive terminal => guided menu
	if len(args) == 0 && isTerminal() {
		return runInteractive()
	}
	c, err := parseArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		usage()
		return 2
	}

	// collect blobs from every input path
	var blobs []blob
	for _, p := range c.paths {
		bs, cleanup, derr := discover(p)
		if derr != nil {
			fmt.Fprintln(os.Stderr, derr)
			continue
		}
		if cleanup != nil {
			cleanup() // nib data is already in memory; temp dir not needed
		}
		blobs = append(blobs, bs...)
	}
	if len(blobs) == 0 {
		fmt.Fprintln(os.Stderr, "error: no NIBArchive .nib files found in input")
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

	if c.fridaOut {
		emitFrida(os.Stdout, blobs)
		if hadErr {
			return 1
		}
		return 0
	}
	if c.jsonOut {
		return emitJSON(blobs, c.cmd)
	}
	return emitText(blobs, c.cmd, hadErr)
}

// ==================== interactive mode ====================

const banner = `nibkit ` + version + ` - NIBArchive decompiler (.nib / .storyboardc / .app / .ipa)
.ipa files are extracted automatically. drop in a path and pick an action.`

func isTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func runInteractive() int {
	fmt.Println(banner)
	r := bufio.NewReader(os.Stdin)
	for {
		fmt.Println()
		fmt.Println("Path to a .ipa / .app / .storyboardc / .nib / directory")
		fmt.Println("(drag one in, or blank to quit):")
		fmt.Print("> ")
		path := cleanPath(readline(r))
		if path == "" {
			return 0
		}
		blobs, cleanup, err := discover(path)
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

		for {
			fmt.Println()
			printMenu()
			fmt.Print("> ")
			choice := strings.ToLower(readline(r))
			if choice == "" || choice == "q" || choice == "quit" || choice == "exit" {
				return 0
			}
			if !doAction(r, choice, blobs) {
				continue
			}
			if !yesNo(r, "Run another action on this bundle? [Y/n]", true) {
				break
			}
		}
		if !yesNo(r, "Load a different file? [y/N]", false) {
			return 0
		}
	}
}

func printMenu() {
	fmt.Println(`actions:
  1  tree      object graph
  2  wiring    outlets + @IBAction selectors + runtime attributes
  3  classes   custom Interface Builder classes
  4  segues    storyboard navigation graph
  5  strings   search archived strings
  6  frida     @IBAction hooks -> hooks.js
  7  json      export structured data
  q  quit`)
}

func doAction(r *bufio.Reader, choice string, blobs []blob) bool {
	switch choice {
	case "1":
		emitText(blobs, "tree", false)
	case "2":
		emitText(blobs, "wiring", false)
	case "3":
		emitText(blobs, "classes", false)
	case "4":
		emitText(blobs, "segues", false)
	case "5":
		fmt.Println("filter (regex, or blank for all):")
		fmt.Print("> ")
		printStrings(blobs, readline(r))
	case "6":
		writeFrida(blobs)
	case "7":
		emitJSON(blobs, "all")
	default:
		fmt.Fprintln(os.Stderr, "  unknown choice; pick 1-7 or q")
		return false
	}
	return true
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

func printStrings(blobs []blob, filter string) {
	var re *regexp.Regexp
	if filter != "" {
		var err error
		if re, err = regexp.Compile(filter); err != nil {
			fmt.Fprintln(os.Stderr, "  bad regex:", err)
			return
		}
	}
	for _, b := range blobs {
		if b.err != nil {
			continue
		}
		var rows []strItem
		for _, s := range b.arc.collectStrings() {
			if filter == "" || re.MatchString(s.Value) {
				rows = append(rows, s)
			}
		}
		if len(rows) == 0 {
			continue
		}
		fmt.Println(headerLine(b.label, b.arc))
		for _, s := range rows {
			fmt.Printf("%s\t%s\n", s.Source, s.Value)
		}
	}
}

func readline(r *bufio.Reader) string {
	s, _ := r.ReadString('\n')
	return strings.TrimSpace(s)
}

func yesNo(r *bufio.Reader, msg string, def bool) bool {
	fmt.Print(msg + " ")
	s := strings.ToLower(readline(r))
	if s == "" {
		return def
	}
	return s[0] == 'y'
}

// cleanPath tidies a path pasted or dragged into a terminal: trims whitespace,
// strips surrounding quotes, and unescapes spaces (\  -> space).
func cleanPath(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\"'")
	s = strings.ReplaceAll(s, `\ `, " ")
	return s
}

// ==================== arg parsing (non-interactive) ====================

// realCmd maps a subcommand (or alias) to an internal command name. Aliases keep
// older invocations working: dump/tree -> tree, json -> tree+JSON, info -> info.
var realCmd = map[string]struct {
	cmd   string
	isJos bool
}{
	"wiring":  {"wiring", false},
	"strings": {"strings", false},
	"classes": {"classes", false},
	"segues":  {"segues", false},
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
  strings     all string values, one per line
  classes     custom (UIClassSwapper) classes
  segues      storyboard segue / navigation graph
  info        header counts only

INPUT
  path          a .ipa (auto-extracted), .nib file, .nib/.storyboardc/.app bundle,
                or any directory (recursively walked for NIBArchive .nib files).
                Multiple paths are aggregated.

FLAGS
  -J, --json    emit JSON (single object for one blob, array for many)
      --frida   generate Frida hook stubs from @IBAction wiring (wiring only)
  -V, --version print version and exit
  -h, --help    show this help

EXAMPLES
  nibkit                              # interactive menu
  nibkit Foo.ipa                      # tree (ipa auto-extracted)
  nibkit wiring Foo.ipa               # outlets, actions, runtime attrs
  nibkit wiring --frida Foo.storyboardc
  nibkit strings Foo.app | grep -i http
  nibkit -J wiring Foo.app | jq '.[] | .actions'
`)
}

// ==================== discovery ====================

func discover(path string) ([]blob, func(), error) {
	// .ipa: extract to a temp dir, then walk the main .app inside Payload
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

// walkDir recursively collects every NIBArchive .nib file under a directory.
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
			return nil // skip unreadable files
		}
		if len(data) < 10 || string(data[:10]) != magic {
			return nil // skip non-NIBArchive (old NeXT/bplist nibs)
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

// extractIPA unzips an .ipa into a temp dir and returns the main .app path plus
// a cleanup func. Falls back to the temp dir root if Payload/*.app is absent.
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
	for _, f := range zr.File {
		fp := filepath.Join(tmp, f.Name)
		// guard against zip slip
		if !strings.HasPrefix(filepath.Clean(fp)+string(os.PathSeparator), root+string(os.PathSeparator)) {
			continue
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
		if _, err := io.Copy(out, rc); err != nil {
			rc.Close()
			out.Close()
			zr.Close()
			cleanup()
			return "", nil, err
		}
		rc.Close()
		out.Close()
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

func pad(s string, w int) string {
	if len(s) >= w {
		return s + " "
	}
	return s + strings.Repeat(" ", w-len(s))
}

// ==================== text output ====================

func emitText(blobs []blob, cmd string, hadErr bool) int {
	for _, b := range blobs {
		if b.err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", b.label, b.err)
			continue
		}
		fmt.Println(headerLine(b.label, b.arc))
		switch cmd {
		case "info":
			// header is the whole output
		case "tree":
			b.arc.printTree(b.arc.buildGraph(0, map[int]bool{}), 0)
		case "wiring":
			printWiringText(b.arc)
		case "strings":
			for _, s := range b.arc.collectStrings() {
				fmt.Printf("%s\t%s\n", s.Source, s.Value)
			}
		case "classes":
			printClassesText(b.arc)
		case "segues":
			printSeguesText(b.arc)
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
		fmt.Printf("  %s %s %s %s\n", pad("TYPE", 8), pad("SELECTOR/OUTLET", 34), pad("SOURCE", 30), "DESTINATION")
		for _, c := range outlets {
			fmt.Printf("  %s %s %s %s\n", pad("outlet", 8), pad(c.Name, 34), pad(c.Source, 30), c.Destination)
		}
		for _, c := range actions {
			ev := c.Event
			if ev != "" {
				ev = " [" + ev + "]"
			}
			fmt.Printf("  %s %s %s %s%s\n", pad("action", 8), pad(c.Name, 34), pad(c.Source, 30), c.Destination, ev)
		}
	}
	if len(attrs) > 0 {
		fmt.Println("RUNTIME ATTRS")
		fmt.Printf("  %s %s %s\n", pad("OBJECT", 30), pad("KEYPATH", 24), "VALUE")
		for _, a := range attrs {
			fmt.Printf("  %s %s %s\n", pad(a.Object, 30), pad(a.KeyPath, 24), a.Value)
		}
	}
	if len(outlets)+len(actions) == 0 && len(attrs) == 0 {
		fmt.Println("(no wiring)")
	}
}

func printClassesText(arc *Archive) {
	cs := arc.customClasses()
	if len(cs) == 0 {
		fmt.Println("(no custom classes)")
		return
	}
	fmt.Printf("  %s %s %s\n", pad("CLASS", 44), pad("BASE", 24), "SCENE ID")
	for _, c := range cs {
		fmt.Printf("  %s %s %s\n", pad(c.Class, 44), pad(c.Base, 24), c.SceneID)
	}
}

func printSeguesText(arc *Archive) {
	ss := arc.segues()
	if len(ss) == 0 {
		fmt.Println("(no segues)")
		return
	}
	fmt.Printf("  %s %s %s %s %s\n", pad("KIND", 12), pad("SOURCE", 36), pad("DESTINATION ID", 36), pad("IDENTIFIER", 14), "SELECTOR")
	for _, s := range ss {
		id := s.Identifier
		if id == "" {
			id = "-"
		}
		fmt.Printf("  %s %s %s %s %s\n", pad(s.Kind, 12), pad(s.SourceClass, 36), pad(s.DestID, 36), pad(id, 14), s.Selector)
		for k, v := range s.Details {
			fmt.Printf("    %s = %s\n", pad(k, 46), v)
		}
	}
}

// ==================== JSON output ====================

func emitJSON(blobs []blob, cmd string) int {
	docs := make([]map[string]interface{}, 0, len(blobs))
	for _, b := range blobs {
		if b.err != nil {
			docs = append(docs, map[string]interface{}{
				"file":  b.label,
				"error": b.err.Error(),
			})
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
			c := b.arc.customClasses()
			if c == nil {
				c = []customClass{}
			}
			g := b.arc.segues()
			if g == nil {
				g = []segue{}
			}
			doc["classes"] = c
			doc["segues"] = g
		case "strings":
			s := b.arc.collectStrings()
			if s == nil {
				s = []strItem{}
			}
			doc["strings"] = s
		case "classes":
			c := b.arc.customClasses()
			if c == nil {
				c = []customClass{}
			}
			doc["classes"] = c
		case "segues":
			g := b.arc.segues()
			if g == nil {
				g = []segue{}
			}
			doc["segues"] = g
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
	return 0
}

// ==================== Frida codegen ====================

type fridaHook struct {
	file, selector, source, dest, event string
}

// isPlaceholder reports whether a resolved destination label is a storyboard
// placeholder / proxy whose real class must be inferred.
func isPlaceholder(s string) bool {
	return strings.HasSuffix(s, "(proxy)") || strings.Contains(s, "Placeholder")
}

// resolveImpl picks the implementing class for an action whose destination is a
// placeholder. Prefers a unique candidate whose base class is a view controller;
// falls back to a unique candidate overall. Returns "" when ambiguous.
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
			fmt.Fprintf(w, "//             onEnter: function (a) { console.log('[+] <CLASS> %s'); }\n", h.selector)
			fmt.Fprintf(w, "//         });\n")
			fmt.Fprintf(w, "//     }\n")
			fmt.Fprintf(w, "// })(); } catch (e) {}\n\n")
		}
	}
}
