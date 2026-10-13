package tllsp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Include delivery of the changed document and every dependent diagnostic.
// completion-ns/op separately measures navigation latency during the check.
func BenchmarkLSPDiagnosticsProtocol(b *testing.B) {
	for _, size := range []int{1000, 10000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			f := newBenchmarkFixture(b, size)
			uri := f.uri(0)
			text := f.files[0].text
			changed := strings.Replace(text, "<=> uint32", "<=> string", 1)
			probe, _ := f.server()
			probe.docs[uri] = parseDocument(uri, changed, 2, true)
			probe.rebuildDefinitions()
			expected := map[string]bool{}
			for _, doc := range probe.documents() {
				if len(probe.diagnostics(doc)) > 0 {
					expected[doc.uri] = true
				}
			}
			if len(expected) == 0 {
				b.Fatal("missing dependent diagnostics in fixture")
			}
			initialize := benchmarkWire(b, rpc("initialize", -1, map[string]any{"rootUri": uriFromPath(f.root)}))
			ready := benchmarkWire(b, rpc("initialized", nil, map[string]any{}), openDoc(uri, text, 1), rpc("textDocument/hover", 0, docParams(uri, position{})))
			pos := newSource(text).position(strings.LastIndex(text, "previous:bench.Record") + len("previous:bench.Record00000"))
			wire := benchmarkWire(b,
				rpc("textDocument/didChange", nil, map[string]any{"textDocument": map[string]any{"uri": uri, "version": 2}, "contentChanges": []any{map[string]any{"text": changed}}}),
				rpc("textDocument/completion", 2, docParams(uri, pos)))
			shutdown := benchmarkWire(b, rpc("shutdown", 9, nil), rpc("exit", nil, nil))
			var active *benchmarkPeer
			b.Cleanup(func() {
				if active != nil {
					active.close()
					active.timeout.Stop()
				}
			})
			var completionTime time.Duration
			b.ReportAllocs()
			for b.Loop() {
				b.StopTimer()
				active = newBenchmarkPeer()
				write := active.send(initialize)
				if msg := active.response(b); string(msg.ID) != "-1" || msg.Error != nil {
					b.Fatalf("initialize: %+v", msg)
				}
				if err := <-write; err != nil {
					b.Fatal(err)
				}
				write = active.send(ready)
				readyResponse, readyDiagnostics := false, false
				for !readyResponse || !readyDiagnostics {
					body, err := readMessage(active.output)
					if err != nil {
						b.Fatal(err)
					}
					var msg message
					if err := json.Unmarshal(body, &msg); err != nil {
						b.Fatal(err)
					}
					if len(msg.ID) > 0 {
						if readyResponse || string(msg.ID) != "0" || msg.Error != nil {
							b.Fatalf("ready: %+v", msg)
						}
						readyResponse = true
					} else if msg.Method == "textDocument/publishDiagnostics" {
						var p diagnosticPublication
						if err := json.Unmarshal(msg.Params, &p); err != nil {
							b.Fatal(err)
						}
						if p.URI == uri && p.Version != nil && *p.Version == 1 {
							readyDiagnostics = true
						}
					}
				}
				if err := <-write; err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				start := time.Now()
				write = active.send(wire)
				seen := map[string]bool{}
				completed := false
				for !completed || len(seen) != len(expected) {
					body, err := readMessage(active.output)
					if err != nil {
						b.Fatal(err)
					}
					var msg message
					if err := json.Unmarshal(body, &msg); err != nil {
						b.Fatal(err)
					}
					if len(msg.ID) > 0 {
						if completed || string(msg.ID) != "2" || msg.Error != nil {
							b.Fatalf("completion: %+v", msg)
						}
						var items []completionItem
						if err := json.Unmarshal(msg.Result, &items); err != nil || len(items) == 0 || len(items) > 10 {
							b.Fatalf("prefix completion: %s", msg.Result)
						}
						completionTime += time.Since(start)
						completed = true
					} else if msg.Method == "textDocument/publishDiagnostics" {
						var p diagnosticPublication
						if err := json.Unmarshal(msg.Params, &p); err != nil {
							b.Fatal(err)
						}
						if !expected[p.URI] || seen[p.URI] || len(p.Diagnostics) == 0 {
							b.Fatalf("missing or repeated dependent errors: %+v", p)
						}
						if p.URI == uri && (p.Version == nil || *p.Version != 2) {
							b.Fatalf("obsolete version: %+v", p)
						}
						seen[p.URI] = true
					}
				}
				if err := <-write; err != nil {
					b.Fatal(err)
				}
				b.StopTimer()
				write = active.send(shutdown)
				if msg := active.response(b); string(msg.ID) != "9" || msg.Error != nil {
					b.Fatalf("shutdown: %+v", msg)
				}
				if err := <-write; err != nil {
					b.Fatal(err)
				}
				if err := <-active.done; err != nil {
					b.Fatal(err)
				}
				active.timeout.Stop()
				active.close()
				active = nil
				b.StartTimer()
			}
			b.ReportMetric(float64(completionTime.Nanoseconds())/float64(b.N), "completion-ns/op")
			b.ReportMetric(float64(len(expected)), "diagnostic-files/op")
		})
	}
}
