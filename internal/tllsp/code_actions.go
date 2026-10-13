package tllsp

import (
	"fmt"
	"github.com/VKCOM/tl/internal/tlast"
	"sort"
	"strings"
)

func (s *server) codeActions(d *document, requested span, context []diagnostic, only []string) []codeAction {
	out := []codeAction{}
	if len(only) > 0 {
		allowed := false
		for _, kind := range only {
			allowed = allowed || kind == "" || kind == "quickfix"
		}
		if !allowed {
			return out
		}
	}
	seen := map[textEdit]bool{}
	add := func(diag diagnostic, edit textEdit, preferred bool) {
		if seen[edit] {
			return
		}
		seen[edit] = true
		workspace := workspaceEdit{Changes: map[string][]textEdit{d.uri: {edit}}}
		if s.documentChanges {
			var version *int
			if d.open {
				v := d.version
				version = &v
			}
			workspace = workspaceEdit{DocumentChanges: []documentEdit{{versionedDocument{d.uri, version}, []textEdit{edit}}}}
		}
		title := "Insert missing ';'"
		if diag.Code == "unknown-type" {
			b, _ := d.offset(edit.Range.Start)
			e, _ := d.offset(edit.Range.End)
			title = fmt.Sprintf("Replace '%s' with '%s'", d.text[b:e], edit.NewText)
		}
		out = append(out, codeAction{title, "quickfix", []diagnostic{diag}, preferred, workspace})
	}
	for _, diag := range s.diagnostics(d) {
		// The client's diagnostics may belong to an older buffer version.
		if len(context) > 0 {
			current := false
			for _, supplied := range context {
				if supplied.Code == diag.Code && supplied.Range == diag.Range && supplied.Message == diag.Message && (supplied.Source == "" || supplied.Source == diag.Source) {
					current = true
					break
				}
			}
			if !current {
				continue
			}
		}
		switch diag.Code {
		case "unknown-type":
			if rangesTouch(requested, diag.Range) {
				for _, edit := range s.typeFixes(d, diag) {
					add(diag, edit, d.language == "tl2" && legacyPrimitive(d.textRange(diag.Range)) == edit.NewText)
				}
			}
		case "syntax":
			if !strings.Contains(diag.Message, "semicolon") && !strings.Contains(diag.Message, "';'") {
				continue
			}
			b, err := d.offset(diag.Range.Start)
			if err != nil {
				continue
			}
			for i, sym := range d.symbols {
				if !sym.incomplete || b < sym.begin || b > sym.end || !rangesTouch(requested, d.span(sym.begin, sym.end)) && !rangesTouch(requested, diag.Range) {
					continue
				}
				if edit, ok := missingSemicolon(d, i); ok {
					add(diag, edit, true)
				}
			}
		}
	}
	return out
}

func (d *document) textRange(r span) string {
	b, be := d.offset(r.Start)
	e, ee := d.offset(r.End)
	if be != nil || ee != nil || e < b {
		return ""
	}
	return d.text[b:e]
}

func positionLess(a, b position) bool {
	return a.Line < b.Line || a.Line == b.Line && a.Character < b.Character
}

func rangesTouch(a, b span) bool {
	if a.Start == a.End || b.Start == b.End {
		return !positionLess(a.End, b.Start) && !positionLess(b.End, a.Start)
	}
	return positionLess(a.Start, b.End) && positionLess(b.Start, a.End)
}

func legacyPrimitive(name string) string {
	switch name {
	case "int":
		return "int32"
	case "long":
		return "int64"
	case "float":
		return "float32"
	case "double":
		return "float64"
	}
	return ""
}

func (s *server) typeFixes(d *document, diag diagnostic) []textEdit {
	b, err := d.offset(diag.Range.Start)
	if err != nil {
		return nil
	}
	e, err := d.offset(diag.Range.End)
	if err != nil || e <= b {
		return nil
	}
	var ref *reference
	for i := range d.refs {
		if d.refs[i].begin == b && d.refs[i].end == e {
			ref = &d.refs[i]
			break
		}
	}
	if ref == nil {
		return nil
	}
	// Reuse completion's scope/category rules, with an empty prefix and
	// plain identifiers. A fix must preserve existing generic arguments.
	plain := *s
	plain.snippetSupport = false
	items := plain.completion(d, b)
	type candidate struct {
		name     string
		distance int
	}
	var candidates []candidate
	legacy := ""
	if d.language == "tl2" {
		legacy = legacyPrimitive(ref.name)
	}
	limit := 1
	if len(ref.name) >= 5 {
		limit = 2
	}
	for _, item := range items {
		name := item.Label
		if name == "0" || name == "Type" || name == "#" || name == ref.name {
			continue
		}
		complete := true
		for _, def := range s.definitions(name) {
			complete = complete && !def.sym.incomplete
		}
		if !complete {
			continue
		}
		distance := spellingDistance(strings.ToLower(ref.name), strings.ToLower(name), limit)
		if name == legacy {
			distance = -1
		}
		if distance <= limit {
			candidates = append(candidates, candidate{name, distance})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].distance != candidates[j].distance {
			return candidates[i].distance < candidates[j].distance
		}
		return candidates[i].name < candidates[j].name
	})
	var edits []textEdit
	for _, c := range candidates {
		if s.cancelled() {
			return nil
		}
		edit := textEdit{diag.Range, c.name}
		if s.validTypeFix(d, diag, edit, b, e) {
			edits = append(edits, edit)
			if len(edits) == 8 {
				break
			}
		}
	}
	return edits
}

