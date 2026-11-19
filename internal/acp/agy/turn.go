package agy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Errors surfaced to the executor for HTTP status mapping.
var (
	ErrAuth    = errors.New("agy-acp authentication failed; re-login the account")
	ErrQuota   = errors.New("agy-acp quota exhausted")
	ErrPolicy  = errors.New("agy-acp account disabled or policy violation")
	ErrBinary  = errors.New("agy binary not found; set AGY_PATH")
	ErrNoModel = errors.New("agy-acp model is not in the agy catalogue")
	ErrNoCred  = errors.New("agy-acp connection has no usable credentials")
)

// TurnEvent is one observable event from a turn. The executor maps these to
// SSE chunks; callbacks keep this package independent of net/http.
type TurnEvent struct {
	Kind     UpdateKind
	Text     string
	ToolCall *ToolCall
	Done     bool
	Err      error
}

type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// TurnOptions configures one ACP session.
type TurnOptions struct {
	// Binary is the agy executable path (empty => discover from PATH / AGY_PATH).
	Binary string
	// AccountRoot is the isolated HOME for this account (unique per connection).
	AccountRoot string
	// Credentials are written into the isolated dir before spawn and cleared
	// after the turn. May be empty to adopt the host's existing agy login.
	Credentials Credentials
	// PermissionMode: "auto" (deny) or "accept-edits"/"auto" (allow).
	PermissionMode string
	// ExtraEnv adds per-connection environment variables.
	ExtraEnv []string
	// MCPTools are exposed to agy over a loopback MCP server. Empty means
	// the session is text-only.
	MCPTools []MCPTool
	// MCPSeeded maps tool names to prior results for multi-turn tool calls.
	MCPSeeded map[string]string
}

