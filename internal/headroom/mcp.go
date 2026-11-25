// Minimal Model Context Protocol (MCP) server for the headroom builtin engine.
//
// The protocol is JSON-RPC 2.0 over HTTP: a client POSTs one request object to
// /mcp and reads back one response object. The surface this server exposes is
// the headroom operations a MCP-capable client needs — compress a conversation
// and read compression stats — as MCP tools, plus a headroom://stats resource
// that mirrors the stats tool.
//
// The JSON-RPC envelope is hand-rolled rather than pulled from an SDK on
// purpose: the gateway runs with no external dependencies, and MCP's wire
// format is small enough that a focused implementation is easier to audit than
// a dependency would be. Only the request/response and tool/resource surface
// MCP actually uses is implemented; transports (stdio, SSE) and capabilities
// negotiation beyond tools and resources are out of scope.
package headroom

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"

	"encoding/json/jsontext"
	json "encoding/json/v2"
)

// mcpProtocolVersion is the protocol revision this server answers with. Clients
// send their own version in the initialize params; this server does not
// negotiate — it always reports the version it was built against.
const mcpProtocolVersion = "2024-11-05"

// JSON-RPC 2.0 error codes, the subset MCP uses.
const (
	mcpErrParse            = -32700 // Parse error
	mcpErrInvalidReq       = -32600 // Invalid Request
	mcpErrNotFound         = -32601 // Method not found
	mcpErrInvalidParams    = -32602 // Invalid params
	mcpErrInternal         = -32603 // Internal error
	mcpErrResourceNotFound = -32002 // Resource not found (MCP)
)

// MCPRequest is one JSON-RPC 2.0 request. ID and Params are raw JSON so a
// request's identity and arguments round-trip into the response without this
// server learning every client's shape.
type MCPRequest struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      any            `json:"id"`
	Method  string         `json:"method"`
	Params  jsontext.Value `json:"params"`
}

// MCPError is the error member of a JSON-RPC response.
type MCPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// MCPResponse is one JSON-RPC 2.0 response. Exactly one of Result or Error is
// set; a notification's response carries both nil and a null ID.
type MCPResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id"`
	Result  any       `json:"result,omitempty"`
	Error   *MCPError `json:"error,omitempty"`
}

// mcpTool is one tool definition in a tools/list result.
type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema jsontext.Value `json:"inputSchema"`
}

// MCPStats are the compression counters the MCP server reports. They are
// server-lifetime counters, incremented on each successful call.
type MCPStats struct {
	Compressions int `json:"compressions"`
	TokensSaved  int `json:"tokens_saved"`
}

// mcpStatsResult is the payload of both the headroom_stats tool and the
// headroom://stats resource, so a client reading either sees the same numbers.
type mcpStatsResult struct {
	Protocol     string `json:"protocol"`
	Compressions int    `json:"compressions"`
	TokensSaved  int    `json:"tokens_saved"`
}

// MCPServer is a minimal MCP server exposing the headroom tools over JSON-RPC
// 2.0. It is safe for concurrent use.
type MCPServer struct {
	engine *BuiltinEngine

	mu    sync.Mutex
	stats MCPStats
}

// NewMCPServer returns an MCPServer backed by the given engine.
func NewMCPServer(engine *BuiltinEngine) *MCPServer {
	return &MCPServer{engine: engine}
}

// mcpTools is the fixed tools/list answer; a tool added here appears on the
// wire on the next process start.
var mcpTools = []mcpTool{
	{
		Name: "headroom_compress",
		Description: "Compress a list of chat messages in-process, folding " +
			"repetitive tool output to save tokens. Returns the compressed " +
			"messages and token accounting. Fail-open: nothing compressible " +
			"reports compressed=false and leaves the messages untouched.",
		InputSchema: jsontext.Value(`{
  "type": "object",
  "properties": {
    "session_id": {"type": "string", "description": "Caller-scoped session identifier"},
    "messages": {"type": "array", "description": "Messages to compress, in OpenAI/Claude wire shape"}
  },
  "required": ["messages"]
}`),
	},
	{
		Name: "headroom_stats",
		Description: "Read headroom compression counters: compressions " +
			"and tokens saved.",
		InputSchema: jsontext.Value(`{
  "type": "object",
  "properties": {}
}`),
	},
}

