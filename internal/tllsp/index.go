package tllsp

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/VKCOM/tl/internal/tlast"
)

var primitives = []string{"int32", "uint32", "int64", "uint64", "float32", "float64", "byte", "string", "bool", "bit", "true"}

type symbol struct {
	params                         []typeParameter
	nat                            bool
	valueType                      *tlast.TL2TypeRef
	optional                       bool
	alias                          *tlast.TL2TypeRef
	incomplete                     bool
	name, signature, comment       string
	signatureOnce                  sync.Once
	typeDeclaration                *tlast.TL2Combinator
	legacyType                     *tlast.TypeRef
	globalKind                     string
	scope                          *symbol
	kind                           int
	begin, end, nameBegin, nameEnd int
	children                       []*symbol
	locals                         map[string]*symbol
}

type reference struct {
	name       string
	begin, end int
	local      *symbol
}

type documentCache struct {
	symbolsOnce sync.Once
	tokensOnce  sync.Once
	symbols     []documentSymbol
	tokens      []int
}

type document struct {
	cache    *documentCache
	uri      string
	language string
	source
	version      int
	open         bool
	symbols      []*symbol
	assistScopes []*symbol
	exported     []*symbol
	refs         []reference
	errors       []diagnostic
	tl           tlast.TL
	tl2          []tlast.TL2Combinator
}

func (d *document) parserRange(pr tlast.PositionRange) (int, int) {
	return pr.Begin.Offset(), pr.End.Offset()
}

func parseDocument(uri, text string, version int, open bool) *document {
	return parseDocumentLanguage(uri, text, version, open, "")
}

func parseDocumentLanguage(uri, text string, version int, open bool, language string) *document {
	path, _ := pathFromURI(uri)
	if language == "" {
		language = "tl2"
		if strings.HasSuffix(path, ".tl") {
			language = "tl"
		}
	}
	d := &document{cache: &documentCache{}, uri: uri, language: language, source: newSource(text), version: version, open: open}
	if language == "tl" {
		d.parseTL(path)
		return d
	}
	ast := tlast.ParseTL2FileEditing(text, path)
	for _, err := range ast.Errors {
		b, e := d.parserRange(err.Pos)
		d.errors = append(d.errors, diagnostic{Range: d.span(b, e), Severity: 1, Source: "tl2lsp", Message: err.Error(), Code: "syntax"})
	}
	// Complete files reuse the parser's declaration array. A partial file keeps
	// a separate semantic list, while symbols retain the complete editing AST.
	d.tl2 = ast.Declarations
	complete := 0
	for _, incomplete := range ast.Incomplete {
		if !incomplete {
			complete++
		}
	}
	if complete != len(ast.Declarations) {
		d.tl2 = make([]tlast.TL2Combinator, 0, complete)
		for i := range ast.Declarations {
			if !ast.Incomplete[i] {
				d.tl2 = append(d.tl2, ast.Declarations[i])
			}
		}
	}
	d.symbols = make([]*symbol, 0, len(ast.Declarations))
	for i := range ast.Declarations {
		comb := &ast.Declarations[i]
		b, e := d.parserRange(comb.PR)
		s := &symbol{begin: b, end: e, kind: 23, globalKind: "type", comment: cleanComment(comb.CommentBefore), locals: map[string]*symbol{}}
		s.typeDeclaration = comb
		s.incomplete = ast.Incomplete[i]
		if s.incomplete {
			s.typeDeclaration = nil
			s.signature = strings.TrimSpace(text[b:e])
		}
		var namePR tlast.PositionRange
		if comb.IsFunction {
			s.name, s.kind = comb.FuncDecl.Name.String(), 12
			s.globalKind = "function"
			namePR = comb.FuncDecl.PRName
		} else {
			s.name = comb.TypeDecl.Name.String()
			namePR = comb.TypeDecl.PRName
			if comb.TypeDecl.Type.IsTypeAlias {
				s.kind = 11
			}
			if comb.TypeDecl.Type.StructType.IsUnionType {
				s.kind = 10
			}
		}
		s.nameBegin, s.nameEnd = d.parserRange(namePR)
		for _, p := range comb.TypeDecl.TemplateArguments {
			s.params = append(s.params, typeParameter{p.Name, p.Category.IsNat()})
			pb, pe := d.parserRange(p.PR)
			nb, ne := d.parserRange(p.PRName)
			local := &symbol{name: p.Name, kind: 26, scope: s, begin: pb, end: pe, nameBegin: nb, nameEnd: ne, signature: p.Name + ": " + p.Category.String()}
			local.nat = p.Category.IsNat()
			if s.incomplete {
				local.signature = strings.TrimSpace(text[pb:pe])
			}
			s.locals[p.Name] = local
			s.children = append(s.children, local)
		}
		if comb.IsFunction {
			d.addFields(s, comb.FuncDecl.Arguments, s.locals)
			// RPC result fields form a scope separate from request fields.
			result := &symbol{begin: s.end, end: s.end, locals: map[string]*symbol{}}
			resultFound := false
			for _, tok := range assistTokens(text, s.begin, s.end) {
				if tok.text == "=>" {
					resultFound = true
					result.begin = tok.end
					break
				}
			}
			d.addDefinition(result, &comb.FuncDecl.ReturnType, result.locals)
			s.children = append(s.children, result.children...)
			if resultFound {
				d.assistScopes = append(d.assistScopes, result)
			}
		} else {
			if comb.TypeDecl.Type.IsTypeAlias {
				s.alias = &comb.TypeDecl.Type.TypeAlias
			}
			d.addDefinition(s, &comb.TypeDecl.Type, s.locals)
		}
		if s.incomplete {
			walkSymbols([]*symbol{s}, func(sym *symbol) { sym.incomplete = true })
		}
		d.symbols = append(d.symbols, s)
	}
	d.exported = d.symbols
	return d
}

