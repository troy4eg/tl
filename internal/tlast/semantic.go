package tlast

import "fmt"

// SemanticError describes a schema error without rendering it or generating code.
// Unknown references have a separate code so an editor can configure their severity.
type SemanticError struct {
	Code    string
	Message string
	Pos     PositionRange
	Related *PositionRange
}

// TemplateArityError is shared with the compiler's type checker.
func TemplateArityError(reference string, expected, actual int) error {
	if expected == actual {
		return nil
	}
	return fmt.Errorf("typeref %s must have %d template arguments, has %d", reference, expected, actual)
}

// TL1TypeRef converts the AST into the representation used by the hybrid compiler.
func TL1TypeRef(tr TypeRef) TL2TypeRef {
	r := TL2TypeRef{PR: tr.PR, SomeType: TL2TypeApplication{
		Name: TL2TypeName(tr.Type), PR: tr.PR, PRName: tr.PR,
		PRArguments: tr.PRArgs, Bare: tr.Bare,
	}}
	for _, a := range tr.Args {
		arg := TL2TypeArgument{IsNumber: a.IsArith, Number: a.Arith.Res, PR: a.T.PR}
		if !a.IsArith {
			arg.Type = TL1TypeRef(a.T)
		}
		r.SomeType.Arguments = append(r.SomeType.Arguments, arg)
	}
	return r
}

type semanticType struct {
	name                string
	pr                  PositionRange
	params              []TL2TypeTemplate
	function, sharedTL1 bool
	alias               *TL2TypeRef
}

type semanticLocal struct {
	pr                   *PositionRange
	nat, field, optional bool
	typ                  *TL2TypeRef
	legacy               *TypeRef
	ready, tl1           bool
}

type semanticChecker struct {
	types  map[string]*semanticType
	errors []SemanticError
	tl1    bool
}

func (c *semanticChecker) report(code string, pr PositionRange, related *PositionRange, format string, args ...any) {
	c.errors = append(c.errors, SemanticError{code, fmt.Sprintf(format, args...), pr, related})
}

func (c *semanticChecker) add(t *semanticType) {
	if old := c.types[t.name]; old != nil {
		if old.sharedTL1 && t.sharedTL1 {
			compatible := len(old.params) == len(t.params)
			for i := range t.params {
				if i >= len(old.params) || old.params[i].Category != t.params[i].Category || old.params[i].Name != t.params[i].Name {
					compatible = false
				}
			}
			if !compatible {
				c.report("template-signature", t.pr, &old.pr, "Constructors of type '%s' must have matching template names and categories", t.name)
			}
			return
		}
		c.report("duplicate-declaration", t.pr, &old.pr, "Duplicate declaration '%s'", t.name)
		return
	}
	c.types[t.name] = t
}

func tl1Params(args []TemplateArgument) []TL2TypeTemplate {
	var result []TL2TypeTemplate
	for _, a := range args {
		result = append(result, TL2TypeTemplate{Name: a.FieldName, Category: TL2TypeCategory{IsNatValue: a.IsNat}, PR: a.PR, PRName: a.PR})
	}
	return result
}

// SchemaDeclarations is a parsed document. Keeping documents separate avoids
// copying their large AST values into one concatenated schema on every edit.
type SchemaDeclarations struct {
	TL  TL
	TL2 []TL2Combinator
}

// SchemaValidator holds immutable global signatures. Validate can check a single
// document against the whole schema, and can run concurrently on the same index.
type SchemaValidator struct {
	types  map[string]*semanticType
	errors []SemanticError
}

func (v *SchemaValidator) GlobalErrors() []SemanticError { return v.errors }

// ValidateSchema checks recovered declarations from all files together. It does
// not read disk, mutate the AST, instantiate types, or write to stdout/stderr.
// Generator-specific migration, naming and backend policies are intentionally
// outside this pass; unresolved dependencies do not prevent other checks.
func ValidateSchema(tl TL, tl2 []TL2Combinator) []SemanticError {
	file := SchemaDeclarations{tl, tl2}
	validator := NewSchemaValidator([]SchemaDeclarations{file})
	return append(validator.GlobalErrors(), validator.Validate(file)...)
}

