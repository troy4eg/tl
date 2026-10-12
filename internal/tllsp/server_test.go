package tllsp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func rpc(method string, id any, params any) map[string]any {
	m := map[string]any{"jsonrpc": "2.0", "method": method}
	if id != nil {
		m["id"] = id
	}
	if params != nil {
		m["params"] = params
	}
	return m
}

func runSession(t *testing.T, messages ...map[string]any) []map[string]json.RawMessage {
	t.Helper()
	var in, out, logs bytes.Buffer
	for _, m := range messages {
		if err := writeMessage(&in, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := Run(&in, &out, &logs); err != nil {
		t.Fatalf("Run: %v, log: %s", err, &logs)
	}
	var results []map[string]json.RawMessage
	r := bufio.NewReader(&out)
	for {
		body, err := readMessage(r)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(body, &m); err != nil {
			t.Fatal(err)
		}
		results = append(results, m)
	}
	return results
}

func response(t *testing.T, results []map[string]json.RawMessage, id any, dst any) {
	t.Helper()
	want, _ := json.Marshal(id)
	for _, r := range results {
		if bytes.Equal(r["id"], want) {
			if err := r["error"]; len(err) > 0 {
				t.Fatalf("request %v: %s", id, err)
			}
			if err := json.Unmarshal(r["result"], dst); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatalf("missing response %v", id)
}

func docParams(uri string, pos position) map[string]any {
	return map[string]any{"textDocument": map[string]any{"uri": uri}, "position": pos}
}

func openDoc(uri, text string, version int) map[string]any {
	return rpc("textDocument/didOpen", nil, map[string]any{"textDocument": map[string]any{"uri": uri, "languageId": "tl2", "version": version, "text": text}})
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestEditorIndependentSession(t *testing.T) {
	root := t.TempDir()
	typesURI := uriFromPath(filepath.Join(root, "типы с пробелом.tl2"))
	useURI := uriFromPath(filepath.Join(root, "use.tl2"))
	writeFile(t, filepath.Join(root, "типы с пробелом.tl2"), "// Documentation 😀\nmodel.item = id:int64;\n")
	writeFile(t, filepath.Join(root, "use.tl2"), "use = x:int32;")
	text := "// Привет 😀\r\nuse<T:Type> = x:model.item y:T;\r\nbroken = x:;\r\nafter = x:int32;\r\n"
	src := newSource(text)
	refPos := src.position(strings.Index(text, "model.item") + 2)
	paramPos := src.position(strings.Index(text, "y:T") + 2)
	changed := strings.Replace(text, "model.item", "model.other", 1)
	results := runSession(t,
		rpc("initialize", "init", map[string]any{"workspaceFolders": []any{map[string]any{"uri": uriFromPath(root), "name": "test"}}}),
		rpc("initialized", nil, map[string]any{}),
		openDoc(useURI, text, 1),
		rpc("textDocument/definition", 1, docParams(useURI, refPos)),
		rpc("textDocument/hover", 2, docParams(useURI, refPos)),
		rpc("textDocument/definition", 3, docParams(useURI, paramPos)),
		rpc("textDocument/documentSymbol", 4, docParams(useURI, position{})),
		rpc("textDocument/completion", 5, docParams(useURI, src.position(strings.Index(text, "model.item")+6))),
		rpc("textDocument/semanticTokens/full", 6, docParams(useURI, position{})),
		openDoc(typesURI, "model.other = id:int64;", 2),
		rpc("textDocument/didChange", nil, map[string]any{"textDocument": map[string]any{"uri": useURI, "version": 2}, "contentChanges": []any{map[string]any{"range": src.span(strings.Index(text, "model.item"), strings.Index(text, "model.item")+10), "text": "model.other"}}}),
		rpc("workspace/didChangeWatchedFiles", nil, map[string]any{"changes": []any{map[string]any{"uri": typesURI, "type": 2}}}),
		rpc("textDocument/definition", 7, docParams(useURI, refPos)),
		// An out-of-order edit must not replace the newest version.
		rpc("textDocument/didChange", nil, map[string]any{"textDocument": map[string]any{"uri": useURI, "version": 1}, "contentChanges": []any{map[string]any{"text": "bad = x:Unknown;"}}}),
		rpc("textDocument/hover", 8, docParams(useURI, refPos)),
		rpc("textDocument/didClose", nil, docParams(typesURI, position{})),
		rpc("textDocument/definition", 9, docParams(useURI, refPos)),
		rpc("shutdown", "shutdown", nil), rpc("exit", nil, nil),
	)
	var defs []location
	response(t, results, 1, &defs)
	if len(defs) != 1 || defs[0].URI != typesURI || defs[0].Range.Start != (position{1, 0}) {
		t.Fatalf("cross-file definition: %+v", defs)
	}
	var hover struct{ Contents markup }
	response(t, results, 2, &hover)
	if !strings.Contains(hover.Contents.Value, "Documentation 😀") {
		t.Fatalf("hover: %+v", hover)
	}
	response(t, results, 3, &defs)
	if len(defs) != 1 || defs[0].URI != useURI || defs[0].Range.Start.Character != 4 {
		t.Fatalf("template definition: %+v", defs)
	}
	var symbols []documentSymbol
	response(t, results, 4, &symbols)
	if len(symbols) != 3 || symbols[1].Name != "broken" || symbols[2].Name != "after" {
		t.Fatalf("recovered symbols: %+v", symbols)
	}
	var items []completionItem
	response(t, results, 5, &items)
	if len(items) != 1 || items[0].Label != "model.item" || items[0].TextEdit.NewText != "model.item" {
		t.Fatalf("completion: %+v", items)
	}
	var tokens struct{ Data []int }
	response(t, results, 6, &tokens)
	if len(tokens.Data) == 0 || len(tokens.Data)%5 != 0 || tokens.Data[2] != 12 || tokens.Data[3] != 5 {
		t.Fatalf("UTF-16 semantic tokens: %v", tokens.Data)
	}
	response(t, results, 7, &defs)
	if len(defs) != 1 || defs[0].Range.Start.Line != 0 {
		t.Fatalf("watcher overwrote open buffer: %+v", defs)
	}
	response(t, results, 8, &hover)
	if !strings.Contains(hover.Contents.Value, "model.other") {
		t.Fatalf("stale edit applied: %+v", hover)
	}
	response(t, results, 9, &defs)
	if len(defs) != 0 {
		t.Fatalf("close did not restore disk: %+v", defs)
	}
	var lastDiags struct {
		URI         string
		Version     int
		Diagnostics []diagnostic
	}
	for _, result := range results {
		if string(result["method"]) != `"textDocument/publishDiagnostics"` {
			continue
		}
		var p struct {
			URI         string
			Version     int
			Diagnostics []diagnostic
		}
		if err := json.Unmarshal(result["params"], &p); err != nil {
			t.Fatal(err)
		}
		if p.URI == useURI && p.Version == 2 {
			lastDiags = p
		}
	}
	if len(lastDiags.Diagnostics) != 2 || newSource(changed).position(strings.Index(changed, "model.other")) != lastDiags.Diagnostics[1].Range.Start {
		t.Fatalf("expected syntax error and missing type after close: %+v", lastDiags)
	}
}

func TestSchemaPathsAndTL1Dependencies(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "external.tl"), "external id:int = External;")
	writeFile(t, filepath.Join(root, "unrelated.tl2"), "Unrelated = x:int32;")
	uri := uriFromPath(filepath.Join(root, "editor.tl2"))
	results := runSession(t,
		rpc("initialize", 0, map[string]any{"rootUri": uriFromPath(root), "initializationOptions": map[string]any{"schemaPaths": []string{"external.tl"}}}),
		openDoc(uri, "Editor = a:External b:Unrelated;", 1),
		rpc("textDocument/definition", 1, docParams(uri, position{0, 12})),
		rpc("textDocument/completion", 2, docParams(uri, position{0, 11})),
		rpc("workspace/didChangeConfiguration", nil, map[string]any{"settings": map[string]any{"tl2": map[string]any{"diagnostics": map[string]any{"unknownTypes": "off"}}}}),
		rpc("shutdown", 3, nil), rpc("exit", nil, nil),
	)
	var defs []location
	response(t, results, 1, &defs)
	if len(defs) != 1 || defs[0].URI != uriFromPath(filepath.Join(root, "external.tl")) {
		t.Fatalf("TL1 dependency: %+v", defs)
	}
	var items []completionItem
	response(t, results, 2, &items)
	for _, item := range items {
		if item.Label == "Unrelated" {
			t.Fatal("indexed a file outside schemaPaths")
		}
	}
	var counts []int
	for _, r := range results {
		if string(r["method"]) == `"textDocument/publishDiagnostics"` {
			var p struct {
				URI         string
				Diagnostics []diagnostic
			}
			if err := json.Unmarshal(r["params"], &p); err != nil {
				t.Fatal(err)
			}
			if p.URI == uri {
				counts = append(counts, len(p.Diagnostics))
			}
		}
	}
	if len(counts) != 2 || counts[0] != 1 || counts[1] != 0 {
		t.Fatalf("diagnostics configuration: %v", counts)
	}
}

func TestProtocolErrorsAndLifecycle(t *testing.T) {
	results := runSession(t,
		rpc("textDocument/hover", "early", map[string]any{}),
		rpc("initialize", 1, map[string]any{}),
		rpc("textDocument/completion", 2, "invalid params"),
		rpc("unknown/method", 3, map[string]any{}),
		rpc("shutdown", 4, nil),
		rpc("textDocument/hover", 5, map[string]any{}),
		rpc("exit", nil, nil),
	)
	for _, item := range []struct{ index, code int }{{0, -32002}, {2, -32602}, {3, -32601}, {5, -32600}} {
		var got rpcError
		if err := json.Unmarshal(results[item.index]["error"], &got); err != nil {
			t.Fatal(err)
		}
		if got.Code != item.code {
			t.Fatalf("result %d: %+v", item.index, got)
		}
	}
	if string(results[4]["result"]) != "null" {
		t.Fatal("shutdown must return null")
	}
	var in, out bytes.Buffer
	if err := writeMessage(&in, rpc("exit", nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := Run(&in, &out, io.Discard); err == nil {
		t.Fatal("exit without shutdown must fail")
	}
	for _, header := range []string{"\r\n", "Content-Length: -1\r\n\r\n", "Content-Length: 999999999\r\n\r\n", "Content-Length: 2\r\nContent-Length: 2\r\n\r\n{}"} {
		if _, err := readMessage(bufio.NewReader(strings.NewReader(header))); err == nil {
			t.Fatalf("accepted invalid header %q", header)
		}
	}
}

func TestUTF16Edits(t *testing.T) {
	s := newSource("// 😀 привет\r\nx = y:int32;\n")
	for _, offset := range []int{0, 3, 7, strings.Index(s.text, "x ="), len(s.text)} {
		pos := s.position(offset)
		got, err := s.offset(pos)
		if err != nil || got != offset {
			t.Fatalf("position round-trip %d -> %+v -> %d: %v", offset, pos, got, err)
		}
	}
	if _, err := s.offset(position{0, 4}); err == nil {
		t.Fatal("accepted a split surrogate pair")
	}
	uri := "file:///tmp/utf16.tl2"
	results := runSession(t,
		rpc("initialize", 0, map[string]any{}), openDoc(uri, s.text, 1),
		rpc("textDocument/didChange", nil, map[string]any{"textDocument": map[string]any{"uri": uri, "version": 2}, "contentChanges": []any{
			map[string]any{"range": span{position{0, 3}, position{0, 5}}, "text": "ok"},
			map[string]any{"range": span{position{1, 0}, position{1, 1}}, "text": "renamed"},
		}}),
		rpc("textDocument/documentSymbol", 1, docParams(uri, position{})),
		rpc("shutdown", 2, nil), rpc("exit", nil, nil),
	)
	var symbols []documentSymbol
	response(t, results, 1, &symbols)
	if len(symbols) != 1 || symbols[0].Name != "renamed" {
		t.Fatalf("UTF-16 edits: %+v", symbols)
	}
}

func TestLocalArraySizesAndUnionScope(t *testing.T) {
	uri := "file:///tmp/scope.tl2"
	text := "Sized = count:Size values:[count]int32;\nChoice = | first count:uint32 values:[count]int32 | second values:[count]int32;\nAlias <=> Sized;\ncall#12345678 request:Alias => Sized;\nSize <=> uint32;"
	src := newSource(text)
	results := runSession(t,
		rpc("initialize", 0, map[string]any{}), openDoc(uri, text, 1),
		rpc("textDocument/definition", 1, docParams(uri, src.position(strings.Index(text, "[count]")+1))),
		rpc("textDocument/documentSymbol", 2, docParams(uri, position{})),
		rpc("shutdown", 3, nil), rpc("exit", nil, nil),
	)
	var defs []location
	response(t, results, 1, &defs)
	if len(defs) != 1 || defs[0].Range.Start.Character != 8 {
		t.Fatalf("array size definition: %+v", defs)
	}
	var symbols []documentSymbol
	response(t, results, 2, &symbols)
	if len(symbols) != 5 || symbols[1].Kind != 10 || len(symbols[1].Children) != 2 || symbols[2].Kind != 11 || symbols[3].Kind != 12 {
		t.Fatalf("TL2 symbol kinds: %+v", symbols)
	}
	for _, result := range results {
		if string(result["method"]) != `"textDocument/publishDiagnostics"` {
			continue
		}
		var p struct{ Diagnostics []diagnostic }
		if err := json.Unmarshal(result["params"], &p); err != nil {
			t.Fatal(err)
		}
		if len(p.Diagnostics) != 1 || p.Diagnostics[0].Message != "Unknown type 'count'" || p.Diagnostics[0].Range.Start != src.position(strings.LastIndex(text, "[count]")+1) {
			t.Fatalf("variant size leaked into sibling: %+v", p)
		}
	}
}

func TestDynamicFileWatchRegistration(t *testing.T) {
	results := runSession(t,
		rpc("initialize", 0, map[string]any{"capabilities": map[string]any{"workspace": map[string]any{"didChangeWatchedFiles": map[string]any{"dynamicRegistration": true}}}}),
		rpc("initialized", nil, map[string]any{}),
		map[string]any{"jsonrpc": "2.0", "id": "tl2lsp-watchers", "result": nil},
		rpc("shutdown", 1, nil), rpc("exit", nil, nil),
	)
	if len(results) != 3 || string(results[1]["method"]) != `"client/registerCapability"` {
		t.Fatalf("watch registration / response handling: %+v", results)
	}
	var p struct {
		Registrations []struct {
			Method          string
			RegisterOptions struct {
				Watchers []struct {
					GlobPattern string
					Kind        int
				}
			}
		}
	}
	if err := json.Unmarshal(results[1]["params"], &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Registrations) != 1 || p.Registrations[0].Method != "workspace/didChangeWatchedFiles" || len(p.Registrations[0].RegisterOptions.Watchers) != 2 {
		t.Fatalf("invalid watcher registration: %+v", p)
	}
}
