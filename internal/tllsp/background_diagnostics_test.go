package tllsp

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBackgroundDiagnosticsCarryPendingConsumers(t *testing.T) {
	defs := parseDocument("file:///tmp/bg-types.tl2", "Size <=> uint32; Count <=> Size;", 1, true)
	use := parseDocument("file:///tmp/bg-use.tl2", "Use = n:Count values:[n]int32;", 1, true)
	s := editingServer(defs, use)
	old := s.index
	var jobs []*diagnosticJob
	s.queueDiagnostics = func(job *diagnosticJob) {
		if job != nil {
			jobs = append(jobs, job)
		}
	}
	s.setDocument(defs.uri, parseDocument(defs.uri, strings.Replace(defs.text, "uint32", "string", 1), 2, true))
	s.refreshIndex(false)
	if !s.pendingDiagnostics[use.uri] {
		t.Fatal("alias consumer was not scheduled")
	}
	obsolete := jobs[0].run()
	// A subsequent unrelated edit must not lose the pending alias consumer.
	other := parseDocument("file:///tmp/bg-other.tl2", "Other = x:int32;", 1, true)
	s.setDocument(other.uri, other)
	s.refreshIndex(false)
	if s.applyDiagnostics(obsolete) {
		t.Fatal("obsolete schema result applied")
	}
	if old.byName.get("Size")[0].sym.alias.SomeType.Name.String() != "uint32" {
		t.Fatal("old snapshot changed")
	}
	latest := jobs[len(jobs)-1]
	if !s.applyDiagnostics(latest.run()) || len(s.semantic[use.uri]) == 0 {
		t.Fatal("pending dependent errors were lost")
	}
	full := &server{docs: s.docs}
	full.rebuildDefinitions()
	full.rebuildSemanticDiagnostics()
	if !reflect.DeepEqual(s.semantic, full.semantic) {
		t.Fatalf("background differs from full validation: %+v vs %+v", s.semantic, full.semantic)
	}
	for _, job := range jobs {
		job.cancel()
	}
}

func TestBackgroundDiagnosticsRejectPendingTextAndCancellation(t *testing.T) {
	d := parseDocument("file:///tmp/bg-version.tl2", "Item = x:Missing;", 1, true)
	s := editingServer(d)
	s.pendingDiagnostics = map[string]bool{d.uri: true}
	job := s.newDiagnosticJob(s.pendingDiagnostics)
	result := job.run()
	s.pendingChanges = map[string]*document{d.uri: {version: 2}}
	if s.applyDiagnostics(result) {
		t.Fatal("result applied after receipt of newer text")
	}
	s.pendingChanges = nil
	job.cancel()
	if s.applyDiagnostics(result) || job.run() != nil {
		t.Fatal("cancelled checks survived")
	}
}

