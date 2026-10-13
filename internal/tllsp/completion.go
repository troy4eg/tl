package tllsp

import (
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"

	"github.com/VKCOM/tl/internal/tlast"
)

func (s *server) natField(d *document, sym *symbol) bool {
	if sym.kind != 8 || sym.optional {
		return false
	}
	if d.language == "tl" {
		return sym.legacyType != nil && sym.legacyType.Type.String() == "#"
	}
	return sym.valueType != nil && s.natType(*sym.valueType, nil, 0)
}

func (s *server) natType(ref tlast.TL2TypeRef, bindings map[string]tlast.TL2TypeRef, depth int) bool {
	if depth > 32 || ref.IsBracket() {
		return false
	}
	app := ref.SomeType
	name := app.Name.String()
	if arg, ok := bindings[name]; ok {
		return s.natType(arg, nil, depth+1)
	}
	if name == "uint32" {
		return len(app.Arguments) == 0
	}
	defs := s.definitions(name)
	if len(defs) != 1 || defs[0].sym.alias == nil {
		return false
	}
	sym := defs[0].sym
	if len(sym.params) != len(app.Arguments) {
		return false
	}
	next := map[string]tlast.TL2TypeRef{}
	for i, p := range sym.params {
		if !app.Arguments[i].IsNumber {
			next[p.name] = substituteBindings(app.Arguments[i].Type, bindings, 0)
		}
	}
	return s.natType(*sym.alias, next, depth+1)
}

func substituteBindings(ref tlast.TL2TypeRef, bindings map[string]tlast.TL2TypeRef, depth int) tlast.TL2TypeRef {
	if depth > 32 || ref.IsBracket() {
		return ref
	}
	if bound, ok := bindings[ref.SomeType.Name.String()]; ok {
		return bound
	}
	args := append([]tlast.TL2TypeArgument(nil), ref.SomeType.Arguments...)
	for i := range args {
		if !args[i].IsNumber {
			args[i].Type = substituteBindings(args[i].Type, bindings, depth+1)
		}
	}
	ref.SomeType.Arguments = args
	return ref
}

const maxCompletionItems = 256

// Internal spelling fixes and compatibility benchmarks need the complete set.
func (s *server) completion(d *document, offset int) []completionItem {
	items, _ := s.collectCompletion(d, offset, 0, false, false)
	return items
}

func (s *server) complete(d *document, offset int) any {
	items, incomplete := s.collectCompletion(d, offset, s.completionLimit, s.resolveCompletionDetail, s.resolveCompletionDocumentation)
	if incomplete {
		return completionList{true, items}
	}
	return items
}

