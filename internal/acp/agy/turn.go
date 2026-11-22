package agy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Errors surfaced to the executor for HTTP status mapping.
var (
	ErrAuth    = errors.New("agy-acp authentication failed; re-login the account")
	ErrQuota   = errors.New("agy-acp quota exhausted")
	ErrPolicy  = errors.New("agy-acp account disabled or policy violation")
	ErrBinary  = errors.New("agy binary not found; set AGY_PATH")
	ErrNoModel = errors.New("agy-acp model is not in the agy catalogue")
	ErrNoCred  = errors.New("agy-acp connection has no usable credentials")
	ErrStall   = errors.New("agy-acp agent returned only a status message and no answer")
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

// RunTurn runs one prompt on an agy ACP worker and streams updates to onEvent
// until the turn ends. Workers come from the warm pool (pre-spawned and
// pre-initialized per account); AGY_ACP_POOL=0 restores the cold
// spawn-per-turn path. It is cancellation-aware: closing ctx cancels the
// prompt; a pooled worker survives, a cold worker is killed.
func RunTurn(ctx context.Context, opts TurnOptions, model string, prompt []PromptBlock, onEvent func(TurnEvent)) error {
	bin, err := resolveBinary(opts.Binary)
	if err != nil {
		return err
	}
	canonical, ok := ResolveModel(model)
	if !ok {
		return fmt.Errorf("%w: %q", ErrNoModel, model)
	}

	if poolEnabled() {
		return runPooledTurn(ctx, opts, bin, canonical, prompt, onEvent)
	}
	return runColdTurn(ctx, opts, bin, canonical, prompt, onEvent)
}

// runPooledTurn leases a warm worker, reuses its prepared session when the
// turn is text-only and the model matches, and returns the worker on success.
// Error paths discard the worker: a banner or half-finished prompt must never
// poison the next turn on the same account.
func runPooledTurn(ctx context.Context, opts TurnOptions, bin, canonical string, prompt []PromptBlock, onEvent func(TurnEvent)) error {
	l, err := DefaultPool().acquire(ctx, opts, bin, canonical)
	if err != nil {
		return err
	}
	defer l.release()

	var bridge *MCPBridge
	sid := l.sid
	if sid == "" {
		mcpServers := []any{}
		if len(opts.MCPTools) > 0 {
			bridge, err = StartMCPBridge(ctx, opts.MCPTools, opts.MCPSeeded)
			if err != nil {
				l.dirty = true
				return err
			}
			defer func() { _ = bridge.Close() }()
			mcpServers = append(mcpServers, map[string]any{"name": "router_client_tools", "url": bridge.URL()})
		}
		sid, err = l.newSession(ctx, canonical, mcpServers)
		if err != nil {
			return err
		}
	}
	// model was set under the pool lock at acquire time.

	if err := streamPrompt(ctx, l.entry.client, sid, prompt, bridge, onEvent); err != nil {
		// Client disconnect cancels the session but leaves the worker healthy;
		// only agent-side faults poison it.
		l.dirty = ctx.Err() == nil
		return err
	}
	return nil
}

// runColdTurn is the original spawn-per-turn path, kept as the AGY_ACP_POOL=0
// fallback and for callers that must not share worker state.
func runColdTurn(ctx context.Context, opts TurnOptions, bin, canonical string, prompt []PromptBlock, onEvent func(TurnEvent)) error {
	root := opts.AccountRoot
	if root == "" {
		root = filepath.Join(os.TempDir(), "9router-agy-acp-default")
	}
	dir, err := NewAccountDir(root)
	if err != nil {
		return err
	}
	if opts.Credentials.AccessToken != "" && opts.Credentials.RefreshToken != "" {
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
	if _, err := client.Call(ctx, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientInfo":         map[string]any{"name": "9router", "version": "1.0"},
		"clientCapabilities": map[string]any{},
	}); err != nil {
		return classifyError(err)
	}

	// Start a loopback MCP server when the client supplied tools, so agy can
	// discover them without an external `nc` shim.
	var bridge *MCPBridge
	mcpServers := []any{}
	if len(opts.MCPTools) > 0 {
		bridge, err = StartMCPBridge(ctx, opts.MCPTools, opts.MCPSeeded)
		if err != nil {
			return err
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
	return streamPrompt(ctx, client, session.SessionID, prompt, bridge, onEvent)
}

// errStatusOnly signals that agy ended a turn with only a deferred-status
// line ("checking model/config in the background") and no answer.
var errStatusOnly = errors.New("agy-acp: status-only turn")

// nudgePrompt is sent on the same session when agy defers the answer. It is
// short and explicit so a fresh agy process also understands it.
const nudgePrompt = "Continue and finish the previous request now. Provide the actual answer in this turn; do not defer it to the background."

// streamPrompt runs one prompt on a session. When agy ends the turn with only
// a deferred-status line, it re-prompts on the same session with a nudge (up
// to 2 times) instead of returning an empty success; if all attempts are
// status-only it returns ErrStall so the router can fail over.
func streamPrompt(ctx context.Context, client *Client, sid string, prompt []PromptBlock, bridge *MCPBridge, onEvent func(TurnEvent)) error {
	for attempt := range 3 {
		current := prompt
		if attempt > 0 {
			current = []PromptBlock{{Type: "text", Text: nudgePrompt}}
		}
		err := streamPromptOnce(ctx, client, sid, current, bridge, onEvent)
		if !errors.Is(err, errStatusOnly) {
			return err
		}
	}
	return fmt.Errorf("%w: agent kept deferring the answer to the background", ErrStall)
}

// streamPrompt sends one prompt on an ACP session and pumps notifications to
// onEvent until the turn ends. If agy only emits a "checking model/config in
// the background" status line, it follows up on the same session with a nudge
// (up to 2 times) asking for the answer now. That keeps the turn alive
// instead of ending with a silent status message.
func streamPromptOnce(ctx context.Context, client *Client, sid string, prompt []PromptBlock, bridge *MCPBridge, onEvent func(TurnEvent)) error {
	promptParams := map[string]any{
		"sessionId": sid,
		"prompt":    prompt,
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	turnCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
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
	handleNote := func(note notification) (bool, error) {
		if note.Method != "session/update" && note.Method != "$/update" {
			return false, nil
		}
		kind, text, update, err := ParseUpdate(note.Params)
		if err != nil {
			return false, nil
		}
		switch kind {
		case UpdateText:
			if text == "" {
				return false, nil
			}
			if leadAlive {
				onEvent(TurnEvent{Kind: UpdateText, Text: text})
				return false, nil
			}
			lead.WriteString(text)
			// Hold a possible status line: "checking model/config in
			// background" must not be flushed as a fake answer.
			if !isAgentDiagnosticPrefix(lead.String()) && !isDeferredStatus(lead.String()) {
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
			return false, nil
		case UpdateError:
			return true, classifyError(fmt.Errorf("agy-acp: %s", text))
		case UpdateDone:
			d := strings.TrimSpace(lead.String())
			if !leadAlive {
				if isAgentDiagnostic(d) {
					return true, classifyError(fmt.Errorf("agy-acp: %s", d))
				}
				if isDeferredStatus(d) {
					return true, errStatusOnly
				}
				flushLead()
			}
			onEvent(TurnEvent{Kind: UpdateDone, Done: true})
			return true, nil
		}
		_ = update
		return false, nil
	}
	for {
		// select picks a ready channel at random; check cancellation first so
		// a cancelled turn never reports success from a racing prompt result.
		if err := ctx.Err(); err != nil {
			_ = client.Notification("session/cancel", map[string]any{"sessionId": sid})
			return err
		}
		select {
		case <-ctx.Done():
			_ = client.Notification("session/cancel", map[string]any{"sessionId": sid})
			select {
			case cerr := <-promptErrCh: // drain; error irrelevant on cancellation
				_ = cerr
			case <-promptResultCh:
			case <-client.done:
			case <-time.After(2 * time.Second):
			}
		drainCancelNotes:
			for {
				select {
				case <-client.Notifications():
				default:
					break drainCancelNotes
				}
			}
			return ctx.Err()
		case err := <-promptErrCh:
			return classifyError(err)
		case res := <-promptResultCh:
			// client.dispatch() enqueues every preceding session/update into
			// Notifications() before delivering the session/prompt response, so drain
			// all already-queued notifications before finalizing the turn.
		drainNotes:
			for {
				select {
				case note := <-client.Notifications():
					if done, err := handleNote(note); done {
						return err
					}
				default:
					break drainNotes
				}
			}
			text, stop := FinalResult(res)
			if text != "" {
				whole := strings.TrimSpace(lead.String() + text)
				if !leadAlive && (isAgentDiagnostic(whole) || isDeferredStatus(whole)) {
					if isDeferredStatus(whole) {
						return errStatusOnly
					}
					return classifyError(fmt.Errorf("agy-acp: %s", whole))
				}
				flushLead()
				onEvent(TurnEvent{Kind: UpdateText, Text: text})
			}
			d := strings.TrimSpace(lead.String())
			if !leadAlive {
				if isAgentDiagnostic(d) {
					return classifyError(fmt.Errorf("agy-acp: %s", d))
				}
				if isDeferredStatus(d) {
					return errStatusOnly
				}
				flushLead()
			}
			onEvent(TurnEvent{Kind: UpdateDone, Done: true})
			return stopReasonError(stop)
		case note := <-client.Notifications():
			if done, err := handleNote(note); done {
				return err
			}
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

// isDeferredStatus reports whether the whole turn is one of agy's "I'll check
// the model/config in the background" status lines. agy 1.5.x emits these as
// ordinary agent_message_chunk text and ends the turn with end_turn, with no
// answer following — the client sees one line and the stream closes. Treat it
// as a stalled turn so the router can surface an error instead of a fake
// empty success (and so failover can try another account).
func isDeferredStatus(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	if t == "" || len(t) > 300 || strings.Contains(t, "\n") {
		return false
	}
	// agy's own status (English) or the model's translated status (Indonesian
	// "di latar belakang") both count. Require a check/model/config word plus
	// a background phrase so ordinary prose that mentions "model" is safe.
	hasCheck := strings.Contains(t, "checking") || strings.Contains(t, "check ") ||
		strings.Contains(t, "model") || strings.Contains(t, "configur") ||
		strings.Contains(t, "konfigurasi")
	hasBackground := strings.Contains(t, "background") || strings.Contains(t, "latar belakang")
	return hasCheck && hasBackground
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
	// Strip overridden isolation vars (including case-variant keys on Windows)
	// plus ambient credentials before appending the account-scoped overrides.
	for _, k := range []string{
		"HOME", "USERPROFILE", "SSH_CONNECTION",
		"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME",
		"BROWSER", "AGY_CLI_DISABLE_AUTO_UPDATE",
		"GEMINI_API_KEY", "GOOGLE_API_KEY", "GOOGLE_APPLICATION_CREDENTIALS", "ANTIGRAVITY_APP_DATA_DIR",
	} {
		env = removeEnv(env, k)
	}
	// Force file-backed token storage and isolate all cross-contaminating vars.
	env = append(env,
		"HOME="+home,
		"USERPROFILE="+home,
		"SSH_CONNECTION=127.0.0.1 0 127.0.0.1 22",
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_CACHE_HOME="+filepath.Join(home, ".cache"),
		"XDG_STATE_HOME="+filepath.Join(home, ".local", "state"),
		"BROWSER="+filepath.Join(os.TempDir(), "false"),
		"AGY_CLI_DISABLE_AUTO_UPDATE=true",
	)
	env = append(env, opts.ExtraEnv...)
	cmd.Env = env

	return cmd
}

func envKeyMatches(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func removeEnv(env []string, key string) []string {
	out := env[:0]
	for _, e := range env {
		if k, _, ok := strings.Cut(e, "="); ok && envKeyMatches(k, key) {
			continue
		}
		out = append(out, e)
	}
	return out
}
