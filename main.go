package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
	c, err := parseArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		usage()
		return 2
	}

	// collect blobs from every input path
	var blobs []blob
	for _, p := range c.paths {
		bs, derr := discover(p)
		if derr != nil {
			fmt.Fprintln(os.Stderr, derr)
			continue
		}
		blobs = append(blobs, bs...)
	}
	if len(blobs) == 0 {
		fmt.Fprintln(os.Stderr, "error: no NIBArchive .nib files found in input")
		return 1
	}

	// parse every blob; track per-blob errors
	hadErr := false
	for i := range blobs {
		arc, perr := parse(blobs[i].data)
		blobs[i].arc = arc
		blobs[i].err = perr
		if perr != nil {
			hadErr = true
		}
	}

	// --frida generates one aggregated JS script from action connections
	if c.fridaOut {
		emitFrida(blobs)
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

// ==================== arg parsing ====================

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
	fmt.Fprint(os.Stderr, `nibkit - NIBArchive (.nib / .storyboardc / .app) decompiler

USAGE
  nibkit [command] <path...> [flags]

COMMANDS
  (default)   object tree with header          (aliases: dump, tree)
  wiring      outlets, @IBAction selectors, runtime attributes
  strings     all string values, one per line
  classes     custom (UIClassSwapper) classes
  segues      storyboard segue / navigation graph
  info        header counts only

INPUT
  path          a .nib file, a .nib/.storyboardc/.app bundle, or any directory
                (recursively walked for NIBArchive .nib files). Multiple paths
                are aggregated.

FLAGS
  -J, --json    emit JSON (single object for one blob, array for many)
      --frida   generate Frida hook stubs from @IBAction wiring (wiring only)
  -V, --version print version and exit
  -h, --help    show this help

EXAMPLES
  nibkit Foo.nib                       # tree + header
  nibkit wiring Foo.storyboardc        # outlets, actions, runtime attrs
  nibkit wiring --frida Foo.storyboardc
  nibkit strings Foo.app | grep -i http
  nibkit classes Foo.app
  nibkit segues Foo.app
  nibkit -J wiring Foo.app | jq '.[] | .actions'
`)
}

// ==================== discovery ====================

func discover(path string) ([]blob, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	if !st.IsDir() {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", path, err)
		}
		if len(data) < 10 || string(data[:10]) != magic {
			return nil, fmt.Errorf("%s: not a NIBArchive (magic mismatch)", path)
		}
		return []blob{{label: filepath.Base(path), data: data}}, nil
	}
	// directory: walk recursively for NIBArchive .nib files
	var out []blob
	werr := filepath.WalkDir(path, func(p string, d fs.DirEntry, werr error) error {
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
		rel, _ := filepath.Rel(path, p)
		out = append(out, blob{label: rel, data: data})
		return nil
	})
	if werr != nil {
		return nil, fmt.Errorf("%s: %v", path, werr)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no NIBArchive .nib files found", path)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].label < out[j].label })
	return out, nil
}

// ==================== text output ====================

func headerLine(label string, arc *Archive) string {
	return fmt.Sprintf("# %s  (format %d, coder %d)  %d objs / %d vals / %d keys / %d classes",
		label, arc.Major, arc.Minor, len(arc.Objects), len(arc.Values), len(arc.Keys), len(arc.Classes))
}

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

func pad(s string, w int) string {
	if len(s) >= w {
		return s + " "
	}
	return s + strings.Repeat(" ", w-len(s))
}

func printWiringText(arc *Archive) {
	var outlets, actions []conn
	for _, c := range arc.connections() {
		if c.Kind == "outlet" {
			outlets = append(outlets, c)
		} else {
			actions = append(actions, c)
		}
	}
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
	attrs := arc.runtimeAttributes()
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
		case "wiring":
			outs, acts := []conn{}, []conn{}
			for _, c := range b.arc.connections() {
				if c.Kind == "outlet" {
					outs = append(outs, c)
				} else {
					acts = append(acts, c)
				}
			}
			ra := b.arc.runtimeAttributes()
			if ra == nil {
				ra = []runtimeAttr{}
			}
			doc["outlets"] = outs
			doc["actions"] = acts
			doc["runtimeAttrs"] = ra
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

func emitFrida(blobs []blob) {
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
	fmt.Println("// nibkit " + version + " - generated Frida hooks for @IBAction handlers")
	fmt.Println("// attach each target-action selector on the implementing class")
	fmt.Println("// usage: frida -U -f <bundle-id> -l hooks.js")
	fmt.Println()
	if len(hooks) == 0 {
		fmt.Println("// no @IBAction connections found in input")
		return
	}
	if len(candidates) > 0 {
		fmt.Println("// candidate implementing classes (from nibkit classes):")
		for _, c := range candidates {
			fmt.Printf("//   %s (%s)\n", c.Class, c.Base)
		}
		fmt.Println()
	}
	for _, h := range hooks {
		ev := h.event
		if ev != "" {
			ev = " [" + ev + "]"
		}
		impl := resolveImpl(h.dest, candidates)
		if impl != "" {
			fmt.Printf("// %s%s  %s -> %s   (%s)\n", h.selector, ev, h.source, impl, h.file)
			fmt.Printf("try { (function () {\n")
			fmt.Printf("    var C = ObjC.classes[%q];\n", impl)
			fmt.Printf("    if (C && C[%q]) {\n", "- "+h.selector)
			fmt.Printf("        Interceptor.attach(C[%q].implementation, {\n", "- "+h.selector)
			fmt.Printf("            onEnter: function (a) { console.log('[+] %s %s'); }\n", impl, h.selector)
			fmt.Printf("        });\n")
			fmt.Printf("    }\n")
			fmt.Printf("})(); } catch (e) {}\n\n")
		} else {
			fmt.Printf("// %s%s  %s -> <unresolved placeholder: %s>   (%s)\n", h.selector, ev, h.source, h.dest, h.file)
			fmt.Printf("// TODO: set implementing class, then uncomment\n")
			fmt.Printf("// try { (function () {\n")
			fmt.Printf("//     var C = ObjC.classes["+"%q"+"];\n", "<CLASS>")
			fmt.Printf("//     if (C && C[%q]) {\n", "- "+h.selector)
			fmt.Printf("//         Interceptor.attach(C[%q].implementation, {\n", "- "+h.selector)
			fmt.Printf("//             onEnter: function (a) { console.log('[+] <CLASS> %s'); }\n", h.selector)
			fmt.Printf("//         });\n")
			fmt.Printf("//     }\n")
			fmt.Printf("// })(); } catch (e) {}\n\n")
		}
	}
}
