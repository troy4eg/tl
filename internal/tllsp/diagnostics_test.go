package tllsp

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

type diagnosticPublication struct {
	URI         string       `json:"uri"`
	Version     *int         `json:"version"`
	Diagnostics []diagnostic `json:"diagnostics"`
}

func publications(t *testing.T, results []map[string]json.RawMessage, uri string) []diagnosticPublication {
	t.Helper()
	var out []diagnosticPublication
	for _, item := range results {
		if string(item["method"]) != `"textDocument/publishDiagnostics"` {
			continue
		}
		var p diagnosticPublication
		if err := json.Unmarshal(item["params"], &p); err != nil {
			t.Fatal(err)
		}
		if p.URI == uri {
			out = append(out, p)
		}
	}
	return out
}

func TestSemanticDiagnosticsAcrossUnsavedAndClosedDocuments(t *testing.T) {
	root := t.TempDir()
	defURI := uriFromPath(filepath.Join(root, "типы с пробелом.tl2"))
	useURI := uriFromPath(filepath.Join(root, "use.tl2"))
	writeFile(t, filepath.Join(root, "типы с пробелом.tl2"), "Box<T:Type> = x:T;")
	use := "// 😀\r\nUse = value:Box<7>;"
	writeFile(t, filepath.Join(root, "use.tl2"), use)
	results := runDiagnosticSession(t, useURI,
		[]map[string]any{rpc("initialize", 0, map[string]any{"rootUri": uriFromPath(root), "capabilities": map[string]any{"textDocument": map[string]any{"publishDiagnostics": map[string]any{"relatedInformation": true}}}}), rpc("initialized", nil, map[string]any{})},
		[]map[string]any{openDoc(defURI, "Box<N:#> = values:[N]int32;", 3)},
		[]map[string]any{rpc("textDocument/didClose", nil, docParams(defURI, position{}))},
		[]map[string]any{openDoc(useURI, strings.Replace(use, "<7>", "<int32>", 1), 4)},
		[]map[string]any{rpc("textDocument/didClose", nil, docParams(useURI, position{}))},
	)
	items := publications(t, results, useURI)
	if len(items) != 5 {
		t.Fatalf("publications: %+v", items)
	}
	for _, i := range []int{0, 2, 4} {
		p := items[i]
		if p.Version != nil || len(p.Diagnostics) != 1 || p.Diagnostics[0].Code != "argument-category" || p.Diagnostics[0].Severity != 1 {
			t.Fatalf("closed consumer %d: %+v", i, p)
		}
		if p.Diagnostics[0].Range != newSource(use).span(strings.Index(use, "7"), strings.Index(use, "7")+1) {
			t.Fatalf("UTF16 range: %+v", p)
		}
		links := p.Diagnostics[0].RelatedInformation
		if len(links) != 1 || links[0].Location.URI != defURI {
			t.Fatalf("dependency link: %+v", p)
		}
	}
	if len(items[1].Diagnostics) != 0 || len(items[3].Diagnostics) != 0 || items[3].Version == nil || *items[3].Version != 4 {
		t.Fatalf("unsaved fixes: %+v", items)
	}
}

func TestDiagnosticsClearWhenSchemaPathsNarrow(t *testing.T) {
	root := t.TempDir()
	badURI := uriFromPath(filepath.Join(root, "bad.tl2"))
	keepURI := uriFromPath(filepath.Join(root, "keep.tl2"))
	writeFile(t, filepath.Join(root, "bad.tl2"), "Bad = x:Missing; broken = x:;")
	writeFile(t, filepath.Join(root, "keep.tl2"), "Keep = x:int32;")
	results := runSession(t,
		rpc("initialize", 0, map[string]any{"rootUri": uriFromPath(root)}),
		rpc("initialized", nil, map[string]any{}),
		rpc("workspace/didChangeConfiguration", nil, map[string]any{"settings": map[string]any{"tl2": map[string]any{"schemaPaths": []string{"keep.tl2"}}}}),
		rpc("shutdown", 1, nil), rpc("exit", nil, nil),
	)
	items := publications(t, results, badURI)
	if len(items) != 2 || len(items[0].Diagnostics) != 2 || len(items[1].Diagnostics) != 0 {
		t.Fatalf("stale diagnostics: %+v", items)
	}
	if items[0].Diagnostics[0].Code != "syntax" || items[0].Diagnostics[1].Code != "unknown-type" {
		t.Fatalf("recovery diagnostics: %+v", items)
	}
	if len(publications(t, results, keepURI)) != 2 {
		t.Fatal("configured document stopped being published")
	}
}

func TestUnknownTypesOffKeepsSemanticErrors(t *testing.T) {
	uri := "file:///tmp/semantic-settings.tl2"
	results := runSession(t,
		rpc("initialize", 0, map[string]any{"initializationOptions": map[string]any{"diagnostics": map[string]any{"unknownTypes": "off"}}}),
		openDoc(uri, "Box<T:Type> = x:T; Item = a:Box<7> b:Missing;", 1),
		rpc("shutdown", 1, nil), rpc("exit", nil, nil),
	)
	items := publications(t, results, uri)
	if len(items) != 1 || len(items[0].Diagnostics) != 1 || items[0].Diagnostics[0].Code != "argument-category" {
		t.Fatalf("semantic error hidden: %+v", items)
	}
	if len(items[0].Diagnostics[0].RelatedInformation) != 0 {
		t.Fatal("sent related information to unsupported client")
	}
}

func TestTLUnknownTypePointsAtName(t *testing.T) {
	uri := "file:///tmp/semantic-range.tl"
	text := "// 😀\r\nitem value:(Missing int) = Item;"
	results := runSession(t,
		rpc("initialize", 0, map[string]any{}), rpc("textDocument/didOpen", nil, map[string]any{"textDocument": map[string]any{"uri": uri, "languageId": "tl", "version": 1, "text": text}}),
		rpc("shutdown", 1, nil), rpc("exit", nil, nil),
	)
	items := publications(t, results, uri)
	if len(items) != 1 || len(items[0].Diagnostics) != 1 || items[0].Diagnostics[0].Range != newSource(text).span(strings.Index(text, "Missing"), strings.Index(text, "Missing")+7) {
		t.Fatalf("TL name range: %+v", items)
	}
}
