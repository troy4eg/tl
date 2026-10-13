package tllsp

import (
	"fmt"
	"maps"
	"sort"
	"strings"
)

const requestFailed = -32803

type occurrence struct {
	doc        *document
	begin, end int
}

type renameTarget struct {
	defs []definition
	span span
}

func walkSymbols(symbols []*symbol, visit func(*symbol)) {
	for _, sym := range symbols {
		visit(sym)
		walkSymbols(sym.children, visit)
	}
}

func (s *server) refDefinitions(d *document, ref reference) []definition {
	if ref.local != nil {
		return []definition{{d, ref.local}}
	}
	return s.definitions(ref.name)
}

// Result types shared by several TL1 constructors have several declaration
// sites but one identity. Other repeated global names are ambiguous schemas.
func (s *server) targetAt(d *document, offset int) renameTarget {
	defs, r := s.symbolAt(d, offset)
	if len(defs) == 0 {
		return renameTarget{span: r}
	}
	first := defs[0].sym
	if first.globalKind != "" && s.index != nil {
		if bucket := s.index.globals.get(first.name); bucket != nil {
			return renameTarget{bucket.defs, r}
		}
		return renameTarget{span: r}
	}
	if first.globalKind != "" {
		defs = nil
		for _, doc := range s.documents() {
			walkSymbols(doc.symbols, func(sym *symbol) {
				if sym.globalKind != "" && sym.name == first.name {
					defs = append(defs, definition{doc, sym})
				}
			})
		}
	}
	return renameTarget{defs, r}
}

func (target renameTarget) check() *rpcError {
	if len(target.defs) == 0 {
		return &rpcError{requestFailed, "No declared symbol at this position"}
	}
	for _, def := range target.defs {
		if def.sym.incomplete {
			return &rpcError{requestFailed, "Finish the declaration and fix its syntax errors before renaming this symbol"}
		}
		if def.sym.name == "_" || strings.HasPrefix(def.sym.name, "$") ||
			(def.sym.globalKind != "" && def.doc.isPrimitive(def.sym.name)) {
			return &rpcError{requestFailed, "Built-in and anonymous symbols cannot be renamed"}
		}
	}
	if len(target.defs) > 1 {
		for _, def := range target.defs {
			if def.doc.language != "tl" || def.sym.globalKind != "type" {
				return &rpcError{requestFailed, "Ambiguous global symbol; configure schemaPaths to select one schema"}
			}
		}
	}
	return nil
}

func (s *server) occurrences(target renameTarget, includeDeclaration bool) []occurrence {
	selected := map[*symbol]bool{}
	for _, def := range target.defs {
		selected[def.sym] = true
	}
	out := []occurrence{}
	if len(target.defs) == 0 {
		return out
	}
	first := target.defs[0]
	globalMatches := false
	for _, def := range s.definitions(first.sym.name) {
		globalMatches = globalMatches || selected[def.sym]
	}
	if includeDeclaration {
		for _, def := range target.defs {
			out = append(out, occurrence{def.doc, def.sym.nameBegin, def.sym.nameEnd})
		}
	}
	addRefs := func(d *document, refs []reference) {
		for _, ref := range refs {
			if s.cancelled() {
				return
			}
			if ref.name != first.sym.name {
				continue
			}
			if ref.local != nil && selected[ref.local] || ref.local == nil && globalMatches {
				out = append(out, occurrence{d, ref.begin, ref.end})
			}
		}
	}
	if s.index != nil {
		for uri, refs := range s.index.refs.get(first.sym.name) {
			if first.sym.globalKind != "" || uri == first.doc.uri {
				addRefs(s.docs[uri], refs)
			}
		}
	} else if first.sym.globalKind == "" {
		addRefs(first.doc, first.doc.refs)
	} else {
		for _, d := range s.documents() {
			addRefs(d, d.refs)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].doc.uri != out[j].doc.uri {
			return out[i].doc.uri < out[j].doc.uri
		}
		if out[i].begin != out[j].begin {
			return out[i].begin < out[j].begin
		}
		return out[i].end < out[j].end
	})
	// A repeated TL declaration may contribute the same source location twice.
	unique := out[:0]
	for _, item := range out {
		if len(unique) == 0 || unique[len(unique)-1] != item {
			unique = append(unique, item)
		}
	}
	return unique
}

func (s *server) references(d *document, offset int, includeDeclaration bool) []location {
	out := []location{}
	for _, item := range s.occurrences(s.targetAt(d, offset), includeDeclaration) {
		out = append(out, location{item.doc.uri, item.doc.span(item.begin, item.end)})
	}
	return out
}

type workspaceEdit struct {
	Changes         map[string][]textEdit `json:"changes,omitempty"`
	DocumentChanges []documentEdit        `json:"documentChanges,omitempty"`
}

