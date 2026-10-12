package tlast

// ParseTL2FileRecovering retains complete declarations around errors, including
// after a missing semicolon. Partial declarations are available to editor tools
// through ParseTL2FileEditing. The strict generator parser is unchanged.
func ParseTL2FileRecovering(text, file string) (TL2File, []*ParseError) {
	parsed := ParseTL2FileEditing(text, file)
	var result TL2File
	for i, c := range parsed.Declarations {
		if !parsed.Incomplete[i] {
			result.Combinators = append(result.Combinators, c)
		}
	}
	return result, parsed.Errors
}
