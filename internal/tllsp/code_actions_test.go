package tllsp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func actionEdits(t *testing.T, d *document, action codeAction) []textEdit {
	t.Helper()
	if action.Kind != "quickfix" || len(action.Diagnostics) != 1 {
		t.Fatalf("invalid action: %+v", action)
	}
	if len(action.Edit.DocumentChanges) > 0 {
		change := action.Edit.DocumentChanges[0]
		if len(action.Edit.DocumentChanges) != 1 || action.Edit.Changes != nil || change.TextDocument.URI != d.uri {
			t.Fatalf("invalid versioned edit: %+v", action.Edit)
		}
		if d.open && (change.TextDocument.Version == nil || *change.TextDocument.Version != d.version) || !d.open && change.TextDocument.Version != nil {
			t.Fatalf("invalid document version: %+v", change.TextDocument)
		}
		return change.Edits
	}
	if len(action.Edit.Changes) != 1 {
		t.Fatalf("invalid edit: %+v", action.Edit)
	}
	return action.Edit.Changes[d.uri]
}

func applyAction(t *testing.T, d *document, action codeAction) *document {
	t.Helper()
	edits := actionEdits(t, d, action)
	if len(edits) != 1 {
		t.Fatalf("expected one occurrence: %+v", edits)
	}
	b, be := d.offset(edits[0].Range.Start)
	e, ee := d.offset(edits[0].Range.End)
	if be != nil || ee != nil || e < b {
		t.Fatalf("invalid edit range: %+v", edits[0])
	}
	return parseDocumentLanguage(d.uri, d.text[:b]+edits[0].NewText+d.text[e:], d.version+1, d.open, d.language)
}

func allActions(s *server, d *document) []codeAction {
	return s.codeActions(d, d.span(0, len(d.text)), nil, nil)
}

func TestTypeQuickFixes(t *testing.T) {
	for _, tc := range []struct {
		language, definitions, text string
		want, absent                []string
	}{
		{"tl2", "", "A = x:int y:long z:float w:double;", []string{"int32", "int64", "float32", "float64"}, nil},
		{"tl2", "Item = x:int32; Items = x:int32;", "// 😀\r\nUse = x:Itme y:Itme;", []string{"Item"}, nil},
		{"tl2", "Item = x:int32; Items = x:int32;", "Use = x:Itemz;", []string{"Item", "Items"}, nil},
		{"tl2", "api.Item = x:int32;", "Use = x:api.Itme;", []string{"api.Item"}, nil},
		{"tl2", "Box<T:Type> = x:T; Boxes = x:int32;", "Use = x:Bxo<int32>;", []string{"Box"}, []string{"Boxes"}},
		{"tl2", "Box<T:Type> = x:T;", "Use = x:Bxo<7>;", nil, []string{"Box"}},
		{"tl2", "Box<T:Type> = x:T;", "Use = x:Bxo;", nil, []string{"Box"}},
		{"tl2", "", "D<T:Type,N:#> = value:t;", []string{"T"}, []string{"N"}},
		{"tl2", "Item = x:int32;", "D<T:Type> = x:itme;", []string{"Item"}, nil},
		{"tl2", "Item = x:int32;", "D<Item:#> = x:Itme;", nil, []string{"Item"}},
		{"tl2", "", "D = x:itn32;", []string{"int32"}, nil},
		{"tl2", "", "D = x:Unknown;", nil, []string{"int32", "uint32", "string"}},
		{"tl2", "Item = x:int32;", "D = Item:Item x:Itme;", nil, []string{"Item"}},
		{"tl2", "Item = x:int32;", "D = x:Itme Item:uint32;", []string{"Item"}, nil},
		{"tl2", "call#12345678 => int32;", "D = x:clal;", nil, []string{"call"}},
		{"tl2", "int <=> string;", "D = x:int;", nil, []string{"int32"}},
		{"tl2", "int32<T:Type> = x:T;", "D = x:int;", nil, []string{"int32"}},
		{"tl2", "Item = x:", "D = x:Itme;", nil, []string{"Item"}},
		{"tl2", "Item = x:int32;", "D = x:Itme; Other = x:", []string{"Item"}, nil},
		{"tl", "item id:int = Item;", "use value:Itme = Use;", []string{"Item"}, nil},
		{"tl", "item id:int = Item;", "use value:%itme = Use;", []string{"item"}, []string{"Item"}},
		{"tl", "box {T:Type} value:T = Box T;", "use value:(Bxo int) = Use;", []string{"Box"}, nil},
		{"tl", "", "use value:int = Use;", nil, []string{"int32"}},
	} {
		t.Run(tc.text+tc.definitions, func(t *testing.T) {
			d := parseDocument("file:///tmp/fixes."+tc.language, tc.text, 4, true)
			s := editingServer(d, parseDocument("file:///tmp/fix-defs."+tc.language, tc.definitions, 0, false))
			s.documentChanges = true
			actions := allActions(s, d)
			got := map[string]bool{}
			for _, action := range actions {
				edit := actionEdits(t, d, action)[0]
				got[edit.NewText] = true
				updated := applyAction(t, d, action)
				fixed := editingServer(updated, s.docs["file:///tmp/fix-defs."+tc.language])
				if len(fixed.semantic[d.uri]) >= len(s.semantic[d.uri]) || len(updated.errors) != len(d.errors) {
					t.Fatalf("fix did not remove a diagnostic: %+v, %q", action, updated.text)
				}
				if action.IsPreferred != (tc.language == "tl2" && legacyPrimitive(d.textRange(edit.Range)) == edit.NewText) {
					t.Fatalf("incorrect preferred action: %+v", action)
				}
			}
			for _, name := range tc.want {
				if !got[name] {
					t.Errorf("missing %s: %+v diagnostics=%+v", name, actions, s.diagnostics(d))
				}
			}
			for _, name := range tc.absent {
				if got[name] {
					t.Errorf("unexpected %s: %+v", name, actions)
				}
			}
			if len(tc.want) == 0 && len(actions) > 0 {
				t.Errorf("unexpected fixes: %+v", actions)
			}
		})
	}
}

