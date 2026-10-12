package tlast

import "errors"

// ParseTLFileRecovering retains complete declarations around syntax errors for
// editor tooling. The generator continues to use the strict ParseTLFile parser.
func ParseTLFileRecovering(text, file string) (TL, []*ParseError) {
	opts := LexerOptions{AllowDirty: true, AllowBuiltin: true}
	lex := newLexer(text, file, opts)
	var diagnostics []*ParseError
	for lex.str != "" {
		if err := lex.nextToken(); err != nil {
			var pe *ParseError
			if errors.As(err, &pe) {
				diagnostics = append(diagnostics, pe)
			}
		}
	}
	lex.advance(0, eof)
	var result TL
	start, functionSection := 0, false
	for i, tok := range lex.tokens {
		if tok.tokenType != semiColon && tok.tokenType != eof {
			continue
		}
		tokens := append([]token(nil), lex.tokens[start:i+1]...)
		start = i + 1
		if tok.tokenType != eof {
			pos := tok.pos
			pos.offset += len(tok.val)
			pos.column += len(tok.val)
			tokens = append(tokens, token{tokenType: eof, pos: pos})
		}
		it := tokenIterator{tokens: tokens}
		commentStart := it
		for it.checkToken(typesSection) || it.checkToken(functionsSection) {
			functionSection = it.popFront().tokenType == functionsSection
			commentStart = it
		}
		if it.checkToken(eof) {
			continue
		}
		bad := false
		for _, t := range tokens {
			bad = bad || t.tokenType == undefined
		}
		if bad {
			continue
		}
		check := lexer{opts: opts, tokens: tokens}
		if _, err := check.validateTokens(); err != nil {
			var pe *ParseError
			if errors.As(err, &pe) {
				diagnostics = append(diagnostics, pe)
			}
			continue
		}
		// AllowBuiltin classifies a standalone '_' as an upper-case type name
		// for internal RHS placeholders. At a constructor position it instead
		// means the legacy anonymous constructor accepted by AllowDirty.
		// Reclassify only this copied token, preserving builtin RHS placeholders
		// and leaving the strict generator parser unchanged.
		_, constructor := parseModifiers(it, it.front().pos)
		if tok := constructor.front(); tok.tokenType == ucIdent && tok.val == "_" {
			tokens[constructor.offset].tokenType = lcIdent
		}
		comb, _, err := parseCombinator(commentStart, it, functionSection, opts.AllowBuiltin)
		if err != nil {
			var pe *ParseError
			if errors.As(err, &pe) {
				diagnostics = append(diagnostics, pe)
			}
			continue
		}
		result = append(result, &comb)
	}
	return result, diagnostics
}