func NewSchemaValidator(files []SchemaDeclarations) *SchemaValidator {
	c := &semanticChecker{types: map[string]*semanticType{}}
	for _, file := range files {
		for _, a := range file.TL {
			params := tl1Params(a.TemplateArguments)
			// Built-in wrappers deliberately reuse primitive constructor names.
			if !a.Builtin {
				c.add(&semanticType{name: a.Construct.Name.String(), pr: a.Construct.NamePR, params: params, function: a.IsFunction})
			}
			if !a.IsFunction {
				c.add(&semanticType{name: a.TypeDecl.Name.String(), pr: a.TypeDecl.NamePR, params: params, sharedTL1: true})
			}
		}
	}
	for _, file := range files {
		for _, a := range file.TL2 {
			t := &semanticType{name: a.ReferenceName().String(), pr: a.ReferenceNamePR(), params: a.TypeDecl.TemplateArguments, function: a.IsFunction}
			if !a.IsFunction && a.TypeDecl.Type.IsAlias() {
				t.alias = &a.TypeDecl.Type.TypeAlias
			}
			c.add(t)
		}
	}
	c.errors = append(c.errors, checkSchemaTags(files)...)
	return &SchemaValidator{types: c.types, errors: c.errors}
}

func (v *SchemaValidator) Validate(file SchemaDeclarations) []SemanticError {
	c := &semanticChecker{types: v.types}
	for _, a := range file.TL {
		c.tl1 = true
		locals := c.templates(tl1Params(a.TemplateArguments))
		if !a.IsFunction && !a.Builtin {
			if a.Construct.Name.String() == "_" && a.TypeDecl.Name.String() != "ReqResult" {
				c.report("legacy-constructor", a.Construct.NamePR, nil, "Constructor '_' is allowed only for legacy ReqResult")
			}
			if len(a.TypeDecl.Arguments) != len(a.TemplateArguments) {
				c.report("template-signature", a.TypeDecl.PR, nil, "Result type must declare all %d template arguments in order", len(a.TemplateArguments))
			} else {
				for i, arg := range a.TypeDecl.Arguments {
					if arg != a.TemplateArguments[i].FieldName {
						c.report("template-signature", a.TypeDecl.ArgumentsPR[i], nil, "Expected result template argument '%s', got '%s'", a.TemplateArguments[i].FieldName, arg)
					}
				}
			}
		}
		c.fieldsTL1(a.Fields, locals)
		if a.IsFunction {
			c.requireType(TL1TypeRef(a.FuncDecl), locals)
		}
	}
	for _, a := range file.TL2 {
		c.tl1 = false
		locals := c.templates(a.TypeDecl.TemplateArguments)
		if a.IsFunction {
			c.fieldsTL2(a.FuncDecl.Arguments, locals)
			// Result fields form their own scope, as in the compiler.
			c.definition(a.FuncDecl.ReturnType, map[string]semanticLocal{})
		} else {
			c.definition(a.TypeDecl.Type, locals)
		}
	}
	return c.errors
}

// CheckConstructorTags uses the same TL1 CRC calculation as generation and a
// single tag space for TL1 constructors/functions and explicit TL2 tags.
func CheckConstructorTags(tl TL, tl2 []TL2Combinator) []SemanticError {
	return checkSchemaTags([]SchemaDeclarations{{tl, tl2}})
}

