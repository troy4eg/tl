package tlast

import (
	"errors"
	"strconv"
)

// EditorParse retains source-backed fragments of malformed declarations. The
// incomplete flags correspond to Declarations; generators must use the strict
// parsers, and semantic validation must use only complete declarations.
type EditorParse[T any] struct {
	Declarations []T
	Incomplete   []bool
	Errors       []*ParseError
}

func editorLex(text, file string, opts LexerOptions) (*lexer, []*ParseError) {
	lex := newLexer(text, file, opts)
	var errs []*ParseError
	for lex.str != "" {
		if err := lex.nextToken(); err != nil {
			var pe *ParseError
			if errors.As(err, &pe) {
				errs = append(errs, pe)
			}
		}
	}
	lex.advance(0, eof)
	return &lex, errs
}

func editorTrivia(t token) bool {
	return t.tokenType == whiteSpace || t.tokenType == tab || t.tokenType == newLine || t.tokenType == comment
}

func editorName(t token) bool { return t.IsName() || t.IsNameNs() || t.val == "_" }

// Recognize an explicit new declaration at the beginning of a line, even when
// the preceding declaration has unclosed brackets. Field lines, type arguments
// and union variants must not become independent declarations.
func editorDeclarationStart(tokens []token, i int, tl2 bool) bool {
	it := tokenIterator{tokens: tokens, offset: i}
	for it.checkToken(annotation) {
		it.popFront()
	}
	if !editorName(it.front()) {
		return false
	}
	name := it.popFront()
	if !tl2 && name.tokenType != lcIdent && name.tokenType != lcIdentNS && name.val != "_" {
		return false
	}
	if it.checkToken(crc32hash) {
		return true
	}
	if it.checkToken(equalSign) || it.checkToken(functionSign) || it.checkToken(tl2alias) {
		return true
	}
	if !tl2 && editorName(it.front()) {
		copy := it
		copy.popFront()
		if copy.checkToken(colon) {
			return true
		}
	}
	if !tl2 {
		if it.checkToken(lCurlyBracket) {
			return true
		}
		return false
	}
	if !it.expect(lAngleBracket) {
		return false
	}
	// A category declaration distinguishes a header from a type application.
	if !editorName(it.front()) {
		return false
	}
	it.popFront()
	return it.checkToken(colon)
}

func editorChunks(tokens []token) [][]token {
	var result [][]token
	start := 0
	for i, tok := range tokens {
		if tok.tokenType != semiColon && tok.tokenType != eof {
			continue
		}
		chunk := append([]token(nil), tokens[start:i+1]...)
		start = i + 1
		if tok.tokenType != eof {
			pos := tok.pos
			pos.offset += len(tok.val)
			pos.column += len(tok.val)
			chunk = append(chunk, token{tokenType: eof, pos: pos})
		}
		result = append(result, chunk)
	}
	return result
}

func editorResyncChunks(tokens []token, tl2 bool) [][]token {
	var result [][]token
	start, lastSignificant := 0, -1
	started, lineStart := false, true
	appendChunk := func(end int, pos Position) {
		chunk := append([]token(nil), tokens[start:end]...)
		if len(chunk) == 0 || chunk[len(chunk)-1].tokenType != eof {
			chunk = append(chunk, token{tokenType: eof, pos: pos})
		}
		result = append(result, chunk)
		start = end
	}
	for i, tok := range tokens {
		if tok.tokenType == eof {
			appendChunk(i+1, tok.pos)
			break
		}
		if editorTrivia(tok) {
			if tok.tokenType == newLine {
				lineStart = true
			}
			continue
		}
		section := !tl2 && (tok.tokenType == typesSection || tok.tokenType == functionsSection)
		if started && (section || lineStart && editorDeclarationStart(tokens, i, tl2)) {
			// Keep leading comments with the following declaration.
			cut := i
			newline := false
			for j := lastSignificant + 1; j < i; j++ {
				newline = newline || tokens[j].tokenType == newLine
				if newline && tokens[j].tokenType == comment {
					cut = j
					break
				}
			}
			appendChunk(cut, tokens[cut].pos)
			started = false
		}
		if tok.tokenType == semiColon {
			pos := tok.pos
			pos.offset += len(tok.val)
			pos.column += len(tok.val)
			appendChunk(i+1, pos)
			started = false
		} else if !section && tok.tokenType != annotation {
			started = true
		}
		lastSignificant = i
		lineStart = false
	}
	return result
}

