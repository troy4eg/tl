package tllsp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type options struct {
	SchemaPaths []string `json:"schemaPaths"`
	Diagnostics struct {
		UnknownTypes string `json:"unknownTypes"`
	} `json:"diagnostics"`
}

type server struct {
	ctx               context.Context
	generation        uint64
	deferChanges      bool
	pendingChanges    map[string]*document
	index             *workspaceIndex
	lastDiagnostics   map[string]diagnosticState
	dirtyDocuments    map[string]bool
	diagnosticChanges map[string]bool

	out                   io.Writer
	log                   *log.Logger
	docs                  map[string]*document
	byName                map[string][]definition
	roots                 []string
	options               options
	initialized, shutdown bool
	dynamicWatchers       bool
	documentChanges       bool
	semantic              map[string][]diagnostic
	published             map[string]bool
	relatedDiagnostics    bool
	snippetSupport        bool
	codeActionLiterals    bool
}

// Run serves one LSP connection. Only protocol messages are written to out;
// diagnostics about the server itself go to logs (normally stderr).
func Run(in io.Reader, out, logs io.Writer) error {
	return runConnection(in, out, logs)
}

func (s *server) respond(id json.RawMessage, result any, rpcErr *rpcError) error {
	response := map[string]any{"jsonrpc": "2.0", "id": id}
	if rpcErr != nil {
		response["error"] = rpcErr
	} else {
		response["result"] = result
	}
	return writeMessage(s.out, response)
}