// mcpToolsList is the cached tools/list result, so the list is built once.
var mcpToolsList = struct {
	Tools []mcpTool `json:"tools"`
}{Tools: mcpTools}

// HandlePOST serves one JSON-RPC 2.0 request. It reads the body, dispatches on
// the method, and writes exactly one response object. A body that does not
// parse answers a parse error with a null ID; a tool or resource error is
// reported as a JSON-RPC error rather than a crash (fail-open).
func (s *MCPServer) HandlePOST(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeMCP(w, s.errorResponse(nil, mcpErrInvalidReq, "method not allowed"))
		return
	}
	if r.Body != nil {
		defer func() { _ = r.Body.Close() }()
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.writeMCP(w, s.errorResponse(nil, mcpErrInvalidReq, "failed to read request body"))
		return
	}

	var req MCPRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.writeMCP(w, s.errorResponse(nil, mcpErrParse, "Parse error"))
		return
	}

	if req.JSONRPC != "" && req.JSONRPC != "2.0" {
		s.writeMCP(w, s.errorResponse(req.ID, mcpErrInvalidReq, "unsupported jsonrpc version"))
		return
	}

	result, rpcErr := s.dispatch(r.Context(), &req)
	if rpcErr != nil {
		s.writeMCP(w, s.errorResponse(req.ID, rpcErr.Code, rpcErr.Message))
		return
	}
	s.writeMCP(w, MCPResponse{JSONRPC: "2.0", ID: req.ID, Result: result})
}

