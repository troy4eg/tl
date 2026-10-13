package tllsp

import (
	"maps"
	"sort"
	"strings"

	"github.com/VKCOM/tl/internal/tlast"
)

// An index is immutable after publication. Requests retain their own generation
// while the connection applies edits to a new one.
type workspaceIndex struct {
	docs         map[string]*document
	ordered      []*document
	byName       map[string][]definition
	globals      map[string][]definition
	refs         map[string]map[string][]reference
	names        []string
	validator    *tlast.SchemaValidator
	checks       map[string][]tlast.SemanticError
	fingerprints map[string]string
}

func (s *server) refreshIndex(full bool) {
	old := s.index
	changed := map[string]bool{}
	if old != nil && !full {
		for uri, d := range old.docs {
			if s.docs[uri] != d {
				changed[uri] = true
			}
		}
	}
	for uri, d := range s.docs {
		if full || old == nil || old.docs[uri] != d {
			changed[uri] = true
		}
	}
	if len(changed) == 0 && old != nil {
		return
	}
	idx := &workspaceIndex{docs: maps.Clone(s.docs), byName: map[string][]definition{}, globals: map[string][]definition{}, refs: map[string]map[string][]reference{}, checks: map[string][]tlast.SemanticError{}, fingerprints: map[string]string{}}
	if old != nil && !full {
		idx.byName = maps.Clone(old.byName)
		idx.globals = maps.Clone(old.globals)
		idx.refs = maps.Clone(old.refs)
		idx.checks = maps.Clone(old.checks)
	}
	for _, d := range s.docs {
		idx.ordered = append(idx.ordered, d)
	}
	sort.Slice(idx.ordered, func(i, j int) bool { return idx.ordered[i].uri < idx.ordered[j].uri })
	for uri := range changed {
		if old != nil && !full {
			if d := old.docs[uri]; d != nil {
				for _, sym := range d.exported {
					removeDefinition(idx.globals, sym.name, uri)
					removeDefinition(idx.byName, sym.name, uri)
				}
				seenRefs := map[string]bool{}
				for _, ref := range d.refs {
					if seenRefs[ref.name] {
						continue
					}
					seenRefs[ref.name] = true
					if group := idx.refs[ref.name]; group != nil {
						group = maps.Clone(group)
						delete(group, uri)
						if len(group) == 0 {
							delete(idx.refs, ref.name)
						} else {
							idx.refs[ref.name] = group
						}
					}
				}
			}
		}
		delete(idx.checks, uri)
		if d := s.docs[uri]; d != nil {
			for _, sym := range d.exported {
				idx.globals[sym.name] = appendClone(idx.globals[sym.name], definition{d, sym})
				if sym.kind != 12 {
					idx.byName[sym.name] = appendClone(idx.byName[sym.name], definition{d, sym})
				}
			}
			grouped := map[string][]reference{}
			for _, ref := range d.refs {
				grouped[ref.name] = append(grouped[ref.name], ref)
			}
			for name, items := range grouped {
				group := maps.Clone(idx.refs[name])
				if group == nil {
					group = map[string][]reference{}
				}
				group[uri] = items
				idx.refs[name] = group
			}
		}
	}
	for name, defs := range idx.globals {
		// Only changed buckets need sorting; never mutate an old generation's slice.
		if len(defs) > 1 && !sort.SliceIsSorted(defs, func(i, j int) bool { return definitionLess(defs[i], defs[j]) }) {
			defs = append([]definition(nil), defs...)
			sort.Slice(defs, func(i, j int) bool { return definitionLess(defs[i], defs[j]) })
			idx.globals[name] = defs
		}
		var signature strings.Builder
		for _, def := range defs {
			sym := def.sym
			signature.WriteString(sym.globalKind)
			if sym.incomplete {
				signature.WriteString("?")
			}
			for _, p := range sym.params {
				signature.WriteString("|" + p.name)
				if p.nat {
					signature.WriteByte('#')
				} else {
					signature.WriteByte('T')
				}
			}
			if sym.alias != nil {
				signature.WriteString("=" + sym.alias.String())
			}
			signature.WriteByte('\n')
		}
		idx.fingerprints[name] = signature.String()
	}
	for name, defs := range idx.byName {
		if len(defs) > 1 {
			idx.byName[name] = idx.globals[name]
			idx.byName[name] = filterTypes(idx.byName[name])
		}
		idx.names = append(idx.names, name)
	}
	sort.Strings(idx.names)
	files := make([]tlast.SchemaDeclarations, 0, len(idx.ordered))
	for _, d := range idx.ordered {
		files = append(files, tlast.SchemaDeclarations{TL: d.tl, TL2: d.tl2})
	}
	idx.validator = tlast.NewSchemaValidator(files)
	affected := maps.Clone(changed)
	aliasUsers := map[string][]string{}
	for _, defs := range idx.globals {
		for _, def := range defs {
			if def.sym.alias == nil {
				continue
			}
			locals := map[string]bool{}
			for _, p := range def.sym.params {
				locals[p.name] = true
			}
			visitAliasNames(*def.sym.alias, func(name string) {
				if !locals[name] {
					aliasUsers[name] = append(aliasUsers[name], def.sym.name)
				}
			})
		}
	}
	pending := []string{}
	seen := map[string]bool{}
	if old != nil && !full {
		for name, signature := range idx.fingerprints {
			if old.fingerprints[name] != signature {
				pending = append(pending, name)
			}
		}
		for name := range old.fingerprints {
			if _, ok := idx.fingerprints[name]; !ok {
				pending = append(pending, name)
			}
		}
		// Related ranges can move even when a signature stays the same.
		for uri, errors := range idx.checks {
			for _, item := range errors {
				if item.Related != nil && changed[uriFromPath(item.Related.Begin.FileName())] {
					affected[uri] = true
					break
				}
			}
		}
		for len(pending) > 0 {
			name := pending[len(pending)-1]
			pending = pending[:len(pending)-1]
			if seen[name] {
				continue
			}
			seen[name] = true
			for uri := range idx.refs[name] {
				affected[uri] = true
			}
			pending = append(pending, aliasUsers[name]...)
		}
	}
	for uri := range affected {
		if d := s.docs[uri]; d != nil {
			idx.checks[uri] = idx.validator.Validate(tlast.SchemaDeclarations{TL: d.tl, TL2: d.tl2})
		}
	}
	s.generation++
	s.index = idx
	s.byName = idx.byName
	s.semantic = map[string][]diagnostic{}
	s.addSemanticDiagnostics(idx.validator.GlobalErrors())
	for _, d := range idx.ordered {
		s.addSemanticDiagnostics(idx.checks[d.uri])
	}
}

