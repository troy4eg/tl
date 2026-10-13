package tllsp

import (
	"context"
	"maps"

	"github.com/VKCOM/tl/internal/tlast"
)

type diagnosticJob struct {
	snapshot server
	affected map[string]bool
	cancel   context.CancelFunc
}

type diagnosticResult struct {
	job      *diagnosticJob
	index    *workspaceIndex
	semantic map[string][]diagnostic
}

func (s *server) newDiagnosticJob(affected map[string]bool) *diagnosticJob {
	snapshot := *s
	snapshot.docs = maps.Clone(s.docs)
	snapshot.semantic = map[string][]diagnostic{}
	snapshot.queueDiagnostics = nil
	ctx, cancel := context.WithCancel(context.Background())
	snapshot.ctx = ctx
	return &diagnosticJob{snapshot, affected, cancel}
}

func (job *diagnosticJob) run() *diagnosticResult {
	s := &job.snapshot
	if s.cancelled() {
		return nil
	}
	idx := s.index.fork()
	idx.checks = idx.checks.clone()
	idx.relatedUsers = idx.relatedUsers.clone()
	s.addSemanticDiagnostics(idx.validator.GlobalErrors())
	for uri := range job.affected {
		if s.cancelled() {
			return nil
		}
		for _, item := range idx.checks.get(uri) {
			if item.Related != nil {
				updateUsers(idx.relatedUsers, uriFromPath(item.Related.Begin.FileName()), uri, false)
			}
		}
		idx.checks.delete(uri)
		if d := s.docs[uri]; d != nil {
			checks := idx.validator.Validate(tlast.SchemaDeclarations{TL: d.tl, TL2: d.tl2})
			if s.cancelled() {
				return nil
			}
			if len(checks) > 0 {
				idx.checks.set(uri, checks)
			}
			for _, item := range checks {
				if item.Related != nil {
					updateUsers(idx.relatedUsers, uriFromPath(item.Related.Begin.FileName()), uri, true)
				}
			}
			s.addSemanticDiagnostics(checks)
		}
	}
	if s.cancelled() {
		return nil
	}
	return &diagnosticResult{job, idx, s.semantic}
}

func (s *server) applyDiagnostics(result *diagnosticResult) bool {
	if result == nil || result.job.snapshot.cancelled() || result.job.snapshot.generation != s.generation || len(s.pendingChanges) > 0 {
		return false
	}
	s.index = result.index
	s.semantic = maps.Clone(s.semantic)
	if s.semantic == nil {
		s.semantic = map[string][]diagnostic{}
	}
	for uri := range result.job.affected {
		delete(s.semantic, uri)
	}
	for uri, items := range result.semantic {
		s.semantic[uri] = items
	}
	s.pendingDiagnostics = nil
	s.markDiagnosticChanges(result.job.affected)
	return true
}

func (s *server) markDiagnosticChanges(affected map[string]bool) {
	if s.diagnosticChanges == nil {
		s.diagnosticChanges = map[string]bool{}
	}
	for uri := range affected {
		s.diagnosticChanges[uri] = true
	}
}

// Drain on shutdown/EOF so finite sessions still receive final diagnostics.
// Normal requests never wait for dependent-document validation.
func (s *server) finishDiagnostics() error {
	if len(s.pendingDiagnostics) == 0 {
		return nil
	}
	if s.queueDiagnostics != nil {
		s.queueDiagnostics(nil)
	}
	job := s.newDiagnosticJob(s.pendingDiagnostics)
	defer job.cancel()
	s.applyDiagnostics(job.run())
	return s.publishDiagnostics()
}