func checkSchemaTags(files []SchemaDeclarations) []SemanticError {
	var errors []SemanticError
	seen := map[uint32]PositionRange{}
	add := func(tag uint32, name string, pr PositionRange, required bool) {
		if tag == 0 {
			if required {
				errors = append(errors, SemanticError{Code: "crc-zero", Message: "constructor tag 0 is prohibited, even if generated implicitly", Pos: pr})
			}
			return
		}
		if old, ok := seen[tag]; ok {
			errors = append(errors, SemanticError{Code: "crc-conflict", Message: fmt.Sprintf("constructor tag #%08x is used again by %q", tag, name), Pos: pr, Related: &old})
		} else {
			seen[tag] = pr
		}
	}
	for _, file := range files {
		for _, a := range file.TL {
			add(a.Crc32(), a.Construct.Name.String(), a.Construct.IDPR, true)
		}
	}
	for _, file := range files {
		for _, a := range file.TL2 {
			if a.IsFunction {
				add(a.FuncDecl.Magic, a.FuncDecl.Name.String(), a.FuncDecl.PRID, false)
			} else {
				add(a.TypeDecl.Magic, a.TypeDecl.Name.String(), a.TypeDecl.PRID, false)
			}
		}
	}
	return errors
}

func (c *semanticChecker) templates(params []TL2TypeTemplate) map[string]semanticLocal {
	locals := map[string]semanticLocal{}
	for i := range params {
		p := &params[i]
		if old, ok := locals[p.Name]; ok {
			c.report("duplicate-local", p.PRName, old.pr, "Duplicate template parameter '%s'", p.Name)
		}
		locals[p.Name] = semanticLocal{pr: &p.PRName, nat: p.Category.IsNat()}
	}
	return locals
}

func copySemanticLocals(locals map[string]semanticLocal) map[string]semanticLocal {
	result := make(map[string]semanticLocal, len(locals))
	for name, value := range locals {
		result[name] = value
	}
	return result
}

func (c *semanticChecker) definition(def TL2TypeDefinition, locals map[string]semanticLocal) {
	if def.IsAlias() {
		c.requireType(def.TypeAlias, locals)
		return
	}
	if !def.StructType.IsUnionType {
		c.fieldsTL2(def.StructType.ConstructorFields, locals)
		return
	}
	seen := map[string]PositionRange{}
	for _, v := range def.StructType.UnionType.Variants {
		if old, ok := seen[v.Name]; ok {
			c.report("duplicate-variant", v.PRName, &old, "Duplicate union variant '%s'", v.Name)
		}
		seen[v.Name] = v.PRName
		if v.IsTypeAlias {
			c.requireType(v.TypeAlias, locals)
		} else {
			c.fieldsTL2(v.Fields, copySemanticLocals(locals))
		}
	}
}

func (c *semanticChecker) addField(name string, pr *PositionRange, typ *TL2TypeRef, legacy *TypeRef, optional bool, locals map[string]semanticLocal, seen map[string]PositionRange) {
	if name == "" || name == "_" {
		return
	}
	if old, ok := seen[name]; ok {
		c.report("duplicate-local", *pr, &old, "Duplicate field '%s'", name)
	}
	seen[name] = *pr
	// Fields may shadow enclosing fields in nested TL1 repeated groups, but
	// must not shadow template parameters in their declaration.
	if old, ok := locals[name]; ok && !old.field {
		c.report("duplicate-local", *pr, old.pr, "Field '%s' conflicts with a template parameter", name)
	}
	locals[name] = semanticLocal{pr: pr, field: true, typ: typ, legacy: legacy, optional: optional, tl1: c.tl1}
}

func (c *semanticChecker) fieldsTL2(fields []TL2Field, locals map[string]semanticLocal) {
	seen := map[string]PositionRange{}
	for i := range fields {
		f := &fields[i]
		c.addField(f.Name, &f.PRName, &f.Type, nil, f.IsOptional, locals, seen)
	}
	for _, f := range fields {
		c.requireType(f.Type, locals)
		if f.Name != "" && f.Name != "_" {
			l := locals[f.Name]
			l.ready = true
			locals[f.Name] = l
		}
	}
}

