// Package tllsp implements an editor-independent TL2 language server.
package tllsp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

type position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type span struct {
	Start position `json:"start"`
	End   position `json:"end"`
}

type location struct {
	URI   string `json:"uri"`
	Range span   `json:"range"`
}

type diagnostic struct {
	Range              span                           `json:"range"`
	Severity           int                            `json:"severity"`
	Source             string                         `json:"source"`
	Message            string                         `json:"message"`
	Code               string                         `json:"code,omitempty"`
	RelatedInformation []diagnosticRelatedInformation `json:"relatedInformation,omitempty"`
}

type diagnosticRelatedInformation struct {
	Location location `json:"location"`
	Message  string   `json:"message"`
}

type codeAction struct {
	Title       string        `json:"title"`
	Kind        string        `json:"kind"`
	Diagnostics []diagnostic  `json:"diagnostics"`
	IsPreferred bool          `json:"isPreferred,omitempty"`
	Edit        workspaceEdit `json:"edit"`
}

type documentSymbol struct {
	Name           string           `json:"name"`
	Detail         string           `json:"detail,omitempty"`
	Kind           int              `json:"kind"`
	Range          span             `json:"range"`
	SelectionRange span             `json:"selectionRange"`
	Children       []documentSymbol `json:"children,omitempty"`
}

type markup struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type completionItem struct {
	Label            string    `json:"label"`
	Kind             int       `json:"kind"`
	Detail           string    `json:"detail,omitempty"`
	Documentation    *markup   `json:"documentation,omitempty"`
	TextEdit         *textEdit `json:"textEdit,omitempty"`
	InsertTextFormat int       `json:"insertTextFormat,omitempty"`
}

type parameterInformation struct {
	Label string `json:"label"`
}
type signatureInformation struct {
	Label           string                 `json:"label"`
	Documentation   *markup                `json:"documentation,omitempty"`
	Parameters      []parameterInformation `json:"parameters"`
	ActiveParameter int                    `json:"activeParameter"`
}
type signatureHelp struct {
	Signatures      []signatureInformation `json:"signatures"`
	ActiveSignature int                    `json:"activeSignature"`
	ActiveParameter int                    `json:"activeParameter"`
}

type textEdit struct {
	Range   span   `json:"range"`
	NewText string `json:"newText"`
}

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Bound individual messages so a corrupt header cannot exhaust server memory.
const maxMessageSize = 32 << 20

func readMessage(r *bufio.Reader) ([]byte, error) {
	length := -1
	for total := 0; ; {
		lineBytes, err := r.ReadSlice('\n')
		if err != nil {
			if errors.Is(err, io.EOF) && (total > 0 || len(lineBytes) > 0) {
				err = io.ErrUnexpectedEOF
			}
			return nil, err
		}
		line := string(lineBytes)
		total += len(line)
		if total > 8192 {
			return nil, fmt.Errorf("LSP header too large")
		}
		if line == "\r\n" {
			break
		}
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			return nil, fmt.Errorf("invalid LSP header")
		}
		if strings.EqualFold(key, "Content-Length") {
			if length >= 0 {
				return nil, fmt.Errorf("duplicate Content-Length")
			}
			length, err = strconv.Atoi(strings.TrimSpace(value))
			if err != nil || length < 0 || length > maxMessageSize {
				return nil, fmt.Errorf("invalid Content-Length")
			}
		}
	}
	if length < 0 {
		return nil, fmt.Errorf("missing Content-Length")
	}
	body := make([]byte, length)
	_, err := io.ReadFull(r, body)
	if errors.Is(err, io.EOF) {
		err = io.ErrUnexpectedEOF
	}
	return body, err
}

func writeMessage(w io.Writer, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

func decodeParams(raw json.RawMessage, dst any) *rpcError {
	if len(raw) == 0 || string(raw) == "null" {
		return &rpcError{-32602, "missing parameters"}
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return &rpcError{-32602, err.Error()}
	}
	return nil
}
