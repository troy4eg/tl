package tllsp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Keep this corpus stable when comparing server implementations. Each file has
// 100 declarations, with dense Shared/Box dependencies and a cross-file chain.
const benchmarkDeclarationsPerFile = 100

type benchmarkFile struct{ name, text, language string }

func benchmarkDeclaration(i int, language string) string {
	previous := "bench.Shared"
	if i > 4 {
		previous = fmt.Sprintf("bench.Record%06d", i-1)
	}
	if language == "tl" {
		return fmt.Sprintf("bench.record%06d#%08x flags:# value:flags.0?int shared:bench.Shared previous:%s boxed:bench.Box<bench.Shared,3> items:3*[value:long] = bench.Record%06d;\r\n", i, 0x40000000+i, previous, i)
	}
	return fmt.Sprintf("bench.Record%06d = id:int32 shared:bench.Shared previous:%s count:bench.Count boxed:bench.Box<bench.Shared,3> values:[count]int64;\r\n", i, previous)
}

func benchmarkCorpus(declarations int) []benchmarkFile {
	var files []benchmarkFile
	for start := 0; start < declarations; start += benchmarkDeclarationsPerFile {
		language := "tl2"
		if start/benchmarkDeclarationsPerFile%4 == 3 {
			language = "tl"
		}
		var text strings.Builder
		text.WriteString("// Benchmark corpus v1: Unicode 😀, CRLF\r\n")
		for i := start; i < min(start+benchmarkDeclarationsPerFile, declarations); i++ {
			switch i {
			case 0:
				text.WriteString("bench.Shared = id:int64;\r\n")
			case 1:
				text.WriteString("bench.Box<T:Type,N:#> = values:[N]T;\r\n")
			case 2:
				text.WriteString("bench.Size <=> uint32;\r\n")
			case 3:
				text.WriteString("bench.Count <=> bench.Size;\r\n")
			default:
				text.WriteString(benchmarkDeclaration(i, language))
			}
		}
		files = append(files, benchmarkFile{fmt.Sprintf("schema-%03d.%s", len(files), language), text.String(), language})
	}
	return files
}

type benchmarkFixture struct {
	root  string
	files []benchmarkFile
	base  *server
}

type benchmarkOutput struct{ bytes int64 }

func (w *benchmarkOutput) Write(p []byte) (int, error) {
	w.bytes += int64(len(p))
	return len(p), nil
}

func newBenchmarkFixture(tb testing.TB, declarations int) *benchmarkFixture {
	tb.Helper()
	f := &benchmarkFixture{root: tb.TempDir(), files: benchmarkCorpus(declarations)}
	s := &server{docs: map[string]*document{}, roots: []string{f.root}, out: io.Discard, log: log.New(io.Discard, "", 0), initialized: true, documentChanges: true, snippetSupport: true, codeActionLiterals: true, relatedDiagnostics: true}
	s.options.Diagnostics.UnknownTypes = "warning"
	for _, file := range f.files {
		path := filepath.Join(f.root, file.name)
		if err := os.WriteFile(path, []byte(file.text), 0600); err != nil {
			tb.Fatal(err)
		}
		uri := uriFromPath(path)
		d := parseDocumentLanguage(uri, file.text, 0, false, file.language)
		if len(d.errors) != 0 || len(d.symbols) != benchmarkDeclarationsPerFile {
			tb.Fatalf("invalid corpus %s: symbols=%d errors=%+v", file.name, len(d.symbols), d.errors)
		}
		s.docs[uri] = d
	}
	s.rebuildDefinitions()
	for _, d := range s.documents() {
		if diagnostics := s.diagnostics(d); len(diagnostics) != 0 {
			tb.Fatalf("invalid corpus %s: %+v", d.uri, diagnostics)
		}
	}
	f.base = s
	return f
}

func (f *benchmarkFixture) uri(index int) string {
	return uriFromPath(filepath.Join(f.root, f.files[index].name))
}

func (f *benchmarkFixture) server() (*server, *benchmarkOutput) {
	s := *f.base
	s.docs = make(map[string]*document, len(f.base.docs))
	for uri, d := range f.base.docs {
		s.docs[uri] = d
	}
	s.published = map[string]bool{}
	for uri := range s.docs {
		s.published[uri] = true
	}
	out := &benchmarkOutput{}
	s.out = out
	return &s, out
}

