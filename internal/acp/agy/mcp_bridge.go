package agy

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// MCPTool is one tool exposed to agy through the loopback MCP server.
type MCPTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema"`
}

// MCPCall is a tool invocation captured from agy. The executor turns this into
// an OpenAI tool_calls delta; the client executes it and returns the result on
// the next turn.
type MCPCall struct {
	ID   string
	Name string
	Args json.RawMessage
}

// MCPBridge serves a minimal Streamable-HTTP MCP endpoint on loopback so agy
// can discover client tools from body.tools without needing `nc`.
type MCPBridge struct {
	server   *http.Server
	listener net.Listener
	mu       sync.Mutex
	tools    []MCPTool
	calls    chan MCPCall
	seeded   map[string]string // tool name -> prior result text
}

// StartMCPBridge binds an ephemeral loopback port and serves tools. The bridge
// must be Close'd when the turn ends.
func StartMCPBridge(ctx context.Context, tools []MCPTool, seeded map[string]string) (*MCPBridge, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("mcp loopback listen: %w", err)
	}
	b := &MCPBridge{
		listener: ln,
		tools:    tools,
		calls:    make(chan MCPCall, 32),
		seeded:   seeded,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", b.handleMCP)
	b.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = b.server.Serve(ln) }()
	return b, nil
}

// URL returns the MCP endpoint URL to pass in session/new's mcpServers array.
func (b *MCPBridge) URL() string {
	return fmt.Sprintf("http://%s/mcp", b.listener.Addr().String())
}

// Calls exposes captured tool invocations.
func (b *MCPBridge) Calls() <-chan MCPCall { return b.calls }

// Close shuts down the loopback server.
func (b *MCPBridge) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return b.server.Shutdown(ctx)
}

// mcpRequest is the subset of the MCP JSON-RPC protocol the bridge handles.
type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func (b *MCPBridge) handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req mcpRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var result any
	switch req.Method {
	case "initialize":
		result = map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "9router-client-tools", "version": "1.0"},
		}
	case "tools/list":
		b.mu.Lock()
		tools := append([]MCPTool(nil), b.tools...)
		b.mu.Unlock()
		result = map[string]any{"tools": tools}
	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.Unmarshal(req.Params, &params)
		args, _ := json.Marshal(params.Arguments)
		callID := fmt.Sprintf("call_%d", time.Now().UnixNano())
		// Return a seeded result if one is available. In that case the client
		// already executed this tool on a previous turn, so do NOT emit a new
		// tool_call — that would restart the round-trip forever on a fresh
		// session. Let agy consume the result and finish the turn.
		text := "tool call accepted; result delivered on the next turn"
		b.mu.Lock()
		seeded, hasSeed := b.seeded[params.Name]
		if hasSeed {
			text = seeded
		}
		b.mu.Unlock()
		if !hasSeed {
			select {
			case b.calls <- MCPCall{ID: callID, Name: params.Name, Args: args}:
			default:
			}
		}
		result = map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
	default:
		result = map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      req.ID,
		"result":  result,
	})
}

// ToolsFromOpenAI converts OpenAI tool definitions into MCP tools.
func ToolsFromOpenAI(raw json.RawMessage) []MCPTool {
	if len(raw) == 0 {
		return nil
	}
	var defs []struct {
		Type     string `json:"type"`
		Function struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			Parameters  map[string]any `json:"parameters"`
		} `json:"function"`
	}
	if json.Unmarshal(raw, &defs) != nil {
		return nil
	}
	out := make([]MCPTool, 0, len(defs))
	for _, d := range defs {
		schema := d.Function.Parameters
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, MCPTool{Name: d.Function.Name, Description: d.Function.Description, InputSchema: schema})
	}
	return out
}