// Signatures are presentation data. Render each one only when a request needs
// it, and share the result safely between concurrent readers of a snapshot.
func (s *symbol) detail() string {
	s.signatureOnce.Do(func() {
		if s.signature != "" {
			return
		}
		if s.typeDeclaration != nil {
			comb := *s.typeDeclaration
			comb.CommentBefore = ""
			s.signature = comb.String()
		} else if s.valueType != nil {
			var out strings.Builder
			out.WriteString(s.name)
			out.WriteString(": ")
			s.valueType.Print(&out)
			s.signature = out.String()
		}
	})
	return s.signature
}

func cleanComment(comment string) string {
	lines := strings.Split(comment, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "//"))
	}
	return strings.Join(lines, "\n")
}

func (d *document) addFields(parent *symbol, fields []tlast.TL2Field, locals map[string]*symbol) {
	// Fields may be used as natural-number arguments (including aliases of
	// uint32). Register all names, as the compiler does, before references.
	// Argument category validation belongs to the semantic compiler.
	fieldSymbols := make([]*symbol, len(fields))
	for i := range fields {
		field := &fields[i]
		if field.Name == "" {
			continue
		}
		b, e := d.parserRange(field.PR)
		nb, ne := d.parserRange(field.PRName)
		child := &symbol{name: field.Name, kind: 8, scope: parent, begin: b, end: e, nameBegin: nb, nameEnd: ne, comment: cleanComment(field.CommentBefore)}
		child.valueType, child.optional = &field.Type, field.IsOptional || field.IsIgnored || field.IsOmitted()
		fieldSymbols[i] = child
		locals[field.Name] = child
	}
	for i := range fields {
		field := &fields[i]
		if child := fieldSymbols[i]; child != nil {
			parent.children = append(parent.children, child)
		}
		d.addTypeRef(&field.Type, locals)
	}
}

func (d *document) addDefinition(parent *symbol, def *tlast.TL2TypeDefinition, locals map[string]*symbol) {
	if def.IsTypeAlias {
		d.addTypeRef(&def.TypeAlias, locals)
		return
	}
	st := &def.StructType
	if !st.IsUnionType {
		d.addFields(parent, st.ConstructorFields, locals)
		return
	}
	for i := range st.UnionType.Variants {
		variant := &st.UnionType.Variants[i]
		variantLocals := map[string]*symbol{}
		for name, local := range locals {
			variantLocals[name] = local
		}
		b, e := d.parserRange(variant.PR)
		nb, ne := d.parserRange(variant.PRName)
		child := &symbol{name: variant.Name, kind: 22, scope: parent, begin: b, end: e, nameBegin: nb, nameEnd: ne, signature: variant.Name, comment: cleanComment(variant.CommentBefore), locals: variantLocals}
		if variant.IsTypeAlias {
			d.addTypeRef(&variant.TypeAlias, variantLocals)
		} else {
			d.addFields(child, variant.Fields, variantLocals)
		}
		parent.children = append(parent.children, child)
	}
}