func editorChunkError(tokens []token, opts LexerOptions) (bad bool, pe *ParseError) {
	for _, tok := range tokens {
		if tok.tokenType == undefined {
			return true, nil
		}
	}
	check := lexer{opts: opts, tokens: tokens}
	_, err := check.validateTokens()
	if err != nil {
		errors.As(err, &pe)
		return true, pe
	}
	return false, nil
}

func editorRange(begin, end, outer Position) PositionRange {
	return PositionRange{Outer: outer, Begin: begin, End: end}
}

// ParseTL2FileEditing exposes partial declarations alongside complete ones.
func ParseTL2FileEditing(text, file string) EditorParse[TL2Combinator] {
	opts := LexerOptions{LexerLanguage: TL2}
	lex, errs := editorLex(text, file, opts)
	result := EditorParse[TL2Combinator]{Errors: errs}
	chunks := editorChunks(lex.tokens)
	for index := 0; index < len(chunks); index++ {
		tokens := chunks[index]
		it := tokenIterator{tokens: tokens}
		if it.expectLazy(eof) {
			continue
		}
		bad, pe := editorChunkError(tokens, opts)
		var comb TL2Combinator
		if !bad {
			comb, _, pe = parseTL2Combinator(it)
		}
		incomplete := bad || pe != nil
		if incomplete {
			if recovered := editorRecoveryChunks(tokens, true); len(recovered) > 1 {
				chunks = replaceEditorChunk(chunks, index, recovered)
				index--
				continue
			}
		}
		if pe != nil {
			result.Errors = append(result.Errors, pe)
		}
		if incomplete {
			comb = editorTL2Combinator(it)
		}
		if comb.ReferenceName().Name == "" {
			continue
		}
		result.Declarations = append(result.Declarations, comb)
		result.Incomplete = append(result.Incomplete, incomplete)
	}
	return result
}

func editorTL2Combinator(it tokenIterator) TL2Combinator {
	start := it
	outer := it.skipWS(Position{}).Begin
	end := it.tokens[len(it.tokens)-1].pos
	c := TL2Combinator{PR: editorRange(outer, end, outer), CommentBefore: parseCommentBefore(start, it)}
	_, it, c.Annotations = zeroOrMore(parseTL2Annotation)(it, outer)
	if !editorName(it.front()) {
		return TL2Combinator{}
	}
	pr := it.skipWS(outer)
	state, rest, name := parseTL2TypeName(it, outer)
	if !state.HasProgress() {
		return TL2Combinator{}
	}
	it = rest
	pr.End = it.front().pos
	c.TypeDecl.Name, c.TypeDecl.PRName = name, pr
	c.TypeDecl.PR = c.PR
	if it.checkToken(crc32hash) {
		tok := it.popFront()
		value, _ := strconv.ParseUint(tok.val[1:], 16, 32)
		c.TypeDecl.Magic = uint32(value)
		c.TypeDecl.PRID = editorRange(tok.pos, it.front().pos, outer)
	}
	if it.expect(lAngleBracket) {
		for !it.checkToken(eof) {
			before := it.offset
			state, next, p := parseTL2TypeArgumentDeclaration(it, outer)
			if p.Name != "" {
				p.PR.End = next.front().pos
				c.TypeDecl.TemplateArguments = append(c.TypeDecl.TemplateArguments, p)
			}
			it = next
			if state.IsFailed() || it.offset == before || !it.expect(commaSign) {
				break
			}
		}
		it.expect(rAngleBracket)
	}
	if it.expect(equalSign) {
		c.TypeDecl.Type = editorTL2Definition(&it, outer, false)
	} else if it.expect(tl2alias) {
		c.TypeDecl.Type.IsTypeAlias = true
		c.TypeDecl.Type.TypeAlias = editorTL2Type(&it, outer)
	} else if !it.checkToken(eof) && !it.checkToken(semiColon) {
		c.IsFunction = true
		c.FuncDecl = TL2FuncDeclaration{Name: name, PRName: pr, PR: c.PR, Magic: c.TypeDecl.Magic, PRID: c.TypeDecl.PRID}
		c.FuncDecl.Arguments = editorTL2Fields(&it, outer)
		if it.expect(functionSign) {
			c.FuncDecl.ReturnType = editorTL2Definition(&it, outer, true)
		}
	}
	return c
}