func TestMissingSemicolonQuickFix(t *testing.T) {
	for _, tc := range []struct{ language, text string }{
		{"tl2", "// 😀\r\nBefore = x:int32 // inline\r\n// after\r\nAfter = x:string;"},
		{"tl2", "Alias <=> int32 // trailing"},
		{"tl2", "Before<T:Type> = x:T\nAfter = x:int32;"},
		{"tl2", "Choice =\n| a x:int32\n| b y:string\n"},
		{"tl2", "call#12345678 x:int32 => int32"},
		{"tl2", "Record ="},
		{"tl2", "Before = x:int32\nAfter = x:"},
		{"tl", "before value:int = Before // inline\n// after\nafter value:Before = After;"},
		{"tl", "---functions---\nget#12345678 x:int = int\n---types---\nafter = After;"},
		{"tl", "before {X:Type} x:X = Before X"},
		{"tl", "_ {X:Type} result:X = ReqResult X"},
	} {
		t.Run(tc.language+tc.text, func(t *testing.T) {
			d := parseDocument("file:///tmp/semicolon."+tc.language, tc.text, 3, true)
			s := editingServer(d)
			actions := allActions(s, d)
			if len(actions) != 1 {
				t.Fatalf("missing fix: %+v, diagnostics=%+v", actions, d.errors)
			}
			if !actions[0].IsPreferred {
				t.Fatal("semicolon is not preferred")
			}
			updated := applyAction(t, d, actions[0])
			if updated.symbols[0].incomplete || len(updated.errors) >= len(d.errors) {
				t.Fatalf("still incomplete: %q", updated.text)
			}
			edit := actionEdits(t, d, actions[0])[0]
			// A caret at the insertion point must work even when the parser
			// reports the error at the next declaration or at EOF.
			if len(s.codeActions(d, edit.Range, nil, []string{"quickfix"})) != 1 {
				t.Fatal("no fix at insertion point")
			}
			if strings.Contains(tc.text, "// inline") && !strings.Contains(updated.text, "; // inline") {
				t.Fatalf("semicolon inserted in comment: %q", updated.text)
			}
		})
	}
	for _, tc := range []struct{ language, text string }{
		{"tl2", "D = x:Box<int32\nAfter = x:int32;"},
		{"tl2", "D = x:\nAfter = x:int32;"},
		{"tl2", "D = x:int32 garbage"},
		{"tl2", "D = |"},
		{"tl2", "D<T:Type"},
		{"tl2", "D = x:int32; // no semicolon needed"},
		{"tl2", "Before = x:int32\rAfter = x:string;"},
		{"tl", "d x:(Box int = D"},
		{"tl", "d x:int"},
		{"tl", "// d x:int = D"},
	} {
		d := parseDocument("file:///tmp/no-fix."+tc.language, tc.text, 0, false)
		if actions := allActions(editingServer(d), d); len(actions) != 0 {
			t.Fatalf("unsafe fix for %q: %+v", tc.text, actions)
		}
	}
}

