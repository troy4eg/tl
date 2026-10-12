package tllsp

import (
	"strings"

	"github.com/VKCOM/tl/internal/tlast"
)

func (d *document) parseTL(path string) {
	ast := tlast.ParseTLFileEditing(d.text, path)
	for _, err := range ast.Errors {
		b, e := d.parserRange(err.Pos)
		d.errors = append(d.errors, diagnostic{Range: d.span(b, e), Severity: 1, Source: "tl2lsp", Message: err.Error(), Code: "syntax"})
	}
	for i, c := range ast.Declarations {
		if !ast.Incomplete[i] {
			d.tl = append(d.tl, c)
		}
		b, e := d.parserRange(c.PR)
		nb, ne := d.parserRange(c.Construct.NamePR)
		s := &symbol{name: c.Construct.Name.String(), kind: 23, globalKind: "constructor", begin: b, end: e, nameBegin: nb, nameEnd: ne,
			signature: strings.TrimSpace(d.text[b:e]), comment: cleanComment(c.CommentBefore), locals: map[string]*symbol{}}
		s.incomplete = ast.Incomplete[i]
		if c.IsFunction {
			s.kind = 12
			s.globalKind = "function"
		} else {
			d.exported = append(d.exported, s)
		}
		for _, p := range c.TemplateArguments {
			pb, pe := d.parserRange(p.PR)
			nb, ne := d.tlNameRange(p.PR, p.FieldName)
			local := &symbol{name: p.FieldName, kind: 26, scope: s, begin: pb, end: pe, nameBegin: nb, nameEnd: ne, signature: strings.TrimSpace(d.text[pb:pe])}
			s.locals[p.FieldName] = local
			s.children = append(s.children, local)
		}
		d.addTLFields(s, c.Fields, s.locals)
		if c.IsFunction {
			d.addTLRef(c.FuncDecl, s.locals)
		} else if !c.TypeDecl.Name.IsEmpty() {
			b, e := d.parserRange(c.TypeDecl.PR)
			nb, ne := d.parserRange(c.TypeDecl.NamePR)
			typeSymbol := &symbol{name: c.TypeDecl.Name.String(), kind: 23, globalKind: "type", begin: b, end: e, nameBegin: nb, nameEnd: ne, signature: s.signature, comment: s.comment}
			s.children = append(s.children, typeSymbol)
			d.exported = append(d.exported, typeSymbol)
			for i, name := range c.TypeDecl.Arguments {
				d.addTLNameRef(name, c.TypeDecl.ArgumentsPR[i], s.locals)
			}
		}
		if s.incomplete {
			walkSymbols([]*symbol{s}, func(sym *symbol) { sym.incomplete = true })
		}
		d.symbols = append(d.symbols, s)
	}
}

// TL1 type-reference ranges include bare markers, parentheses and arguments.
// Locate only the base name to keep cursor lookup and semantic tokens precise.
func (d *document) tlNameRange(pr tlast.PositionRange, name string) (int, int) {
	b, e := d.parserRange(pr)
	for start := b; start < e; {
		i := strings.Index(d.text[start:e], name)
		if i < 0 {
			break
		}
		i += start
		end := i + len(name)
		if (i == b || !nameByte(d.text[i-1])) && (end == e || !nameByte(d.text[end])) {
			return i, end
		}
		start = i + 1
	}
	return b, b
}

func (d *document) addTLNameRef(name string, pr tlast.PositionRange, locals map[string]*symbol) {
	if name == "" {
		return
	}
	b, e := d.tlNameRange(pr, name)
	if b < e {
		d.refs = append(d.refs, reference{name, b, e, locals[name]})
	}
}

func (d *document) addTLRef(ref tlast.TypeRef, locals map[string]*symbol) {
	d.addTLNameRef(ref.Type.String(), ref.PR, locals)
	for _, arg := range ref.Args {
		if !arg.IsArith {
			d.addTLRef(arg.T, locals)
		}
	}
}

func (d *document) addTLFields(parent *symbol, fields []tlast.Field, locals map[string]*symbol) {
	children := make([]*symbol, len(fields))
	for i, f := range fields {
		if f.FieldName == "" {
			continue
		}
		b, e := d.parserRange(f.PR)
		nb, ne := d.parserRange(f.PRName)
		child := &symbol{name: f.FieldName, kind: 8, scope: parent, begin: b, end: e, nameBegin: nb, nameEnd: ne, signature: strings.TrimSpace(d.text[b:e]), comment: cleanComment(f.CommentBefore)}
		locals[f.FieldName] = child
		children[i] = child
		parent.children = append(parent.children, child)
	}
	for i, f := range fields {
		if f.Mask != nil {
			d.addTLNameRef(f.Mask.MaskName, f.Mask.PRName, locals)
		}
		if !f.IsRepeated {
			d.addTLRef(f.FieldType, locals)
			continue
		}
		if f.ScaleRepeat.ExplicitScale && !f.ScaleRepeat.Scale.IsArith {
			d.addTLNameRef(f.ScaleRepeat.Scale.Scale, f.ScaleRepeat.Scale.PR, locals)
		}
		nestedLocals := map[string]*symbol{}
		for name, sym := range locals {
			nestedLocals[name] = sym
		}
		nestedParent := parent
		if children[i] != nil {
			nestedParent = children[i]
			nestedParent.locals = nestedLocals
		}
		d.addTLFields(nestedParent, f.ScaleRepeat.Rep, nestedLocals)
	}
}