func (c *semanticChecker) fieldsTL1(fields []Field, locals map[string]semanticLocal) {
	seen := map[string]PositionRange{}
	for i := range fields {
		f := &fields[i]
		c.addField(f.FieldName, &f.PRName, nil, &f.FieldType, f.Mask != nil || f.IsRepeated, locals, seen)
	}
	for i, f := range fields {
		if f.Mask != nil {
			c.requireNat(f.Mask.MaskName, f.Mask.PRName, locals)
			if f.Mask.BitNumber > 31 {
				c.report("mask-bit", f.Mask.PRBits, nil, "Field mask bit must be between 0 and 31")
			}
		}
		if f.IsRepeated {
			if f.ScaleRepeat.ExplicitScale && !f.ScaleRepeat.Scale.IsArith {
				c.requireNat(f.ScaleRepeat.Scale.Scale, f.ScaleRepeat.Scale.PR, locals)
			}
			if !f.ScaleRepeat.ExplicitScale {
				if i > 0 {
					prev := fields[i-1]
					if prev.FieldType.String() != "#" || prev.Mask != nil {
						c.report("field-dependency", f.PR, &prev.PR, "Implicit repeat size must reference a preceding required # field")
					}
				} else {
					nat := false
					for _, l := range locals {
						if !l.field && l.nat {
							nat = true
						}
					}
					if !nat {
						c.report("field-dependency", f.PR, nil, "Implicit repeat size needs a preceding # field or # template parameter")
					}
				}
			}
			c.fieldsTL1(f.ScaleRepeat.Rep, copySemanticLocals(locals))
		} else {
			c.requireType(TL1TypeRef(f.FieldType), locals)
		}
		if f.FieldName != "" && f.FieldName != "_" {
			l := locals[f.FieldName]
			l.ready = true
			locals[f.FieldName] = l
		}
	}
}

func (c *semanticChecker) requireNat(name string, pr PositionRange, locals map[string]semanticLocal) {
	local, ok := locals[name]
	if !ok {
		c.report("field-dependency", pr, nil, "Reference '%s' must name a preceding # parameter or uint32 field", name)
		return
	}
	_, valid := c.localCategory(name, local, pr)
	if valid && !local.nat && !local.field {
		c.report("argument-category", pr, local.pr, "Reference '%s' must have category #", name)
	}
}

func (c *semanticChecker) localCategory(name string, local semanticLocal, pr PositionRange) (bool, bool) {
	if !local.field {
		return local.nat, true
	}
	if !local.ready {
		c.report("field-dependency", pr, local.pr, "Reference '%s' must name a preceding field", name)
		return false, false
	}
	var typ TL2TypeRef
	if local.legacy != nil {
		typ = TL1TypeRef(*local.legacy)
	} else if local.typ != nil {
		typ = *local.typ
	}
	primitive, known := c.primitive(typ, nil, map[string]bool{})
	if !known {
		return false, false
	}
	if local.optional || (primitive != "#" && (primitive != "uint32" || local.tl1)) {
		c.report("field-dependency", pr, local.pr, "Reference '%s' must name a required # or uint32 field", name)
		return false, false
	}
	return true, true
}

func (c *semanticChecker) requireType(tr TL2TypeRef, locals map[string]semanticLocal) {
	nat, ok := c.typeCategory(tr, locals)
	if ok && nat {
		c.report("argument-category", tr.PR, nil, "Field or alias type must have category Type, got #")
	}
}

func (c *semanticChecker) argumentCategory(arg TL2TypeArgument, locals map[string]semanticLocal) (bool, bool) {
	if arg.IsNumber {
		return true, true
	}
	return c.typeCategory(arg.Type, locals)
}