// Restricted Damerau-Levenshtein distance also recognizes swapped letters.
// Bound pathological identifiers; typo suggestions are deliberately local.
func spellingDistance(a, b string, limit int) int {
	if len(a) > 256 || len(b) > 256 || len(a)-len(b) > limit || len(b)-len(a) > limit {
		return limit + 1
	}
	previous := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}
	var before []int
	for i := 1; i <= len(a); i++ {
		row := make([]int, len(b)+1)
		row[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			row[j] = min(previous[j]+1, row[j-1]+1, previous[j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				row[j] = min(row[j], before[j-2]+1)
			}
		}
		before, previous = previous, row
	}
	return previous[len(b)]
}

func (s *server) validTypeFix(d *document, target diagnostic, edit textEdit, begin, end int) bool {
	updated := parseDocumentLanguage(d.uri, d.text[:begin]+edit.NewText+d.text[end:], d.version, d.open, d.language)
	if len(updated.errors) != len(d.errors) || len(updated.symbols) != len(d.symbols) || len(updated.refs) != len(d.refs) {
		return false
	}
	candidate := &server{docs: map[string]*document{}}
	for uri, doc := range s.docs {
		candidate.docs[uri] = doc
	}
	candidate.docs[d.uri] = updated
	candidate.rebuildNavigation()
	candidate.semantic = map[string][]diagnostic{}
	if s.index != nil {
		files := make([]tlast.SchemaDeclarations, 0, len(candidate.docs))
		for _, doc := range candidate.documents() {
			files = append(files, tlast.SchemaDeclarations{TL: doc.tl, TL2: doc.tl2})
		}
		validator := tlast.NewSchemaValidator(files)
		candidate.addSemanticDiagnostics(validator.GlobalErrors())
		candidate.addSemanticDiagnostics(validator.Validate(tlast.SchemaDeclarations{TL: updated.tl, TL2: updated.tl2}))
	} else {
		candidate.rebuildSemanticDiagnostics()
	}
	// Removing one diagnostic must not introduce an arity/category error,
	// an invalid bare reference, or a different failure at this occurrence.
	delta := len(edit.NewText) - (end - begin)
	translate := func(offset int) int {
		if offset >= end {
			return offset + delta
		}
		return offset
	}
	key := func(diag diagnostic) string { return fmt.Sprintf("%s:%s:%v", diag.Code, diag.Message, diag.Range) }
	allowed := map[string]int{}
	for _, diag := range s.semantic[d.uri] {
		if diag.Code == target.Code && diag.Range == target.Range && diag.Message == target.Message {
			continue
		}
		b, be := d.offset(diag.Range.Start)
		e, ee := d.offset(diag.Range.End)
		if be != nil || ee != nil {
			return false
		}
		diag.Range = updated.span(translate(b), translate(e))
		allowed[key(diag)]++
	}
	for _, diag := range candidate.semantic[d.uri] {
		k := key(diag)
		if allowed[k] == 0 {
			return false
		}
		allowed[k]--
	}
	return true
}

func missingSemicolon(d *document, index int) (textEdit, bool) {
	sym := d.symbols[index]
	tokens := assistTokens(d.text, sym.begin, sym.end)
	if len(tokens) == 0 || tokens[len(tokens)-1].text == ";" {
		return textEdit{}, false
	}
	at := tokens[len(tokens)-1].end
	updated := parseDocumentLanguage(d.uri, d.text[:at]+";"+d.text[at:], d.version, d.open, d.language)
	if len(updated.errors) >= len(d.errors) || len(updated.symbols) != len(d.symbols) || updated.symbols[index].incomplete {
		return textEdit{}, false
	}
	// Each editor chunk uses the strict parser before becoming complete.
	// Ensure the insertion cannot split, merge or complete other fragments.
	for i, old := range d.symbols {
		fresh := updated.symbols[i]
		nameBegin := old.nameBegin
		if nameBegin >= at {
			nameBegin++
		}
		if fresh.name != old.name || fresh.nameBegin != nameBegin || fresh.globalKind != old.globalKind || i != index && fresh.incomplete != old.incomplete {
			return textEdit{}, false
		}
	}
	return textEdit{d.span(at, at), ";"}, true
}