func benchmarkJSON(tb testing.TB, value any) json.RawMessage {
	tb.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		tb.Fatal(err)
	}
	return raw
}

func benchmarkRequest(tb testing.TB, s *server, method string, params json.RawMessage) any {
	tb.Helper()
	result, err := s.request(method, params)
	if err != nil {
		tb.Fatalf("%s: %+v", method, err)
	}
	return result
}

var benchmarkResult any

// BenchmarkLSP measures actual request/notification handlers. Corpus construction,
// initial indexing and client parameter encoding are excluded except in Start.
func BenchmarkLSP(b *testing.B) {
	for _, declarations := range []int{1000, 10000} {
		b.Run(fmt.Sprintf("%d", declarations), func(b *testing.B) {
			f := newBenchmarkFixture(b, declarations)
			b.Run("Start/Initialize", func(b *testing.B) {
				params := benchmarkJSON(b, map[string]any{"rootUri": uriFromPath(f.root)})
				b.ReportAllocs()
				for b.Loop() {
					s := &server{docs: map[string]*document{}, out: io.Discard, log: log.New(io.Discard, "", 0)}
					benchmarkResult = benchmarkRequest(b, s, "initialize", params)
				}
			})
			b.Run("Start/Session", func(b *testing.B) {
				var wire bytes.Buffer
				for _, msg := range []any{rpc("initialize", 0, map[string]any{"rootUri": uriFromPath(f.root)}), rpc("initialized", nil, map[string]any{}), rpc("shutdown", 1, nil), rpc("exit", nil, nil)} {
					if err := writeMessage(&wire, msg); err != nil {
						b.Fatal(err)
					}
				}
				out := &benchmarkOutput{}
				b.ReportAllocs()
				for b.Loop() {
					if err := Run(bytes.NewReader(wire.Bytes()), out, io.Discard); err != nil {
						b.Fatal(err)
					}
				}
				b.ReportMetric(float64(out.bytes)/float64(b.N), "output-B/op")
			})
			for _, language := range []string{"tl2", "tl"} {
				index := 0
				if language == "tl" {
					index = 3
				}
				file := f.files[index]
				b.Run("Parse/"+language+"File", func(b *testing.B) {
					b.ReportAllocs()
					b.SetBytes(int64(len(file.text)))
					for b.Loop() {
						benchmarkResult = parseDocumentLanguage(f.uri(index), file.text, 1, true, language)
					}
				})
				var text strings.Builder
				for i := 4; i < declarations+4; i++ {
					text.WriteString(benchmarkDeclaration(i, language))
				}
				large := text.String()
				b.Run("Parse/"+language+"LargeFile", func(b *testing.B) {
					parsed := parseDocumentLanguage(f.uri(index), large, 1, true, language)
					if len(parsed.symbols) != declarations || len(parsed.errors) > 0 {
						b.Fatal("invalid large-file corpus")
					}
					b.ReportAllocs()
					b.SetBytes(int64(len(large)))
					for b.Loop() {
						benchmarkResult = parseDocumentLanguage(f.uri(index), large, 1, true, language)
					}
				})
			}
			for _, operation := range []string{"DefinitionsAndDiagnostics", "SemanticDiagnostics", "PublishDiagnostics", "ReindexUnchanged"} {
				b.Run("Index/"+operation, func(b *testing.B) {
					s, out := f.server()
					b.ReportAllocs()
					for b.Loop() {
						switch operation {
						case "DefinitionsAndDiagnostics":
							s.rebuildDefinitions()
						case "SemanticDiagnostics":
							s.rebuildSemanticDiagnostics()
						case "PublishDiagnostics":
							if err := s.publishDiagnostics(); err != nil {
								b.Fatal(err)
							}
						case "ReindexUnchanged":
							s.reindex()
						}
					}
					b.ReportMetric(float64(out.bytes)/float64(b.N), "output-B/op")
				})
			}
			benchmarkLSPRequests(b, f, declarations)
			benchmarkLSPChanges(b, f)
			benchmarkLSPFileEvents(b, f)
		})
	}
}

