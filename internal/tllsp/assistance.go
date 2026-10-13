package tllsp

import "strings"

type typeParameter struct {
	name string
	nat  bool
}
type assistToken struct {
	text       string
	begin, end int
}

// This scanner only tracks the context at the cursor. Schema parsing and name
// binding remain in tlast; comments cannot introduce delimiters or parameters.
func assistTokens(text string, begin, end int) []assistToken {
	var out []assistToken
	for i := begin; i < end; {
		if strings.HasPrefix(text[i:end], "//") {
			for i < end && text[i] != '\n' && text[i] != '\r' {
				i++
			}
			continue
		}
		if strings.ContainsRune(" \t\r\n", rune(text[i])) {
			i++
			continue
		}
		start := i
		if nameByte(text[i]) {
			for i < end && nameByte(text[i]) {
				i++
			}
		} else if strings.HasPrefix(text[i:end], "<=>") {
			i += 3
		} else if strings.HasPrefix(text[i:end], "=>") {
			i += 2
		} else {
			i++
		}
		out = append(out, assistToken{text[start:i], start, i})
	}
	return out
}

type assistance struct {
	kind              string // declaration, field, type, nat, category, mask-bit, none
	begin, end        int
	prefix            string
	locals            map[string]*symbol
	root              *symbol
	call              *typeCall
	mask              bool
	bare              bool
	fieldBegin        int
	apply             bool
	resultDeclaration bool
}

func (d *document) assistScope(offset int) (*symbol, map[string]*symbol) {
	var root *symbol
	var locals map[string]*symbol
	var visit func(*symbol)
	visit = func(sym *symbol) {
		if offset < sym.begin || offset > sym.end {
			return
		}
		if sym.locals != nil {
			locals = sym.locals
		}
		for _, c := range sym.children {
			visit(c)
		}
	}
	for _, sym := range d.symbols {
		if offset >= sym.begin && offset <= sym.end {
			root = sym
			visit(sym)
		}
	}
	for _, scope := range d.assistScopes {
		visit(scope)
	}
	return root, locals
}