func (d *document) addTypeRef(ref *tlast.TL2TypeRef, locals map[string]*symbol) {
	if ref.BracketType != nil {
		br := ref.BracketType
		if br.HasIndex && !br.IndexType.IsNumber {
			d.addTypeRef(&br.IndexType.Type, locals)
		}
		d.addTypeRef(&br.ArrayType, locals)
		return
	}
	app := &ref.SomeType
	name := app.Name.String()
	if name == "" {
		return
	}
	b, e := d.parserRange(app.PRName)
	d.refs = append(d.refs, reference{name, b, e, locals[name]})
	for i := range app.Arguments {
		arg := &app.Arguments[i]
		if !arg.IsNumber {
			d.addTypeRef(&arg.Type, locals)
		}
	}
}

func (d *document) documentSymbols() []documentSymbol {
	d.cache.symbolsOnce.Do(func() { d.cache.symbols = d.computeDocumentSymbols() })
	return d.cache.symbols
}

func (d *document) computeDocumentSymbols() []documentSymbol {
	result := make([]documentSymbol, 0, len(d.symbols))
	var convert func(*symbol) documentSymbol
	convert = func(s *symbol) documentSymbol {
		out := documentSymbol{Name: s.name, Detail: s.detail(), Kind: s.kind, Range: d.span(s.begin, s.end), SelectionRange: d.span(s.nameBegin, s.nameEnd)}
		for _, c := range s.children {
			out.Children = append(out.Children, convert(c))
		}
		return out
	}
	for _, s := range d.symbols {
		result = append(result, convert(s))
	}
	return result
}

type definition struct {
	doc *document
	sym *symbol
}

func (def definition) location() location {
	return location{def.doc.uri, def.doc.span(def.sym.nameBegin, def.sym.nameEnd)}
}

func (s *server) documents() []*document {
	if s.index != nil {
		return s.index.documents()
	}
	docs := make([]*document, 0, len(s.docs))
	for _, d := range s.docs {
		docs = append(docs, d)
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].uri < docs[j].uri })
	return docs
}

func (s *server) definitions(name string) []definition {
	if s.byName != nil {
		return s.byName[name]
	}
	if s.index != nil {
		return s.index.byName.get(name)
	}
	return nil
}

func (s *server) rebuildDefinitions() { s.refreshIndex(true) }

func (s *server) symbolAt(d *document, offset int) ([]definition, span) {
	for _, ref := range d.refs {
		if offset >= ref.begin && offset < ref.end {
			if ref.local != nil {
				return []definition{{d, ref.local}}, d.span(ref.begin, ref.end)
			}
			return s.definitions(ref.name), d.span(ref.begin, ref.end)
		}
	}
	var find func(*symbol) *symbol
	find = func(sym *symbol) *symbol {
		if offset >= sym.nameBegin && offset < sym.nameEnd {
			return sym
		}
		for _, child := range sym.children {
			if got := find(child); got != nil {
				return got
			}
		}
		return nil
	}
	for _, sym := range d.symbols {
		if got := find(sym); got != nil {
			return []definition{{d, got}}, d.span(got.nameBegin, got.nameEnd)
		}
	}
	return nil, span{}
}

