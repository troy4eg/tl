package tllsp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func referenceParams(uri string, pos position, declarations bool) map[string]any {
	p := docParams(uri, pos)
	p["context"] = map[string]any{"includeDeclaration": declarations}
	return p
}

func renameParams(uri string, pos position, name string) map[string]any {
	p := docParams(uri, pos)
	p["newName"] = name
	return p
}

func applyEdits(t *testing.T, text string, edits []textEdit) string {
	t.Helper()
	src := newSource(text)
	type replacement struct {
		begin, end int
		text       string
	}
	var replacements []replacement
	for _, edit := range edits {
		b, e1 := src.offset(edit.Range.Start)
		e, e2 := src.offset(edit.Range.End)
		if e1 != nil || e2 != nil || e < b {
			t.Fatalf("invalid edit: %+v", edit)
		}
		replacements = append(replacements, replacement{b, e, edit.NewText})
	}
	sort.Slice(replacements, func(i, j int) bool { return replacements[i].begin > replacements[j].begin })
	last := len(text)
	for _, r := range replacements {
		if r.end > last {
			t.Fatal("overlapping edits")
		}
		text = text[:r.begin] + r.text + text[r.end:]
		last = r.begin
	}
	return text
}

func responseError(t *testing.T, results []map[string]json.RawMessage, id int) rpcError {
	t.Helper()
	want, _ := json.Marshal(id)
	for _, r := range results {
		if string(r["id"]) == string(want) {
			var err rpcError
			if len(r["error"]) == 0 {
				t.Fatalf("request %d succeeded unexpectedly: %s", id, r["result"])
			}
			if e := json.Unmarshal(r["error"], &err); e != nil {
				t.Fatal(e)
			}
			return err
		}
	}
	t.Fatalf("missing response %d", id)
	return rpcError{}
}

func TestReferencesAndRenameSharedTLTypeAcrossFormats(t *testing.T) {
	root := t.TempDir()
	typesPath, usePath := filepath.Join(root, "types.tl"), filepath.Join(root, "use.tl2")
	typesURI, useURI := uriFromPath(typesPath), uriFromPath(usePath)
	types := "---types---\napi.item id:int = api.Item;\napi.empty = api.Item;\n"
	text := "// api.Item 😀\r\nBox<T:Type> = first:api.Item second:api.Item template:T;\r\ncall#12345678 request:api.Item => <=> Box<string>;\r\n"
	writeFile(t, typesPath, types)
	writeFile(t, usePath, "Disk = value:int32;")
	pos := newSource(text).position(strings.Index(text, "first:api.Item") + 6)
	results := runSession(t,
		rpc("initialize", 0, map[string]any{"rootUri": uriFromPath(root), "capabilities": map[string]any{"workspace": map[string]any{"workspaceEdit": map[string]any{"documentChanges": true}}}}),
		openDoc(useURI, text, 7),
		rpc("textDocument/references", 1, referenceParams(useURI, pos, false)),
		rpc("textDocument/references", 2, referenceParams(typesURI, newSource(types).position(strings.Index(types, "api.Item")), true)),
		rpc("textDocument/prepareRename", 3, docParams(useURI, pos)),
		rpc("textDocument/rename", 4, renameParams(useURI, pos, "api.Record")),
		// Rename returns edits; it must not mutate buffers or the server index.
		rpc("textDocument/references", 5, referenceParams(useURI, pos, false)),
	)
	var refs []location
	response(t, results, 1, &refs)
	if len(refs) != 3 {
		t.Fatalf("references without declarations: %+v", refs)
	}
	for _, ref := range refs {
		if ref.URI != useURI || ref.Range.Start.Line == 0 {
			t.Fatalf("comment or disk contents indexed as usage: %+v", ref)
		}
	}
	response(t, results, 2, &refs)
	if len(refs) != 5 {
		t.Fatalf("shared result type declarations: %+v", refs)
	}
	var prepared struct {
		Range       span
		Placeholder string
	}
	response(t, results, 3, &prepared)
	if prepared.Placeholder != "api.Item" || prepared.Range != newSource(text).span(strings.Index(text, "first:api.Item")+6, strings.Index(text, "first:api.Item")+14) {
		t.Fatalf("prepare: %+v", prepared)
	}
	var edit workspaceEdit
	response(t, results, 4, &edit)
	if len(edit.DocumentChanges) != 2 || edit.Changes != nil {
		t.Fatalf("versioned edit: %+v", edit)
	}
	for _, change := range edit.DocumentChanges {
		if change.TextDocument.URI == typesURI {
			if change.TextDocument.Version != nil || len(change.Edits) != 2 {
				t.Fatalf("disk edits: %+v", change)
			}
			got := applyEdits(t, types, change.Edits)
			if got != strings.ReplaceAll(types, "api.Item", "api.Record") {
				t.Fatalf("types after rename: %q", got)
			}
		} else if change.TextDocument.URI == useURI {
			if change.TextDocument.Version == nil || *change.TextDocument.Version != 7 || len(change.Edits) != 3 {
				t.Fatalf("buffer edits: %+v", change)
			}
			got := applyEdits(t, text, change.Edits)
			want := "// api.Item 😀\r\n" + strings.ReplaceAll(strings.TrimPrefix(text, "// api.Item 😀\r\n"), "api.Item", "api.Record")
			if got != want {
				t.Fatalf("use after rename: %q", got)
			}
		} else {
			t.Fatalf("unexpected URI: %+v", change)
		}
	}
	response(t, results, 5, &refs)
	if len(refs) != 3 {
		t.Fatal("rename mutated the index before client applied it")
	}
}

