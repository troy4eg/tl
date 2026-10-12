package tllsp

import (
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"
)

// LSP positions use UTF-16 code units; the TL parser uses UTF-8 byte offsets.
type source struct {
	text  string
	lines []int
}

func newSource(text string) source {
	s := source{text: text, lines: []int{0}}
	for i, b := range []byte(text) {
		if b == '\n' {
			s.lines = append(s.lines, i+1)
		}
	}
	return s
}

func (s source) position(offset int) position {
	offset = max(0, min(offset, len(s.text)))
	line := sort.Search(len(s.lines), func(i int) bool { return s.lines[i] > offset }) - 1
	units := 0
	for _, r := range s.text[s.lines[line]:offset] {
		units++
		if r > 0xffff {
			units++
		}
	}
	return position{line, units}
}

func (s source) offset(p position) (int, error) {
	if p.Line < 0 || p.Line >= len(s.lines) || p.Character < 0 {
		return 0, fmt.Errorf("position outside document")
	}
	start := s.lines[p.Line]
	end := len(s.text)
	if p.Line+1 < len(s.lines) {
		end = s.lines[p.Line+1] - 1
	}
	if end > start && s.text[end-1] == '\r' {
		end--
	}
	units := 0
	for i := start; i < end; {
		if units == p.Character {
			return i, nil
		}
		r, size := utf8.DecodeRuneInString(s.text[i:end])
		units++
		if r > 0xffff {
			units++
		}
		if units > p.Character {
			return 0, fmt.Errorf("position splits UTF-16 surrogate pair")
		}
		i += size
	}
	if units == p.Character {
		return end, nil
	}
	return 0, fmt.Errorf("position outside line")
}

func (s source) span(begin, end int) span {
	return span{s.position(begin), s.position(end)}
}

func pathFromURI(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return "", fmt.Errorf("expected file URI: %s", uri)
	}
	p := u.Path
	if u.Host != "" && u.Host != "localhost" {
		if runtime.GOOS != "windows" {
			return "", fmt.Errorf("unsupported file URI host: %s", u.Host)
		}
		p = "//" + u.Host + p
	}
	if runtime.GOOS == "windows" && len(p) > 2 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	p = filepath.FromSlash(p)
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("file URI must be absolute")
	}
	return filepath.Clean(p), nil
}

func uriFromPath(path string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

func canonicalURI(uri string) string {
	if p, err := pathFromURI(uri); err == nil {
		return uriFromPath(p)
	}
	return uri
}