func benchmarkLSPRequests(b *testing.B, f *benchmarkFixture, declarations int) {
	for _, language := range []string{"tl2", "tl"} {
		index := 0
		if language == "tl" {
			index = 3
		}
		d := f.base.docs[f.uri(index)]
		at := func(fragment string, prefix int) position {
			return d.position(strings.LastIndex(d.text, fragment) + prefix)
		}
		cases := []struct {
			name, method string
			params       any
		}{
			{"Definition", "textDocument/definition", docParams(d.uri, at("shared:bench.Shared", len("shared:")))},
			{"Hover", "textDocument/hover", docParams(d.uri, at("shared:bench.Shared", len("shared:")))},
			{"CompletionBroad", "textDocument/completion", docParams(d.uri, at("shared:bench.Shared", len("shared:")))},
			{"CompletionPrefix", "textDocument/completion", docParams(d.uri, at("previous:bench.Record", len("previous:bench.Record00000")))},
			{"SignatureHelp", "textDocument/signatureHelp", docParams(d.uri, at("bench.Box<bench.Shared,3>", len("bench.Box<bench.Shared,")))},
			{"DocumentSymbols", "textDocument/documentSymbol", docParams(d.uri, position{})},
			{"SemanticTokens", "textDocument/semanticTokens/full", docParams(d.uri, position{})},
		}
		for _, tc := range cases {
			b.Run("Request/"+tc.name+"/"+language, func(b *testing.B) {
				s, _ := f.server()
				params := benchmarkJSON(b, tc.params)
				result := benchmarkRequest(b, s, tc.method, params)
				if result == nil {
					b.Fatalf("empty %s result", tc.name)
				}
				items := 0
				switch r := result.(type) {
				case []location:
					items = len(r)
				case []completionItem:
					items = len(r)
				case []documentSymbol:
					items = len(r)
				case *signatureHelp:
					if r == nil || len(r.Signatures) == 0 || r.ActiveParameter != 1 {
						b.Fatal("signature missing")
					}
					items = len(r.Signatures)
				case map[string]any:
					if tc.name == "SemanticTokens" && len(r["data"].([]int)) == 0 {
						b.Fatal("semantic tokens missing")
					}
				}
				if tc.name == "CompletionBroad" && items < declarations || tc.name == "CompletionPrefix" && (items == 0 || items > 10) || tc.name == "Definition" && items != 1 {
					b.Fatalf("%s returned %d items", tc.name, items)
				}
				b.ReportAllocs()
				for b.Loop() {
					benchmarkResult = benchmarkRequest(b, s, tc.method, params)
				}
				if items > 0 {
					b.ReportMetric(float64(items), "items/op")
				}
			})
		}
	}
	for _, target := range []string{"Shared", "Leaf"} {
		name := "bench.Shared"
		if target == "Leaf" {
			name = fmt.Sprintf("bench.Record%06d", declarations-1)
		}
		def := f.base.definitions(name)[0]
		for _, operation := range []string{"References", "Rename"} {
			b.Run("Request/"+operation+"/"+target, func(b *testing.B) {
				s, _ := f.server()
				params := docParams(def.doc.uri, def.doc.position(def.sym.nameBegin))
				method := "textDocument/references"
				params["context"] = map[string]any{"includeDeclaration": true}
				if operation == "Rename" {
					method = "textDocument/rename"
					params["newName"] = "bench.Renamed"
				}
				raw := benchmarkJSON(b, params)
				result := benchmarkRequest(b, s, method, raw)
				items := 0
				if refs, ok := result.([]location); ok {
					items = len(refs)
				}
				if edit, ok := result.(workspaceEdit); ok {
					for _, change := range edit.DocumentChanges {
						items += len(change.Edits)
					}
				}
				if target == "Leaf" && items != 1 || target == "Shared" && items < declarations {
					b.Fatalf("%s %s returned %d locations", operation, target, items)
				}
				b.ReportAllocs()
				for b.Loop() {
					benchmarkResult = benchmarkRequest(b, s, method, raw)
				}
				b.ReportMetric(float64(items), "items/op")
			})
		}
	}
	b.Run("Request/Rename/Local", func(b *testing.B) {
		s, _ := f.server()
		d := s.docs[f.uri(0)]
		params := docParams(d.uri, d.position(strings.Index(d.text, "T:Type")))
		params["newName"] = "U"
		raw := benchmarkJSON(b, params)
		if len(benchmarkRequest(b, s, "textDocument/rename", raw).(workspaceEdit).DocumentChanges) != 1 {
			b.Fatal("local rename escaped its file")
		}
		b.ReportAllocs()
		for b.Loop() {
			benchmarkResult = benchmarkRequest(b, s, "textDocument/rename", raw)
		}
	})
	for _, query := range []string{"Record", "Record00000"} {
		b.Run("Request/WorkspaceSymbols/"+query, func(b *testing.B) {
			s, _ := f.server()
			raw := benchmarkJSON(b, map[string]any{"query": query})
			items := len(benchmarkRequest(b, s, "workspace/symbol", raw).([]any))
			if items == 0 {
				b.Fatal("empty symbol result")
			}
			b.ReportAllocs()
			for b.Loop() {
				benchmarkResult = benchmarkRequest(b, s, "workspace/symbol", raw)
			}
			b.ReportMetric(float64(items), "items/op")
		})
	}
	for _, fix := range []string{"TypeTypo", "LegacyPrimitive", "Semicolon"} {
		b.Run("Request/QuickFix/"+fix, func(b *testing.B) {
			s, _ := f.server()
			text := f.files[0].text
			fragment := "shared:bench.Shared"
			prefix := len("shared:")
			switch fix {
			case "TypeTypo":
				text = strings.Replace(text, fragment, "shared:bench.Shraed", 1)
				fragment = "shared:bench.Shraed"
			case "LegacyPrimitive":
				fragment, prefix = "id:int;", len("id:")
				text = strings.Replace(text, "id:int64;", fragment, 1)
			case "Semicolon":
				fragment, prefix = "id:int64", len("id:int64")
				text = strings.Replace(text, fragment+";", fragment, 1)
			}
			d := parseDocument(f.uri(0), text, 1, true)
			s.docs[d.uri] = d
			s.rebuildDefinitions()
			pos := d.position(strings.Index(text, fragment) + prefix)
			raw := benchmarkJSON(b, map[string]any{"textDocument": map[string]any{"uri": d.uri}, "range": span{pos, pos}, "context": map[string]any{"diagnostics": []diagnostic{}, "only": []string{"quickfix"}}})
			items := len(benchmarkRequest(b, s, "textDocument/codeAction", raw).([]codeAction))
			if items == 0 {
				b.Fatalf("missing %s fix", fix)
			}
			b.ReportAllocs()
			for b.Loop() {
				benchmarkResult = benchmarkRequest(b, s, "textDocument/codeAction", raw)
			}
			b.ReportMetric(float64(items), "items/op")
		})
	}
}