func TestLocalRenameScopes(t *testing.T) {
	cases := []struct {
		name, language, text, needle, replacement string
		count                                     int
	}{
		{"tl2 template", "tl2", "Box<T:Type> = value:T; Other<T:Type> = value:T;", "value:T", "U", 2},
		{"tl2 variant field", "tl2", "Choice = | first n:uint32 xs:[n]int32 | second n:uint32 xs:[n]int32;", "[n]", "size", 2},
		{"tl template", "tl", "box {T:Type} value:T = Box T; other {T:Type} value:T = Other T;", "value:T", "U", 3},
		{"tl mask", "tl", "box flags:# value:flags.0?int = Box; other flags:# value:flags.0?int = Other;", "flags.0", "mask", 2},
		{"tl nested field", "tl", "box n:# xs:n*[n:# ys:n*[int]] = Box;", "ys:n*", "inner", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			uri := "file:///tmp/local." + tc.language
			open := openDoc(uri, tc.text, 1)
			open["params"].(map[string]any)["textDocument"].(map[string]any)["languageId"] = tc.language
			off := strings.Index(tc.text, tc.needle)
			if strings.HasPrefix(tc.needle, "value:") {
				off += 6
			} else if tc.needle == "[n]" {
				off++
			} else if tc.needle == "ys:n*" {
				off += 3
			}
			pos := newSource(tc.text).position(off)
			results := runSession(t, rpc("initialize", 0, map[string]any{}), open,
				rpc("textDocument/references", 1, referenceParams(uri, pos, true)),
				rpc("textDocument/rename", 2, renameParams(uri, pos, tc.replacement)))
			var refs []location
			response(t, results, 1, &refs)
			if len(refs) != tc.count {
				t.Fatalf("local references: %+v", refs)
			}
			var edit workspaceEdit
			response(t, results, 2, &edit)
			if len(edit.Changes[uri]) != tc.count {
				t.Fatalf("local edits: %+v", edit)
			}
			got := applyEdits(t, tc.text, edit.Changes[uri])
			if strings.Contains(tc.text, "other") && !strings.HasSuffix(got, tc.text[strings.Index(tc.text, "other"):]) {
				t.Fatalf("renamed another scope: %q", got)
			}
		})
	}
}

func TestRenameRejectsCollisionsAndCapture(t *testing.T) {
	cases := []struct{ name, text, needle, newName string }{
		{"global collision", "Item = x:int32; Other = x:int32; Use = value:Item;", "Item =", "Other"},
		{"local collision", "Box<T:Type,U:Type> = x:T y:U;", "T:Type", "U"},
		{"local captures global", "Global = x:int32; Box<T:Type> = x:T y:Global;", "T:Type", "Global"},
		{"global captured by local", "Item = x:int32; Box<T:Type> = x:Item y:T;", "Item =", "T"},
		{"captures unresolved reference", "Item = x:int32; Use = x:Missing y:Item;", "Item =", "Missing"},
		{"invalid name", "Item = x:int32;", "Item =", "a-b"},
		{"builtin name", "Item = x:int32;", "Item =", "int32"},
		{"duplicate global", "Item = x:int32; Item = y:int32; Use = x:Item;", "Item =", "Renamed"},
		{"ignored field", "Item = _:int32;", "_:int32", "field"},
		{"local namespace", "Box<T:Type> = x:T;", "T:Type", "ns.T"},
		{"namespace casing", "Item = x:int32;", "Item =", "Ns.Item"},
		{"broken schema", "Item = x:int32; Broken = x:;", "Item =", "Renamed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			uri := "file:///tmp/fail.tl2"
			pos := newSource(tc.text).position(strings.Index(tc.text, tc.needle))
			results := runSession(t, rpc("initialize", 0, map[string]any{}), openDoc(uri, tc.text, 1), rpc("textDocument/rename", 1, renameParams(uri, pos, tc.newName)))
			err := responseError(t, results, 1)
			if err.Code != requestFailed && err.Code != -32602 {
				t.Fatalf("error: %+v", err)
			}
		})
	}
}

