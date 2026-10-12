package tllsp

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestTLNavigationAndRecovery(t *testing.T) {
	root := t.TempDir()
	uri := uriFromPath(filepath.Join(root, "schema.tl"))
	text := `---types---
item id:int = Item;
empty = Item;
box {X:Type} flags:# value:flags.0?X count:# items:count*[item:Item] = Box X;
broken value: = Broken;
after value:%item = After;
---functions---
get value:Item = Box Item;
`
	open := openDoc(uri, text, 1)
	open["params"].(map[string]any)["textDocument"].(map[string]any)["languageId"] = "tl"
	src := newSource(text)
	results := runSession(t,
		rpc("initialize", 0, map[string]any{"rootUri": uriFromPath(root)}), open,
		rpc("textDocument/definition", 1, docParams(uri, src.position(strings.Index(text, "item:Item")+5))),
		rpc("textDocument/definition", 2, docParams(uri, src.position(strings.Index(text, "flags.0")+1))),
		rpc("textDocument/definition", 3, docParams(uri, src.position(strings.Index(text, "?X")+1))),
		rpc("textDocument/definition", 4, docParams(uri, src.position(strings.Index(text, "%item")+1))),
		rpc("textDocument/definition", 5, docParams(uri, src.position(strings.Index(text, "count*")+1))),
		rpc("textDocument/definition", 6, docParams(uri, src.position(strings.LastIndex(text, "Box Item")))),
		rpc("textDocument/documentSymbol", 7, docParams(uri, position{})),
		rpc("textDocument/completion", 8, docParams(uri, src.position(strings.LastIndex(text, "Item")+2))),
	)
	for id, offsets := range map[int][]int{
		1: {strings.Index(text, "= Item") + 2, strings.Index(text, "empty = Item") + 8},
		2: {strings.Index(text, "flags:#")},
		3: {strings.Index(text, "X:Type")},
		4: {strings.Index(text, "item id:")},
		5: {strings.Index(text, "count:#")},
		6: {strings.Index(text, "= Box") + 2},
	} {
		var defs []location
		response(t, results, id, &defs)
		if len(defs) != len(offsets) {
			t.Fatalf("request %d: definitions %+v, expected %v", id, defs, offsets)
		}
		for i, off := range offsets {
			if defs[i].Range.Start != src.position(off) {
				t.Errorf("request %d: definition %+v, expected %+v", id, defs[i], src.position(off))
			}
		}
	}
	var syms []documentSymbol
	response(t, results, 7, &syms)
	if len(syms) != 5 || syms[3].Name != "after" || syms[4].Kind != 12 {
		t.Fatalf("symbols after invalid declaration: %+v", syms)
	}
	var items []completionItem
	response(t, results, 8, &items)
	if len(items) != 1 || items[0].Label != "Item" {
		t.Fatalf("TL type completion: %+v", items)
	}
}

func TestManualTL2LanguageOnTLFileSurvivesEdits(t *testing.T) {
	uri := "file:///tmp/manual.tl"
	text := "item = id:int32; use = value:item;"
	results := runSession(t,
		rpc("initialize", 0, map[string]any{}), openDoc(uri, text, 1),
		rpc("textDocument/didChange", nil, map[string]any{"textDocument": map[string]any{"uri": uri, "version": 2}, "contentChanges": []any{map[string]any{"text": text + " after = id:int32;"}}}),
		rpc("textDocument/definition", 1, docParams(uri, newSource(text).position(strings.LastIndex(text, "item")))),
	)
	var defs []location
	response(t, results, 1, &defs)
	if len(defs) != 1 || defs[0].Range.Start != (position{}) {
		t.Fatalf("manual language was ignored: %+v", defs)
	}
}

func TestLegacyReqResultDoesNotBlockRename(t *testing.T) {
	root := t.TempDir()
	legacyPath := filepath.Join(root, "legacy.tl")
	legacyURI := uriFromPath(legacyPath)
	legacy := "---types---\n_ {X:Type} result:X = ReqResult X;\nuse result:(ReqResult int) = Use;\n"
	writeFile(t, legacyPath, legacy)
	uri := uriFromPath(filepath.Join(root, "editor.tl2"))
	results := runSession(t,
		rpc("initialize", 0, map[string]any{"rootUri": uriFromPath(root)}),
		openDoc(uri, "Item = value:int32;", 1),
		rpc("textDocument/rename", 1, renameParams(uri, position{}, "Renamed")),
		rpc("textDocument/definition", 2, docParams(legacyURI, newSource(legacy).position(strings.LastIndex(legacy, "ReqResult")))),
		rpc("textDocument/rename", 3, renameParams(legacyURI, newSource(legacy).position(strings.Index(legacy, "result:X")+len("result:")), "T")))
	var edit workspaceEdit
	response(t, results, 1, &edit)
	if len(edit.Changes[uri]) != 1 {
		t.Fatalf("legacy wrapper blocked unrelated rename: %+v", edit)
	}
	var defs []location
	response(t, results, 2, &defs)
	if len(defs) != 1 || defs[0].Range.Start != newSource(legacy).position(strings.Index(legacy, "ReqResult")) {
		t.Fatalf("legacy wrapper definition is missing: %+v", defs)
	}
	response(t, results, 3, &edit)
	got := applyEdits(t, legacy, edit.Changes[legacyURI])
	if got != strings.ReplaceAll(legacy, "X", "T") {
		t.Fatalf("template rename changed anonymous constructor or wrapper type: %q", got)
	}
}
