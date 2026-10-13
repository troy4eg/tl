package tllsp

import "encoding/json"

// Connection traffic batches consecutive changes. Direct handler calls retain
// synchronous semantics, which also keeps the control benchmarks comparable.
func (s *server) changeDocument(raw json.RawMessage) *rpcError {
	var p struct {
		TextDocument struct {
			URI     string `json:"uri"`
			Version int    `json:"version"`
		} `json:"textDocument"`
		ContentChanges []struct {
			Range *span  `json:"range"`
			Text  string `json:"text"`
		} `json:"contentChanges"`
	}
	if err := decodeParams(raw, &p); err != nil {
		return err
	}
	uri := canonicalURI(p.TextDocument.URI)
	d := s.docs[uri]
	if pending := s.pendingChanges[uri]; pending != nil {
		d = pending
	}
	if d == nil || !d.open || p.TextDocument.Version <= d.version {
		return nil
	}
	text := d.text
	for _, change := range p.ContentChanges {
		if change.Range == nil {
			text = change.Text
			continue
		}
		src := newSource(text)
		begin, e1 := src.offset(change.Range.Start)
		end, e2 := src.offset(change.Range.End)
		if e1 != nil || e2 != nil || end < begin {
			s.log.Printf("ignored invalid edit for %s", uri)
			return nil
		}
		text = text[:begin] + change.Text + text[end:]
	}
	if s.deferChanges {
		if s.pendingChanges == nil {
			s.pendingChanges = map[string]*document{}
		}
		s.pendingChanges[uri] = &document{uri: uri, language: d.language, source: newSource(text), version: p.TextDocument.Version, open: true}
	} else {
		s.docs[uri] = parseDocumentLanguage(uri, text, p.TextDocument.Version, true, d.language)
	}
	return nil
}

func (s *server) flushChanges() error {
	if len(s.pendingChanges) == 0 {
		return nil
	}
	for uri, d := range s.pendingChanges {
		s.docs[uri] = parseDocumentLanguage(uri, d.text, d.version, true, d.language)
	}
	s.pendingChanges = nil
	s.refreshIndex(false)
	return s.publishDiagnostics()
}