// RunTurn spawns one agy ACP subprocess, performs the full handshake, sends a
// single prompt and streams updates to onEvent until the turn ends. It is
// cancellation-aware: closing ctx cancels the prompt and kills the worker.
func RunTurn(ctx context.Context, opts TurnOptions, model string, prompt []PromptBlock, onEvent func(TurnEvent)) error {
	bin, err := resolveBinary(opts.Binary)
	if err != nil {
		return err
	}
	canonical, ok := ResolveModel(model)
	if !ok {
		return fmt.Errorf("%w: %q", ErrNoModel, model)
	}

	root := opts.AccountRoot
	if root == "" {
		root = filepath.Join(os.TempDir(), "9router-agy-acp-default")
	}
	dir, err := NewAccountDir(root)
	if err != nil {
		return err
	}
	seed := opts.Credentials.AccessToken != "" && opts.Credentials.RefreshToken != ""
	if seed {
		if err := dir.WriteCredentials(opts.Credentials); err != nil {
			return err
		}
		defer func() { _ = dir.Clear() }()
	}

	cmd := buildCommand(ctx, bin, dir, opts)
	client, err := Start(ctx, cmd)
	if err != nil {
		return err
	}
	defer client.Close()

	// initialize — protocolVersion 1 is what agy 1.5.9 answers.
	initResult, err := client.Call(ctx, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientInfo":         map[string]any{"name": "9router", "version": "1.0"},
		"clientCapabilities": map[string]any{},
	})
	if err != nil {
		return classifyError(err)
	}
	_ = initResult

	// Start a loopback MCP server when the client supplied tools, so agy can
	// discover them without an external `nc` shim.
	var bridge *MCPBridge
	mcpServers := []any{}
	if len(opts.MCPTools) > 0 {
		var berr error
		bridge, berr = StartMCPBridge(ctx, opts.MCPTools, opts.MCPSeeded)
		if berr != nil {
			return berr
		}
		defer func() { _ = bridge.Close() }()
		mcpServers = append(mcpServers, map[string]any{"name": "router_client_tools", "url": bridge.URL()})
	}
	// session/new with an mcpServers array (required by agy; an object is
	// rejected with -32602).
	newResult, err := client.Call(ctx, "session/new", map[string]any{
		"mcpServers": mcpServers,
		"model":      canonical,
	})
	if err != nil {
		return classifyError(err)
	}
	var session struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(newResult, &session); err != nil || session.SessionID == "" {
		return fmt.Errorf("agy-acp: session/new returned no sessionId")
	}

	promptParams := map[string]any{
		"sessionId": session.SessionID,
		"prompt":    prompt,
	}
	turnCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	promptResultCh := make(chan json.RawMessage, 1)
	promptErrCh := make(chan error, 1)
	go func() {
		res, err := client.Call(turnCtx, "session/prompt", promptParams)
		if err != nil {
			promptErrCh <- err
			return
		}
		promptResultCh <- res
	}()

	// agy reports fatal agent faults (dead credentials, unusable model plan)
	// as ordinary agent_message_chunk text followed by stopReason end_turn —
	// not as a JSON-RPC error. Forwarding that text made every auth failure a
	// billed HTTP 200 and stopped the multi-account failover loop. Hold the
	// turn-leading text until real output proves the turn is alive, and turn
	// a lone diagnostic into a classified error instead.
	var lead strings.Builder
	leadAlive := false // real output seen; leading text is no longer suspect
	flushLead := func() {
		if lead.Len() > 0 {
			onEvent(TurnEvent{Kind: UpdateText, Text: lead.String()})
			lead.Reset()
		}
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-promptErrCh:
			return classifyError(err)
		case res := <-promptResultCh:
			text, stop := FinalResult(res)
			if text != "" {
				if !leadAlive && isAgentDiagnostic(lead.String()+text) {
					return classifyError(fmt.Errorf("agy-acp: %s", strings.TrimSpace(lead.String()+text)))
				}
				flushLead()
				onEvent(TurnEvent{Kind: UpdateText, Text: text})
			}
			if !leadAlive {
				if d := strings.TrimSpace(lead.String()); isAgentDiagnostic(d) {
					return classifyError(fmt.Errorf("agy-acp: %s", d))
				}
				flushLead()
			}
			onEvent(TurnEvent{Kind: UpdateDone, Done: true})
			return stopReasonError(stop)
		case note := <-client.Notifications():
			if note.Method != "session/update" && note.Method != "$/update" {
				continue
			}
			kind, text, update, err := ParseUpdate(note.Params)
			if err != nil {
				continue
			}
			switch kind {
			case UpdateText:
				if text == "" {
					continue
				}
				if leadAlive {
					onEvent(TurnEvent{Kind: UpdateText, Text: text})
					continue
				}
				lead.WriteString(text)
				if !isAgentDiagnosticPrefix(lead.String()) {
					leadAlive = true
					flushLead()
				}
			case UpdateThinking:
				leadAlive = true
				flushLead()
				onEvent(TurnEvent{Kind: UpdateThinking, Text: text})
			case UpdateToolCall:
				// agy calls client tools through loopback MCP, not through
				// session/update. A tool_call notification here is advisory;
				// the bridge emits the actual OpenAI tool_call.
				continue
			case UpdateError:
				return classifyError(fmt.Errorf("agy-acp: %s", text))
			case UpdateDone:
				if !leadAlive {
					if d := strings.TrimSpace(lead.String()); isAgentDiagnostic(d) {
						return classifyError(fmt.Errorf("agy-acp: %s", d))
					}
					flushLead()
				}
				onEvent(TurnEvent{Kind: UpdateDone, Done: true})
				return nil
			}
			_ = update
		case call := <-bridgeCalls(bridge):
			flushLead()
			onEvent(TurnEvent{Kind: UpdateToolCall, ToolCall: &ToolCall{
				ID: call.ID, Name: call.Name, Input: call.Args,
			}})
			return nil // turn ends; the client will send the result next turn
		case <-client.done:
			return classifyError(client.processError())
		}
	}
}

// isAgentDiagnostic reports whether a whole turn's text is one of agy's
// fatal fault banners rather than model output. agy prefixes these with
// "⚠" and ends the turn with end_turn; the marker set is taken from the
// agy binary's own diagnostic strings (1.5.x).
func isAgentDiagnostic(text string) bool {
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(t, "⚠") {
		return false
	}
	for _, marker := range []string{
		"failed to construct executor",
		"plan model not specified",
		"needs sign-in", "needs sign in", "needs setup",
		"quota", "rate limit", "rate_limit",
		"unauthorized", "invalid_grant", "authentication",
		"permission denied", "403",
	} {
		if strings.Contains(strings.ToLower(t), marker) {
			return true
		}
	}
	return false
}

