package tllsp

import (
	"maps"
	"sort"
	"strings"
	"sync"

	"github.com/VKCOM/tl/internal/tlast"
)

type globalBucket struct {
	defs        []definition
	fingerprint string
	aliases     []string
}

// An index is immutable after publication. Requests retain their own generation
// while the connection applies edits to a new one.
type workspaceIndex struct {
	docs         indexTable[*document]
	syntax       indexTable[*document]
	byName       indexTable[[]definition]
	globals      indexTable[*globalBucket]
	refs         indexTable[map[string][]reference]
	checks       indexTable[[]tlast.SemanticError]
	aliasUsers   indexTable[map[string]bool]
	relatedUsers indexTable[map[string]bool]
	globalFiles  map[string]bool
	validator    *tlast.SchemaIndex

	orderedOnce sync.Once
	ordered     []*document
}

// Copy the immutable tables, with fresh lazy document-list state. Callers clone
// an individual table before editing it; published snapshots remain unchanged.
func (idx *workspaceIndex) fork() *workspaceIndex {
	return &workspaceIndex{docs: idx.docs, syntax: idx.syntax, byName: idx.byName,
		globals: idx.globals, refs: idx.refs, checks: idx.checks,
		aliasUsers: idx.aliasUsers, relatedUsers: idx.relatedUsers,
		globalFiles: idx.globalFiles, validator: idx.validator}
}

func (idx *workspaceIndex) documents() []*document {
	idx.orderedOnce.Do(func() {
		idx.ordered = make([]*document, 0, idx.docs.tree.Len())
		idx.docs.each(func(_ string, d *document) { idx.ordered = append(idx.ordered, d) })
	})
	return idx.ordered
}

func (s *server) setDocument(uri string, d *document) {
	if s.docs[uri] == d {
		return
	}
	if s.dirtyDocuments == nil {
		s.dirtyDocuments = map[string]bool{}
	}
	s.dirtyDocuments[uri] = true
	if d == nil {
		delete(s.docs, uri)
	} else {
		if s.docs == nil {
			s.docs = map[string]*document{}
		}
		s.docs[uri] = d
	}
}

func (s *server) refreshIndex(full bool) {
	old := s.index
	changed := s.dirtyDocuments
	s.dirtyDocuments = nil
	if old == nil || full {
		changed = make(map[string]bool, len(s.docs))
		for uri := range s.docs {
			changed[uri] = true
		}
	}
	if len(changed) == 0 && old != nil && !full {
		return
	}
	idx := &workspaceIndex{globalFiles: map[string]bool{}}
	if old == nil || full {
		idx.docs = newIndexTable[*document]()
		idx.syntax = newIndexTable[*document]()
		idx.byName = newIndexTable[[]definition]()
		idx.globals = newIndexTable[*globalBucket]()
		idx.refs = newIndexTable[map[string][]reference]()
		idx.checks = newIndexTable[[]tlast.SemanticError]()
		idx.aliasUsers = newIndexTable[map[string]bool]()
		idx.relatedUsers = newIndexTable[map[string]bool]()
	} else {
		idx.docs = old.docs.clone()
		idx.syntax = old.syntax.clone()
		idx.byName = old.byName.clone()
		idx.globals = old.globals.clone()
		idx.refs = old.refs.clone()
		idx.checks = old.checks.clone()
		idx.aliasUsers = old.aliasUsers.clone()
		idx.relatedUsers = old.relatedUsers.clone()
	}
	// Collect contributions once per changed document. A bucket is copied once,
	// even when several changed files export or reference the same name.
	removed := map[string]bool{}
	names := map[string][]definition{}
	references := map[string]map[string][]reference{}
	for uri := range changed {
		var previous *document
		if old != nil && !full {
			previous = old.docs.get(uri)
		}
		if previous == s.docs[uri] {
			delete(changed, uri)
			continue
		}
		removed[uri] = true
		if previous != nil {
			for _, sym := range previous.exported {
				if _, ok := names[sym.name]; !ok {
					names[sym.name] = nil
				}
			}
			for _, ref := range previous.refs {
				if references[ref.name] == nil {
					references[ref.name] = map[string][]reference{}
				}
				references[ref.name][uri] = nil
			}
		}
		if d := s.docs[uri]; d != nil {
			idx.docs.set(uri, d)
			if len(d.errors) > 0 {
				idx.syntax.set(uri, d)
			} else {
				idx.syntax.delete(uri)
			}
			for _, sym := range d.exported {
				names[sym.name] = append(names[sym.name], definition{d, sym})
			}
			for _, ref := range d.refs {
				if references[ref.name] == nil {
					references[ref.name] = map[string][]reference{}
				}
				references[ref.name][uri] = append(references[ref.name][uri], ref)
			}
		} else {
			idx.docs.delete(uri)
			idx.syntax.delete(uri)
		}
	}
	if len(changed) == 0 && old != nil && !full {
		return
	}
	pending := []string{}
	for name, added := range names {
		previous := idx.globals.get(name)
		defs := make([]definition, 0, len(added))
		if previous != nil {
			for _, def := range previous.defs {
				if !removed[def.doc.uri] {
					defs = append(defs, def)
				}
			}
		}
		defs = append(defs, added...)
		sort.Slice(defs, func(i, j int) bool { return definitionLess(defs[i], defs[j]) })
		bucket := &globalBucket{defs: defs, fingerprint: definitionFingerprint(defs)}
		if previous == nil || bucket.fingerprint != previous.fingerprint {
			pending = append(pending, name)
			if previous != nil {
				for _, dependency := range previous.aliases {
					updateUsers(idx.aliasUsers, dependency, name, false)
				}
			}
			bucket.aliases = aliasDependencies(defs)
			for _, dependency := range bucket.aliases {
				updateUsers(idx.aliasUsers, dependency, name, true)
			}
		} else {
			bucket.aliases = previous.aliases
		}
		if len(defs) == 0 {
			idx.globals.delete(name)
		} else {
			idx.globals.set(name, bucket)
		}
		if types := filterTypes(defs); len(types) > 0 {
			idx.byName.set(name, types)
		} else {
			idx.byName.delete(name)
		}
	}
	for name, updates := range references {
		group := maps.Clone(idx.refs.get(name))
		if group == nil {
			group = map[string][]reference{}
		}
		for uri, refs := range updates {
			if len(refs) == 0 {
				delete(group, uri)
			} else {
				group[uri] = refs
			}
		}
		if len(group) == 0 {
			idx.refs.delete(name)
		} else {
			idx.refs.set(name, group)
		}
	}
	updates := make(map[string]*tlast.SchemaDeclarations, len(changed))
	for uri := range changed {
		if d := s.docs[uri]; d != nil {
			updates[uri] = &tlast.SchemaDeclarations{TL: d.tl, TL2: d.tl2}
		} else {
			updates[uri] = nil
		}
	}
	if old == nil || full {
		files := make(map[string]tlast.SchemaDeclarations, len(updates))
		for uri, declarations := range updates {
			if declarations != nil {
				files[uri] = *declarations
			}
		}
		idx.validator = tlast.NewSchemaIndex(files)
	} else {
		idx.validator = old.validator.WithDocuments(updates)
	}
	affected := maps.Clone(changed)
	if old != nil && !full {
		// Related ranges can move even when the declaration's signature does not.
		for uri := range changed {
			for consumer := range old.relatedUsers.get(uri) {
				affected[consumer] = true
			}
		}
		seen := map[string]bool{}
		for len(pending) > 0 {
			name := pending[len(pending)-1]
			pending = pending[:len(pending)-1]
			if seen[name] {
				continue
			}
			seen[name] = true
			for uri := range idx.refs.get(name) {
				affected[uri] = true
			}
			for alias := range idx.aliasUsers.get(name) {
				pending = append(pending, alias)
			}
		}
	}
	globalErrors := idx.validator.GlobalErrors()
	for _, item := range globalErrors {
		idx.globalFiles[uriFromPath(item.Pos.Begin.FileName())] = true
	}
	if old != nil && !full {
		for uri := range old.globalFiles {
			affected[uri] = true
		}
	}
	for uri := range idx.globalFiles {
		affected[uri] = true
	}
	for uri := range s.pendingDiagnostics {
		affected[uri] = true
	}
	s.generation++
	s.index = idx
	s.byName = nil
	if old == nil || full {
		s.semantic = map[string][]diagnostic{}
	}
	s.markDiagnosticChanges(affected)
	if full && old != nil {
		old.docs.each(func(uri string, _ *document) { s.diagnosticChanges[uri] = true })
	}
	job := s.newDiagnosticJob(affected)
	if s.queueDiagnostics != nil && old != nil {
		s.pendingDiagnostics = affected
		s.queueDiagnostics(job)
	} else {
		if s.queueDiagnostics != nil {
			s.queueDiagnostics(nil)
		}
		s.applyDiagnostics(job.run())
		job.cancel()
	}
}

