// Package mcp serves Sapu's read-only tools to AI agents over the Model
// Context Protocol (spec revision 2025-06-18), as JSON-RPC 2.0 on stdio:
// one JSON message per line.
//
// The server cannot change the disk. No tool applies, undoes, empties or runs
// anything. The plan tools write one file, a locked plan in the state folder,
// and the human applies it with `sapu apply FILE`. stdout carries only
// protocol messages; logs go to stderr.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/khmuhtadin/sapu/internal/env"
)

const protocolVersion = "2025-06-18"

// JSON-RPC error codes.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

const instructions = `Sapu shows where a Mac's disk space went and prepares safe cleanup plans.

This server is read-only. Sapu never deletes anything through MCP: no tool deletes, moves, undoes or runs anything. The plan tools (plan_clean, plan_purge) only write a locked plan file and return the exact command for the human to run: "sapu apply <file>". Tell the user to review and run that command themselves. Never try to apply a plan for them.

Start with summary or explain to see where the space is. Use analyze to look inside a folder. Items that are protected (editor data, AI tool data, mail, notes and so on), whitelisted or in use are never selected.`

// Server answers MCP requests for one user.
type Server struct {
	Env     env.Env
	Version string    // Sapu's version, reported in serverInfo
	Log     io.Writer // stderr; never stdout
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"` // absent for a notification
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"` // null when the request could not be read
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// writer writes one message per line. Tool calls answer from their own
// goroutines, so writes are serialized.
type writer struct {
	mu  sync.Mutex
	w   io.Writer
	log io.Writer
}

func (w *writer) send(r response) {
	r.JSONRPC = "2.0"
	b, err := json.Marshal(r)
	if err != nil {
		fmt.Fprintln(w.log, "sapu mcp: encode response:", err)
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.w.Write(append(b, '\n')); err != nil {
		fmt.Fprintln(w.log, "sapu mcp: write response:", err)
	}
}

func (w *writer) result(id json.RawMessage, result any) { w.send(response{ID: id, Result: result}) }

func (w *writer) fail(id json.RawMessage, code int, msg string) {
	w.send(response{ID: id, Error: &rpcError{Code: code, Message: msg}})
}

// running holds the cancel function of every tool call in progress, by request id.
type running struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

func (r *running) add(id json.RawMessage, cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancels[string(id)] = cancel
}

func (r *running) remove(id json.RawMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.cancels, string(id))
}

func (r *running) cancel(id json.RawMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cancel, ok := r.cancels[string(id)]; ok {
		cancel()
	}
}

type line struct {
	text []byte
	err  error
}

// Serve reads requests from in and writes responses to out until in reaches
// EOF or ctx is cancelled. At EOF it waits for the tool calls in progress to
// answer. A cancelled ctx cancels them, and they do not answer.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	w := &writer{w: out, log: s.log()}
	calls := &running{cancels: map[string]context.CancelFunc{}}
	var wg sync.WaitGroup
	defer wg.Wait()

	// Reading blocks, so it gets its own goroutine and a cancelled ctx can
	// still end the loop.
	lines := make(chan line)
	go func() {
		r := bufio.NewReader(in)
		for {
			text, err := r.ReadBytes('\n')
			select {
			case lines <- line{text, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case l := <-lines:
			if text := bytes.TrimSpace(l.text); len(text) > 0 {
				s.handle(ctx, text, w, calls, &wg)
			}
			if errors.Is(l.err, io.EOF) {
				return nil
			}
			if l.err != nil {
				return l.err
			}
		}
	}
}

func (s *Server) log() io.Writer {
	if s.Log == nil {
		return io.Discard
	}
	return s.Log
}

func (s *Server) handle(ctx context.Context, text []byte, w *writer, calls *running, wg *sync.WaitGroup) {
	if text[0] == '[' {
		w.fail(nil, codeInvalidRequest, "batches are not supported: send one message per line")
		return
	}
	var req request
	if err := json.Unmarshal(text, &req); err != nil {
		w.fail(nil, codeParse, "parse error: "+err.Error())
		return
	}
	hasID := len(req.ID) > 0 && string(req.ID) != "null"

	if req.Method == "" {
		if hasID {
			w.fail(req.ID, codeInvalidRequest, "missing method")
		}
		return // a response from the client: Sapu sends no requests
	}
	if !hasID {
		if req.Method == "notifications/cancelled" {
			s.cancelled(req.Params, calls)
		}
		return // notifications/initialized and the rest need no answer
	}

	switch req.Method {
	case "initialize":
		s.initialize(req, w)
	case "ping":
		w.result(req.ID, struct{}{})
	case "tools/list":
		w.result(req.ID, map[string]any{"tools": toolList()})
	case "tools/call":
		s.callTool(ctx, req, w, calls, wg)
	default:
		w.fail(req.ID, codeMethodNotFound, "method not found: "+req.Method)
	}
}

func (s *Server) cancelled(params json.RawMessage, calls *running) {
	var p struct {
		RequestID json.RawMessage `json:"requestId"`
		Reason    string          `json:"reason"`
	}
	if err := json.Unmarshal(params, &p); err != nil || len(p.RequestID) == 0 {
		return // a malformed cancellation is ignored
	}
	fmt.Fprintf(s.log(), "sapu mcp: request %s cancelled: %s\n", p.RequestID, p.Reason)
	calls.cancel(p.RequestID)
}

func (s *Server) initialize(req request, w *writer) {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil || p.ProtocolVersion == "" {
		w.fail(req.ID, codeInvalidParams, "initialize needs params with protocolVersion")
		return
	}
	// Sapu speaks one revision. A client that cannot use it disconnects.
	w.result(req.ID, map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "sapu", "title": "Sapu", "version": s.Version},
		"instructions":    instructions,
	})
}

// callTool runs a tool in its own goroutine, so that a slow scan does not
// block ping or a cancellation.
func (s *Server) callTool(ctx context.Context, req request, w *writer, calls *running, wg *sync.WaitGroup) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
		w.fail(req.ID, codeInvalidParams, "tools/call needs params with a tool name and optional arguments object")
		return
	}
	t := findTool(p.Name)
	if t == nil {
		w.fail(req.ID, codeInvalidParams, "unknown tool: "+p.Name)
		return
	}
	args := p.Arguments
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}

	callCtx, cancel := context.WithCancel(ctx)
	calls.add(req.ID, cancel)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer calls.remove(req.ID)
		defer cancel()

		res, err := t.run(callCtx, s.Env, args)
		if callCtx.Err() != nil {
			return // cancelled: the client does not want an answer
		}
		var bad badArgs
		switch {
		case errors.As(err, &bad):
			w.fail(req.ID, codeInvalidParams, p.Name+": "+bad.Error())
		case err != nil:
			w.result(req.ID, toolErrorResult(err))
		default:
			w.result(req.ID, res.message())
		}
	}()
}

// badArgs marks arguments that do not fit the tool's input schema.
type badArgs struct{ error }

// decodeArgs reads the arguments object into dst. Unknown fields and wrong
// types are errors.
func decodeArgs(raw json.RawMessage, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return badArgs{fmt.Errorf("invalid arguments: %s", strings.TrimPrefix(err.Error(), "json: "))}
	}
	return nil
}
