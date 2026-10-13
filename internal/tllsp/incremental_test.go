package tllsp

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestIncrementalDiagnosticsMatchFullValidation(t *testing.T) {
	s := &server{docs: map[string]*document{}, out: io.Discard, log: log.New(io.Discard, "", 0), relatedDiagnostics: true}
	set := func(file, text string) {
		uri := "file:///tmp/incremental-" + file
		if text == "" {
			delete(s.docs, uri)
		} else {
			s.docs[uri] = parseDocument(uri, text, 1, true)
		}
		s.refreshIndex(false)
	}
	check := func() {
		t.Helper()
		full := &server{docs: s.docs, relatedDiagnostics: true}
		full.rebuildDefinitions()
		full.rebuildSemanticDiagnostics()
		if !reflect.DeepEqual(s.semantic, full.semantic) {
			t.Fatalf("incremental diagnostics differ\nincremental: %+v\nfull: %+v", s.semantic, full.semantic)
		}
		for name, defs := range full.byName {
			if !reflect.DeepEqual(defs, s.byName[name]) {
				t.Fatalf("definitions differ for %s", name)
			}
		}
	}
	steps := [][2]string{
		{"use.tl2", "Use = n:Count values:[n]int32 box:Box<7> missing:Later;"},
		{"types.tl2", "Size <=> uint32; Count <=> Size; Box<T:Type> = value:T;"},
		{"types.tl2", "Size <=> string; Count <=> Size; Box<N:#> = values:[N]int32;"},
		{"types.tl2", "// 😀\r\nSize <=> uint32; Count <=> Size; Box<T:Type> = value:T; Later = value:;"},
		{"types.tl2", "Size <=> uint32; Count <=> Size; Box<T:Type> = value:T; Later = value:int32;"},
		{"duplicate.tl2", "Box<N:#> = n:[N]int32; get#11223344 => int32;"},
		{"legacy.tl", "get#11223344 => int; item {N:#} value:int = Box N;"},
		{"types.tl2", "Size <=> Count; Count <=> Size; Box<T:Type> = value:T;"},
		{"duplicate.tl2", ""},
		{"types.tl2", ""},
		{"legacy.tl", "get#55667788 => int; item {T:Type} value:T = Box T;"},
		{"types.tl2", "Size <=> uint32; Count <=> Size; Box<T:Type> = value:T;"},
	}
	for i, step := range steps {
		t.Run(step[0]+string(rune('a'+i)), func(t *testing.T) { set(step[0], step[1]); check() })
	}
}

func TestUnchangedFilesAndSnapshotsAreReused(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a.tl2")
	b := filepath.Join(root, "b.tl2")
	writeFile(t, a, "Item = id:int32;")
	writeFile(t, b, "Use = value:Item;")
	s := &server{docs: map[string]*document{}, roots: []string{root}, out: io.Discard, log: log.New(io.Discard, "", 0)}
	s.reindex()
	old := s.index
	oldDoc := s.docs[uriFromPath(b)]
	s.reindex()
	if s.index != old || s.docs[uriFromPath(b)] != oldDoc {
		t.Fatal("unchanged files were reindexed")
	}
	writeFile(t, a, "Other = id:int32;")
	s.reindex()
	if old.byName["Item"][0].sym.name != "Item" || len(old.byName["Other"]) != 0 {
		t.Fatal("old generation was mutated")
	}
	if s.docs[uriFromPath(b)] != oldDoc || len(s.semantic[oldDoc.uri]) != 1 {
		t.Fatal("unchanged consumer was reparsed or did not update")
	}
	if err := os.Remove(a); err != nil {
		t.Fatal(err)
	}
	s.reindex()
	if len(s.byName["Other"]) != 0 {
		t.Fatal("deleted definition remained in index")
	}
}

func TestTypingChangesCoalesceAndRequestsSeeLatestVersion(t *testing.T) {
	uri := "file:///tmp/coalesced.tl2"
	messages := []map[string]any{rpc("initialize", 0, map[string]any{}), openDoc(uri, "Item = id:int32;", 1)}
	for version := 2; version <= 11; version++ {
		messages = append(messages, rpc("textDocument/didChange", nil, map[string]any{"textDocument": map[string]any{"uri": uri, "version": version}, "contentChanges": []any{map[string]any{"text": "Renamed = id:int64;"}}}))
	}
	messages = append(messages, rpc("textDocument/hover", 1, docParams(uri, position{})), rpc("shutdown", 2, nil), rpc("exit", nil, nil))
	results := runSession(t, messages...)
	items := publications(t, results, uri)
	if len(items) != 2 || items[1].Version == nil || *items[1].Version != 11 {
		t.Fatalf("obsolete typing diagnostics published: %+v", items)
	}
	var hover struct{ Contents markup }
	response(t, results, 1, &hover)
	if !strings.Contains(hover.Contents.Value, "Renamed") {
		t.Fatalf("request saw old text: %+v", hover)
	}
}