func benchmarkLSPChanges(b *testing.B, f *benchmarkFixture) {
	for _, operation := range []string{"FieldType", "SharedAliasCategory", "TypingBurst"} {
		b.Run("Change/"+operation, func(b *testing.B) {
			s, out := f.server()
			d := *s.docs[f.uri(0)]
			d.open, d.version = true, 1
			s.docs[d.uri] = &d
			s.rebuildDefinitions()
			begin, old := strings.Index(d.text, "id:int32")+len("id:"), "int32"
			values := []string{"int64", "int32"}
			if operation == "SharedAliasCategory" {
				begin, old = strings.Index(d.text, "<=> uint32")+len("<=> "), "uint32"
				values = []string{"string", "uint32"}
			}
			if operation == "TypingBurst" {
				values = []string{"", "i", "in", "int", "int6", "int64", "int6", "int", "int3", "int32"}
			}
			version, iteration := 1, 0
			var completionTime time.Duration
			completion := benchmarkJSON(b, docParams(d.uri, d.position(strings.LastIndex(d.text, "shared:bench.Shared")+len("shared:"))))
			b.ReportAllocs()
			for b.Loop() {
				b.StopTimer()
				var payloads []json.RawMessage
				chosen := values[iteration%2 : iteration%2+1]
				if operation == "TypingBurst" {
					chosen = values
				}
				for _, value := range chosen {
					version++
					payloads = append(payloads, benchmarkJSON(b, map[string]any{"textDocument": map[string]any{"uri": d.uri, "version": version}, "contentChanges": []any{map[string]any{"range": d.span(begin, begin+len(old)), "text": value}}}))
					old = value
				}
				b.StartTimer()
				for _, raw := range payloads {
					if err := s.notification("textDocument/didChange", raw); err != nil {
						b.Fatal(err)
					}
				}
				if operation == "TypingBurst" {
					start := time.Now()
					benchmarkResult = benchmarkRequest(b, s, "textDocument/completion", completion)
					completionTime += time.Since(start)
				}
				iteration++
			}
			if s.docs[d.uri].version != version {
				b.Fatal("changes did not advance document version")
			}
			if current := s.docs[d.uri]; current.text[begin:begin+len(old)] != old || operation == "TypingBurst" && current.text != d.text {
				b.Fatal("change applied to the wrong fragment")
			}
			if operation == "SharedAliasCategory" && (len(s.diagnostics(s.docs[f.uri(1)])) > 0) != (old == "string") {
				b.Fatal("dependent diagnostics did not update")
			}
			b.ReportMetric(float64(out.bytes)/float64(b.N), "output-B/op")
			if operation == "TypingBurst" {
				b.ReportMetric(10, "changes/op")
				b.ReportMetric(float64(completionTime.Nanoseconds())/float64(b.N), "completion-ns/op")
			}
		})
	}
}

