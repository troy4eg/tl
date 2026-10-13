package tllsp

import (
	"sort"
	"strings"
)

func nameByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_' || b == '.'
}

// Shared semantic tokens let clients without a TL2 Tree-sitter grammar provide
// syntax highlighting as well. The order is the advertised LSP legend.
var tokenTypes = []string{"type", "typeParameter", "property", "enumMember", "function", "comment", "number", "operator", "decorator"}

type semanticToken struct{ begin, end, kind int }

func (d *document) semanticTokens() []int {
	var tokens []semanticToken
	add := func(begin, end, kind int) {
		if end > begin {
			tokens = append(tokens, semanticToken{begin, end, kind})
		}
	}
	var visit func(*symbol)
	visit = func(sym *symbol) {
		kind := 0
		switch sym.kind {
		case 26:
			kind = 1
		case 8:
			kind = 2
		case 22:
			kind = 3
		case 12:
			kind = 4
		}
		add(sym.nameBegin, sym.nameEnd, kind)
		for _, c := range sym.children {
			visit(c)
		}
	}
	for _, sym := range d.symbols {
		visit(sym)
	}
	for _, ref := range d.refs {
		kind := 0
		if ref.local != nil {
			kind = 1
			if ref.local.kind == 8 {
				kind = 2
			}
		}
		add(ref.begin, ref.end, kind)
	}
	for i := 0; i < len(d.text); {
		if strings.HasPrefix(d.text[i:], "//") {
			end := strings.IndexAny(d.text[i:], "\r\n")
			if end < 0 {
				end = len(d.text) - i
			}
			add(i, i+end, 5)
			i += end
			continue
		}
		b := d.text[i]
		if b == '@' {
			end := i + 1
			for end < len(d.text) && nameByte(d.text[end]) {
				end++
			}
			add(i, end, 8)
			i = end
			continue
		}
		if b == '#' || b >= '0' && b <= '9' {
			end := i + 1
			for end < len(d.text) && nameByte(d.text[end]) {
				end++
			}
			add(i, end, 6)
			i = end
			continue
		}
		if strings.ContainsRune("=<>|?:[]", rune(b)) {
			add(i, i+1, 7)
		}
		if nameByte(b) {
			for i < len(d.text) && nameByte(d.text[i]) {
				i++
			}
		} else {
			i++
		}
	}
	sort.Slice(tokens, func(i, j int) bool { return tokens[i].begin < tokens[j].begin })
	data := []int{}
	previous := position{}
	lastEnd := -1
	for _, t := range tokens {
		if t.begin < lastEnd {
			continue
		}
		start, end := d.position(t.begin), d.position(t.end)
		if start.Line != end.Line || start.Character == end.Character {
			continue
		}
		deltaLine := start.Line - previous.Line
		deltaColumn := start.Character
		if deltaLine == 0 {
			deltaColumn -= previous.Character
		}
		data = append(data, deltaLine, deltaColumn, end.Character-start.Character, t.kind, 0)
		previous, lastEnd = start, t.end
	}
	return data
}