func TestProtocolNavigationContinuesDuringDiagnostics(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "types.tl2")
	uri := uriFromPath(path)
	text := "Size <=> uint32; Count <=> Size;"
	writeFile(t, path, text)
	writeFile(t, filepath.Join(root, "use.tl2"), "Use = n:Count values:[n]int32;")
	var paused atomic.Bool
	started := make(chan *diagnosticJob, 4)
	release := make(chan struct{})
	runner := func(job *diagnosticJob) *diagnosticResult {
		if paused.Load() {
			started <- job
			select {
			case <-release:
			case <-job.snapshot.ctx.Done():
			}
		}
		return job.run()
	}
	p := newBenchmarkPeerServing(func(in io.Reader, out, logs io.Writer) error {
		return runConnectionWithDiagnostics(in, out, logs, runner)
	})
	defer p.close()
	defer p.timeout.Stop()
	send := func(messages ...map[string]any) <-chan error { return p.send(benchmarkWire(t, messages...)) }
	read := func() message {
		body, err := readMessage(p.output)
		if err != nil {
			t.Fatal(err)
		}
		var msg message
		if err := json.Unmarshal(body, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.Method == "textDocument/publishDiagnostics" {
			var pub diagnosticPublication
			if err := json.Unmarshal(msg.Params, &pub); err != nil {
				t.Fatal(err)
			}
			if pub.Version != nil && *pub.Version == 2 || len(pub.Diagnostics) > 0 {
				t.Fatalf("obsolete errors published: %+v", pub)
			}
		}
		return msg
	}
	write := send(rpc("initialize", -1, map[string]any{"rootUri": uriFromPath(root)}))
	if msg := read(); string(msg.ID) != "-1" || msg.Error != nil {
		t.Fatalf("initialize: %+v", msg)
	}
	if err := <-write; err != nil {
		t.Fatal(err)
	}
	write = send(rpc("initialized", nil, map[string]any{}), openDoc(uri, text, 1))
	for {
		msg := read()
		if msg.Method == "textDocument/publishDiagnostics" {
			var pub diagnosticPublication
			json.Unmarshal(msg.Params, &pub)
			if pub.URI == uri && pub.Version != nil && *pub.Version == 1 {
				break
			}
		}
	}
	if err := <-write; err != nil {
		t.Fatal(err)
	}
	paused.Store(true)
	change := func(version int, value string) map[string]any {
		return rpc("textDocument/didChange", nil, map[string]any{"textDocument": map[string]any{"uri": uri, "version": version}, "contentChanges": []any{map[string]any{"text": strings.Replace(text, "uint32", value, 1)}}})
	}
	write = send(change(2, "string"), rpc("textDocument/hover", 2, docParams(uri, position{})))
	for {
		msg := read()
		if string(msg.ID) == "2" {
			if msg.Error != nil || !strings.Contains(string(msg.Result), "string") {
				t.Fatalf("old navigation result: %s", msg.Result)
			}
			break
		}
	}
	if err := <-write; err != nil {
		t.Fatal(err)
	}
	var obsolete *diagnosticJob
	select {
	case obsolete = <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("diagnostic worker did not start")
	}
	write = send(change(3, "uint32"), rpc("textDocument/completion", 3, docParams(uri, position{0, 10})))
	for {
		msg := read()
		if string(msg.ID) == "3" {
			if msg.Error != nil || len(msg.Result) == 0 {
				t.Fatalf("completion blocked: %+v", msg)
			}
			break
		}
	}
	if err := <-write; err != nil {
		t.Fatal(err)
	}
	select {
	case <-obsolete.snapshot.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("obsolete checks were not cancelled")
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("replacement checks were not scheduled")
	}
	close(release)
	for {
		msg := read()
		if msg.Method == "textDocument/publishDiagnostics" {
			var pub diagnosticPublication
			json.Unmarshal(msg.Params, &pub)
			if pub.URI == uri && pub.Version != nil && *pub.Version == 3 {
				break
			}
		}
	}
	write = send(rpc("shutdown", 9, nil), rpc("exit", nil, nil))
	for {
		if msg := read(); string(msg.ID) == "9" {
			break
		}
	}
	if err := <-write; err != nil {
		t.Fatal(err)
	}
	if err := <-p.done; err != nil {
		t.Fatal(err)
	}
}

func TestCodeActionsUseCurrentPendingDiagnostics(t *testing.T) {
	d := parseDocument("file:///tmp/bg-fix.tl2", "Item = x:int32;", 1, true)
	s := editingServer(d)
	s.codeActionLiterals = true
	s.queueDiagnostics = func(job *diagnosticJob) {
		if job != nil {
			defer job.cancel()
		}
	}
	current := parseDocument(d.uri, "Item = x:in32;", 2, true)
	s.setDocument(d.uri, current)
	s.refreshIndex(false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.ctx = ctx
	actions := s.codeActions(current, current.span(9, 9), nil, nil)
	if len(actions) == 0 || actions[0].Edit.Changes[d.uri][0].NewText != "int32" {
		t.Fatalf("pending error has no current fix: %+v", actions)
	}
}
