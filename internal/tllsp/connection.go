package tllsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"strings"
	"time"
)

const requestCancelled = -32800
const changeDelay = 25 * time.Millisecond
const maxChangeDelay = 100 * time.Millisecond
const maxPendingRequests = 32

type requestJob struct {
	msg      message
	snapshot server
	cancel   context.CancelFunc
}
type requestResult struct {
	job   *requestJob
	value any
	err   *rpcError
}
type incomingMessage struct {
	body []byte
	err  error
}

func (s *server) cancelled() bool { return s.ctx != nil && s.ctx.Err() != nil }

func backgroundRequest(method string) bool {
	switch method {
	case "textDocument/rename", "textDocument/references", "textDocument/codeAction", "textDocument/semanticTokens/full", "textDocument/documentSymbol", "workspace/symbol":
		return true
	}
	return false
}

func runConnection(in io.Reader, out, logs io.Writer) error {
	return runConnectionWithDiagnostics(in, out, logs, (*diagnosticJob).run)
}

func runConnectionWithDiagnostics(in io.Reader, out, logs io.Writer, runDiagnostics func(*diagnosticJob) *diagnosticResult) error {
	s := &server{out: out, log: log.New(logs, "tl2lsp: ", 0), docs: map[string]*document{}, deferChanges: true}
	s.options.Diagnostics.UnknownTypes = "warning"
	stop := make(chan struct{})
	incoming := make(chan incomingMessage, 64)
	jobs := make(chan *requestJob)
	completed := make(chan requestResult, 1)
	workerDone := make(chan struct{})
	diagnosticJobs := make(chan *diagnosticJob)
	diagnosticResults := make(chan *diagnosticResult, 1)
	diagnosticDone := make(chan struct{})
	var queuedDiagnostic, activeDiagnostic *diagnosticJob
	s.queueDiagnostics = func(job *diagnosticJob) {
		if activeDiagnostic != nil {
			activeDiagnostic.cancel()
		}
		if queuedDiagnostic != nil {
			queuedDiagnostic.cancel()
		}
		queuedDiagnostic = job
	}
	go func() {
		defer close(diagnosticDone)
		for {
			select {
			case <-stop:
				return
			case job := <-diagnosticJobs:
				result := runDiagnostics(job)
				select {
				case diagnosticResults <- result:
				case <-stop:
					return
				}
			}
		}
	}()
	go func() {
		defer close(workerDone)
		for {
			select {
			case <-stop:
				return
			case job := <-jobs:
				var result any
				var rpcErr *rpcError
				if !job.snapshot.cancelled() {
					result, rpcErr = job.snapshot.request(job.msg.Method, job.msg.Params)
				}
				if job.snapshot.cancelled() {
					result = nil
					rpcErr = &rpcError{requestCancelled, "Request cancelled"}
				}
				select {
				case completed <- requestResult{job, result, rpcErr}:
				case <-stop:
					return
				}
			}
		}
	}()
	readerOutput := incoming
	go func() {
		r := bufio.NewReader(in)
		for {
			body, err := readMessage(r)
			select {
			case readerOutput <- incomingMessage{body, err}:
			case <-stop:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	pending := map[string]*requestJob{}
	queue := []*requestJob{}
	defer func() {
		s.queueDiagnostics(nil)
		for _, job := range pending {
			job.cancel()
		}
		close(stop)
		if closer, ok := in.(io.Closer); ok {
			closer.Close()
		}
		<-workerDone
		<-diagnosticDone
	}()
	var deadline *time.Timer
	var tick <-chan time.Time
	var firstChange time.Time
	defer func() {
		if deadline != nil {
			deadline.Stop()
		}
	}()
	flush := func() error {
		if deadline != nil {
			deadline.Stop()
		}
		tick = nil
		firstChange = time.Time{}
		return s.flushChanges()
	}
	var finishErr error
	ending, active := false, false
	for {
		if ending && len(pending) == 0 {
			return finishErr
		}
		var dispatch chan *requestJob
		var next *requestJob
		if !active && len(queue) > 0 {
			dispatch = jobs
			next = queue[0]
		}
		var diagnosticDispatch chan *diagnosticJob
		if activeDiagnostic == nil && queuedDiagnostic != nil {
			diagnosticDispatch = diagnosticJobs
		}
		select {
		case diagnosticDispatch <- queuedDiagnostic:
			activeDiagnostic = queuedDiagnostic
			queuedDiagnostic = nil
		case result := <-diagnosticResults:
			completedDiagnostic := activeDiagnostic
			activeDiagnostic = nil
			if result != nil {
				// Inspect the result before cancelling its completed context.
				// Cancellation is used to discard superseded jobs.
				if s.applyDiagnostics(result) {
					if err := s.publishDiagnostics(); err != nil {
						return err
					}
				}
			}
			completedDiagnostic.cancel()
		case dispatch <- next:
			queue = queue[1:]
			active = true
		case result := <-completed:
			active = false
			job := result.job
			if job.snapshot.cancelled() {
				result.value = nil
				result.err = &rpcError{requestCancelled, "Request cancelled"}
			}
			if result.err == nil && job.msg.Method == "textDocument/rename" {
				if err := flush(); err != nil {
					return err
				}
			}
			// Closed documents have no versioned-edit protection. Do not return
			// a rename prepared against a workspace that has since changed.
			if result.err == nil && job.msg.Method == "textDocument/rename" && job.snapshot.generation != s.generation {
				result.value = nil
				result.err = &rpcError{requestFailed, "Schema changed while preparing rename; retry the request"}
			}
			if err := s.respond(job.msg.ID, result.value, result.err); err != nil {
				return err
			}
			delete(pending, requestIDKey(job.msg.ID))
			job.cancel()
		case <-tick:
			if err := flush(); err != nil {
				return err
			}
		case event := <-incoming:
			if event.err != nil {
				if err := flush(); err != nil {
					return err
				}
				if err := s.finishDiagnostics(); err != nil {
					return err
				}
				if !errors.Is(event.err, io.EOF) {
					finishErr = event.err
				}
				ending = true
				incoming = nil
				continue
			}
			var msg message
			if err := json.Unmarshal(event.body, &msg); err != nil {
				if err := s.respond(json.RawMessage("null"), nil, &rpcError{-32700, "invalid JSON"}); err != nil {
					return err
				}
				continue
			}
			if msg.JSONRPC == "2.0" && msg.Method == "" && len(msg.ID) > 0 && (len(msg.Result) > 0 || msg.Error != nil) {
				if msg.Error != nil {
					s.log.Printf("client request %s: %s", msg.ID, msg.Error.Message)
				}
				continue
			}
			if msg.JSONRPC != "2.0" || msg.Method == "" {
				if err := s.respond(json.RawMessage("null"), nil, &rpcError{-32600, "invalid request"}); err != nil {
					return err
				}
				continue
			}
			if msg.Method == "$/cancelRequest" && len(msg.ID) == 0 {
				var p struct {
					ID json.RawMessage `json:"id"`
				}
				if decodeParams(msg.Params, &p) == nil {
					if job := pending[requestIDKey(p.ID)]; job != nil {
						job.cancel()
					}
				}
				continue
			}
			if msg.Method != "textDocument/didChange" || len(msg.ID) > 0 {
				if err := flush(); err != nil {
					return err
				}
			}
			if msg.Method == "exit" && len(msg.ID) == 0 {
				if err := s.finishDiagnostics(); err != nil {
					return err
				}
				if !s.shutdown {
					finishErr = fmt.Errorf("exit before shutdown")
				}
				ending = true
				incoming = nil
				continue
			}
			if len(msg.ID) > 0 {
				if msg.Method == "shutdown" {
					if err := s.finishDiagnostics(); err != nil {
						return err
					}
				}
				if s.initialized && !s.shutdown && backgroundRequest(msg.Method) {
					if _, exists := pending[requestIDKey(msg.ID)]; exists {
						if err := s.respond(msg.ID, nil, &rpcError{-32600, "duplicate pending request ID"}); err != nil {
							return err
						}
						continue
					}
					if len(pending) >= maxPendingRequests {
						if err := s.respond(msg.ID, nil, &rpcError{requestFailed, "Too many pending requests; retry"}); err != nil {
							return err
						}
						continue
					}
					snapshot := *s
					snapshot.docs = maps.Clone(s.docs)
					snapshot.pendingChanges = nil
					ctx, cancel := context.WithCancel(context.Background())
					snapshot.ctx = ctx
					job := &requestJob{msg, snapshot, cancel}
					pending[requestIDKey(msg.ID)] = job
					queue = append(queue, job)
				} else {
					value, rpcErr := s.request(msg.Method, msg.Params)
					if err := s.respond(msg.ID, value, rpcErr); err != nil {
						return err
					}
				}
			} else {
				if err := s.notification(msg.Method, msg.Params); err != nil {
					return err
				}
				if len(s.pendingChanges) > 0 {
					s.queueDiagnostics(nil)
					now := time.Now()
					if firstChange.IsZero() {
						firstChange = now
					}
					delay := min(changeDelay, maxChangeDelay-now.Sub(firstChange))
					delay = max(0, delay)
					if deadline == nil {
						deadline = time.NewTimer(delay)
					} else {
						deadline.Reset(delay)
					}
					tick = deadline.C
				}
			}
		}
	}
}

// String IDs may use equivalent JSON escape spellings in the request and cancel.
// Keep strings distinct from numeric IDs, while replying with the original ID.
func requestIDKey(raw json.RawMessage) string {
	var id string
	if len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, &id) == nil {
		return "string:" + id
	}
	return "number:" + strings.TrimSpace(string(raw))
}