func (s *server) request(method string, raw json.RawMessage) (any, *rpcError) {
	if method == "initialize" {
		if s.initialized {
			return nil, &rpcError{-32600, "already initialized"}
		}
		var p struct {
			RootURI          string `json:"rootUri"`
			RootPath         string `json:"rootPath"`
			WorkspaceFolders []struct {
				URI string `json:"uri"`
			} `json:"workspaceFolders"`
			InitializationOptions json.RawMessage `json:"initializationOptions"`
			Capabilities          struct {
				Workspace struct {
					WorkspaceEdit struct {
						DocumentChanges bool `json:"documentChanges"`
					} `json:"workspaceEdit"`
					DidChangeWatchedFiles struct {
						DynamicRegistration bool `json:"dynamicRegistration"`
					} `json:"didChangeWatchedFiles"`
				} `json:"workspace"`
				TextDocument struct {
					CodeAction struct {
						LiteralSupport *json.RawMessage `json:"codeActionLiteralSupport"`
					} `json:"codeAction"`
					Completion struct {
						CompletionItem struct {
							SnippetSupport bool `json:"snippetSupport"`
						} `json:"completionItem"`
					} `json:"completion"`
					PublishDiagnostics struct {
						RelatedInformation bool `json:"relatedInformation"`
					} `json:"publishDiagnostics"`
				} `json:"textDocument"`
			} `json:"capabilities"`
		}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		if len(p.InitializationOptions) > 0 && string(p.InitializationOptions) != "null" {
			if err := decodeParams(p.InitializationOptions, &s.options); err != nil {
				return nil, err
			}
		}
		for _, folder := range p.WorkspaceFolders {
			if path, err := pathFromURI(folder.URI); err == nil {
				s.roots = append(s.roots, path)
			}
		}
		if len(s.roots) == 0 && p.RootURI != "" {
			if path, err := pathFromURI(p.RootURI); err == nil {
				s.roots = append(s.roots, path)
			}
		}
		if len(s.roots) == 0 && filepath.IsAbs(p.RootPath) {
			s.roots = append(s.roots, p.RootPath)
		}
		s.dynamicWatchers = p.Capabilities.Workspace.DidChangeWatchedFiles.DynamicRegistration
		s.documentChanges = p.Capabilities.Workspace.WorkspaceEdit.DocumentChanges
		s.relatedDiagnostics = p.Capabilities.TextDocument.PublishDiagnostics.RelatedInformation
		s.snippetSupport = p.Capabilities.TextDocument.Completion.CompletionItem.SnippetSupport
		s.codeActionLiterals = p.Capabilities.TextDocument.CodeAction.LiteralSupport != nil
		var codeActionProvider any = false
		if s.codeActionLiterals {
			codeActionProvider = map[string]any{"codeActionKinds": []string{"quickfix"}}
		}
		s.reindex()
		s.initialized = true
		return map[string]any{
			"serverInfo": map[string]any{"name": "tl2lsp", "version": "0.6.0"},
			"capabilities": map[string]any{
				"positionEncoding":   "utf-16",
				"textDocumentSync":   map[string]any{"openClose": true, "change": 2, "save": map[string]any{"includeText": false}},
				"definitionProvider": true, "hoverProvider": true, "documentSymbolProvider": true,
				"referencesProvider": true, "renameProvider": map[string]any{"prepareProvider": true},
				"workspaceSymbolProvider": true,
				"completionProvider":      map[string]any{"triggerCharacters": []string{".", ":", "<", "[", ",", "?", "%"}},
				"signatureHelpProvider":   map[string]any{"triggerCharacters": []string{"<", ",", "(", " "}, "retriggerCharacters": []string{">", ")", "]"}},
				"codeActionProvider":      codeActionProvider,
				"semanticTokensProvider":  map[string]any{"legend": map[string]any{"tokenTypes": tokenTypes, "tokenModifiers": []string{}}, "full": true},
				"workspace":               map[string]any{"workspaceFolders": map[string]any{"supported": true, "changeNotifications": true}},
			},
		}, nil
	}
	if !s.initialized {
		return nil, &rpcError{-32002, "server not initialized"}
	}
	if s.shutdown {
		return nil, &rpcError{-32600, "server is shutting down"}
	}
	if method == "shutdown" {
		s.shutdown = true
		return nil, nil
	}
	if method == "workspace/symbol" {
		var p struct {
			Query string `json:"query"`
		}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		out := []any{}
		for _, d := range s.documents() {
			for _, sym := range d.symbols {
				if strings.Contains(strings.ToLower(sym.name), strings.ToLower(p.Query)) {
					out = append(out, map[string]any{"name": sym.name, "kind": sym.kind, "location": definition{d, sym}.location()})
				}
			}
		}
		return out, nil
	}
	switch method {
	case "textDocument/definition", "textDocument/hover", "textDocument/completion", "textDocument/signatureHelp", "textDocument/codeAction", "textDocument/documentSymbol", "textDocument/semanticTokens/full", "textDocument/references", "textDocument/prepareRename", "textDocument/rename":
	default:
		return nil, &rpcError{-32601, "method not found: " + method}
	}
	var p struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
		Position position `json:"position"`
		Range    span     `json:"range"`
		Context  struct {
			IncludeDeclaration bool         `json:"includeDeclaration"`
			Diagnostics        []diagnostic `json:"diagnostics"`
			Only               []string     `json:"only"`
		} `json:"context"`
		NewName string `json:"newName"`
	}
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	d := s.docs[canonicalURI(p.TextDocument.URI)]
	if d == nil {
		return nil, &rpcError{-32602, "document is not indexed"}
	}
	if method == "textDocument/documentSymbol" {
		return d.documentSymbols(), nil
	}
	if method == "textDocument/semanticTokens/full" {
		return map[string]any{"data": d.semanticTokens()}, nil
	}
	if method == "textDocument/codeAction" {
		begin, err := d.offset(p.Range.Start)
		if err != nil {
			return nil, &rpcError{-32602, err.Error()}
		}
		end, err := d.offset(p.Range.End)
		if err != nil || end < begin {
			return nil, &rpcError{-32602, "invalid code action range"}
		}
		if !s.codeActionLiterals {
			return []codeAction{}, nil
		}
		return s.codeActions(d, p.Range, p.Context.Diagnostics, p.Context.Only), nil
	}
	offset, err := d.offset(p.Position)
	if err != nil {
		return nil, &rpcError{-32602, err.Error()}
	}
	if method == "textDocument/completion" {
		return s.completion(d, offset), nil
	}
	if method == "textDocument/signatureHelp" {
		return s.signatures(d, offset), nil
	}
	if method == "textDocument/references" {
		return s.references(d, offset, p.Context.IncludeDeclaration), nil
	}
	if method == "textDocument/prepareRename" {
		target := s.targetAt(d, offset)
		if len(target.defs) == 0 {
			return nil, nil
		}
		if err := target.check(); err != nil {
			return nil, err
		}
		return map[string]any{"range": target.span, "placeholder": target.defs[0].sym.name}, nil
	}
	if method == "textDocument/rename" {
		return s.rename(d, offset, p.NewName)
	}
	defs, refRange := s.symbolAt(d, offset)
	if method == "textDocument/definition" {
		out := []location{}
		for _, def := range defs {
			out = append(out, def.location())
		}
		return out, nil
	}
	if len(defs) == 0 {
		return nil, nil
	}
	sym := defs[0].sym
	value := sym.detail()
	if sym.comment != "" {
		value += "\n\n" + sym.comment
	}
	return map[string]any{"contents": markup{"plaintext", value}, "range": refRange}, nil
}