func (s *server) collectCompletion(d *document, offset, limit int, resolveDetail, resolveDocumentation bool) ([]completionItem, bool) {
	a := d.assistance(offset, true)
	out := []completionItem{}
	if limit > 0 {
		out = make([]completionItem, 0, limit)
	}
	incomplete := false
	seen := map[string]bool{}
	editRange := d.span(a.begin, a.end)
	add := func(name string, kind int, detail, comment, insert string, snippet bool) *completionItem {
		if seen[name] || !strings.HasPrefix(name, a.prefix) {
			return nil
		}
		if limit > 0 && len(out) >= limit {
			incomplete = true
			return nil
		}
		seen[name] = true
		item := completionItem{Label: name, Kind: kind, Detail: detail, TextEdit: &textEdit{editRange, insert}}
		if snippet {
			item.InsertTextFormat = 2
		}
		if comment != "" {
			item.Documentation = &markup{"plaintext", comment}
		}
		out = append(out, item)
		return &out[len(out)-1]
	}
	addSymbol := func(def definition, kind int, insert string, snippet bool) {
		sym := def.sym
		if seen[sym.name] || !strings.HasPrefix(sym.name, a.prefix) {
			return
		}
		if limit > 0 && len(out) >= limit {
			incomplete = true
			return
		}
		detail, comment := "", ""
		if !resolveDetail {
			detail = sym.detail()
		}
		if !resolveDocumentation {
			comment = sym.comment
		}
		if item := add(sym.name, kind, detail, comment, insert, snippet); item != nil && (resolveDetail || resolveDocumentation) {
			item.Data = &completionData{s.generation, def.doc.uri, sym.nameBegin, sym.name}
		}
	}
	addSnippets := func() {
		if s.snippetSupport && !(d.language == "tl" && a.call != nil) {
			for _, snip := range completionSnippets(d.language, a.kind) {
				if a.kind == "type" && len(s.definitions(snip.label)) > 0 {
					continue
				}
				add(snip.label, 15, snip.detail, "", snip.text, true)
			}
		}
	}
	if a.kind == "none" {
		return out, false
	}
	if a.kind == "category" {
		add("Type", 14, "Type parameter category", "", "Type", false)
		add("#", 14, "Natural-number parameter category", "", "#", false)
	} else if a.kind == "mask-bit" {
		for i := 0; i < 32; i++ {
			name := fmt.Sprint(i)
			insert := name
			if a.end == len(d.text) || d.text[a.end] != '?' {
				insert += "?"
			}
			add(name, 12, "Mask bit", "", insert, false)
		}
	} else if a.kind == "type" || a.kind == "nat" {
		wantType, wantNat := a.kind == "type", a.kind == "nat"
		defs := s.callDefinitions(a.call)
		if a.call != nil && a.locals[a.call.name] != nil {
			defs = nil
		}
		if len(defs) > 0 {
			a.mask = false
			wantType, wantNat = false, false
			for _, def := range defs {
				if a.call.argument < len(def.sym.params) {
					if def.sym.params[a.call.argument].nat {
						wantNat = true
					} else {
						wantType = true
					}
				}
			}
		} else if a.call != nil {
			wantNat = true
			a.mask = false
		}
		if !wantType && wantNat {
			a.kind = "nat"
		}
		if !wantType && !wantNat {
			return out, false
		}
		localNames := make([]string, 0, len(a.locals))
		for name := range a.locals {
			localNames = append(localNames, name)
		}
		sort.Strings(localNames)
		for _, localName := range localNames {
			local := a.locals[localName]
			if local.kind == 26 && (local.nat && (wantNat || a.mask) || !local.nat && wantType) ||
				(wantNat || a.mask) && local.nameBegin < a.fieldBegin && s.natField(d, local) {
				kind := 25
				if local.kind == 8 {
					kind = 5
				}
				addSymbol(definition{d, local}, kind, local.name, false)
			}
		}
		if wantNat {
			add("0", 12, "Natural-number literal", "", "0", false)
		}
		if wantType {
			// Reserve once for broad results. Growing the item slice repeatedly
			// copied several MiB on every empty-prefix completion.
			if s.index != nil && limit == 0 {
				capacity := len(out) + len(d.primitives()) + 3
				s.index.byName.prefix(a.prefix, func(name string, defs []definition) {
					if name == "_" {
						return
					}
					for _, def := range defs {
						if !a.bare || def.sym.globalKind == "constructor" {
							capacity++
							break
						}
					}
				})
				if capacity > cap(out) {
					reserved := make([]completionItem, len(out), capacity)
					copy(reserved, out)
					out = reserved
				}
				if capacity > 32 {
					reserved := make(map[string]bool, capacity)
					for name, value := range seen {
						reserved[name] = value
					}
					seen = reserved
				}
			}
			for _, p := range d.primitives() {
				if p == "Type" || p == "#" && (!a.mask || a.call != nil) {
					continue
				}
				add(p, 14, "Built-in type", "", p, false)
			}
			addSnippets()
			next := nextAssistCharacter(d.text, a.end)
			existing := next == '<' || a.apply && (nameByte(next) || next == '%' || next == '(')
			addGlobal := func(def definition) bool {
				sym := def.sym
				if sym.kind == 12 || sym.name == "_" || a.bare && sym.globalKind != "constructor" || seen[sym.name] || !strings.HasPrefix(sym.name, a.prefix) {
					return true
				}
				if limit > 0 && len(out) >= limit {
					incomplete = true
					return false
				}
				insert, snippet := sym.name, false
				if s.snippetSupport && len(sym.params) > 0 && !a.resultDeclaration && !existing {
					var args []string
					for i, p := range sym.params {
						value := "int32"
						if d.language == "tl" {
							value = "int"
						}
						if p.nat {
							value = "0"
						}
						args = append(args, fmt.Sprintf("${%d:%s}", i+1, value))
					}
					if a.apply {
						insert += " " + strings.Join(args, " ") + "$0"
					} else {
						insert += "<" + strings.Join(args, ", ") + ">$0"
					}
					snippet = true
				}
				kind := 22
				if sym.kind == 10 {
					kind = 13
				}
				addSymbol(def, kind, insert, snippet)
				return true
			}
			if s.index != nil {
				s.index.byName.prefixWhile(a.prefix, func(_ string, defs []definition) bool {
					for _, def := range defs {
						if !addGlobal(def) {
							return false
						}
					}
					return true
				})
			} else {
				for _, doc := range s.documents() {
					for _, sym := range doc.exported {
						addGlobal(definition{doc, sym})
					}
				}
			}
		}
	}
	addSnippets()
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out, incomplete
}

