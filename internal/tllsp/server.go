package tllsp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
)

type options struct {
	SchemaPaths []string `json:"schemaPaths"`
	Diagnostics struct {
		UnknownTypes string `json:"unknownTypes"`
	} `json:"diagnostics"`
}

type server struct {
	out                   io.Writer
	log                   *log.Logger
	docs                  map[string]*document
	byName                map[string][]definition
	roots                 []string
	options               options
	initialized, shutdown bool
	dynamicWatchers       bool
}

// Run serves one LSP connection. Only protocol messages are written to out;
// diagnostics about the server itself go to logs (normally stderr).
func Run(in io.Reader, out, logs io.Writer) error {
	s := &server{out: out, log: log.New(logs, "tl2lsp: ", 0), docs: map[string]*document{}}
	s.options.Diagnostics.UnknownTypes = "warning"
	r := bufio.NewReader(in)
	for {
		body, err := readMessage(r)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		var msg message
		if err := json.Unmarshal(body, &msg); err != nil {
			if err := s.respond(json.RawMessage("null"), nil, &rpcError{-32700, "invalid JSON"}); err != nil {
				return err
			}
			continue
		}
		// Responses to server-initiated registration requests must not receive
		// another response (which would cause a JSON-RPC error loop).
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
		if msg.Method == "exit" && len(msg.ID) == 0 {
			if !s.shutdown {
				return fmt.Errorf("exit before shutdown")
			}
			return nil
		}
		if len(msg.ID) != 0 {
			result, rpcErr := s.request(msg.Method, msg.Params)
			if err := s.respond(msg.ID, result, rpcErr); err != nil {
				return err
			}
		} else if err := s.notification(msg.Method, msg.Params); err != nil {
			return err
		}
	}
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
					DidChangeWatchedFiles struct {
						DynamicRegistration bool `json:"dynamicRegistration"`
					} `json:"didChangeWatchedFiles"`
				} `json:"workspace"`
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
		s.reindex()
		s.dynamicWatchers = p.Capabilities.Workspace.DidChangeWatchedFiles.DynamicRegistration
		s.initialized = true
		return map[string]any{
			"serverInfo": map[string]any{"name": "tl2lsp", "version": "0.2.0"},
			"capabilities": map[string]any{
				"positionEncoding":   "utf-16",
				"textDocumentSync":   map[string]any{"openClose": true, "change": 2, "save": map[string]any{"includeText": false}},
				"definitionProvider": true, "hoverProvider": true, "documentSymbolProvider": true,
				"workspaceSymbolProvider": true,
				"completionProvider":      map[string]any{"triggerCharacters": []string{".", ":", "<", "["}},
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
	case "textDocument/definition", "textDocument/hover", "textDocument/completion", "textDocument/documentSymbol", "textDocument/semanticTokens/full":
	default:
		return nil, &rpcError{-32601, "method not found: " + method}
	}
	var p struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
		Position position `json:"position"`
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
	offset, err := d.offset(p.Position)
	if err != nil {
		return nil, &rpcError{-32602, err.Error()}
	}
	if method == "textDocument/completion" {
		return s.completion(d, offset), nil
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
	value := sym.signature
	if sym.comment != "" {
		value += "\n\n" + sym.comment
	}
	return map[string]any{"contents": markup{"plaintext", value}, "range": refRange}, nil
}

func (s *server) publishDiagnostics() error {
	for _, d := range s.documents() {
		if !d.open {
			continue
		}
		if err := writeMessage(s.out, map[string]any{
			"jsonrpc": "2.0", "method": "textDocument/publishDiagnostics",
			"params": map[string]any{"uri": d.uri, "version": d.version, "diagnostics": s.diagnostics(d)},
		}); err != nil {
			return err
		}
	}
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
			return writeMessage(s.out, map[string]any{
				"jsonrpc": "2.0", "id": "tl2lsp-watchers", "method": "client/registerCapability",
				"params": map[string]any{"registrations": []any{map[string]any{
					"id": "tl2lsp-watchers", "method": "workspace/didChangeWatchedFiles",
					"registerOptions": map[string]any{"watchers": []any{
						map[string]any{"globPattern": "**/*.tl2", "kind": 7},
						map[string]any{"globPattern": "**/*.tl", "kind": 7},
					}},
				}}},
			})
		}
		return nil
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
			s.docs[uri] = parseDocumentLanguage(uri, p.TextDocument.Text, p.TextDocument.Version, true, p.TextDocument.Language)
		}
	case "textDocument/didChange":
		var p struct {
			TextDocument struct {
				URI     string `json:"uri"`
				Version int    `json:"version"`
			} `json:"textDocument"`
			ContentChanges []struct {
				Range *span  `json:"range"`
				Text  string `json:"text"`
			} `json:"contentChanges"`
		}
		if err = decodeParams(raw, &p); err == nil {
			uri := canonicalURI(p.TextDocument.URI)
			d := s.docs[uri]
			if d == nil || !d.open || p.TextDocument.Version <= d.version {
				return nil
			}
			text := d.text
			for _, change := range p.ContentChanges {
				if change.Range == nil {
					text = change.Text
					continue
				}
				src := newSource(text)
				begin, e1 := src.offset(change.Range.Start)
				end, e2 := src.offset(change.Range.End)
				if e1 != nil || e2 != nil || end < begin {
					s.log.Printf("ignored invalid edit for %s", uri)
					return nil
				}
				text = text[:begin] + change.Text + text[end:]
			}
			s.docs[uri] = parseDocumentLanguage(uri, text, p.TextDocument.Version, true, d.language)
		}
	case "textDocument/didClose":
		var p struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
		}
		if err = decodeParams(raw, &p); err == nil {
			uri := canonicalURI(p.TextDocument.URI)
			delete(s.docs, uri)
			s.reindex()
			if e := writeMessage(s.out, map[string]any{"jsonrpc": "2.0", "method": "textDocument/publishDiagnostics", "params": map[string]any{"uri": uri, "diagnostics": []diagnostic{}}}); e != nil {
				return e
			}
		}
	case "textDocument/didSave", "workspace/didChangeWatchedFiles":
		s.reindex()
	case "workspace/didChangeConfiguration":
		var p struct {
			Settings struct {
				TL2 options `json:"tl2"`
			} `json:"settings"`
		}
		p.Settings.TL2 = s.options
		if err = decodeParams(raw, &p); err == nil {
			s.options = p.Settings.TL2
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
	s.rebuildDefinitions()
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