func (d *document) assistance(offset int, completion bool) assistance {
	a := assistance{kind: "none", begin: offset, end: offset}
	lineStart := strings.LastIndexAny(d.text[:offset], "\r\n") + 1
	if strings.Contains(d.text[lineStart:offset], "//") {
		return a
	}
	for a.begin > 0 && nameByte(d.text[a.begin-1]) {
		a.begin--
	}
	for a.end < len(d.text) && nameByte(d.text[a.end]) {
		a.end++
	}
	a.prefix = d.text[a.begin:offset]
	a.root, a.locals = d.assistScope(offset)
	if a.root == nil {
		a.kind = "declaration"
		return a
	}
	limit := offset
	if completion {
		limit = a.begin
	}
	tokens := assistTokens(d.text, a.root.begin, limit)
	if len(tokens) == 0 || tokens[len(tokens)-1].text == ";" {
		a.kind = "declaration"
		return a
	}
	// A prefix of a new declaration is itself a partial indexed symbol.
	if offset <= a.root.nameEnd && a.root.incomplete {
		a.kind = "declaration"
		return a
	}
	named := false
	if completion {
		walkSymbols(a.root.children, func(child *symbol) {
			if a.begin >= child.nameBegin && a.begin < child.nameEnd {
				if child.kind != 8 && child.kind != 22 && child.kind != 26 {
					return
				}
				a.kind = "field"
				if child.kind == 22 {
					a.kind = "variant"
				}
				if child.kind == 26 {
					a.kind = "none"
				}
				named = true
			}
		})
	}
	if named {
		return a
	}
	// Template declaration categories precede the body delimiter.
	body := -1
	curly, angle := 0, 0
	headerAngle := false
	for i, tok := range tokens {
		switch tok.text {
		case "{":
			curly++
		case "}":
			if curly > 0 {
				curly--
			}
		case "<":
			angle++
			if i > 0 && tokens[i-1].begin == a.root.nameBegin {
				headerAngle = true
			}
		case ">":
			if angle > 0 {
				angle--
			}
		case "=", "<=>", "=>":
			if curly == 0 && angle == 0 {
				body = i
			}
		}
	}
	if body < 0 && (curly > 0 || d.language != "tl" && angle > 0 && headerAngle) {
		for i := len(tokens) - 1; i >= 0; i-- {
			if tokens[i].text == ":" {
				a.kind = "category"
				if completion && offset > 0 && d.text[offset-1] == '#' {
					a.begin = offset - 1
					a.prefix = "#"
				}
				return a
			}
			if tokens[i].text == "{" || tokens[i].text == "<" || tokens[i].text == "," {
				break
			}
		}
		return a
	}
	if body < 0 && a.root.kind != 12 && d.language != "tl" {
		return a
	}
	start := 1
	if body >= 0 {
		start = body + 1
	}
	rhs := body >= 0 && (d.language == "tl" || tokens[body].text == "<=>")
	a.resultDeclaration = rhs && d.language == "tl" && a.root.kind != 12
	colon := -1
	for i := start; i < len(tokens); i++ {
		if tokens[i].text == ":" {
			colon = i
		}
		if d.language == "tl" && tokens[i].text == "[" {
			colon = -1
			start = i + 1
		}
		if d.language != "tl" && tokens[i].text == "|" {
			colon = -1
			start = i + 1
		}
	}
	if colon >= 0 {
		start = colon + 1
		if colon > 0 {
			a.fieldBegin = tokens[colon-1].begin
		}
	}
	if colon < 0 && !rhs {
		if body >= 0 && tokens[body].text == "=>" {
			a.kind = "type" // anonymous return type or the start of result fields
		} else {
			a.kind = "field"
			if d.language != "tl" && tokens[len(tokens)-1].text == "|" {
				a.kind = "variant"
			}
			return a

		}
	} else {
		a.kind = "type"
	}
	expr := tokens[start:]
	if d.language == "tl" && colon >= 0 {
		a.mask = true
		lastMask := -1
		for i, tok := range expr {
			if tok.text == "?" {
				lastMask = i
			}
		}
		if lastMask >= 0 {
			expr = expr[lastMask+1:]
			a.mask = false
		}
		if dot := strings.LastIndex(a.prefix, "."); dot >= 0 {
			if local := a.locals[a.prefix[:dot]]; local != nil && (local.kind == 26 && local.nat || !local.optional && local.nameBegin < a.fieldBegin && local.legacyType != nil && local.legacyType.Type.String() == "#") {
				a.kind = "mask-bit"
				a.begin += dot + 1
				a.prefix = a.prefix[dot+1:]
				return a
			}
		}
	}
	frames := typeFrames(expr, rhs && d.language == "tl")
	if len(frames) > 0 {
		f := frames[len(frames)-1]
		a.apply = (f.kind == "(" || f.kind == "apply") && f.name == ""
	}
	for i := len(frames) - 1; i >= 0; i-- {
		f := frames[i]
		if f.kind == "[" && d.language != "tl" {
			a.kind = "nat"
			break
		}
		if f.name != "" {
			if f.kind != "<" && f.values == 0 && offset <= f.lastEnd {
				continue
			}
			call := f.call(offset)
			a.call = &call
			break
		}
	}
	if len(expr) > 0 {
		a.bare = expr[len(expr)-1].text == "%"
		last := expr[len(expr)-1]
		complete := nameByte(last.text[len(last.text)-1]) || last.text == ">" || d.language == "tl" && (last.text == ")" || last.text == "]" || last.text == "#")
		if len(frames) == 0 && last.end < limit && complete {
			if colon >= 0 {
				a.kind = "field"
			} // a complete field type followed by whitespace
			if a.root.kind == 11 {
				a.kind = "none"
			}
		}
	}
	return a
}

