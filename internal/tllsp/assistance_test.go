package tllsp

import (
	"regexp"
	"strings"
	"testing"

	"github.com/VKCOM/tl/internal/tlast"
)

func cursorDocument(language, text string) (*document, int) {
	offset := strings.Index(text, "^")
	return parseDocument("file:///tmp/assistance."+language, strings.Replace(text, "^", "", 1), 1, true), offset
}

func assistanceServer(d *document) *server {
	return editingServer(d,
		parseDocument("file:///tmp/definitions.tl2", "// Box documentation\nBox<T:Type,N:#> = values:[N]T; List<T:Type> = value:T; Size <=> uint32; Count<T:Type> <=> T; Outer<T:Type> <=> Count<T>; api.Item = id:int32;", 0, false),
		parseDocument("file:///tmp/definitions.tl", "pair {T:Type} {N:#} n:# values:N*[value:T] = Pair T N;", 0, false))
}

func TestCompletionContexts(t *testing.T) {
	for _, tc := range []struct {
		name, language, text string
		want, absent         []string
	}{
		{"field type", "tl2", "D<T:Type,N:#> = n:uint32 value:^ later:uint32;", []string{"T", "int32", "Box", "api.Item"}, []string{"N", "n", "later", "value", "Type", "#"}},
		{"type argument", "tl2", "D<T:Type,N:#> = n:uint32 value:Box<^>;", []string{"T", "int32", "List"}, []string{"N", "n", "0"}},
		{"nat argument", "tl2", "D<T:Type,N:#> = n:Size bad:string value:Box<T,^> late:uint32;", []string{"N", "n", "0"}, []string{"T", "int32", "Box", "bad", "late", "value"}},
		{"nested nat argument", "tl2", "D<T:Type,N:#> = n:uint32 value:List<Box<T,^>>;", []string{"N", "n", "0"}, []string{"T", "int32"}},
		{"outer nat argument", "tl2", "D<T:Type,N:#> = n:uint32 value:Box<List<T>,^>;", []string{"N", "n", "0"}, []string{"T", "int32"}},
		{"array index", "tl2", "D<T:Type,N:#> = n:Count<uint32> optional?:uint32 value:[^]T late:uint32;", []string{"N", "n", "0"}, []string{"T", "int32", "optional", "value", "late"}},
		{"ignored size", "tl2", "D = _n:uint32 value:[^]int32;", []string{"0"}, []string{"_n", "int32"}},
		{"array element", "tl2", "D<T:Type,N:#> = n:uint32 value:[n]^;", []string{"T", "int32", "Box"}, []string{"N", "n", "0"}},
		{"generic alias chain", "tl2", "D = n:Outer<uint32> value:[^]int32;", []string{"n", "0"}, []string{"int32", "Box"}},
		{"request scope", "tl2", "call#12345678 count:uint32 values:[^]int32 => answer:uint32;", []string{"count", "0"}, []string{"answer", "int32"}},
		{"unfinished request", "tl2", "call#12345678 count:uint32 values:[^", []string{"count", "0"}, []string{"int32"}},
		{"result scope", "tl2", "call#12345678 count:uint32 => values:[^]int32;", []string{"0"}, []string{"count", "int32"}},
		{"anonymous repeat scope", "tl", "d outer:# [n:# value:(Pair int ^)] = D;", []string{"n", "outer", "0"}, []string{"int"}},
		{"category", "tl2", "D<T:^>", []string{"Type", "#"}, []string{"int32", "Box", "T"}},
		{"category prefix", "tl2", "D<T:Ty^>", []string{"Type"}, []string{"#", "int32"}},
		{"category hash", "tl2", "D<T:#^>", []string{"#"}, []string{"Type"}},
		{"tl category", "tl", "d {T:^}", []string{"Type", "#"}, []string{"int", "Pair"}},
		{"tl mask prefix", "tl", "d {N:#} flags:# bad:int value:^ later:# = D N;", []string{"N", "flags", "int", "Pair"}, []string{"bad", "later", "value"}},
		{"tl masked type", "tl", "d {T:Type} {N:#} flags:# value:flags.0?^ = D T N;", []string{"T", "int", "Pair"}, []string{"N", "flags", "0"}},
		{"tl nat angle", "tl", "d {T:Type} {N:#} flags:# value:Pair<T,^> = D T N;", []string{"N", "flags", "0"}, []string{"T", "int", "Pair"}},
		{"tl nat apply", "tl", "d {T:Type} {N:#} flags:# value:(Pair T ^) = D T N;", []string{"N", "flags", "0"}, []string{"T", "int", "Pair"}},
		{"tl masked nat apply", "tl", "d flags:# count:flags.0?# bad:flags.0?int _ignored:# group:2*[#] value:flags.0?%(Pair int ^) later:# = D;", []string{"flags", "count", "0"}, []string{"bad", "_ignored", "group", "later", "value", "int", "Pair"}},
		{"tl masked mask prefix", "tl", "d flags:# mask:flags.0?# value:^ = D;", []string{"flags", "mask", "int"}, []string{"value"}},
		{"tl arithmetic nat apply", "tl", "d {N:#} value:(Pair int 3 + ^) = D N;", []string{"N", "0"}, []string{"int", "Pair"}},
		{"tl nested scope", "tl", "d {T:Type} outer:# group:[n:# value:(Pair T ^)] = D T;", []string{"n", "outer", "0"}, []string{"T", "int"}},
		{"namespace prefix", "tl2", "D = value:api.I^tem;", []string{"api.Item"}, []string{"int32", "Box"}},
		{"comments", "tl2", "D = value:int32; // Box<^", nil, []string{"Type", "int32", "Box", "0"}},
		{"field name", "tl2", "D = val^ue:int32;", nil, []string{"int32", "Box", "value"}},
		{"between fields", "tl2", "D<T:Type> = value:T ^;", nil, []string{"T", "int32", "Box"}},
		{"after generic field", "tl2", "D<T:Type> = value:Box<T,7> ^;", nil, []string{"T", "int32", "Box"}},
		{"element after index", "tl2", "D = value:[7] ^;", []string{"int32", "Box"}, []string{"0"}},
		{"finished alias", "tl2", "A <=> Box<int32,7> ^;", nil, []string{"int32", "Box"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, offset := cursorDocument(tc.language, tc.text)
			s := assistanceServer(d)
			items := s.completion(d, offset)
			for _, name := range tc.want {
				if !hasCompletion(items, name) {
					t.Errorf("missing %s: %+v, context=%+v", name, items, d.assistance(offset, true))
				}
			}
			for _, name := range tc.absent {
				if hasCompletion(items, name) {
					t.Errorf("unexpected %s: %+v", name, items)
				}
			}
			for _, item := range items {
				if item.InsertTextFormat == 2 || strings.Contains(item.TextEdit.NewText, "${") {
					t.Fatal("snippet sent to unsupported client")
				}
			}
		})
	}
}