func TestCancelledRenameDoesNotParseCandidate(t *testing.T) {
	d := parseDocument("file:///tmp/cancel.tl2", "Item = id:int32; Use = value:Item;", 1, true)
	s := editingServer(d)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.ctx = ctx
	_, err := s.rename(d, 0, "Renamed")
	if err == nil || err.Code != requestCancelled {
		t.Fatalf("cancel ignored: %+v", err)
	}
}

func TestProtocolCancellationAndCompletion(t *testing.T) {
	f := newBenchmarkFixture(t, 1000)
	uri := f.uri(0)
	results := runSession(t, rpc("initialize", 0, map[string]any{"rootUri": uriFromPath(f.root)}),
		rpc("textDocument/rename", 1, renameParams(uri, newSource(f.files[0].text).position(strings.Index(f.files[0].text, "bench.Shared")), "bench.Renamed")),
		rpc("$/cancelRequest", nil, map[string]any{"id": 1}),
		rpc("textDocument/completion", 2, docParams(uri, newSource(f.files[0].text).position(strings.LastIndex(f.files[0].text, "shared:bench.Shared")+len("shared:")))),
		rpc("shutdown", 3, nil), rpc("exit", nil, nil))
	if err := responseError(t, results, 1); err.Code != requestCancelled {
		t.Fatalf("cancel ignored: %+v", err)
	}
	var completion []completionItem
	response(t, results, 2, &completion)
	if len(completion) < 1000 {
		t.Fatalf("completion missing: %d", len(completion))
	}
	seen := map[string]bool{}
	for _, result := range results {
		if id := string(result["id"]); len(result["id"]) > 0 {
			if seen[id] {
				t.Fatalf("duplicate response %s", id)
			}
			seen[id] = true
		}
	}
}

func TestDiagnosticsSkipUnchangedButPublishVersions(t *testing.T) {
	uri := "file:///tmp/diagnostic-versions.tl2"
	results := runSession(t, rpc("initialize", 0, map[string]any{}), openDoc(uri, "Item = x:Missing;", 1),
		rpc("textDocument/didSave", nil, docParams(uri, position{})),
		rpc("textDocument/didChange", nil, map[string]any{"textDocument": map[string]any{"uri": uri, "version": 2}, "contentChanges": []any{map[string]any{"text": "Item = x:Missing;"}}}),
		rpc("shutdown", 3, nil), rpc("exit", nil, nil))
	items := publications(t, results, uri)
	if len(items) != 2 || items[1].Version == nil || *items[1].Version != 2 {
		t.Fatalf("publication versions: %+v", items)
	}
}

func TestFieldEditKeepsIndependentAliasConsumerChecks(t *testing.T) {
	defs := parseDocument("file:///tmp/local-change.tl2", "Size <=> uint32; Count <=> Size; Item = x:int32; Next = x:Item;", 1, true)
	use := parseDocument("file:///tmp/alias-consumer.tl2", "Use = n:Count values:[n]int32 unknown:Missing;", 0, false)
	s := editingServer(defs, use)
	before := s.index.checks[use.uri]
	if len(before) != 1 {
		t.Fatalf("missing control diagnostic: %+v", before)
	}
	s.docs[defs.uri] = parseDocument(defs.uri, "Size <=> uint32; Count <=> Size; Item = x:; Next = x:Item;", 2, true)
	s.refreshIndex(false)
	if after := s.index.checks[use.uri]; len(after) != 1 || &after[0] != &before[0] {
		t.Fatal("an unrelated declaration invalidated alias consumers")
	}
}

