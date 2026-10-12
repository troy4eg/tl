package tllsp

import (
	"strings"
	"testing"
)

func editingServer(docs ...*document) *server {
	s := &server{docs: map[string]*document{}}
	for _, d := range docs {
		s.docs[d.uri] = d
	}
	s.rebuildDefinitions()
	return s
}

func hasCompletion(items []completionItem, name string) bool {
	for _, item := range items {
		if item.Label == name {
			return true
		}
	}
	return false
}

func TestEditingNavigationAndScopes(t *testing.T) {
	for _, tc := range []struct{ language, known, text, local, use string }{
		{"tl2", "Item = id:int64; List<T:Type> = x:T;", "// 😀\r\nDraft<T:Type> = value:Item items:List<T,\r\nAfter = x:Item;", "T", "List<T"},
		{"tl", "item id:int = Item; box {T:Type} x:T = Box T;", "// 😀\r\ndraft {T:Type} value:Item items:(Box T\r\nafter value:Item = After;", "T", "Box T"},
	} {
		t.Run(tc.language, func(t *testing.T) {
			d := parseDocument("file:///tmp/editing."+tc.language, tc.text, 1, true)
			known := parseDocument("file:///tmp/known."+tc.language, tc.known, 0, false)
			s := editingServer(d, known)
			if len(d.symbols) != 2 || !d.symbols[0].incomplete || d.symbols[1].incomplete {
				t.Fatalf("recovery: %+v", d.symbols)
			}
			localOffset := strings.Index(tc.text, tc.use) + len(tc.use) - 1
			defs, r := s.symbolAt(d, localOffset)
			if len(defs) != 1 || defs[0].sym.name != tc.local || defs[0].doc != d || r != d.span(localOffset, localOffset+1) {
				t.Fatalf("parameter definition: %+v %+v", defs, r)
			}
			if refs := s.references(d, localOffset, true); len(refs) != 2 {
				t.Fatalf("parameter references: %+v", refs)
			}
			if _, err := s.rename(d, localOffset, "U"); err == nil || !strings.Contains(err.Message, "Finish the declaration") {
				t.Fatalf("rename accepted partial syntax: %v", err)
			}
			defs, _ = s.symbolAt(d, strings.Index(tc.text, "value:Item")+6)
			if len(defs) != 1 || defs[0].doc != known || !strings.Contains(defs[0].sym.signature, "id:") {
				t.Fatalf("global definition/hover: %+v", defs)
			}
			items := s.completion(d, strings.Index(tc.text, "value:")+6)
			for _, name := range []string{"T", "value", "items", "Item"} {
				if !hasCompletion(items, name) {
					t.Errorf("missing %s: %+v", name, items)
				}
			}
			// Completed declarations elsewhere retain local rename despite this fragment.
			if _, err := s.rename(known, strings.Index(tc.known, "T:Type"), "U"); err != nil {
				t.Fatalf("complete local rename: %v", err)
			}
		})
	}

	d := parseDocument("file:///tmp/union.tl2", "Choice<T:Type> = | first n:uint32 items:[n]T | second m:uint32 items:", 1, true)
	s := editingServer(d)
	items := s.completion(d, len(d.text))
	if !hasCompletion(items, "T") || !hasCompletion(items, "m") || !hasCompletion(items, "items") || hasCompletion(items, "n") {
		t.Fatalf("union scope: %+v", items)
	}
	for _, text := range []string{"Choice<T:Type> =\nfirst x:T", "Choice<T:Type> =\nfirst x:T\n| second y:", "Choice = empty"} {
		d = parseDocument("file:///tmp/union.tl2", text, 1, true)
		if len(d.symbols) != 1 || d.symbols[0].kind != 10 {
			t.Fatalf("partial union became a function: %+v, text=%q", d.symbols, text)
		}
		if len(d.symbols[0].children) == 0 {
			t.Fatal("lost partial variant")
		}
	}
	d = parseDocument("file:///tmp/nested.tl", "item {T:Type} outer:# group:[n:# value:n*[x:T] pending:", 1, true)
	s = editingServer(d)
	items = s.completion(d, len(d.text))
	if !hasCompletion(items, "n") || !hasCompletion(items, "outer") || !hasCompletion(items, "T") {
		t.Fatalf("nested scope: %+v", items)
	}
}

