package tlast

import "errors"

// ParseTL2FileRecovering parses independently terminated declarations for editor
// tooling. Bad declarations are skipped up to the next semicolon. Unlike the
// strict generator parser, it retains valid declarations on either side of an
// error. All positions refer to the original, unmodified input.
func ParseTL2FileRecovering(text, file string) (TL2File, []*ParseError) {
	lex := newLexer(text, file, LexerOptions{LexerLanguage: TL2})
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
	var result TL2File
	start := 0
	for i, tok := range lex.tokens {
		if tok.tokenType != semiColon && tok.tokenType != eof {
			continue
		}
		end := i + 1
		tokens := append([]token(nil), lex.tokens[start:end]...)
		if tok.tokenType != eof {
			pos := tok.pos
			pos.offset += len(tok.val)
			pos.column += len(tok.val)
			tokens = append(tokens, token{tokenType: eof, pos: pos})
		}
		start = end
		it := tokenIterator{tokens: tokens}
		if it.expectLazy(eof) {
			continue
		}
		// Lexer failures already have precise diagnostics; avoid a second
		// parser diagnostic for the same declaration.
		bad := false
		for _, t := range tokens {
			bad = bad || t.tokenType == undefined
		}
		if bad {
			continue
		}
		check := lexer{opts: LexerOptions{LexerLanguage: TL2}, tokens: tokens}
		if _, err := check.validateTokens(); err != nil {
			var pe *ParseError
			if errors.As(err, &pe) {
				diagnostics = append(diagnostics, pe)
			}
			continue
		}
		comb, _, pe := parseTL2Combinator(it)
		if pe != nil {
			diagnostics = append(diagnostics, pe)
			continue
		}
		result.Combinators = append(result.Combinators, comb)
	}
	return result, diagnostics
}