func editorTL2Type(it *tokenIterator, outer Position) TL2TypeRef {
	begin := it.skipWS(outer).Begin
	_, next, tr := parseTL2Type(*it, outer)
	*it = next
	tr.PR = editorRange(begin, it.front().pos, outer)
	return tr
}

func editorTL2Fields(it *tokenIterator, outer Position) []TL2Field {
	var fields []TL2Field
	for !it.checkToken(eof) && !it.checkToken(semiColon) && !it.checkToken(verticalBar) && !it.checkToken(functionSign) {
		before := it.offset
		state, next, f := parseTL2Field(*it, outer)
		*it = next
		if !state.StartProcessing {
			break
		}
		f.PR.End = it.front().pos
		if f.Name != "" {
			fields = append(fields, f)
		}
		if state.IsFailed() {
			// Resume at another named field, preserving already parsed types.
			for !it.checkToken(eof) && !it.checkToken(semiColon) && !it.checkToken(verticalBar) && !it.checkToken(functionSign) {
				copy := *it
				if editorName(copy.front()) {
					copy.popFront()
					copy.expect(questionMark)
					if copy.checkToken(colon) {
						break
					}
				}
				it.popFront()
			}
		}
		if it.offset <= before {
			break
		}
	}
	return fields
}

func editorTL2Definition(it *tokenIterator, outer Position, result bool) TL2TypeDefinition {
	def := TL2TypeDefinition{PR: it.skipWS(outer)}
	if result && it.expect(tl2alias) {
		def.IsTypeAlias = true
		def.TypeAlias = editorTL2Type(it, outer)
		return def
	}
	// Named constructors may omit the leading bar, including when there is
	// only one variant. A function result may instead be an anonymous type.
	union := it.checkToken(verticalBar)
	if !union && editorName(it.front()) {
		copy := *it
		copy.popFront()
		if !result {
			union = !copy.checkToken(colon) && !copy.checkToken(questionMark)
		} else if editorName(copy.front()) {
			copy.popFront()
			copy.expect(questionMark)
			union = copy.checkToken(colon)
		}
	}
	if !union {
		for _, tok := range it.tokens[it.offset:] {
			if tok.tokenType == verticalBar {
				union = true
				break
			}
		}
	}
	if union {
		def.StructType.IsUnionType = true
		it.expect(verticalBar)
		for !it.checkToken(eof) && !it.checkToken(semiColon) {
			begin := it.skipWS(outer).Begin
			if !editorName(it.front()) {
				break
			}
			v := TL2UnionConstructor{Name: it.popFront().val, PRName: editorRange(begin, it.front().pos, outer)}
			v.Fields = editorTL2Fields(it, outer)
			if len(v.Fields) == 0 && !it.checkToken(verticalBar) && !it.checkToken(eof) && !it.checkToken(semiColon) {
				v.IsTypeAlias = true
				v.TypeAlias = editorTL2Type(it, outer)
			}
			v.PR = editorRange(begin, it.front().pos, outer)
			def.StructType.UnionType.Variants = append(def.StructType.UnionType.Variants, v)
			if !it.expect(verticalBar) {
				break
			}
		}
	} else {
		def.StructType.ConstructorFields = editorTL2Fields(it, outer)
		if result && len(def.StructType.ConstructorFields) == 0 && !it.checkToken(eof) && !it.checkToken(semiColon) {
			tr := editorTL2Type(it, outer)
			def.StructType.ConstructorFields = []TL2Field{{Type: tr, PR: tr.PR}}
		}
	}
	def.PR.End = it.front().pos
	return def
}

func editorTL1ConstructorToken(tokens []token, it tokenIterator) {
	_, constructor := parseModifiers(it, it.front().pos)
	if tok := constructor.front(); tok.tokenType == ucIdent && tok.val == "_" {
		tokens[constructor.offset].tokenType = lcIdent
	}
}

