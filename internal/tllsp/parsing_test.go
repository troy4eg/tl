package tllsp

import (
	"strings"
	"sync"
	"testing"

	"github.com/VKCOM/tl/internal/tlast"
)

func TestConcurrentSignatureRenderingPreservesFormatting(t *testing.T) {
	text := "// 😀 documentation\r\nBox<T:Type,N:#> =\n// field\nvalue:T items:[N]List<T,7>;\nChoice = | a item:Box<string,3> | b;\nget#12345678 value:Box<int32,2> => item:Choice;"
	d := parseDocument("file:///tmp/lazy-signatures.tl2", text, 1, true)
	if len(d.errors) != 0 || len(d.symbols) != 3 {
		t.Fatalf("invalid fixture: %+v", d.errors)
	}
	want := make([]string, len(d.tl2))
	for i := range d.tl2 {
		comb := d.tl2[i]
		comb.CommentBefore = ""
		want[i] = comb.String()
	}
	var readers sync.WaitGroup
	for range 8 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for range 10 {
				for i, sym := range d.symbols {
					if got := sym.detail(); got != want[i] {
						t.Errorf("signature %d: %q, want %q", i, got, want[i])
					}
					walkSymbols(sym.children, func(child *symbol) {
						if child.valueType != nil && child.detail() != child.name+": "+child.valueType.String() {
							t.Errorf("field signature: %s", child.detail())
						}
					})
				}
				if symbols := d.documentSymbols(); len(symbols) != 3 || symbols[0].Detail != want[0] {
					t.Error("document symbols lost a signature")
				}
			}
		}()
	}
	readers.Wait()
	if d.symbols[0].comment != "😀 documentation" || d.tl2[0].CommentBefore != "// 😀 documentation" {
		t.Fatal("rendering changed AST documentation")
	}
}

func TestEditingASTOwnershipAndSemanticFiltering(t *testing.T) {
	text := "Item = value:int32;\nAlias <=> Item;\nDraft = missing:;\nUse = value:Alias;"
	d := parseDocument("file:///tmp/ast-ownership.tl2", text, 1, true)
	if len(d.tl2) != 3 || len(d.symbols) != 4 || !d.symbols[2].incomplete {
		t.Fatal("partial declaration entered the semantic list")
	}
	if d.symbols[1].alias.SomeType.Name.String() != "Item" || d.symbols[0].children[0].valueType.SomeType.Name.String() != "int32" {
		t.Fatal("symbols lost their AST references")
	}
	s := editingServer(d)
	old := s.index
	s.setDocument(d.uri, parseDocument(d.uri, strings.Replace(text, "int32", "int64", 1), 2, true))
	s.refreshIndex(false)
	previous := old.docs.get(d.uri)
	if previous != d || d.symbols[0].children[0].detail() != "value: int32" || !strings.Contains(d.symbols[0].detail(), "int32") {
		t.Fatal("new parsing changed an older snapshot")
	}
	// The immutable registry must still accept the old AST while newer symbols
	// and source positions are already published.
	_ = old.validator.Validate(tlast.SchemaDeclarations{TL2: d.tl2})
}