func TestMaskBitCompletionKeepsMaskAndQuestionMark(t *testing.T) {
	for _, text := range []string{"d flags:# value:flags.^ = D;", "d flags:# value:flags.3^?int = D;", "d mode:# flags:mode.0?# value:flags.^ = D;"} {
		d, offset := cursorDocument("tl", text)
		items := assistanceServer(d).completion(d, offset)
		if len(items) == 0 {
			t.Fatal("missing bits")
		}
		for _, item := range items {
			b, _ := d.offset(item.TextEdit.Range.Start)
			e, _ := d.offset(item.TextEdit.Range.End)
			updated := d.text[:b] + item.TextEdit.NewText + d.text[e:]
			if !strings.Contains(updated, "flags."+item.Label+"?") || strings.Contains(updated, "??") {
				t.Fatalf("bad bit edit: %q", updated)
			}
		}
	}
}

func TestSignatureHelpApplications(t *testing.T) {
	for _, tc := range []struct {
		language, text, name string
		argument             int
	}{
		{"tl2", "D = value:Box<^>;", "Box", 0},
		{"tl2", "D = value:Box<int32,^>;", "Box", 1},
		{"tl2", "D = value:Box<List<int32>, ^>;", "Box", 1},
		{"tl2", "D = value:List<Box<int32,^>>;", "Box", 1},
		{"tl2", "D = value:Box<List<^>,7>;", "List", 0},
		{"tl2", "D = value:Box<\n int32, // comma , < > ignored\n ^>;", "Box", 1},
		{"tl", "d value:Pair<int,^> = D;", "Pair", 1},
		{"tl", "d value:(Pair ^) = D;", "Pair", 0},
		{"tl", "d value:(Pair int ^) = D;", "Pair", 1},
		{"tl", "d value:(Pair int 3+4^) = D;", "Pair", 1},
		{"tl", "d value:(Pair %int ^) = D;", "Pair", 1},
		{"tl", "---functions--- get = Pair int ^;", "Pair", 1},
		{"tl", "d value:(Pair (List int) ^) = D;", "Pair", 1},
	} {
		t.Run(tc.text, func(t *testing.T) {
			d, offset := cursorDocument(tc.language, tc.text)
			help := assistanceServer(d).signatures(d, offset)
			if help == nil || len(help.Signatures) != 1 || !strings.HasPrefix(help.Signatures[0].Label, tc.name+"<") || help.ActiveParameter != tc.argument {
				t.Fatalf("signature=%+v context=%+v", help, d.assistance(offset, false))
			}
			if help.Signatures[0].ActiveParameter != tc.argument {
				t.Fatal("active parameter differs from signature")
			}
		})
	}
	for _, text := range []string{"D = value:Box<int32,7>^;", "D = value:int32; // Box<^", "D<T:^> = x:T;", "D = x:int32^;"} {
		d, offset := cursorDocument("tl2", text)
		if help := assistanceServer(d).signatures(d, offset); help != nil {
			t.Fatalf("unexpected signature: %+v", help)
		}
	}
	d, offset := cursorDocument("tl", "d value:(Pair<int,7> ^) = D;")
	if help := assistanceServer(d).signatures(d, offset); help != nil {
		t.Fatalf("closed angle call inside parentheses: %+v", help)
	}
}