// ParseTLFileEditing preserves parsed TL1 fields and template parameters even
// when the result type, a field, or a bracket is still being written.
func ParseTLFileEditing(text, file string) EditorParse[*Combinator] {
	opts := LexerOptions{AllowDirty: true, AllowBuiltin: true}
	lex, errs := editorLex(text, file, opts)
	result := EditorParse[*Combinator]{Errors: errs}
	functionSection := false
	chunks := editorChunks(lex.tokens)
	for index := 0; index < len(chunks); index++ {
		tokens := chunks[index]
		it := tokenIterator{tokens: tokens}
		commentStart := it
		for it.checkToken(typesSection) || it.checkToken(functionsSection) {
			functionSection = it.popFront().tokenType == functionsSection
			commentStart = it
		}
		if it.checkToken(eof) {
			continue
		}
		editorTL1ConstructorToken(tokens, it)
		bad, pe := editorChunkError(tokens, opts)
		var c Combinator
		var err error
		if !bad {
			c, _, err = parseCombinator(commentStart, it, functionSection, true)
			if err != nil {
				errors.As(err, &pe)
			}
		}
		incomplete := bad || err != nil
		if incomplete {
			if recovered := editorRecoveryChunks(tokens, false); len(recovered) > 1 {
				chunks = replaceEditorChunk(chunks, index, recovered)
				index--
				continue
			}
		}
		if pe != nil {
			result.Errors = append(result.Errors, pe)
		}
		if incomplete {
			c = editorTL1Combinator(commentStart, it, functionSection)
		}
		if c.Construct.Name.IsEmpty() {
			continue
		}
		result.Declarations = append(result.Declarations, &c)
		result.Incomplete = append(result.Incomplete, incomplete)
	}
	return result
}

func editorTL1Combinator(commentStart, it tokenIterator, function bool) Combinator {
	outer := it.skipWS(Position{}).Begin
	end := it.tokens[len(it.tokens)-1].pos
	c := Combinator{PR: editorRange(outer, end, outer), IsFunction: function, CommentBefore: parseCommentBefore(commentStart, it)}
	c.Modifiers, it = parseModifiers(it, outer)
	con, next, err := parseConstructor(it, outer, true)
	if err != nil {
		return Combinator{}
	}
	c.Construct, it = con, next
	c.TemplateArgumentsPR = it.skipWS(outer)
	for it.expect(lCurlyBracket) {
		begin := it.tokens[it.offset-1].pos
		p := TemplateArgument{PR: editorRange(begin, begin, outer)}
		name, rest, err := parseVarIdent(it, outer)
		if err != nil {
			break
		}
		p.FieldName, it = name, rest
		if it.expect(colon) {
			if it.checkToken(numberSign) {
				p.IsNat = true
				it.popFront()
			} else if it.front().val == "Type" {
				it.popFront()
			}
		}
		it.expect(rCurlyBracket)
		p.PR.End = it.front().pos
		c.TemplateArguments = append(c.TemplateArguments, p)
	}
	c.TemplateArgumentsPR.End = it.front().pos
	if it.expect(questionMark) {
		c.Builtin = true
	} else {
		c.Fields = editorTL1Fields(&it, outer, 0)
	}
	if it.checkToken(functionSign) {
		c.IsFunction = true
		it.popFront()
	} else {
		it.expect(equalSign)
	}
	if c.IsFunction {
		c.FuncDecl = editorTL1Type(&it, outer, true, 0)
	} else {
		decl, rest, err := parseTypeDeclaration(it, outer)
		if err == nil {
			c.TypeDecl, it = decl, rest
		}
	}
	return c
}

