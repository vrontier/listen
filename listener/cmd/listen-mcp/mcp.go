package main

// A minimal MCP server over stdio: newline-delimited JSON-RPC 2.0 with the
// methods a tools-only server needs (initialize, ping, tools/list,
// tools/call). Written by hand to keep the module free of new dependencies;
// the transport is separate from the tools, so an HTTP transport can follow.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"sync"
)

const latestProtocol = "2025-06-18"

var supportedProtocols = map[string]bool{"2025-06-18": true, "2025-03-26": true, "2024-11-05": true}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// Tool is one MCP tool: its schema and its handler. Handlers return a value
// that is sent as JSON text content, or an error shown to the model.
type Tool struct {
	Name        string                                                       `json:"name"`
	Description string                                                       `json:"description"`
	InputSchema map[string]any                                               `json:"inputSchema"`
	Handler     func(ctx context.Context, args json.RawMessage) (any, error) `json:"-"`
}

type server struct {
	name, version string
	tools         []Tool
	out           io.Writer
	mu            sync.Mutex // one response line at a time
}

func (s *server) send(r rpcResponse) {
	r.JSONRPC = "2.0"
	b, err := json.Marshal(r)
	if err != nil {
		log.Printf("mcp: encode: %v", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.out.Write(append(b, '\n'))
}

// serve reads requests until in closes. Tool calls run concurrently, since an
// extract waits for its recording window.
func (s *server) serve(ctx context.Context, in io.Reader) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	var wg sync.WaitGroup
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			s.send(rpcResponse{ID: json.RawMessage("null"), Error: &rpcError{Code: -32700, Message: "parse error"}})
			continue
		}
		if len(req.ID) == 0 {
			continue // a notification (e.g. notifications/initialized): no reply
		}
		wg.Add(1)
		go func(req rpcRequest) {
			defer wg.Done()
			s.handle(ctx, req)
		}(req)
	}
	wg.Wait()
	return sc.Err()
}

func (s *server) handle(ctx context.Context, req rpcRequest) {
	reply := func(result any) { s.send(rpcResponse{ID: req.ID, Result: result}) }
	fail := func(code int, msg string) {
		s.send(rpcResponse{ID: req.ID, Error: &rpcError{Code: code, Message: msg}})
	}

	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := latestProtocol
		if supportedProtocols[p.ProtocolVersion] {
			version = p.ProtocolVersion
		}
		reply(map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": s.name, "version": s.version},
			"instructions": "Read-only access to Listening Observatory: the state of its stream listeners, " +
				"the configured sources, and extracts of analysis data (no audio).",
		})
	case "ping":
		reply(map[string]any{})
	case "tools/list":
		reply(map[string]any{"tools": s.tools})
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			fail(-32602, "invalid params")
			return
		}
		for _, t := range s.tools {
			if t.Name != p.Name {
				continue
			}
			args := p.Arguments
			if len(args) == 0 {
				args = json.RawMessage("{}")
			}
			v, err := t.Handler(ctx, args)
			if err != nil {
				reply(map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": err.Error()}}})
				return
			}
			b, err := json.MarshalIndent(v, "", " ")
			if err != nil {
				reply(map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": fmt.Sprintf("encode result: %v", err)}}})
				return
			}
			reply(map[string]any{"content": []any{map[string]any{"type": "text", "text": string(b)}}})
			return
		}
		fail(-32602, "unknown tool: "+p.Name)
	default:
		fail(-32601, "method not found: "+req.Method)
	}
}