func TestQuickFixRangeContextAndVersions(t *testing.T) {
	d := parseDocument("file:///tmp/context.tl2", "D = x:int y:int;", 7, true)
	s := editingServer(d)
	diag := s.diagnostics(d)[0]
	if actions := s.codeActions(d, diag.Range, []diagnostic{diag}, nil); len(actions) != 1 {
		t.Fatalf("actions=%+v", actions)
	}
	stale := diag
	stale.Message = "Unknown type 'long'"
	if actions := s.codeActions(d, diag.Range, []diagnostic{stale}, nil); len(actions) != 0 {
		t.Fatal("accepted stale diagnostics")
	}
	stale = diag
	stale.Source = "another-server"
	if actions := s.codeActions(d, diag.Range, []diagnostic{stale}, nil); len(actions) != 0 {
		t.Fatal("accepted another server's diagnostics")
	}
	if actions := s.codeActions(d, d.span(0, 0), nil, nil); len(actions) != 0 {
		t.Fatal("unrelated range")
	}
	for _, only := range [][]string{{"source.fixAll"}, {"refactor"}, {"quickfix.other"}} {
		if actions := s.codeActions(d, diag.Range, nil, only); len(actions) != 0 {
			t.Fatalf("ignored kind filter %v", only)
		}
	}
	for _, versioned := range []bool{false, true} {
		for _, open := range []bool{false, true} {
			d.open, s.documentChanges = open, versioned
			for _, action := range allActions(s, d) {
				applyAction(t, d, action)
			}
		}
	}
	s.options.Diagnostics.UnknownTypes = "off"
	if actions := allActions(s, d); len(actions) != 0 {
		t.Fatal("fix offered for disabled diagnostic")
	}
}

func TestQuickFixProtocolUsesUnsavedBuffer(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "quick fixes.tl2")
	writeFile(t, file, "Saved = x:int32;")
	uri := uriFromPath(file)
	text := "// 😀\r\nUnsaved = x:int;"
	r := newSource(text).span(strings.Index(text, "int;"), strings.Index(text, "int;")+3)
	request := func(r span, only []string) map[string]any {
		return map[string]any{"textDocument": map[string]any{"uri": uri}, "range": r, "context": map[string]any{"diagnostics": []diagnostic{}, "only": only}}
	}
	caps := map[string]any{"workspace": map[string]any{"workspaceEdit": map[string]any{"documentChanges": true}}, "textDocument": map[string]any{"codeAction": map[string]any{"codeActionLiteralSupport": map[string]any{"codeActionKind": map[string]any{"valueSet": []string{"quickfix"}}}}}}
	results := runSession(t,
		rpc("initialize", 0, map[string]any{"rootUri": uriFromPath(root), "capabilities": caps}),
		openDoc(uri, text, 9),
		rpc("textDocument/codeAction", 1, request(r, []string{"quickfix"})),
		openDoc(uri, strings.Replace(text, "int;", "int32;", 1), 10),
		rpc("textDocument/codeAction", 2, request(r, nil)),
		rpc("textDocument/codeAction", 3, request(span{position{1, 999}, position{1, 1000}}, nil)),
		rpc("textDocument/codeAction", 4, request(span{r.End, r.Start}, nil)),
		rpc("shutdown", 5, nil), rpc("exit", nil, nil))
	var initialized struct {
		Capabilities struct {
			CodeActionProvider struct {
				Kinds []string `json:"codeActionKinds"`
			} `json:"codeActionProvider"`
		} `json:"capabilities"`
	}
	response(t, results, 0, &initialized)
	if len(initialized.Capabilities.CodeActionProvider.Kinds) != 1 || initialized.Capabilities.CodeActionProvider.Kinds[0] != "quickfix" {
		t.Fatal("missing capability")
	}
	var actions []codeAction
	response(t, results, 1, &actions)
	d := parseDocument(uri, text, 9, true)
	if len(actions) != 1 || applyAction(t, d, actions[0]).text != strings.Replace(text, "int;", "int32;", 1) {
		t.Fatalf("unsaved fix: %+v", actions)
	}
	response(t, results, 2, &actions)
	if len(actions) != 0 {
		t.Fatal("fixed diagnostic remained")
	}
	for _, id := range []string{"3", "4"} {
		found := false
		for _, item := range results {
			if string(item["id"]) == id {
				var rpcErr rpcError
				if json.Unmarshal(item["error"], &rpcErr) != nil || rpcErr.Code != -32602 {
					t.Fatalf("invalid range response: %+v", item)
				}
				found = true
			}
		}
		if !found {
			t.Fatal("missing invalid range response")
		}
	}
	if disk, err := os.ReadFile(file); err != nil || string(disk) != "Saved = x:int32;" {
		t.Fatal("code action wrote to disk")
	}
	unsupported := runSession(t, rpc("initialize", 0, map[string]any{}), openDoc(uri, text, 1), rpc("textDocument/codeAction", 1, request(r, nil)), rpc("shutdown", 2, nil), rpc("exit", nil, nil))
	response(t, unsupported, 1, &actions)
	if len(actions) != 0 {
		t.Fatal("literal sent to unsupported client")
	}
}