type typeCall struct {
	name     string
	argument int
}
type typeFrame struct {
	kind, name                string
	argument, values, lastEnd int
	last                      string
}

func (f typeFrame) call(offset int) typeCall {
	arg := f.argument
	if f.kind != "<" {
		arg = f.values
		if f.values > 0 && (offset <= f.lastEnd || f.last == "+" || f.last == "%") {
			arg--
		}
	}
	return typeCall{f.name, arg}
}

// Commas separate angle arguments, while TL1 applications use whitespace.
// Every nested bracket consumes one parent argument, irrespective of the
// number of tokens inside it. Closed calls no longer provide signature help.
func typeFrames(tokens []assistToken, apply bool) []typeFrame {
	var stack []typeFrame
	if apply {
		stack = append(stack, typeFrame{kind: "apply"})
	}
	previous := ""
	for _, tok := range tokens {
		t := tok.text
		if t == "<" || t == "(" || t == "[" {
			if len(stack) > 0 && t != "<" {
				f := &stack[len(stack)-1]
				if f.kind == "(" || f.kind == "apply" {
					f.values++
					f.lastEnd = tok.end
				}
			}
			frame := typeFrame{kind: t}
			if t == "<" {
				frame.name = previous
			}
			stack = append(stack, frame)
		} else if t == ">" || t == ")" || t == "]" {
			want := map[string]string{">": "<", ")": "(", "]": "["}[t]
			for len(stack) > 0 {
				last := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if last.kind == want {
					if want == "<" && len(stack) > 0 {
						parent := &stack[len(stack)-1]
						if parent.values == 0 && parent.name == last.name {
							parent.name = ""
						}
					}
					break
				}
			}
			if len(stack) > 0 {
				stack[len(stack)-1].lastEnd = tok.end
			}
		} else if len(stack) > 0 {
			f := &stack[len(stack)-1]
			if t == "," && f.kind == "<" {
				f.argument++
			} else if t == "%" && (f.kind == "(" || f.kind == "apply") && f.name != "" {
				f.values++
				f.lastEnd = tok.end
			} else if (f.kind == "(" || f.kind == "apply") && len(t) > 0 && nameByte(t[0]) {
				if f.name == "" {
					f.name = t
					f.lastEnd = tok.end
				} else if f.last != "+" && f.last != "%" {
					f.values++
					f.lastEnd = tok.end
				} else {
					f.lastEnd = tok.end
				}
			}
			f.last = t
		}
		previous = t
	}
	return stack
}

func (s *server) callDefinitions(call *typeCall) []definition {
	if call == nil {
		return nil
	}
	var out []definition
	for _, def := range s.definitions(call.name) {
		if len(def.sym.params) > 0 {
			out = append(out, def)
		}
	}
	return out
}

func (s *server) signatures(d *document, offset int) *signatureHelp {
	a := d.assistance(offset, false)
	if a.call != nil && a.locals[a.call.name] != nil {
		return nil
	}
	defs := s.callDefinitions(a.call)
	if len(defs) == 0 {
		return nil
	}
	out := &signatureHelp{Signatures: []signatureInformation{}}
	seen := map[string]bool{}
	for _, def := range defs {
		sym := def.sym
		info := signatureInformation{Parameters: []parameterInformation{}, ActiveParameter: min(a.call.argument, len(sym.params)-1)}
		var labels []string
		for _, p := range sym.params {
			category := "Type"
			if p.nat {
				category = "#"
			}
			label := p.name + ": " + category
			labels = append(labels, label)
			info.Parameters = append(info.Parameters, parameterInformation{label})
		}
		info.Label = sym.name + "<" + strings.Join(labels, ", ") + ">"
		if seen[info.Label] {
			continue
		}
		seen[info.Label] = true
		if sym.comment != "" {
			info.Documentation = &markup{"plaintext", sym.comment}
		}
		out.Signatures = append(out.Signatures, info)
	}
	out.ActiveParameter = out.Signatures[0].ActiveParameter
	return out
}