var snippetPlaceholder = regexp.MustCompile(`\$\{[0-9]+:([^}]*)\}|\$[0-9]+`)

func expandSnippet(text string) string {
	return snippetPlaceholder.ReplaceAllStringFunc(text, func(s string) string {
		if strings.HasPrefix(s, "${") {
			return s[strings.Index(s, ":")+1 : len(s)-1]
		}
		return ""
	})
}

func TestSnippetsAndGenericEdits(t *testing.T) {
	d, offset := cursorDocument("tl2", "Choice = | ^;")
	s := assistanceServer(d)
	s.snippetSupport = true
	items := s.completion(d, offset)
	if len(items) != 1 || items[0].Label != "variant" {
		t.Fatalf("variant snippets: %+v", items)
	}
	text := d.text[:offset] + expandSnippet(items[0].TextEdit.NewText) + d.text[offset:]
	if _, err := tlast.ParseTL2(text); err != nil {
		t.Fatalf("bad variant snippet: %q: %v", text, err)
	}
	for _, language := range []string{"tl", "tl2"} {
		d, offset := cursorDocument(language, "^")
		s := assistanceServer(d)
		s.snippetSupport = true
		for _, item := range s.completion(d, offset) {
			if item.InsertTextFormat != 2 {
				t.Fatalf("missing snippet format: %+v", item)
			}
			text := expandSnippet(item.TextEdit.NewText)
			if language == "tl2" {
				if _, err := tlast.ParseTL2(text); err != nil {
					t.Fatalf("invalid %s snippet %q: %v", item.Label, text, err)
				}
			} else if _, err := tlast.ParseTLFile(text, "snippet.tl", tlast.LexerOptions{AllowDirty: true}); err != nil {
				t.Fatalf("invalid %s snippet %q: %v", item.Label, text, err)
			}
		}
	}
	for _, text := range []string{"// 😀\r\nD = value:Bo^x;", "D = value:Bo^x<int32,7>;", "D = value:Bo^x // arguments\n<int32,7>;"} {
		d, offset := cursorDocument("tl2", text)
		s := assistanceServer(d)
		s.snippetSupport = true
		for _, item := range s.completion(d, offset) {
			if item.Label != "Box" {
				continue
			}
			b, _ := d.offset(item.TextEdit.Range.Start)
			e, _ := d.offset(item.TextEdit.Range.End)
			updated := d.text[:b] + expandSnippet(item.TextEdit.NewText) + d.text[e:]
			if _, err := tlast.ParseTL2(updated); err != nil {
				t.Fatalf("invalid generic edit: %q: %v", updated, err)
			}
			if strings.Contains(text, "<") && item.InsertTextFormat == 2 {
				t.Fatal("duplicated existing argument list")
			}
		}
	}
	for _, text := range []string{"d value:(Pa^ir) = D;", "d value:(Pa^ir int 3) = D;", "d value:Pa^ir <int,3> = D;"} {
		d, offset := cursorDocument("tl", text)
		s := assistanceServer(d)
		s.snippetSupport = true
		found := false
		for _, item := range s.completion(d, offset) {
			if item.Label != "Pair" {
				continue
			}
			found = true
			b, _ := d.offset(item.TextEdit.Range.Start)
			e, _ := d.offset(item.TextEdit.Range.End)
			updated := d.text[:b] + expandSnippet(item.TextEdit.NewText) + d.text[e:]
			if _, err := tlast.ParseTLFile(updated, "test.tl", tlast.LexerOptions{AllowDirty: true}); err != nil {
				t.Fatalf("bad TL application snippet: %q: %v", updated, err)
			}
		}
		if !found {
			t.Fatal("missing Pair completion")
		}
	}
}

