package tllsp

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestBoundedCompletionRemainsReachableByPrefix(t *testing.T) {
	f := newBenchmarkFixture(t, 1000)
	s, _ := f.server()
	s.completionLimit = maxCompletionItems
	s.resolveCompletionDetail, s.resolveCompletionDocumentation = true, true
	d := s.docs[f.uri(0)]
	offset := strings.LastIndex(d.text, "shared:bench.Shared") + len("shared:")
	result, ok := s.complete(d, offset).(completionList)
	if !ok || !result.IsIncomplete || len(result.Items) != maxCompletionItems {
		t.Fatalf("unbounded completion: %+v", result)
	}
	if !hasCompletion(result.Items, "int32") {
		t.Fatal("built-ins were displaced by globals")
	}
	var box completionItem
	for _, item := range result.Items {
		if item.Data != nil && (item.Detail != "" || item.Documentation != nil) {
			t.Fatal("unresolved symbol was eagerly formatted")
		}
		if item.Label == "bench.Box" {
			box = item
		}
	}
	if box.Data == nil {
		t.Fatal("generic completion missing resolve data")
	}
	if len(s.definitions("bench.Box")) == 0 || s.definitions("bench.Box")[0].sym.signature != "" {
		t.Fatal("signature computed before resolve")
	}
	resolved := s.resolveCompletion(box)
	if !strings.Contains(resolved.Detail, "bench.Box<T:Type,N:#>") || !reflect.DeepEqual(resolved.TextEdit, box.TextEdit) || resolved.InsertTextFormat != box.InsertTextFormat || resolved.Label != box.Label {
		t.Fatalf("bad resolve: %+v", resolved)
	}
	late := parseDocument("file:///tmp/prefix-late.tl2", "D = value:bench.Record000999;", 1, true)
	s.setDocument(late.uri, late)
	s.refreshIndex(false)
	prefix := strings.Index(late.text, "bench.Record000999") + len("bench.Record000999")
	items, ok := s.complete(late, prefix).([]completionItem)
	if !ok || len(items) != 1 || items[0].Label != "bench.Record000999" {
		t.Fatalf("omitted name cannot be reached: %+v", items)
	}
	if got := s.resolveCompletion(box); !reflect.DeepEqual(got, box) {
		t.Fatal("stale completion resolved against a different generation")
	}
}

func TestCompletionResolveProtocolCapabilitiesAndEdits(t *testing.T) {
	uri := "file:///tmp/resolve.tl2"
	text := "// 😀 docs\nBox<T:Type> = value:T; Use = value:Box;"
	for _, properties := range [][]string{{"documentation"}, {"detail", "documentation"}, nil} {
		t.Run(fmt.Sprint(properties), func(t *testing.T) {
			p := newBenchmarkPeer()
			defer p.close()
			defer p.timeout.Stop()
			itemCaps := map[string]any{"snippetSupport": true}
			if properties != nil {
				itemCaps["resolveSupport"] = map[string]any{"properties": properties}
			}
			write := p.send(benchmarkWire(t, rpc("initialize", 0, map[string]any{"capabilities": map[string]any{"textDocument": map[string]any{"completion": map[string]any{"completionItem": itemCaps}}}})))
			init := p.response(t)
			if init.Error != nil || !strings.Contains(string(init.Result), `"resolveProvider":true`) {
				t.Fatalf("resolve not advertised: %s", init.Result)
			}
			if err := <-write; err != nil {
				t.Fatal(err)
			}
			write = p.send(benchmarkWire(t, openDoc(uri, text, 1), rpc("textDocument/completion", 1, docParams(uri, newSource(text).position(strings.LastIndex(text, "Box")+2)))))
			msg := p.response(t)
			var items []completionItem
			if msg.Error != nil || json.Unmarshal(msg.Result, &items) != nil || len(items) != 1 {
				t.Fatalf("completion: %+v", msg)
			}
			if err := <-write; err != nil {
				t.Fatal(err)
			}
			item := items[0]
			if item.InsertTextFormat != 2 || !strings.Contains(item.TextEdit.NewText, "${1:int32}") {
				t.Fatalf("generic insertion lost: %+v", item)
			}
			if properties == nil && (item.Documentation == nil || item.Detail == "") {
				t.Fatal("legacy client lost details")
			}
			if len(properties) == 1 && (item.Detail == "" || item.Documentation != nil) {
				t.Fatal("unsupported resolve property delayed")
			}
			write = p.send(benchmarkWire(t, rpc("completionItem/resolve", 2, item)))
			msg = p.response(t)
			var resolved completionItem
			if msg.Error != nil || json.Unmarshal(msg.Result, &resolved) != nil || resolved.Documentation == nil || !strings.Contains(resolved.Documentation.Value, "docs") || resolved.Detail == "" || !reflect.DeepEqual(resolved.TextEdit, item.TextEdit) {
				t.Fatalf("resolve: %+v", msg)
			}
			if err := <-write; err != nil {
				t.Fatal(err)
			}
			write = p.send(benchmarkWire(t, rpc("shutdown", 9, nil), rpc("exit", nil, nil)))
			if msg := p.response(t); string(msg.ID) != "9" || msg.Error != nil {
				t.Fatalf("shutdown: %+v", msg)
			}
			if err := <-write; err != nil {
				t.Fatal(err)
			}
			if err := <-p.done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func BenchmarkLSPCompletionBounded(b *testing.B) {
	for _, size := range []int{1000, 10000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			f := newBenchmarkFixture(b, size)
			s, _ := f.server()
			s.completionLimit = maxCompletionItems
			s.resolveCompletionDetail, s.resolveCompletionDocumentation = true, true
			d := s.docs[f.uri(0)]
			offset := strings.LastIndex(d.text, "shared:bench.Shared") + len("shared:")
			initial, ok := s.complete(d, offset).(completionList)
			if !ok || !initial.IsIncomplete || len(initial.Items) != maxCompletionItems {
				b.Fatal("bad bounded completion")
			}
			b.ReportAllocs()
			for b.Loop() {
				benchmarkResult = s.complete(d, offset)
			}
			b.ReportMetric(float64(len(initial.Items)), "items/op")
		})
	}
}