func benchmarkLSPFileEvents(b *testing.B, f *benchmarkFixture) {
	for _, operation := range []string{"SaveUnchanged", "Close", "WatchedUnchanged", "WatchedChanged", "WatchedDeleteCreate"} {
		b.Run("FileEvent/"+operation, func(b *testing.B) {
			s, out := f.server()
			index := len(f.files) - 1
			file, uri := f.files[index], f.uri(index)
			path := filepath.Join(f.root, file.name)
			defer func() {
				if err := os.WriteFile(path, []byte(file.text), 0600); err != nil {
					b.Fatal(err)
				}
			}()
			params := benchmarkJSON(b, docParams(uri, position{}))
			watch := func(kind int) json.RawMessage {
				return benchmarkJSON(b, map[string]any{"changes": []any{map[string]any{"uri": uri, "type": kind}}})
			}
			changed, deleted, created := watch(2), watch(3), watch(1)
			opened := *s.docs[uri]
			opened.open, opened.version = true, 1
			if operation == "SaveUnchanged" {
				s.docs[uri] = &opened
				s.rebuildDefinitions()
			}
			iteration := 0
			b.ReportAllocs()
			for b.Loop() {
				b.StopTimer()
				if operation == "Close" {
					s.docs[uri] = &opened
				}
				if operation == "WatchedChanged" {
					text := file.text
					if iteration%2 == 0 {
						text = strings.Replace(text, "value:flags.0?int", "value:flags.0?long", 1)
						text = strings.Replace(text, "id:int32", "id:int64", 1)
					}
					if err := os.WriteFile(path, []byte(text), 0600); err != nil {
						b.Fatal(err)
					}
				}
				if operation == "WatchedDeleteCreate" {
					if err := os.Remove(path); err != nil {
						b.Fatal(err)
					}
				}
				b.StartTimer()
				method, raw := "workspace/didChangeWatchedFiles", changed
				if operation == "SaveUnchanged" {
					method, raw = "textDocument/didSave", params
				}
				if operation == "Close" {
					method, raw = "textDocument/didClose", params
				}
				if operation == "WatchedDeleteCreate" {
					raw = deleted
				}
				if err := s.notification(method, raw); err != nil {
					b.Fatal(err)
				}
				if operation == "WatchedDeleteCreate" {
					b.StopTimer()
					if s.docs[uri] != nil {
						b.Fatal("deleted file remained indexed")
					}
					if err := os.WriteFile(path, []byte(file.text), 0600); err != nil {
						b.Fatal(err)
					}
					b.StartTimer()
					if err := s.notification("workspace/didChangeWatchedFiles", created); err != nil {
						b.Fatal(err)
					}
				}
				iteration++
			}
			if s.docs[uri] == nil || operation == "Close" && s.docs[uri].open {
				b.Fatal("file event was not applied")
			}
			b.ReportMetric(float64(out.bytes)/float64(b.N), "output-B/op")
			if operation == "WatchedDeleteCreate" {
				b.ReportMetric(2, "events/op")
			}
		})
	}
}

func TestLSPBenchmarkCorpus(t *testing.T) {
	f := newBenchmarkFixture(t, 1000)
	if len(f.files) != 10 || len(f.base.docs) != 10 {
		t.Fatal("unexpected corpus size")
	}
	shared := f.base.definitions("bench.Shared")[0]
	refs := f.base.references(shared.doc, shared.sym.nameBegin, true)
	if len(refs) < 1000 {
		t.Fatal("corpus lost shared dependencies")
	}
	for _, language := range []string{"tl", "tl2"} {
		found := false
		for _, file := range f.files {
			found = found || file.language == language
		}
		if !found {
			t.Fatalf("corpus lost %s coverage", language)
		}
	}
}