func editorTL1Fields(it *tokenIterator, outer Position, depth int) []Field {
	if depth >= 128 {
		return nil
	}
	var fields []Field
	for !it.checkToken(eof) && !it.checkToken(semiColon) && !it.checkToken(equalSign) && !it.checkToken(functionSign) && !it.checkToken(rSquareBracket) {
		start := *it
		f, next, err := parseField(start, *it, outer)
		if err == nil {
			*it = next
			fields = append(fields, f)
			continue
		}
		begin := it.skipWS(outer).Begin
		f = Field{PR: editorRange(begin, begin, outer), PRName: editorRange(begin, begin, outer), CommentBefore: parseCommentBefore(start, *it)}
		var nameEnd Position
		f.FieldName, next, nameEnd = parseFieldName(*it, outer)
		*it = next
		if f.FieldName != "" {
			f.PRName.End = nameEnd
		}
		maskStart := *it
		mask, rest, maskErr := parseFieldMask(*it, outer)
		if maskErr == nil {
			f.Mask, *it = mask, rest
		} else {
			maskStart.skipWS(outer)
			if editorName(maskStart.front()) {
				tok := maskStart.popFront()
				nameEnd := maskStart.front().pos
				if maskStart.expect(dotSign) {
					f.Mask = &FieldMask{MaskName: tok.val, PRName: editorRange(tok.pos, nameEnd, outer)}
					*it = maskStart
				}
			}
		}
		f.Excl = it.expect(exclamation)
		scale, rest, _ := parseScaleFactorOpt(*it, outer)
		repeated := false
		if scale != nil && rest.expect(asterisk) && rest.expect(lSquareBracket) {
			f.ScaleRepeat.ExplicitScale = true
			f.ScaleRepeat.Scale = *scale
			*it = rest
			repeated = true
		} else {
			repeated = it.expect(lSquareBracket)
		}
		if repeated {
			f.IsRepeated = true
			f.ScaleRepeat.Rep = editorTL1Fields(it, outer, depth+1)
			it.expect(rSquareBracket)
			f.ScaleRepeat.PR = editorRange(begin, it.front().pos, outer)
		} else {
			f.FieldType = editorTL1Type(it, outer, false, depth+1)
		}
		f.PR.End = it.front().pos
		if f.FieldName != "" || f.IsRepeated || !f.FieldType.Type.IsEmpty() {
			fields = append(fields, f)
		}
		if it.offset <= start.offset {
			it.popFront()
		}
		// Skip an unrecognized fragment, stopping before a new field or RHS.
		for !it.checkToken(eof) && !it.checkToken(semiColon) && !it.checkToken(equalSign) && !it.checkToken(functionSign) && !it.checkToken(rSquareBracket) {
			copy := *it
			if editorName(copy.front()) {
				copy.popFront()
				if copy.checkToken(colon) {
					break
				}
			}
			it.popFront()
		}
	}
	return fields
}

func editorTL1Type(it *tokenIterator, outer Position, apply bool, depth int) TypeRef {
	if depth >= 128 {
		return TypeRef{}
	}
	begin := it.skipWS(outer).Begin
	tr, next, err := parseTypeRef(*it, apply, true, outer)
	if err == nil && tr != nil {
		*it = next
		return *tr
	}
	bare := it.expect(percentSign)
	round := it.expect(lRoundBracket)
	if round {
		tr := editorTL1Type(it, outer, true, depth+1)
		it.expect(rRoundBracket)
		tr.Bare = tr.Bare || bare
		tr.PR = editorRange(begin, it.front().pos, outer)
		return tr
	}
	result := TypeRef{Bare: bare, PR: editorRange(begin, begin, outer)}
	if it.expect(numberSign) {
		result.Type.Name = "#"
	} else {
		name, rest, err := parseTypeRefAsName(*it, outer)
		if err != nil {
			return result
		}
		result.Type, *it = name, rest
	}
	angle := it.expect(lAngleBracket)
	if angle || apply {
		for !it.checkToken(eof) && !it.checkToken(semiColon) && !it.checkToken(rRoundBracket) && !it.checkToken(rAngleBracket) && !it.checkToken(equalSign) && !it.checkToken(rSquareBracket) {
			copy := *it
			if editorName(copy.front()) {
				copy.popFront()
				if copy.checkToken(colon) {
					break
				}
			}
			start := it.offset
			arg, rest, err := parseArithmeticOrTypeOpt(*it, false, outer)
			if err == nil && arg != nil {
				result.Args = append(result.Args, *arg)
				*it = rest
			} else {
				part := editorTL1Type(it, outer, false, depth+1)
				if part.Type.IsEmpty() {
					break
				}
				result.Args = append(result.Args, ArithmeticOrType{T: part})
			}
			if it.offset <= start || angle && !it.expect(commaSign) {
				break
			}
		}
		if angle {
			it.expect(rAngleBracket)
		}
	}
	result.PR.End = it.front().pos
	return result
}

// Retry only failed chunks. Valid declarations are parsed once; speculative
// parsing previously built and discarded a second AST for every declaration.
func replaceEditorChunk(chunks [][]token, index int, recovered [][]token) [][]token {
	out := make([][]token, 0, len(chunks)+len(recovered)-1)
	out = append(out, chunks[:index]...)
	out = append(out, recovered...)
	return append(out, chunks[index+1:]...)
}

func editorRecoveryChunks(tokens []token, tl2 bool) [][]token {
	chunks := editorResyncChunks(tokens, tl2)
	out := chunks[:0]
	for _, chunk := range chunks {
		for _, tok := range chunk {
			if tok.tokenType != eof && !editorTrivia(tok) {
				out = append(out, chunk)
				break
			}
		}
	}
	return out
}