func appendClone(defs []definition, def definition) []definition {
	out := make([]definition, len(defs)+1)
	copy(out, defs)
	out[len(defs)] = def
	return out
}
func definitionLess(a, b definition) bool {
	return a.doc.uri < b.doc.uri || a.doc.uri == b.doc.uri && a.sym.nameBegin < b.sym.nameBegin
}
func filterTypes(defs []definition) []definition {
	out := make([]definition, 0, len(defs))
	for _, def := range defs {
		if def.sym.kind != 12 {
			out = append(out, def)
		}
	}
	return out
}
func removeDefinition(index map[string][]definition, name, uri string) {
	defs := index[name]
	out := make([]definition, 0, len(defs))
	for _, def := range defs {
		if def.doc.uri != uri {
			out = append(out, def)
		}
	}
	if len(out) == 0 {
		delete(index, name)
	} else {
		index[name] = out
	}
}

// Navigation-only candidates avoid semantic validation when checking name binding.
func (s *server) rebuildNavigation() {
	s.byName = map[string][]definition{}
	for _, d := range s.documents() {
		for _, sym := range d.exported {
			if sym.kind != 12 {
				s.byName[sym.name] = append(s.byName[sym.name], definition{d, sym})
			}
		}
	}
}

func visitAliasNames(ref tlast.TL2TypeRef, visit func(string)) {
	if br := ref.BracketType; br != nil {
		if br.HasIndex && !br.IndexType.IsNumber {
			visitAliasNames(br.IndexType.Type, visit)
		}
		visitAliasNames(br.ArrayType, visit)
		return
	}
	visit(ref.SomeType.Name.String())
	for _, arg := range ref.SomeType.Arguments {
		if !arg.IsNumber {
			visitAliasNames(arg.Type, visit)
		}
	}
}