type diagnosticState struct {
	version     int
	open        bool
	diagnostics []diagnostic
}

func (s *server) publishDiagnostics() error {
	changed := s.diagnosticChanges
	if s.lastDiagnostics == nil {
		changed = make(map[string]bool, len(s.docs)+len(s.published))
		for uri := range s.docs {
			changed[uri] = true
		}
		for uri := range s.published {
			changed[uri] = true
		}
	}
	if len(changed) == 0 {
		return nil
	}
	s.lastDiagnostics = maps.Clone(s.lastDiagnostics)
	if s.lastDiagnostics == nil {
		s.lastDiagnostics = map[string]diagnosticState{}
	}
	if s.published == nil {
		s.published = map[string]bool{}
	}
	uris := make([]string, 0, len(changed))
	for uri := range changed {
		uris = append(uris, uri)
	}
	sort.Strings(uris)
	for _, uri := range uris {
		d := s.docs[uri]
		if d == nil {
			if !s.published[uri] {
				continue
			}
			if err := writeMessage(s.out, map[string]any{"jsonrpc": "2.0", "method": "textDocument/publishDiagnostics", "params": map[string]any{"uri": uri, "diagnostics": []diagnostic{}}}); err != nil {
				return err
			}
			delete(s.published, uri)
			delete(s.lastDiagnostics, uri)
			continue
		}
		diagnostics := s.diagnostics(d)
		state := diagnosticState{d.version, d.open, diagnostics}
		if previous, ok := s.lastDiagnostics[uri]; ok && previous.version == state.version && previous.open == state.open && equalDiagnostics(previous.diagnostics, state.diagnostics) {
			continue
		}
		params := map[string]any{"uri": uri, "diagnostics": diagnostics}
		if d.open {
			params["version"] = d.version
		}
		if err := writeMessage(s.out, map[string]any{
			"jsonrpc": "2.0", "method": "textDocument/publishDiagnostics",
			"params": params,
		}); err != nil {
			return err
		}
		s.published[uri] = true
		s.lastDiagnostics[uri] = state
	}
	s.diagnosticChanges = nil
	return nil
}