func TestAssistanceProtocolCapabilities(t *testing.T) {
	uri := "file:///tmp/capabilities.tl2"
	text := "Box<T:Type,N:#> = values:[N]T; D = value:Box<int32,"
	results := runSession(t,
		rpc("initialize", 0, map[string]any{"capabilities": map[string]any{"textDocument": map[string]any{"completion": map[string]any{"completionItem": map[string]any{"snippetSupport": true}}}}}),
		openDoc(uri, text, 1), rpc("textDocument/signatureHelp", 1, docParams(uri, newSource(text).position(len(text)))),
		rpc("textDocument/completion", 2, docParams(uri, newSource(text).position(strings.LastIndex(text, "Box<")+2))),
		rpc("shutdown", 3, nil), rpc("exit", nil, nil))
	var init struct {
		Capabilities struct {
			SignatureHelpProvider struct{ TriggerCharacters []string }
		}
	}
	response(t, results, 0, &init)
	if len(init.Capabilities.SignatureHelpProvider.TriggerCharacters) == 0 {
		t.Fatal("signature help not advertised")
	}
	var help signatureHelp
	response(t, results, 1, &help)
	if help.ActiveParameter != 1 || len(help.Signatures) != 1 {
		t.Fatalf("wire signature: %+v", help)
	}
	var items []completionItem
	response(t, results, 2, &items)
	if len(items) != 1 || items[0].Label != "Box" || items[0].InsertTextFormat != 1 && items[0].InsertTextFormat != 0 {
		t.Fatalf("existing angle list: %+v", items)
	}
}