func TestTLConstructorAndFunctionRename(t *testing.T) {
	uri := "file:///tmp/constructor.tl"
	text := "item#12345678 id:int = Item; use value:%item = Use; ---functions--- get value:Item = Item;"
	open := openDoc(uri, text, 1)
	open["params"].(map[string]any)["textDocument"].(map[string]any)["languageId"] = "tl"
	results := runSession(t, rpc("initialize", 0, map[string]any{}), open,
		rpc("textDocument/rename", 1, renameParams(uri, position{}, "entry")),
		rpc("textDocument/rename", 2, renameParams(uri, newSource(text).position(strings.Index(text, "get value")), "fetch")),
		rpc("textDocument/rename", 3, renameParams(uri, position{}, "Uppercase")))
	var edit workspaceEdit
	response(t, results, 1, &edit)
	got := applyEdits(t, text, edit.Changes[uri])
	if got != "entry#12345678 id:int = Item; use value:%entry = Use; ---functions--- get value:Item = Item;" {
		t.Fatalf("constructor edit changes result type or tag: %q", got)
	}
	response(t, results, 2, &edit)
	if len(edit.Changes[uri]) != 1 {
		t.Fatalf("function edits: %+v", edit)
	}
	responseError(t, results, 3)
}

func TestPrepareAndReferencesOnNonSymbols(t *testing.T) {
	uri := "file:///tmp/non-symbol.tl2"
	text := "// Item\nItem = x:int32 y:Unknown;"
	results := runSession(t, rpc("initialize", 0, map[string]any{}), openDoc(uri, text, 1),
		rpc("textDocument/prepareRename", 1, docParams(uri, position{0, 3})),
		rpc("textDocument/prepareRename", 2, docParams(uri, newSource(text).position(strings.Index(text, "int32")))),
		rpc("textDocument/references", 3, referenceParams(uri, newSource(text).position(strings.Index(text, "Unknown")), true)))
	var value any
	response(t, results, 1, &value)
	if value != nil {
		t.Fatalf("comment prepare: %+v", value)
	}
	response(t, results, 2, &value)
	if value != nil {
		t.Fatalf("builtin prepare: %+v", value)
	}
	var refs []location
	response(t, results, 3, &refs)
	if len(refs) != 0 {
		t.Fatalf("unknown refs: %+v", refs)
	}
}

func TestRenameReportsSyntaxErrorInUnopenedFile(t *testing.T) {
	root := t.TempDir()
	badPath := filepath.Join(root, "broken schema.tl2")
	writeFile(t, badPath, "Good = x:int32;\r\nBroken = value:;\r\n")
	uri := uriFromPath(filepath.Join(root, "valid.tl2"))
	results := runSession(t,
		rpc("initialize", 0, map[string]any{"rootUri": uriFromPath(root)}),
		openDoc(uri, "Item = value:int32;", 1),
		rpc("textDocument/rename", 1, renameParams(uri, position{}, "Renamed")))
	err := responseError(t, results, 1)
	if err.Code != requestFailed || !strings.Contains(err.Message, badPath+":2:16: expected type of field") {
		t.Fatalf("rename error does not identify the unopened file and parser error: %+v", err)
	}
}

func TestRenameSyntaxErrorsKeepFullListInOutput(t *testing.T) {
	var output bytes.Buffer
	s := &server{docs: map[string]*document{}, log: log.New(&output, "", 0)}
	root := t.TempDir()
	for i := 7; i >= 1; i-- {
		uri := uriFromPath(filepath.Join(root, fmt.Sprintf("bad-%d.tl", i)))
		s.docs[uri] = parseDocument(uri, "---types---\nbroken value: = Broken;", 0, false)
	}
	err := s.renameSyntaxError()
	if err == nil || !strings.Contains(err.Message, "and 2 more errors") {
		t.Fatalf("missing summary: %+v", err)
	}
	if strings.Count(err.Message, "field type is expected here") != 5 || strings.Count(output.String(), "field type is expected here") != 7 {
		t.Fatalf("summary/log did not retain the expected errors: message=%q output=%q", err.Message, output.String())
	}
	for _, doc := range s.docs {
		path, _ := pathFromURI(doc.uri)
		if !strings.Contains(output.String(), path+":2:15:") {
			t.Errorf("file missing in server output: %s", path)
		}
	}
}
