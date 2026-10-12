package tllsp

import "github.com/VKCOM/tl/internal/tlast"

func (s *server) rebuildSemanticDiagnostics() {
	var tl tlast.TL
	var tl2 []tlast.TL2Combinator
	for _, d := range s.documents() {
		tl = append(tl, d.tl...)
		tl2 = append(tl2, d.tl2...)
	}
	s.semantic = map[string][]diagnostic{}
	for _, item := range tlast.ValidateSchema(tl, tl2) {
		uri := uriFromPath(item.Pos.Begin.FileName())
		d := s.docs[uri]
		if d == nil {
			continue
		}
		b, e := d.parserRange(item.Pos)
		// TL1 typeref positions include bare markers and parentheses. Point
		// unknown-type diagnostics at the same name range as navigation.
		if item.Code == "unknown-type" && d.language == "tl" {
			for _, ref := range d.refs {
				if ref.begin >= b && ref.end <= e {
					b, e = ref.begin, ref.end
					break
				}
			}
		}
		diag := diagnostic{Range: d.span(b, e), Severity: 1, Source: "tl2lsp", Message: item.Message, Code: item.Code}
		if s.relatedDiagnostics && item.Related != nil {
			otherURI := uriFromPath(item.Related.Begin.FileName())
			if other := s.docs[otherURI]; other != nil {
				b, e := other.parserRange(*item.Related)
				diag.RelatedInformation = []diagnosticRelatedInformation{{Location: location{otherURI, other.span(b, e)}, Message: "Declared here"}}
			}
		}
		s.semantic[uri] = append(s.semantic[uri], diag)
	}
}