// writeMCP writes one JSON-RPC response object.
func (s *MCPServer) writeMCP(w http.ResponseWriter, resp MCPResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.MarshalWrite(w, resp, json.Deterministic(true)); err != nil {
		// Unreachable for the shapes this server produces, but a half-written
		// body is worse than a clean error, so fall back to a minimal literal.
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"internal error"}}`)
	}
}

// errorResponse builds an error envelope. A nil ID stays null, so a parse
// error (where the ID was unreadable) still answers with the null ID JSON-RPC
// requires.
func (s *MCPServer) errorResponse(id any, code int, message string) MCPResponse {
	return MCPResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &MCPError{Code: code, Message: message},
	}
}

// mcpRPCError is a JSON-RPC error produced inside dispatch.
type mcpRPCError struct {
	Code    int
	Message string
}

func rpcError(code int, format string, args ...any) *mcpRPCError {
	return &mcpRPCError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// dispatch routes a request to its method handler and returns the result the
// envelope carries, or an RPC error.
func (s *MCPServer) dispatch(ctx context.Context, req *MCPRequest) (any, *mcpRPCError) {
	switch req.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities": map[string]any{
				"tools":     map[string]any{},
				"resources": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    "9router-headroom",
				"version": "1.0.0",
			},
		}, nil
	case "tools/list":
		return mcpToolsList, nil
	case "tools/call":
		return s.handleToolsCall(ctx, req.Params)
	case "resources/list":
		return map[string]any{
			"resources": []map[string]string{{
				"uri":      "headroom://stats",
				"name":     "Headroom compression stats",
				"mimeType": "application/json",
			}},
		}, nil
	case "resources/read":
		return s.handleResourcesRead(ctx, req.Params)
	default:
		return nil, rpcError(mcpErrNotFound, "method not found: %s", req.Method)
	}
}

// mcpToolsCallParams is the arguments envelope of tools/call. The tool's own
// arguments stay raw so each tool parses the shape it declares.
type mcpToolsCallParams struct {
	Name      string         `json:"name"`
	Arguments jsontext.Value `json:"arguments"`
}

// mcpTextContent is one content block. MCP tools answer with a text content
// block whose text is the JSON-encoded result.
type mcpTextContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// mcpToolsCallResult is the tools/call result envelope: a list of content
// blocks plus an optional structured answer.
type mcpToolsCallResult struct {
	Content []mcpTextContent `json:"content"`
}

// handleToolsCall dispatches a tools/call to the named headroom tool.
func (s *MCPServer) handleToolsCall(ctx context.Context, rawParams jsontext.Value) (any, *mcpRPCError) {
	var params mcpToolsCallParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return nil, rpcError(mcpErrInvalidParams, "invalid tools/call params: %v", err)
		}
	}
	switch params.Name {
	case "headroom_compress":
		return s.toolCompress(params.Arguments)
	case "headroom_stats":
		return s.toolStats()
	case "":
		return nil, rpcError(mcpErrInvalidParams, "missing tool name")
	default:
		return nil, rpcError(mcpErrInvalidParams, "unknown tool: %s", params.Name)
	}
}

// mcpCompressArgs are the headroom_compress tool arguments.
type mcpCompressArgs struct {
	SessionID string `json:"session_id"`
	Messages  []any  `json:"messages"`
}

// toolCompress runs the builtin engine and returns an MCP text content block
// carrying the compression result. Counters are updated only on a real
// compression, so a no-op call does not inflate the numbers.
func (s *MCPServer) toolCompress(args jsontext.Value) (any, *mcpRPCError) {
	var a mcpCompressArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, rpcError(mcpErrInvalidParams, "invalid headroom_compress arguments: %v", err)
		}
	}
	if a.Messages == nil {
		return nil, rpcError(mcpErrInvalidParams, "headroom_compress requires a messages array")
	}

	res, ok := s.engine.CompressMessagesWithSession(a.SessionID, a.Messages)
	if !ok {
		// Fail-open: nothing was compressible. Report that rather than
		// failing the call, so a client that cannot compress still runs.
		text, _ := json.Marshal(map[string]any{
			"compressed": false,
			"session_id": a.SessionID,
			"messages":   a.Messages,
		}, json.Deterministic(true))
		return mcpToolsCallResult{Content: []mcpTextContent{{Type: "text", Text: string(text)}}}, nil
	}

	s.mu.Lock()
	s.stats.Compressions++
	s.stats.TokensSaved += res.TokensSaved
	s.mu.Unlock()

	text, err := json.Marshal(map[string]any{
		"compressed":         true,
		"session_id":         a.SessionID,
		"messages":           res.Messages,
		"tokens_before":      res.TokensBefore,
		"tokens_after":       res.TokensAfter,
		"tokens_saved":       res.TokensSaved,
		"compression_ratio":  res.CompressionRatio,
		"transforms_summary": res.TransformsSummary,
	}, json.Deterministic(true))
	if err != nil {
		return nil, rpcError(mcpErrInternal, "failed to encode compression result: %v", err)
	}
	return mcpToolsCallResult{Content: []mcpTextContent{{Type: "text", Text: string(text)}}}, nil
}

// toolStats answers the headroom_stats tool.
func (s *MCPServer) toolStats() (any, *mcpRPCError) {
	snap := s.snapshot()
	text, err := json.Marshal(snap, json.Deterministic(true))
	if err != nil {
		return nil, rpcError(mcpErrInternal, "failed to encode stats: %v", err)
	}
	return mcpToolsCallResult{Content: []mcpTextContent{{Type: "text", Text: string(text)}}}, nil
}

// snapshot returns the current compression counters.
func (s *MCPServer) snapshot() mcpStatsResult {
	s.mu.Lock()
	st := s.stats
	s.mu.Unlock()
	return mcpStatsResult{
		Protocol:     mcpProtocolVersion,
		Compressions: st.Compressions,
		TokensSaved:  st.TokensSaved,
	}
}

// handleResourcesRead serves resources/read. The only resource this server
// exposes is headroom://stats.
func (s *MCPServer) handleResourcesRead(ctx context.Context, rawParams jsontext.Value) (any, *mcpRPCError) {
	var params struct {
		URI string `json:"uri"`
	}
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return nil, rpcError(mcpErrInvalidParams, "invalid resources/read params: %v", err)
		}
	}
	if params.URI == "headroom://stats" {
		snap := s.snapshot()
		text, err := json.Marshal(snap, json.Deterministic(true))
		if err != nil {
			return nil, rpcError(mcpErrInternal, "failed to encode stats resource: %v", err)
		}
		return map[string]any{
			"contents": []map[string]string{{
				"uri":      "headroom://stats",
				"mimeType": "application/json",
				"text":     string(text),
			}},
		}, nil
	}
	return nil, rpcError(mcpErrResourceNotFound, "unknown resource: %s", params.URI)
}