// isAgentDiagnosticPrefix reports whether buffered turn-leading text could
// still become a fatal banner. Anything else means real model output has
// started and streaming resumes unbuffered.
func isAgentDiagnosticPrefix(buf string) bool {
	t := strings.TrimSpace(buf)
	if t == "" {
		return true
	}
	if !strings.HasPrefix(t, "⚠") {
		return strings.HasPrefix("⚠", t)
	}
	// A banner is a single short line; long or multi-line text is content.
	if len(t) > 512 || strings.Contains(strings.Trim(buf, " \t\r\n"), "\n") {
		return false
	}
	return true
}

// bridgeCalls returns the bridge's call channel, or nil when no bridge is
// active. A nil channel blocks forever in select, which is the desired no-op.
func bridgeCalls(b *MCPBridge) <-chan MCPCall {
	if b == nil {
		return nil
	}
	return b.Calls()
}

func stopReasonError(stop string) error {
	switch strings.ToLower(stop) {
	case "", "end_turn", "stop", "cancelled":
		return nil
	case "max_tokens", "length":
		return nil // executor decides finish_reason from the event stream
	case "refusal":
		return fmt.Errorf("%w: refusal", ErrPolicy)
	default:
		return nil
	}
}

func classifyError(err error) error {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "invalid_grant"), strings.Contains(msg, "unauthorized"),
		strings.Contains(msg, "401"), strings.Contains(msg, "oauth-personal"), strings.Contains(msg, "login"),
		strings.Contains(msg, "sign-in"), strings.Contains(msg, "sign in"), strings.Contains(msg, "authentication"):
		return fmt.Errorf("%w: %w", ErrAuth, err)
	case strings.Contains(msg, "quota"), strings.Contains(msg, "429"), strings.Contains(msg, "rate_limit"):
		return fmt.Errorf("%w: %w", ErrQuota, err)
	case strings.Contains(msg, "403"), strings.Contains(msg, "policy"), strings.Contains(msg, "disabled"), strings.Contains(msg, "tos"):
		return fmt.Errorf("%w: %w", ErrPolicy, err)
	default:
		return err
	}
}

func resolveBinary(binary string) (string, error) {
	if binary == "" {
		binary = os.Getenv("AGY_PATH")
	}
	if binary != "" {
		if _, err := os.Stat(binary); err == nil {
			return binary, nil
		}
		return "", fmt.Errorf("%w: %s", ErrBinary, binary)
	}
	p, err := exec.LookPath("agy")
	if err != nil {
		return "", ErrBinary
	}
	return p, nil
}

func buildCommand(ctx context.Context, bin string, dir *AccountDir, opts TurnOptions) *exec.Cmd {
	home := dir.Root
	geminiDir := filepath.Join(home, ".gemini")
	args := []string{
		"acp",
		"--gemini-dir", geminiDir,
		"--app-data-dir", "antigravity-cli",
	}
	if strings.EqualFold(opts.PermissionMode, "accept-edits") || strings.EqualFold(opts.PermissionMode, "auto") ||
		strings.EqualFold(opts.PermissionMode, "bypass") {
		args = append(args, "--dangerously-skip-permissions")
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	env := os.Environ()
	// Force file-backed token storage and isolate all cross-contaminating vars.
	env = append(env,
		"HOME="+home,
		"SSH_CONNECTION=127.0.0.1 0 127.0.0.1 22",
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_CACHE_HOME="+filepath.Join(home, ".cache"),
		"XDG_STATE_HOME="+filepath.Join(home, ".local", "state"),
		"BROWSER="+filepath.Join(os.TempDir(), "false"),
		"AGY_CLI_DISABLE_AUTO_UPDATE=true",
	)
	for _, k := range []string{"GEMINI_API_KEY", "GOOGLE_API_KEY", "GOOGLE_APPLICATION_CREDENTIALS", "ANTIGRAVITY_APP_DATA_DIR"} {
		env = removeEnv(env, k)
	}
	env = append(env, opts.ExtraEnv...)
	cmd.Env = env

	return cmd
}

func removeEnv(env []string, key string) []string {
	out := env[:0]
	prefix := key + "="
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return out
}