func updateUsers(table indexTable[map[string]bool], name, user string, add bool) {
	previous := table.get(name)
	if previous[user] == add {
		return
	}
	users := maps.Clone(previous)
	if add {
		if users == nil {
			users = map[string]bool{}
		}
		users[user] = true
	} else {
		delete(users, user)
	}
	if len(users) == 0 {
		table.delete(name)
	} else {
		table.set(name, users)
	}
}

func definitionFingerprint(defs []definition) string {
	var signature strings.Builder
	for _, def := range defs {
		sym := def.sym
		signature.WriteString(sym.globalKind)
		if sym.incomplete {
			signature.WriteString("?")
		}
		for _, p := range sym.params {
			signature.WriteByte('|')
			signature.WriteString(p.name)
			if p.nat {
				signature.WriteByte('#')
			} else {
				signature.WriteByte('T')
			}
		}
		if sym.alias != nil {
			signature.WriteByte('=')
			sym.alias.Print(&signature)
		}
		signature.WriteByte('\n')
	}
	return signature.String()
}

func aliasDependencies(defs []definition) []string {
	seen := map[string]bool{}
	for _, def := range defs {
		if def.sym.alias == nil {
			continue
		}
		locals := map[string]bool{}
		for _, p := range def.sym.params {
			locals[p.name] = true
		}
		visitAliasNames(def.sym.alias, func(name string) {
			if !locals[name] {
				seen[name] = true
			}
		})
	}
	dependencies := make([]string, 0, len(seen))
	for name := range seen {
		dependencies = append(dependencies, name)
	}
	return dependencies
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

func visitAliasNames(ref *tlast.TL2TypeRef, visit func(string)) {
	if br := ref.BracketType; br != nil {
		if br.HasIndex && !br.IndexType.IsNumber {
			visitAliasNames(&br.IndexType.Type, visit)
		}
		visitAliasNames(&br.ArrayType, visit)
		return
	}
	visit(ref.SomeType.Name.String())
	for i := range ref.SomeType.Arguments {
		arg := &ref.SomeType.Arguments[i]
		if !arg.IsNumber {
			visitAliasNames(&arg.Type, visit)
		}
	}
}