func (s *server) notification(method string, raw json.RawMessage) error {
	if !s.initialized || s.shutdown {
		return nil
	}
	var err *rpcError
	switch method {
	case "initialized":
		if s.dynamicWatchers {
			if e := writeMessage(s.out, map[string]any{
				"jsonrpc": "2.0", "id": "tl2lsp-watchers", "method": "client/registerCapability",
				"params": map[string]any{"registrations": []any{map[string]any{
					"id": "tl2lsp-watchers", "method": "workspace/didChangeWatchedFiles",
					"registerOptions": map[string]any{"watchers": []any{
						map[string]any{"globPattern": "**/*.tl2", "kind": 7},
						map[string]any{"globPattern": "**/*.tl", "kind": 7},
					}},
				}}},
			}); e != nil {
				return e
			}
		}
		return s.publishDiagnostics()
	case "$/cancelRequest", "$/setTrace":
		return nil
	case "textDocument/didOpen":
		var p struct {
			TextDocument struct {
				URI      string `json:"uri"`
				Language string `json:"languageId"`
				Text     string `json:"text"`
				Version  int    `json:"version"`
			} `json:"textDocument"`
		}
		if err = decodeParams(raw, &p); err == nil {
			uri := canonicalURI(p.TextDocument.URI)
			s.setDocument(uri, parseDocumentLanguage(uri, p.TextDocument.Text, p.TextDocument.Version, true, p.TextDocument.Language))
		}
	case "textDocument/didChange":
		if err = s.changeDocument(raw); err == nil && s.deferChanges {
			return nil
		}
	case "textDocument/didClose":
		var p struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
		}
		if err = decodeParams(raw, &p); err == nil {
			uri := canonicalURI(p.TextDocument.URI)
			s.setDocument(uri, nil)
			s.refreshDiskDocument(uri)
		}
	case "textDocument/didSave":
		var p struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
		}
		if err = decodeParams(raw, &p); err == nil {
			s.refreshDiskDocument(p.TextDocument.URI)
		}
	case "workspace/didChangeWatchedFiles":
		var p struct {
			Changes []struct {
				URI  string `json:"uri"`
				Type int    `json:"type"`
			} `json:"changes"`
		}
		if err = decodeParams(raw, &p); err == nil {
			for _, change := range p.Changes {
				s.refreshDiskDocument(change.URI)
			}
		}
	case "workspace/didChangeConfiguration":
		var p struct {
			Settings struct {
				TL2 options `json:"tl2"`
			} `json:"settings"`
		}
		p.Settings.TL2 = s.options
		if err = decodeParams(raw, &p); err == nil {
			s.options = p.Settings.TL2
			s.lastDiagnostics = nil
			s.reindex()
		}
	case "workspace/didChangeWorkspaceFolders":
		var p struct {
			Event struct {
				Added []struct {
					URI string `json:"uri"`
				} `json:"added"`
				Removed []struct {
					URI string `json:"uri"`
				} `json:"removed"`
			} `json:"event"`
		}
		if err = decodeParams(raw, &p); err == nil {
			for _, f := range p.Event.Removed {
				path, _ := pathFromURI(f.URI)
				for i := len(s.roots) - 1; i >= 0; i-- {
					if s.roots[i] == path {
						s.roots = append(s.roots[:i], s.roots[i+1:]...)
					}
				}
			}
			for _, f := range p.Event.Added {
				if path, e := pathFromURI(f.URI); e == nil {
					s.roots = append(s.roots, path)
				}
			}
			s.reindex()
		}
	default:
		return nil
	}
	if err != nil {
		s.log.Print(err.Message)
		return nil
	}
	s.refreshIndex(false)
	return s.publishDiagnostics()
}

// Main is kept separate from Run so protocol integration tests use the same
// implementation without launching an editor or process.
func Main() {
	if err := Run(os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "tl2lsp:", err)
		os.Exit(1)
	}
}

func equalDiagnostics(a, b []diagnostic) bool {
	if len(a) != len(b) {
		return false
	}
	for i, left := range a {
		right := b[i]
		if left.Range != right.Range || left.Severity != right.Severity || left.Source != right.Source || left.Message != right.Message || left.Code != right.Code || len(left.RelatedInformation) != len(right.RelatedInformation) {
			return false
		}
		for j, related := range left.RelatedInformation {
			if related != right.RelatedInformation[j] {
				return false
			}
		}
	}
	return true
}
