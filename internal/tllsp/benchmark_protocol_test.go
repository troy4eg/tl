package tllsp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"
)

// Use Run over pipes so a future concurrent scheduler/cancellation implementation
// can improve these measurements. Calling handlers serially would bake the
// current head-of-line blocking into the benchmark itself.
type benchmarkPeer struct {
	input     *io.PipeWriter
	output    *bufio.Reader
	inReader  *io.PipeReader
	outReader *io.PipeReader
	done      chan error
	timeout   *time.Timer
}

func newBenchmarkPeer() *benchmarkPeer {
	inReader, input := io.Pipe()
	outReader, output := io.Pipe()
	p := &benchmarkPeer{input: input, output: bufio.NewReader(outReader), inReader: inReader, outReader: outReader, done: make(chan error, 1)}
	p.timeout = time.AfterFunc(30*time.Second, p.close)
	go func() {
		err := Run(inReader, output, io.Discard)
		inReader.CloseWithError(err)
		output.CloseWithError(err)
		p.done <- err
	}()
	return p
}

func (p *benchmarkPeer) close() {
	p.input.Close()
	p.inReader.Close()
	p.outReader.Close()
}

func benchmarkWire(tb testing.TB, messages ...map[string]any) []byte {
	tb.Helper()
	var wire bytes.Buffer
	for _, msg := range messages {
		if err := writeMessage(&wire, msg); err != nil {
			tb.Fatal(err)
		}
	}
	return wire.Bytes()
}

func (p *benchmarkPeer) send(wire []byte) <-chan error {
	done := make(chan error, 1)
	go func() { _, err := p.input.Write(wire); done <- err }()
	return done
}

func (p *benchmarkPeer) response(tb testing.TB) message {
	tb.Helper()
	for {
		body, err := readMessage(p.output)
		if err != nil {
			tb.Fatal(err)
		}
		var msg message
		if err := json.Unmarshal(body, &msg); err != nil {
			tb.Fatal(err)
		}
		if len(msg.ID) > 0 {
			return msg
		}
	}
}

func BenchmarkLSPProtocol(b *testing.B) {
	for _, declarations := range []int{1000, 10000} {
		b.Run(fmt.Sprintf("%d", declarations), func(b *testing.B) {
			f := newBenchmarkFixture(b, declarations)
			shared := f.base.definitions("bench.Shared")[0]
			d := f.base.docs[f.uri(0)]
			completion := rpc("textDocument/completion", 2, docParams(d.uri, d.position(bytes.LastIndex([]byte(d.text), []byte("shared:bench.Shared"))+len("shared:"))))
			initialize := benchmarkWire(b, rpc("initialize", -1, map[string]any{"rootUri": uriFromPath(f.root), "capabilities": map[string]any{
				"workspace":    map[string]any{"workspaceEdit": map[string]any{"documentChanges": true}},
				"textDocument": map[string]any{"completion": map[string]any{"completionItem": map[string]any{"snippetSupport": true}}},
			}}))
			ready := benchmarkWire(b, rpc("initialized", nil, map[string]any{}), rpc("textDocument/hover", 0, docParams(d.uri, d.position(shared.sym.nameBegin))))
			shutdown := benchmarkWire(b, rpc("shutdown", 9, nil), rpc("exit", nil, nil))
			for _, scenario := range []string{"CompletionAlone", "ReferencesThenCompletion", "RenameThenCompletion", "CancelRenameThenCompletion"} {
				b.Run(scenario, func(b *testing.B) {
					messages := []map[string]any{}
					if scenario != "CompletionAlone" {
						params := docParams(shared.doc.uri, shared.doc.position(shared.sym.nameBegin))
						method := "textDocument/references"
						params["context"] = map[string]any{"includeDeclaration": true}
						if scenario != "ReferencesThenCompletion" {
							method = "textDocument/rename"
							params["newName"] = "bench.Renamed"
						}
						messages = append(messages, rpc(method, 1, params))
						if scenario == "CancelRenameThenCompletion" {
							messages = append(messages, rpc("$/cancelRequest", nil, map[string]any{"id": 1}))
						}
					}
					messages = append(messages, completion)
					wire := benchmarkWire(b, messages...)
					var active *benchmarkPeer
					b.Cleanup(func() {
						if active != nil {
							active.close()
							active.timeout.Stop()
						}
					})
					var completionTime, heavyTime time.Duration
					cancelled := 0
					b.ReportAllocs()
					for b.Loop() {
						b.StopTimer()
						active = newBenchmarkPeer()
						write := active.send(initialize)
						if msg := active.response(b); string(msg.ID) != "-1" || msg.Error != nil {
							b.Fatalf("initialization failed: %+v", msg)
						}
						if err := <-write; err != nil {
							b.Fatal(err)
						}
						write = active.send(ready)
						if msg := active.response(b); string(msg.ID) != "0" || msg.Error != nil {
							b.Fatalf("ready failed: %+v", msg)
						}
						if err := <-write; err != nil {
							b.Fatal(err)
						}
						b.StartTimer()
						start := time.Now()
						write = active.send(wire)
						responses := 1
						if scenario != "CompletionAlone" {
							responses++
						}
						seen := map[string]bool{}
						for n := 0; n < responses; n++ {
							msg := active.response(b)
							elapsed := time.Since(start)
							id := string(msg.ID)
							if seen[id] {
								b.Fatal("duplicate response")
							}
							seen[id] = true
							if id == "2" {
								if msg.Error != nil || bytes.Equal(msg.Result, []byte("[]")) || len(msg.Result) == 0 {
									b.Fatalf("completion failed: %+v", msg)
								}
								completionTime += elapsed
							} else if id == "1" {
								heavyTime += elapsed
								if msg.Error != nil {
									if scenario != "CancelRenameThenCompletion" || msg.Error.Code != -32800 {
										b.Fatalf("heavy request failed: %+v", msg.Error)
									}
									cancelled++
								}
							} else {
								b.Fatalf("unexpected response %s", id)
							}
						}
						if !seen["2"] {
							b.Fatal("completion response missing")
						}
						if err := <-write; err != nil {
							b.Fatal(err)
						}
						b.StopTimer()
						write = active.send(shutdown)
						if msg := active.response(b); string(msg.ID) != "9" || msg.Error != nil {
							b.Fatalf("shutdown failed: %+v", msg)
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
					if scenario != "CompletionAlone" {
						b.ReportMetric(float64(heavyTime.Nanoseconds())/float64(b.N), "heavy-response-ns/op")
					}
					if scenario == "CancelRenameThenCompletion" {
						b.ReportMetric(float64(cancelled)/float64(b.N), "cancelled/op")
					}
				})
			}
		})
	}
}