type versionedDocument struct {
	URI     string `json:"uri"`
	Version *int   `json:"version"`
}

type documentEdit struct {
	TextDocument versionedDocument `json:"textDocument"`
	Edits        []textEdit        `json:"edits"`
}

func validRenameName(name string, qualified bool) bool {
	parts := strings.Split(name, ".")
	if len(parts) > 2 || (!qualified && len(parts) != 1) {
		return false
	}
	for _, part := range parts {
		if len(part) == 0 || !asciiLetter(part[0]) {
			return false
		}
		for i := 1; i < len(part); i++ {
			if !nameByte(part[i]) || part[i] == '.' {
				return false
			}
		}
	}
	return len(parts) != 2 || parts[0][0] >= 'a' && parts[0][0] <= 'z'
}

func asciiLetter(b byte) bool { return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' }

func (s *server) rename(d *document, offset int, name string) (any, *rpcError) {
	if s.cancelled() {
		return nil, &rpcError{requestCancelled, "Request cancelled"}
	}
	target := s.targetAt(d, offset)
	if err := target.check(); err != nil {
		return nil, err
	}
	first := target.defs[0].sym
	if first.globalKind != "" {
		if err := s.renameSyntaxError(); err != nil {
			return nil, err
		}
	}
	if !validRenameName(name, first.globalKind != "") {
		return nil, &rpcError{-32602, "Invalid TL identifier (local names cannot contain a namespace)"}
	}
	for _, def := range target.defs {
		if def.doc.isPrimitive(name) {
			return nil, &rpcError{requestFailed, "The new name is reserved for a built-in type"}
		}
	}
	if name == first.name {
		return workspaceEdit{Changes: map[string][]textEdit{}}, nil
	}
	selected := map[*symbol]bool{}
	for _, def := range target.defs {
		selected[def.sym] = true
	}
	var collision bool
	if first.globalKind != "" && s.index != nil {
		collision = s.index.globals.get(name) != nil
	} else {
		docs := []*document{d}
		if first.globalKind != "" {
			docs = s.documents()
		}
		for _, doc := range docs {
			walkSymbols(doc.symbols, func(sym *symbol) {
				if selected[sym] || sym.name != name {
					return
				}
				if first.globalKind != "" && sym.globalKind != "" ||
					first.globalKind == "" && doc == d && sym.scope == first.scope && sym.globalKind == "" {
					collision = true
				}
			})
		}
	}
	if collision {
		return nil, &rpcError{requestFailed, "A symbol with the new name already exists in this scope"}
	}
	items := s.occurrences(target, true)
	if err := s.validateRename(target, items, name); err != nil {
		return nil, &rpcError{requestFailed, err.Error()}
	}
	edit := workspaceEdit{Changes: map[string][]textEdit{}}
	for _, item := range items {
		edit.Changes[item.doc.uri] = append(edit.Changes[item.doc.uri], textEdit{item.doc.span(item.begin, item.end), name})
	}
	if s.documentChanges {
		for _, doc := range s.documents() {
			if edits := edit.Changes[doc.uri]; len(edits) > 0 {
				var version *int
				if doc.open {
					v := doc.version
					version = &v
				}
				edit.DocumentChanges = append(edit.DocumentChanges, documentEdit{versionedDocument{doc.uri, version}, edits})
			}
		}
		edit.Changes = nil
	}
	return edit, nil
}

func (s *server) renameSyntaxError() *rpcError {
	var details []string
	docs := []*document{}
	if s.index != nil {
		s.index.syntax.each(func(_ string, doc *document) { docs = append(docs, doc) })
	} else {
		docs = s.documents()
	}
	for _, doc := range docs {
		path, err := pathFromURI(doc.uri)
		if err != nil {
			path = doc.uri
		}
		diagnostics := append([]diagnostic(nil), doc.errors...)
		sort.SliceStable(diagnostics, func(i, j int) bool {
			a, b := diagnostics[i].Range.Start, diagnostics[j].Range.Start
			if a.Line != b.Line {
				return a.Line < b.Line
			}
			return a.Character < b.Character
		})
		for _, diagnostic := range diagnostics {
			pos := diagnostic.Range.Start
			details = append(details, fmt.Sprintf("%s:%d:%d: %s", path, pos.Line+1, pos.Character+1, diagnostic.Message))
		}
	}
	if len(details) == 0 {
		return nil
	}
	if s.log != nil {
		s.log.Printf("Global rename blocked by syntax errors:\n%s", strings.Join(details, "\n"))
	}
	const maxErrors = 5
	message := "Cannot rename a global symbol: syntax errors in indexed schemas:\n" + strings.Join(details[:min(len(details), maxErrors)], "\n")
	if len(details) > maxErrors {
		message += fmt.Sprintf("\n... and %d more errors; see the language server output for the full list.", len(details)-maxErrors)
	}
	message += "\nFix these errors or narrow schemaPaths."
	return &rpcError{requestFailed, message}
}

// Check every reference in edited buffers and references to the old/new global
// names elsewhere. Only these bindings can change; new-name users include
// unresolved references that the proposed declaration could capture.
func (s *server) validateRename(target renameTarget, items []occurrence, name string) error {
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
	candidate := &server{docs: maps.Clone(s.docs)}
	updates := map[string]*document{}
	for uri, edits := range byURI {
		doc := s.docs[uri]
		if s.cancelled() {
			return fmt.Errorf("request cancelled")
		}
		text := doc.text
		if len(edits) > 0 {
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
		updates[doc.uri] = updated
	}
	if s.index == nil {
		candidate.rebuildNavigation()
	} else {
		candidate.index = s.index.fork()
		candidate.index.docs = s.index.docs.clone()
		candidate.index.byName = s.index.byName.clone()
		names := map[string][]definition{}
		for uri, updated := range updates {
			candidate.index.docs.set(uri, updated)
			for _, sym := range s.docs[uri].exported {
				if _, ok := names[sym.name]; !ok {
					names[sym.name] = nil
				}
			}
			for _, sym := range updated.exported {
				if sym.kind != 12 {
					names[sym.name] = append(names[sym.name], definition{updated, sym})
				}
			}
		}
		for name, added := range names {
			defs := make([]definition, 0, len(added))
			for _, def := range s.definitions(name) {
				if updates[def.doc.uri] == nil {
					defs = append(defs, def)
				}
			}
			defs = append(defs, added...)
			sort.Slice(defs, func(i, j int) bool { return definitionLess(defs[i], defs[j]) })
			if len(defs) == 0 {
				candidate.index.byName.delete(name)
			} else {
				candidate.index.byName.set(name, defs)
			}
		}
	}
	checked := map[string][]reference{}
	for uri := range updates {
		checked[uri] = s.docs[uri].refs
	}
	if target.defs[0].sym.globalKind != "" {
		for _, watched := range []string{target.defs[0].sym.name, name} {
			if s.index != nil {
				for uri, refs := range s.index.refs.get(watched) {
					if updates[uri] == nil {
						checked[uri] = append(checked[uri], refs...)
					}
				}
			} else {
				for _, doc := range s.documents() {
					if updates[doc.uri] == nil {
						for _, ref := range doc.refs {
							if ref.name == watched {
								checked[doc.uri] = append(checked[doc.uri], ref)
							}
						}
					}
				}
			}
		}
	}
	// Global bindings repeat heavily (for example, Shared appears twice per
	// declaration). Resolve and translate each name once, rather than creating
	// a new identity set for every occurrence.
	oldBindings, newBindings := map[string][]symbolKey{}, map[string][]symbolKey{}
	bindings := func(owner *server, doc *document, ref reference, cache map[string][]symbolKey, before bool) []symbolKey {
		makeKey := func(def definition) symbolKey {
			position := def.sym.nameBegin
			if before {
				position = translated(def.doc.uri, position)
			}
			return key(def.doc.uri, position)
		}
		if ref.local != nil {
			return []symbolKey{makeKey(definition{doc, ref.local})}
		}
		if keys, ok := cache[ref.name]; ok {
			return keys
		}
		defs := owner.definitions(ref.name)
		keys := make([]symbolKey, 0, len(defs))
		for _, def := range defs {
			keys = append(keys, makeKey(def))
		}
		cache[ref.name] = keys
		return keys
	}
	for uri, refs := range checked {
		doc := s.docs[uri]
		if s.cancelled() {
			return fmt.Errorf("request cancelled")
		}
		newRefs := map[int]reference{}
		if updates[uri] != nil {
			for _, ref := range candidate.docs[doc.uri].refs {
				newRefs[ref.begin] = ref
			}
		}
		for _, ref := range refs {
			if s.cancelled() {
				return fmt.Errorf("request cancelled")
			}
			updated, ok := ref, true
			if updates[uri] != nil {
				updated, ok = newRefs[translated(doc.uri, ref.begin)]
			}
			if !ok {
				return fmt.Errorf("The new name changes a reference's syntax")
			}
			oldDefs := bindings(s, doc, ref, oldBindings, true)
			newDefs := bindings(candidate, candidate.docs[doc.uri], updated, newBindings, false)
			if len(oldDefs) != len(newDefs) {
				return fmt.Errorf("The new name would change the binding of an existing reference")
			}
			for _, def := range newDefs {
				found := false
				for _, previous := range oldDefs {
					found = found || previous == def
				}
				if !found {
					return fmt.Errorf("The new name would change the binding of an existing reference")
				}
			}
		}
	}
	return nil
}
