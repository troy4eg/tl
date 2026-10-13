package tllsp

import (
	"encoding/json"
	"io"
	"testing"
)

// Semantics tests wait for each publication before making the next edit.
// A batch-only session legitimately coalesces intermediate diagnostics now.
func runDiagnosticSession(t *testing.T, uri string, steps ...[]map[string]any) []map[string]json.RawMessage {
	t.Helper()
	p := newBenchmarkPeer()
	defer p.close()
	defer p.timeout.Stop()
	var results []map[string]json.RawMessage
	read := func() map[string]json.RawMessage {
		body, err := readMessage(p.output)
		if err != nil {
			t.Fatal(err)
		}
		var msg map[string]json.RawMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			t.Fatal(err)
		}
		results = append(results, msg)
		return msg
	}
	for _, messages := range steps {
		write := p.send(benchmarkWire(t, messages...))
		for {
			msg := read()
			if string(msg["method"]) == `"textDocument/publishDiagnostics"` {
				var pub diagnosticPublication
				if err := json.Unmarshal(msg["params"], &pub); err != nil {
					t.Fatal(err)
				}
				if pub.URI == uri {
					break
				}
			}
		}
		if err := <-write; err != nil {
			t.Fatal(err)
		}
	}
	write := p.send(benchmarkWire(t, rpc("shutdown", -999, nil), rpc("exit", nil, nil)))
	for {
		msg := read()
		if string(msg["id"]) == "-999" {
			break
		}
	}
	if err := <-write; err != nil {
		t.Fatal(err)
	}
	for {
		body, err := readMessage(p.output)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		var msg map[string]json.RawMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			t.Fatal(err)
		}
		results = append(results, msg)
	}
	if err := <-p.done; err != nil {
		t.Fatal(err)
	}
	return results
}