func (s *server) resolveCompletion(item completionItem) completionItem {
	data := item.Data
	if data == nil || data.Generation != s.generation || data.Name != item.Label {
		return item
	}
	doc := s.docs[data.URI]
	if doc == nil {
		return item
	}
	walkSymbols(doc.symbols, func(sym *symbol) {
		if sym.nameBegin != data.Offset || sym.name != data.Name {
			return
		}
		if s.resolveCompletionDetail {
			item.Detail = sym.detail()
		}
		if s.resolveCompletionDocumentation && sym.comment != "" {
			item.Documentation = &markup{"plaintext", sym.comment}
		}
	})
	return item
}

func nextAssistCharacter(text string, offset int) byte {
	for offset < len(text) {
		if strings.ContainsRune(" \t\r\n", rune(text[offset])) {
			offset++
			continue
		}
		if strings.HasPrefix(text[offset:], "//") {
			for offset < len(text) && text[offset] != '\r' && text[offset] != '\n' {
				offset++
			}
			continue
		}
		return text[offset]
	}
	return 0
}

type completionSnippet struct{ label, detail, text string }

func completionSnippets(language, context string) []completionSnippet {
	primitive := "int32"
	if language == "tl" {
		primitive = "int"
	}
	field := "${1:value}:${2:" + primitive + "}$0"
	switch context {
	case "declaration":
		magic := rand.Uint32()
		if magic == 0 {
			magic = 1
		}
		if language == "tl" {
			return []completionSnippet{
				{"constructor", "TL constructor", "${1:item} ${2:value}:${3:int} = ${4:Item};$0"},
				{"generic", "TL generic constructor", "${1:box} {${2:T}:Type} ${3:value}:${2:T} = ${4:Box} ${2:T};$0"},
				{"function", "TL RPC function", fmt.Sprintf("${1:get}#%08x ${2:value}:${3:int} => ${4:int};$0", magic)},
			}
		}
		return []completionSnippet{
			{"struct", "TL2 struct", "${1:Record} = ${2:value}:${3:int32};$0"},
			{"generic", "TL2 generic struct", "${1:Box}<${2:T}:Type> = ${3:value}:${2:T};$0"},
			{"alias", "TL2 alias", "${1:Alias} <=> ${2:int32};$0"},
			{"union", "TL2 union", "${1:Choice} =\n    | ${2:first}\n    | ${3:second};$0"},
			{"function", "TL2 RPC function", fmt.Sprintf("${1:get}#%08x ${2:value}:${3:int32} => ${4:int32};$0", magic)},
		}
	case "field":
		return []completionSnippet{{"field", "Field declaration", field}}
	case "variant":
		return []completionSnippet{{"variant", "TL2 union variant", "${1:variant} ${2:value}:${3:int32}$0"}}
	case "type":
		if language == "tl" {
			return []completionSnippet{{"repeat", "TL repeated fields", "${1:count}*[${2:value}:${3:int}]$0"}, {"optional", "TL masked field", "${1:flags}.${2:0}?${3:int}$0"}}
		}
		return []completionSnippet{{"array", "TL2 array", "[]${1:int32}$0"}, {"fixed-array", "TL2 fixed-size array", "[${1:0}]${2:int32}$0"}}
	}
	return nil
}