func TestWatcherUpdatesSelectedFilesAndPreservesOpenBuffers(t *testing.T) {
	root := t.TempDir()
	allowed := filepath.Join(root, "schema")
	if err := os.Mkdir(allowed, 0700); err != nil {
		t.Fatal(err)
	}
	usePath := filepath.Join(allowed, "use.tl2")
	targetPath := filepath.Join(allowed, "target.tl2")
	writeFile(t, usePath, "Use = x:Target;")
	s := &server{docs: map[string]*document{}, roots: []string{root}, out: io.Discard, log: log.New(io.Discard, "", 0), initialized: true}
	s.options.SchemaPaths = []string{"schema"}
	s.reindex()
	watch := func(path string, kind int) {
		t.Helper()
		raw := benchmarkJSON(t, map[string]any{"changes": []any{map[string]any{"uri": uriFromPath(path), "type": kind}}})
		if err := s.notification("workspace/didChangeWatchedFiles", raw); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, targetPath, "Target = x:int32;")
	watch(targetPath, 1)
	if len(s.semantic[uriFromPath(usePath)]) != 0 {
		t.Fatal("created dependency did not clear the error")
	}
	outside := filepath.Join(root, "outside.tl2")
	writeFile(t, outside, "Outside = x:int32;")
	watch(outside, 1)
	if s.docs[uriFromPath(outside)] != nil {
		t.Fatal("watcher bypassed schemaPaths")
	}
	uri := uriFromPath(targetPath)
	s.docs[uri] = parseDocument(uri, "Unsaved = x:int32;", 2, true)
	s.refreshIndex(false)
	old := s.index
	watch(targetPath, 2)
	if s.index != old || s.docs[uri].symbols[0].name != "Unsaved" {
		t.Fatal("watcher replaced the open buffer")
	}
	if err := s.notification("textDocument/didClose", benchmarkJSON(t, docParams(uri, position{}))); err != nil {
		t.Fatal(err)
	}
	if s.docs[uri].open || s.docs[uri].symbols[0].name != "Target" {
		t.Fatal("close did not restore disk content")
	}
	if err := os.Remove(targetPath); err != nil {
		t.Fatal(err)
	}
	watch(targetPath, 3)
	if s.docs[uri] != nil || len(s.semantic[uriFromPath(usePath)]) != 1 {
		t.Fatal("delete did not invalidate the consumer")
	}
}

func TestRenameResponseCannotFollowANewerWorkspace(t *testing.T) {
	f := newBenchmarkFixture(t, 1000)
	uri := f.uri(0)
	results := runSession(t, rpc("initialize", 0, map[string]any{"rootUri": uriFromPath(f.root)}),
		rpc("textDocument/rename", 1, renameParams(uri, newSource(f.files[0].text).position(strings.Index(f.files[0].text, "bench.Shared")), "bench.Renamed")),
		openDoc(uri, f.files[0].text, 2), rpc("shutdown", 2, nil), rpc("exit", nil, nil))
	changed := false
	found := false
	for _, result := range results {
		if string(result["method"]) == `"textDocument/publishDiagnostics"` {
			var publication diagnosticPublication
			if err := json.Unmarshal(result["params"], &publication); err != nil {
				t.Fatal(err)
			}
			if publication.URI == uri && publication.Version != nil && *publication.Version == 2 {
				changed = true
			}
		}
		if string(result["id"]) == "1" {
			found = true
			if changed {
				var rpcErr rpcError
				if err := json.Unmarshal(result["error"], &rpcErr); err != nil || rpcErr.Code != requestFailed {
					t.Fatalf("stale edits returned: %+v", result)
				}
			}
		}
	}
	if !found || !changed {
		t.Fatal("missing rename or updated diagnostics")
	}
}

func TestCancellationMatchesEscapedStringID(t *testing.T) {
	f := newBenchmarkFixture(t, 1000)
	uri := f.uri(0)
	results := runSession(t, rpc("initialize", 0, map[string]any{"rootUri": uriFromPath(f.root)}),
		rpc("textDocument/rename", json.RawMessage(`"\u006aob"`), renameParams(uri, newSource(f.files[0].text).position(strings.Index(f.files[0].text, "bench.Shared")), "bench.Renamed")),
		rpc("$/cancelRequest", nil, map[string]any{"id": "job"}), rpc("shutdown", 2, nil), rpc("exit", nil, nil))
	for _, result := range results {
		var id string
		if json.Unmarshal(result["id"], &id) == nil && id == "job" {
			var rpcErr rpcError
			if json.Unmarshal(result["error"], &rpcErr) != nil || rpcErr.Code != requestCancelled {
				t.Fatalf("string ID cancellation ignored: %+v", result)
			}
			return
		}
	}
	t.Fatal("missing response with string ID")
}
