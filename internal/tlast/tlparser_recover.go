package tlast

// ParseTLFileRecovering retains complete declarations around errors, including
// after a missing semicolon. The strict generator parser remains unchanged.
func ParseTLFileRecovering(text, file string) (TL, []*ParseError) {
	parsed := ParseTLFileEditing(text, file)
	var result TL
	for i, c := range parsed.Declarations {
		if !parsed.Incomplete[i] {
			result = append(result, c)
		}
	}
	return result, parsed.Errors
}
