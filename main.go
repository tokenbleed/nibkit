package main

import (
	"github.com/tokenbleed/nibkit/internal/mcp"
	"github.com/tokenbleed/nibkit/internal/nib"

	"bufio"
	"fmt"
	"os"
	"strings"
)

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
	if len(args) > 0 && args[0] == "mcp" {
		if err := mcp.Serve(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "mcp:", err)
			return 1
		}
		return 0
	}
	if len(args) == 0 && nib.IsTerminal() {
		return runInteractive()
	}
	c, err := parseArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		usage()
		return 2
	}

	var blobs []nib.Blob
	for _, p := range c.paths {
		bs, cleanup, derr := nib.Discover(p)
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

	hadErr := nib.ParseAll(blobs)

	if c.mermaidOut {
		nib.EmitMermaid(blobs)
		if hadErr {
			return 1
		}
		return 0
	}
	if c.fridaOut {
		nib.EmitFrida(os.Stdout, blobs)
		if hadErr {
			return 1
		}
		return 0
	}
	if c.jsonOut {
		return nib.EmitJSON(blobs, c.cmd, hadErr)
	}
	return nib.EmitText(blobs, c.cmd, hadErr)
}

// ==================== interactive mode ====================

const banner = `nibkit ` + nib.Version + ` - NIBArchive decompiler (.nib / .storyboardc / .app / .ipa)
.ipa files are extracted automatically. drop in a path and pick an action.

sample commands (this menu is optional; the CLI works directly):
  nibkit wiring Foo.app              outlets + @IBAction selectors + runtime attrs
  nibkit classes Foo.ipa             custom Interface Builder classes
  nibkit segues --mermaid Foo.app    navigation graph as Mermaid
  nibkit wiring --frida Foo.ipa      Frida hooks for @IBAction handlers
  type 'help' anywhere for the full command list.`

func runInteractive() int {
	fmt.Println(banner)
	r := bufio.NewReader(os.Stdin)
	for {
		fmt.Println()
		fmt.Println("Path to a .ipa / .app / .storyboardc / .nib / directory")
		fmt.Println("(drag one in, 'help' for commands, blank to quit):")
		fmt.Print("> ")
		line := cleanPath(readline(r))
		if line == "" || line == "q" || line == "quit" || line == "exit" {
			return 0
		}
		if isHelpWord(line) {
			printHelp()
			continue
		}
		blobs, cleanup, err := nib.Discover(line)
		if err != nil {
			fmt.Fprintln(os.Stderr, "  error:", err)
			continue
		}
		if cleanup != nil {
			cleanup()
		}
		nib.ParseAll(blobs)
		bad := 0
		for i := range blobs {
			if blobs[i].Arc == nil {
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
  7  mermaid   navigation graph as Mermaid (renders on GitHub)
  8  all       classes + wiring + navigation in one report`)
}

func doAction(r *bufio.Reader, choice string, blobs []nib.Blob) {
	switch choice {
	case "1", "tree", "dump":
		nib.EmitText(blobs, "tree", false)
	case "2", "wiring":
		nib.EmitText(blobs, "wiring", false)
	case "3", "classes":
		nib.EmitText(blobs, "classes", false)
	case "4", "nav", "segues":
		nib.EmitText(blobs, "segues", false)
	case "5", "frida":
		writeFrida(blobs)
	case "6", "json":
		nib.EmitJSON(blobs, "all", false)
	case "7", "mermaid":
		nib.EmitMermaid(blobs)
	case "8", "all":
		nib.EmitText(blobs, "all", false)
	default:
		fmt.Fprintln(os.Stderr, "  unknown action; pick 1-8, or help / b / q")
	}
}

func writeFrida(blobs []nib.Blob) {
	var n int
	for _, b := range blobs {
		if b.Arc == nil {
			continue
		}
		for _, c := range b.Arc.Connections() {
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
	nib.EmitFrida(f, blobs)
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
			fmt.Println("nibkit " + nib.Version)
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
	// exactly one output mode; combos are almost certainly a mistake
	nmodes := 0
	if c.jsonOut {
		nmodes++
	}
	if c.fridaOut {
		nmodes++
	}
	if c.mermaidOut {
		nmodes++
	}
	if nmodes > 1 {
		return c, fmt.Errorf("-J, --frida and --mermaid are mutually exclusive")
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
  nibkit mcp                          # MCP server on stdio (for AI clients)

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
