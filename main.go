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

const usageText = `nibkit - parse compiled iOS/macOS Interface Builder archives (.nib / .storyboardc)

usage: nibkit <command> <path>

commands:
  info      header + table counts (cheap fingerprint)
  dump      tree view of the archived object graph
  json      object graph as JSON (pipe into jq)
  strings   all string values, class names, keys (endpoint + secret mining)
  wiring    outlet + action connections (selectors, sources, targets)
`

func usage() {
	fmt.Fprint(os.Stderr, usageText)
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		usage()
		os.Exit(0)
	}
	cmd := args[0]
	switch cmd {
	case "info", "dump", "json", "strings", "wiring":
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		usage()
		os.Exit(2)
	}
	rest := args[1:]
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "error: path required")
		usage()
		os.Exit(2)
	}
	path := rest[len(rest)-1]

	blobs, err := discover(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	multi := len(blobs) > 1
	rc := 0
	for _, b := range blobs {
		arc, err := parse(b.data)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %s\n", b.label, err)
			rc = 1
			continue
		}
		if multi && cmd != "strings" {
			fmt.Fprintf(os.Stderr, "\n===== %s (coderVersion %d) =====\n", b.label, arc.Minor)
		}
		switch cmd {
		case "info":
			fmt.Printf("file:            %s\n", b.label)
			fmt.Printf("formatVersion:   %d\n", arc.Major)
			fmt.Printf("coderVersion:    %d\n", arc.Minor)
			fmt.Printf("objects:         %d\n", len(arc.Objects))
			fmt.Printf("coder values:    %d\n", len(arc.Values))
			fmt.Printf("keys:            %d\n", len(arc.Keys))
			fmt.Printf("class names:     %d\n", len(arc.Classes))
		case "json":
			graph := arc.buildGraph(0, map[int]bool{})
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(graph)
		case "strings":
			if multi {
				fmt.Printf("# %s\n", b.label)
			}
			for _, s := range arc.collectStrings() {
				fmt.Printf("%s\t%s\n", s.Source, s.Value)
			}
		case "wiring":
			rows := arc.connections()
			if len(rows) == 0 {
				continue
			}
			fmt.Printf("%-7s %-26s %-28s %s\n", "TYPE", "SELECTOR / OUTLET", "SOURCE", "DESTINATION")
			for _, c := range rows {
				ev := ""
				if c.Kind == "action" {
					ev = "  [" + c.Event + "]"
				}
				fmt.Printf("%-7s %-26s %-28s %s%s\n",
					strings.ToUpper(c.Kind), orQ(c.Name), orQ(c.Source), orQ(c.Destination), ev)
			}
		case "dump":
			arc.printTree(arc.buildGraph(0, map[int]bool{}), 0)
		}
	}
	os.Exit(rc)
}

func orQ(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

type blob struct {
	label string
	data  []byte
}

// discover turns a path into one or more archives to parse.
// Accepts a flat .nib file or a .nib/.storyboardc bundle directory.
func discover(path string) ([]blob, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	if st.IsDir() {
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".nib" && ext != ".storyboardc" {
			return nil, fmt.Errorf("%s: pass a .nib / .storyboardc, not a directory", path)
		}
		var out []blob
		err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if strings.ToLower(filepath.Ext(p)) != ".nib" {
				return nil
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			out = append(out, blob{label: filepath.Base(p), data: data})
			return nil
		})
		if err != nil {
			return nil, err
		}
		sort.Slice(out, func(i, j int) bool { return out[i].label < out[j].label })
		if len(out) == 0 {
			return nil, fmt.Errorf("%s: no .nib files inside bundle", path)
		}
		return out, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	if len(data) < 10 || string(data[:10]) != magic {
		return nil, fmt.Errorf("%s: not a NIBArchive (magic mismatch)", path)
	}
	return []blob{{label: filepath.Base(path), data: data}}, nil
}
