// Package mcp implements a minimal MCP server over stdio: newline-delimited
// JSON-RPC 2.0 with initialize / tools/list / tools/call. It exposes nibkit's
// read-only analysis commands to MCP clients (Claude Code, opencode, pi, ...)
// with no dependencies beyond the standard library.
package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/tokenbleed/nibkit/internal/nib"
)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type toolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

var tools = []toolDef{
	{
		Name:        "nibkit_wiring",
		Description: "Outlet wiring, target-action selectors, Cocoa bindings and user-defined runtime attributes from compiled .nib/.storyboardc/.app/.ipa interface files",
		InputSchema: objectSchema("Path to a .ipa, .app, .storyboardc, .nib file or directory"),
	},
	{
		Name:        "nibkit_classes",
		Description: "Custom Interface Builder classes with their base classes (UIClassSwapper / NSCustomObject), Swift-mangled names recovered",
		InputSchema: objectSchema("Path to a .ipa, .app, .storyboardc, .nib file or directory"),
	},
	{
		Name:        "nibkit_segues",
		Description: "Navigation graph: storyboard segue templates plus container relationships (tabs, navigation roots) across scenes",
		InputSchema: objectSchema("Path to a .ipa, .app, .storyboardc, .nib file or directory"),
	},
	{
		Name:        "nibkit_tree",
		Description: "Full decoded object tree of a nib, with resolved custom-class annotations",
		InputSchema: objectSchema("Path to a .ipa, .app, .storyboardc, .nib file or directory"),
	},
	{
		Name:        "nibkit_all",
		Description: "Classes, wiring and navigation in one report",
		InputSchema: objectSchema("Path to a .ipa, .app, .storyboardc, .nib file or directory"),
	},
	{
		Name:        "nibkit_info",
		Description: "Per-file header counts (format, coder version, object/value/key/class totals)",
		InputSchema: objectSchema("Path to a .ipa, .app, .storyboardc, .nib file or directory"),
	},
}

func objectSchema(pathDesc string) map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "description": pathDesc},
		},
		"required": []string{"path"},
	}
}

// Serve reads newline-delimited JSON-RPC requests from r and writes responses
// to w until EOF. Returns nil on clean EOF.
func Serve(r io.Reader, w io.Writer) error {
	dec := json.NewDecoder(bufio.NewReader(r))
	enc := json.NewEncoder(w)
	for {
		var req request
		if err := dec.Decode(&req); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if len(req.ID) == 0 {
			continue // notification (notifications/initialized and friends): no reply
		}
		var result any
		var rerr *rpcError
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "nibkit", "version": nib.Version},
			}
		case "ping":
			result = map[string]any{}
		case "tools/list":
			result = map[string]any{"tools": tools}
		case "tools/call":
			result, rerr = callTool(req.Params)
		default:
			rerr = &rpcError{Code: -32601, Message: fmt.Sprintf("method not found: %s", req.Method)}
		}
		resp := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID)}
		if rerr != nil {
			resp["error"] = rerr
		} else {
			resp["result"] = result
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
}

func callTool(params json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      string `json:"name"`
		Arguments struct {
			Path string `json:"path"`
		} `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid params"}
	}
	if p.Arguments.Path == "" {
		return errorResult("missing required argument: path"), nil
	}
	cmd := ""
	for _, t := range tools {
		if t.Name == p.Name {
			cmd = map[string]string{
				"nibkit_wiring":  "wiring",
				"nibkit_classes": "classes",
				"nibkit_segues":  "segues",
				"nibkit_tree":    "",
				"nibkit_all":     "all",
				"nibkit_info":    "info",
			}[p.Name]
		}
	}
	if cmd == "" && p.Name != "nibkit_tree" {
		return nil, &rpcError{Code: -32602, Message: fmt.Sprintf("unknown tool: %s", p.Name)}
	}

	blobs, cleanup, err := nib.Discover(p.Arguments.Path)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	if cleanup != nil {
		defer cleanup()
	}
	if len(blobs) == 0 {
		return errorResult("no nib files found in path"), nil
	}
	nib.ParseAll(blobs)

	// capture emitter output; the protocol channel is stdout itself
	var buf bytes.Buffer
	saved := nib.Out
	nib.Out = &buf
	if p.Name == "nibkit_tree" {
		for _, b := range blobs {
			if b.Arc == nil {
				continue
			}
			fmt.Fprintln(&buf, nib.HeaderLine(b.Label, b.Arc))
			b.Arc.PrintTree(b.Arc.BuildGraph(0, map[int]bool{}), 0)
		}
	} else {
		nib.EmitText(blobs, cmd, false)
	}
	nib.Out = saved
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": buf.String()}},
	}, nil
}

func errorResult(msg string) any {
	return map[string]any{
		"isError": true,
		"content": []any{map[string]any{"type": "text", "text": "error: " + msg}},
	}
}
