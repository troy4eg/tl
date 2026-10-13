package tllsp

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// Compare the narrowed candidate check with the pre-optimization full scan.
func TestIndexedRenameMatchesFullBindingCheck(t *testing.T) {
	cases := []struct{ name, language, declaration, consumer, needle, newName string }{
		{"safe global", "tl2", "Item = x:int32; Other = x:int64;", "Use = x:Item y:Other;", "Item =", "Renamed"},
		{"capture outside edited file", "tl2", "Item = x:int32;", "Use = x:Future;", "Item =", "Future"},
		{"global captured by local", "tl2", "Item = x:int32;", "Use<T:Type> = x:Item y:T;", "Item =", "T"},
		{"local captures global", "tl2", "Global = x:int32; Box<T:Type> = x:T y:Global;", "Use = x:Global;", "T:Type", "Global"},
		{"local does not escape", "tl2", "Box<T:Type> = x:T;", "Use = x:Future;", "T:Type", "Future"},
		{"union field", "tl2", "Choice = | first n:uint32 xs:[n]int32 | second n:uint32 xs:[n]int32;", "Use = x:Choice;", "n:uint32", "size"},
		{"TL repeated result type", "tl", "a x:int = Item; b y:int = Item;", "Use = x:Item;", "Item;", "Record"},
		{"TL constructor", "tl", "item#12345678 id:int = Item;", "Use = x:item;", "item#", "entry"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := parseDocumentLanguage("file:///tmp/rename-index."+tc.language, tc.declaration, 1, true, tc.language)
			other := parseDocument("file:///tmp/rename-consumer.tl2", tc.consumer, 1, true)
			unrelated := parseDocument("file:///tmp/rename-unrelated.tl2", "Unrelated = x:int32;", 1, true)
			s := editingServer(d, other, unrelated)
			target := s.targetAt(d, strings.Index(tc.declaration, tc.needle))
			if len(target.defs) == 0 {
				t.Fatal("missing target")
			}
			items := s.occurrences(target, true)
			before := s.index
			narrow := s.validateRename(target, items, tc.newName)
			full := s.fullRenameBindingOracle(items, tc.newName)
			if (narrow == nil) != (full == nil) {
				t.Fatalf("narrowed=%v full=%v", narrow, full)
			}
			if s.index != before || s.docs[d.uri] != d || s.docs[unrelated.uri] != unrelated {
				t.Fatal("candidate changed live workspace")
			}
		})
	}
}

func (s *server) fullRenameBindingOracle(items []occurrence, name string) error {
	byURI := map[string][]occurrence{}
	for _, item := range items {
		byURI[item.doc.uri] = append(byURI[item.doc.uri], item)
	}
	type offsetDelta struct{ end, delta int }
	deltas := map[string][]offsetDelta{}
	for uri, edits := range byURI {
		delta := 0
		for _, edit := range edits {
			delta += len(name) - (edit.end - edit.begin)
			deltas[uri] = append(deltas[uri], offsetDelta{edit.end, delta})
		}
	}
	translated := func(uri string, offset int) int {
		changes := deltas[uri]
		i := sort.Search(len(changes), func(i int) bool { return changes[i].end > offset })
		if i > 0 {
			return offset + changes[i-1].delta
		}
		return offset
	}
	type symbolKey struct {
		uri    string
		offset int
	}
	key := func(uri string, offset int) symbolKey { return symbolKey{uri, offset} }
	candidate := &server{docs: map[string]*document{}}
	for _, doc := range s.documents() {
		if s.cancelled() {
			return fmt.Errorf("request cancelled")
		}
		text := doc.text
		if edits := byURI[doc.uri]; len(edits) > 0 {
			var buffer strings.Builder
			cursor := 0
			for _, item := range edits {
				buffer.WriteString(doc.text[cursor:item.begin])
				buffer.WriteString(name)
				cursor = item.end
			}
			buffer.WriteString(doc.text[cursor:])
			text = buffer.String()
		}
		updated := doc
		if text != doc.text {
			updated = parseDocumentLanguage(doc.uri, text, doc.version, doc.open, doc.language)
			if len(updated.errors) > len(doc.errors) || len(updated.refs) != len(doc.refs) {
				return fmt.Errorf("The new name is not valid in this declaration or reference")
			}
			oldSymbols, newSymbols := map[symbolKey]*symbol{}, map[symbolKey]*symbol{}
			walkSymbols(doc.symbols, func(sym *symbol) { oldSymbols[key(doc.uri, translated(doc.uri, sym.nameBegin))] = sym })
			walkSymbols(updated.symbols, func(sym *symbol) { newSymbols[key(doc.uri, sym.nameBegin)] = sym })
			if len(oldSymbols) != len(newSymbols) {
				return fmt.Errorf("The new name changes the declaration structure")
			}
			for k, old := range oldSymbols {
				if new := newSymbols[k]; new == nil || old.kind != new.kind || old.globalKind != new.globalKind {
					return fmt.Errorf("The new name changes the declaration structure")
				}
			}
		}
		candidate.docs[doc.uri] = updated
	}
	candidate.rebuildNavigation()
	for _, doc := range s.documents() {
		if s.cancelled() {
			return fmt.Errorf("request cancelled")
		}
		newRefs := map[int]reference{}
		for _, ref := range candidate.docs[doc.uri].refs {
			newRefs[ref.begin] = ref
		}
		for _, ref := range doc.refs {
			if s.cancelled() {
				return fmt.Errorf("request cancelled")
			}
			updated, ok := newRefs[translated(doc.uri, ref.begin)]
			if !ok {
				return fmt.Errorf("The new name changes a reference's syntax")
			}
			oldDefs := s.refDefinitions(doc, ref)
			newDefs := candidate.refDefinitions(candidate.docs[doc.uri], updated)
			expected := map[symbolKey]bool{}
			for _, def := range oldDefs {
				expected[key(def.doc.uri, translated(def.doc.uri, def.sym.nameBegin))] = true
			}
			if len(oldDefs) != len(newDefs) {
				return fmt.Errorf("The new name would change the binding of an existing reference")
			}
			for _, def := range newDefs {
				if !expected[key(def.doc.uri, def.sym.nameBegin)] {
					return fmt.Errorf("The new name would change the binding of an existing reference")
				}
			}
		}
	}
	return nil
}