// Scanning is restricted to schemaPaths, or the workspace folders by default.
// Open buffers always override disk content, including during watcher events.
func (s *server) reindex() {
	defer s.refreshIndex(false)
	seen := map[string]bool{}
	paths := s.schemaRoots()
	for _, root := range paths {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				s.log.Printf("index %s: %v", path, err)
				return nil
			}
			if entry.IsDir() {
				switch entry.Name() {
				case ".git", "node_modules", "target", "vendor", "out", ".zed", ".vscode":
					return filepath.SkipDir
				}
				return nil
			}
			if !entry.Type().IsRegular() {
				return nil
			}
			ext := filepath.Ext(path)
			if ext != ".tl2" && ext != ".tl" {
				return nil
			}
			uri := uriFromPath(path)
			seen[uri] = true
			if d := s.docs[uri]; d != nil && d.open {
				return nil
			}
			text, err := os.ReadFile(path)
			if err != nil {
				s.log.Printf("read %s: %v", path, err)
				return nil
			}
			if d := s.docs[uri]; d == nil || d.text != string(text) {
				s.setDocument(uri, parseDocument(uri, string(text), 0, false))
			}
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			s.log.Printf("index %s: %v", root, err)
		}
	}
	for uri, d := range s.docs {
		if !d.open && !seen[uri] {
			s.setDocument(uri, nil)
		}
	}
}

func (s *server) diagnostics(d *document) []diagnostic {
	out := append([]diagnostic{}, d.errors...)
	items := s.semantic[d.uri]
	if s.pendingDiagnostics[d.uri] && s.index != nil {
		// Code actions run in the request worker and need current diagnostics
		// even while the workspace-wide publication is pending.
		current := *s
		current.semantic = map[string][]diagnostic{}
		current.addSemanticDiagnostics(s.index.validator.Validate(tlast.SchemaDeclarations{TL: d.tl, TL2: d.tl2}))
		for _, item := range s.index.validator.GlobalErrors() {
			if uriFromPath(item.Pos.Begin.FileName()) == d.uri {
				current.addSemanticDiagnostics([]tlast.SemanticError{item})
			}
		}
		items = current.semantic[d.uri]
	}
	severity := 2
	switch s.options.Diagnostics.UnknownTypes {
	case "off":
		severity = 0
	case "error":
		severity = 1
	case "information":
		severity = 3
	case "hint":
		severity = 4
	}
	for _, item := range items {
		if item.Code == "unknown-type" {
			if severity == 0 {
				continue
			}
			item.Severity = severity
		}
		out = append(out, item)
	}
	return out
}

func (d *document) isPrimitive(name string) bool {
	for _, p := range d.primitives() {
		if p == name {
			return true
		}
	}
	return false
}

var tlPrimitives = []string{"int", "long", "double", "float", "uint32", "uint64", "string", "bytes", "#", "Type"}

func (d *document) primitives() []string {
	if d.language == "tl" {
		return tlPrimitives
	}
	return primitives
}

func (s *server) schemaRoots() []string {
	paths := append([]string(nil), s.roots...)
	if len(s.options.SchemaPaths) > 0 {
		paths = nil
		for _, path := range s.options.SchemaPaths {
			if !filepath.IsAbs(path) {
				if len(s.roots) == 0 {
					continue
				}
				path = filepath.Join(s.roots[0], path)
			}
			paths = append(paths, filepath.Clean(path))
		}
	}
	return paths
}

// File notifications identify the affected URI; a full walk is only needed for
// startup, configuration or workspace-folder changes.
func (s *server) refreshDiskDocument(uri string) {
	uri = canonicalURI(uri)
	if d := s.docs[uri]; d != nil && d.open {
		return
	}
	path, err := pathFromURI(uri)
	if err != nil || !s.schemaFile(path) {
		s.setDocument(uri, nil)
		return
	}
	entry, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		s.setDocument(uri, nil)
		return
	}
	if err != nil {
		s.log.Printf("index %s: %v", path, err)
		return
	}
	if !entry.Mode().IsRegular() {
		s.setDocument(uri, nil)
		return
	}
	text, err := os.ReadFile(path)
	if err != nil {
		s.log.Printf("read %s: %v", path, err)
		return
	}
	if d := s.docs[uri]; d == nil || d.text != string(text) {
		s.setDocument(uri, parseDocument(uri, string(text), 0, false))
	}
}

func (s *server) schemaFile(path string) bool {
	if ext := filepath.Ext(path); ext != ".tl" && ext != ".tl2" {
		return false
	}
	for _, root := range s.schemaRoots() {
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		excluded := false
		for _, part := range strings.Split(filepath.Join(filepath.Base(root), rel), string(filepath.Separator)) {
			switch part {
			case ".git", "node_modules", "target", "vendor", "out", ".zed", ".vscode":
				excluded = true
			}
		}
		if !excluded {
			return true
		}
	}
	return false
}