func (c *semanticChecker) typeCategory(tr TL2TypeRef, locals map[string]semanticLocal) (bool, bool) {
	if br := tr.BracketType; br != nil {
		if br.HasIndex {
			c.argumentCategory(br.IndexType, locals)
		}
		c.requireType(br.ArrayType, locals)
		return false, true
	}
	app := tr.SomeType
	name := app.Name.String()
	if name == "" {
		return false, false
	}
	local, localFound := locals[name]
	// Only earlier fields shadow global types. Keep future field names for
	// precise dependency diagnostics when no global type has that name.
	if localFound && local.field && !local.ready && (c.types[name] != nil || semanticBuiltin(name, c.tl1)) {
		localFound = false
	}
	if localFound {
		if len(app.Arguments) > 0 {
			c.report("template-arity", tr.PR, local.pr, "Local reference '%s' cannot have template arguments", name)
		}
		return c.localCategory(name, local, app.PRName)
	}
	var cats, known []bool
	for _, arg := range app.Arguments {
		cat, ok := c.argumentCategory(arg, locals)
		cats = append(cats, cat)
		known = append(known, ok)
	}
	t := c.types[name]
	if t == nil && semanticBuiltin(name, c.tl1) {
		t = &semanticType{name: name}
	}
	if t == nil {
		c.report("unknown-type", app.PRName, nil, "Unknown type '%s'", name)
		return false, false
	}
	if t.function {
		c.report("function-reference", app.PRName, &t.pr, "Cannot reference function '%s' as a type", name)
		return false, false
	}
	if len(t.params) != len(app.Arguments) {
		err := TemplateArityError(app.String(), len(t.params), len(app.Arguments))
		c.report("template-arity", tr.PR, &t.pr, "%s", err)
	} else {
		for i, p := range t.params {
			if known[i] && p.Category.IsNat() != cats[i] {
				c.report("argument-category", app.Arguments[i].PR, &p.PR, "Template argument '%s' of '%s' must have category %s", p.Name, name, p.Category)
			}
		}
	}
	return false, true
}

// Resolve aliases only far enough to classify fields used as sizes or masks.
// Unknown dependencies and cycles suppress secondary category errors.
func (c *semanticChecker) primitive(tr TL2TypeRef, substitutions map[string]TL2TypeArgument, active map[string]bool) (string, bool) {
	resolved := substituteSemanticArgument(TL2TypeArgument{Type: tr}, substitutions)
	if resolved.IsNumber {
		return "", true
	}
	tr = resolved.Type
	if tr.IsBracket() {
		return "", true
	}
	app := tr.SomeType
	name := app.Name.String()
	t := c.types[name]
	if t == nil && (semanticBuiltin(name, true) || semanticBuiltin(name, false)) {
		return name, true
	}
	key := tr.String()
	if t == nil || active[key] || len(active) >= 64 {
		return "", false
	}
	if t.alias == nil {
		return name, true
	}
	if len(t.params) != len(app.Arguments) {
		return "", false
	}
	args := map[string]TL2TypeArgument{}
	for i, p := range t.params {
		args[p.Name] = app.Arguments[i]
	}
	active[key] = true
	defer delete(active, key)
	return c.primitive(*t.alias, args, active)
}

func substituteSemanticArgument(arg TL2TypeArgument, substitutions map[string]TL2TypeArgument) TL2TypeArgument {
	if arg.IsNumber || len(substitutions) == 0 {
		return arg
	}
	if br := arg.Type.BracketType; br != nil {
		copy := *br
		copy.ArrayType = substituteSemanticArgument(TL2TypeArgument{Type: br.ArrayType}, substitutions).Type
		if br.HasIndex {
			copy.IndexType = substituteSemanticArgument(br.IndexType, substitutions)
		}
		arg.Type.BracketType = &copy
		return arg
	}
	app := &arg.Type.SomeType
	if len(app.Arguments) == 0 {
		if replacement, ok := substitutions[app.Name.String()]; ok {
			return replacement
		}
		return arg
	}
	app.Arguments = append([]TL2TypeArgument(nil), app.Arguments...)
	for i, a := range app.Arguments {
		app.Arguments[i] = substituteSemanticArgument(a, substitutions)
	}
	return arg
}

func semanticBuiltin(name string, tl1 bool) bool {
	if tl1 {
		switch name {
		case "int", "long", "double", "float", "uint32", "uint64", "string", "bytes", "#", "Type":
			return true
		}
	} else {
		switch name {
		case "int32", "uint32", "int64", "uint64", "float32", "float64", "byte", "string", "bool", "bit", "true":
			return true
		}
	}
	return false
}