func TestIncrementalTypingPromotesDeclarations(t *testing.T) {
	uri, consumerURI := "file:///tmp/typing.tl2", "file:///tmp/consumer.tl2"
	text := "// 😀\r\nDraft<T:Type> = value:T\r\nAfter = x:int32;"
	at := newSource(text).position(strings.Index(text, "value:T") + len("value:T"))
	change := func(version int, r span, text string) map[string]any {
		return rpc("textDocument/didChange", nil, map[string]any{"textDocument": map[string]any{"uri": uri, "version": version}, "contentChanges": []any{map[string]any{"range": r, "text": text}}})
	}
	results := runSession(t,
		rpc("initialize", 0, map[string]any{}),
		openDoc(consumerURI, "Use = x:Draft<7>;", 1), openDoc(uri, text, 1),
		rpc("textDocument/definition", 1, docParams(uri, newSource(text).position(strings.Index(text, "value:T")+6))),
		rpc("textDocument/hover", 2, docParams(uri, newSource(text).position(strings.Index(text, "value:T")+6))),
		change(2, span{at, at}, ";"),
		rpc("textDocument/documentSymbol", 3, docParams(uri, position{})),
		// Remove the complete field and its semicolon: old symbols must disappear.
		change(3, span{newSource(text).position(strings.Index(text, "value:T")), position{at.Line, at.Character + 1}}, ""),
		rpc("textDocument/documentSymbol", 4, docParams(uri, position{})),
		rpc("textDocument/completion", 5, docParams(uri, newSource(text).position(strings.Index(text, "value:T")))),
		rpc("shutdown", 6, nil), rpc("exit", nil, nil),
	)
	var defs []location
	response(t, results, 1, &defs)
	if len(defs) != 1 || defs[0].Range.Start != (position{1, 6}) {
		t.Fatalf("partial navigation: %+v", defs)
	}
	var h struct{ Contents markup }
	response(t, results, 2, &h)
	if !strings.Contains(h.Contents.Value, "T:Type") {
		t.Fatalf("partial hover: %+v", h)
	}
	var symbols []documentSymbol
	response(t, results, 3, &symbols)
	if len(symbols) != 2 || len(symbols[0].Children) != 2 {
		t.Fatalf("promoted symbols: %+v", symbols)
	}
	response(t, results, 4, &symbols)
	if len(symbols) != 2 || len(symbols[0].Children) != 1 {
		t.Fatalf("stale field: %+v", symbols)
	}
	var items []completionItem
	response(t, results, 5, &items)
	if !hasCompletion(items, "T") || hasCompletion(items, "value") {
		t.Fatalf("stale completion: %+v", items)
	}
	ps := publications(t, results, consumerURI)
	if len(ps) != 4 || len(ps[1].Diagnostics) != 0 || len(ps[2].Diagnostics) != 1 || ps[2].Diagnostics[0].Code != "argument-category" || len(ps[3].Diagnostics) != 0 {
		t.Fatalf("semantic transitions: %+v", ps)
	}
}

func TestPartialTypesDoNotHideUnrelatedUnknownTypes(t *testing.T) {
	partial := parseDocument("file:///tmp/partial.tl2", "Known<T:Type> = x:", 1, true)
	consumer := parseDocument("file:///tmp/consumer.tl", "use value:(Missing Known) = Use;", 1, true)
	s := editingServer(partial, consumer)
	diagnostics := s.diagnostics(consumer)
	if len(diagnostics) != 1 || diagnostics[0].Message != "Unknown type 'Missing'" {
		t.Fatalf("partial argument suppressed unknown base type: %+v", diagnostics)
	}
}

func checkEditingRanges(t *testing.T, text string) {
	t.Helper()
	for _, language := range []string{"tl", "tl2"} {
		d := parseDocument("file:///tmp/typing."+language, text, 1, true)
		check := func(b, e int) {
			if b < 0 || e < b || e > len(text) {
				t.Fatalf("%s invalid range %d..%d, text=%q", language, b, e, text)
			}
		}
		walkSymbols(d.symbols, func(sym *symbol) {
			check(sym.begin, sym.end)
			check(sym.nameBegin, sym.nameEnd)
			if sym.nameBegin < sym.begin || sym.nameEnd > sym.end {
				t.Fatalf("selection outside symbol: %+v, text=%q", sym, text)
			}
			if text[sym.nameBegin:sym.nameEnd] != sym.name {
				t.Fatalf("invented symbol %s: %q", sym.name, text[sym.nameBegin:sym.nameEnd])
			}
		})
		for _, ref := range d.refs {
			check(ref.begin, ref.end)
			if text[ref.begin:ref.end] != ref.name {
				t.Fatalf("invented ref %s: %q", ref.name, text[ref.begin:ref.end])
			}
		}
		s := editingServer(d)
		_ = s.completion(d, len(text))
		_ = d.semanticTokens()
		_ = s.diagnostics(d)
	}
}

func TestEveryTypingPrefix(t *testing.T) {
	for _, text := range []string{
		"// 😀\r\nBox<T:Type,N:#> = n:uint32 items:[N]List<T>;\r\nAfter = x:Box<int32,7>;",
		"Choice<T:Type> = | one n:uint32 items:[n]T | two value:T;",
		"Choice<T:Type> =\nfirst x:T\n| second y:T;",
		"get#12345678 value:List<Item> => <=> Item;",
		"Alias <=> List<Item>;",
		"---types---\nbox {T:Type} flags:# value:flags.0?T items:n*[n:# value:(Box T)] = Box T;\n---functions---\nget value:Box<int> = Box int;",
		"_ {X:Type} result:X = ReqResult X;",
	} {
		for i := 0; i <= len(text); i++ {
			checkEditingRanges(t, text[:i])
		}
	}
}

func FuzzEditingDocuments(f *testing.F) {
	for _, text := range []string{"Box<T:", "Box<T:Type> = x:List<T,\nAfter = ;", "Choice = | a x:[n]Item | b value:", "item {T:Type} x:(Box T\nafter = After;", "item items:[n:# value:", "item x:flags.", "@;", "Box = x:💥;", "// привет 😀\r\nget#12345678 x:Item => <=> List<"} {
		f.Add(text)
	}
	f.Fuzz(func(t *testing.T, text string) {
		if len(text) < 8192 {
			checkEditingRanges(t, text)
		}
	})
}
