package media

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"strings"

	json "encoding/json/v2"

	"9router/proxy/internal/clientconfig"
	chathandlers "9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/handlerutil"
)

// Handler for the nine CLI-client config adapters.
//
// Upstream serves one Next route per client (`src/app/api/cli-tools/<tool>-settings`),
// each of which is the same three verbs over the same shape. The adapters in
// internal/clientconfig already know each client's files and keys; this is only
// the HTTP surface onto them, so the two cannot drift.
//
// The route carries the client id (`/api/cli-tools/{tool}-settings`), and the
// id is resolved through clientconfig.Resolve so an unknown name is a 404 that
// lists what is accepted rather than a silent no-op.

// cliToolSettingsRequest is the POST body every adapter accepts. Tiers carries
// the per-tier model overrides only claude uses; an adapter that has no tiers
// ignores it.
type cliToolSettingsRequest struct {
	BaseURL string            `json:"baseUrl"`
	APIKey  string            `json:"apiKey"`
	Model   string            `json:"model"`
	Tiers   map[string]string `json:"tiers"`
}

// CLIToolHandler serves the per-client settings routes.
//
// models is the chat handler whose catalog the limits resolve against: the
// adapters write each client's context window so auto-compact fires at the
// real boundary, and the only source of that number is the same chain
// /v1/models serves (declared custom limits, capability tables, combo seat
// minimums).
type CLIToolHandler struct {
	chat *chathandlers.ChatHandler
}

// NewCLIToolHandler returns a CLIToolHandler. The chat handler may be nil in
// tests; the limits then resolve empty and adapters keep their defaults.
func NewCLIToolHandler(chat *chathandlers.ChatHandler) *CLIToolHandler {
	return &CLIToolHandler{chat: chat}
}

// toolFromPath pulls the client id out of "/api/cli-tools/<tool>-settings".
func toolFromPath(path string) string {
	const prefix = "/api/cli-tools/"
	i := strings.Index(path, prefix)
	if i < 0 {
		return ""
	}
	rest := path[i+len(prefix):]
	rest = strings.TrimSuffix(rest, "-settings")
	return strings.TrimSuffix(rest, "/")
}

// resolveTool turns the path segment into an adapter.
func (h *CLIToolHandler) resolveTool(w http.ResponseWriter, r *http.Request) (clientconfig.Adapter, bool) {
	name := toolFromPath(r.URL.Path)
	if name == "" {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "unknown tool")
		return nil, false
	}
	adapter, err := clientconfig.Resolve(name)
	if err != nil {
		// The error names every accepted spelling, which is exactly what a
		// caller with a typo needs.
		handlerutil.WriteJSONError(w, http.StatusNotFound, err.Error())
		return nil, false
	}
	return adapter, true
}

// installedFor reports whether the client is present on this machine: its
// binary is on PATH, or its config file already exists. Paths() only says where
// the file *would* be — it resolves a path without touching the disk — so
// treating a non-empty list as "installed" reported every client as installed
// on a clean machine.
func installedFor(adapter clientconfig.Adapter) bool {
	if _, err := exec.LookPath(adapter.ID()); err == nil {
		return true
	}
	paths, err := adapter.Paths(clientconfig.Options{})
	if err != nil {
		return false
	}
	for _, f := range paths.Files {
		if _, statErr := os.Stat(f.Path); statErr == nil {
			return true
		}
	}
	return false
}

// HandleCLIToolGet serves GET /api/cli-tools/{tool}-settings: what the adapter
// would write, resolved against the operator's home directory.
//
// It reports Plan rather than reading the client's file back, because the
// adapters own the write direction; a read-back would need a second parser per
// dialect and would disagree with Apply the first time they drifted.
func (h *CLIToolHandler) HandleCLIToolGet(w http.ResponseWriter, r *http.Request) {
	adapter, ok := h.resolveTool(w, r)
	if !ok {
		return
	}
	opts := optionsFromQuery(r)
	h.attachModelLimits(r.Context(), &opts, opts.Model)
	paths, err := adapter.Paths(opts)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	files := make([]map[string]any, 0, len(paths.Files))
	for _, f := range paths.Files {
		files = append(files, map[string]any{
			"path":    f.Path,
			"format":  f.Format.String(),
			"purpose": f.Purpose,
		})
	}
	plan, err := adapter.Plan(opts)
	if err != nil {
		// A client that has never run on this machine has no config directory,
		// and Plan refuses a base it cannot prove containment for. That is "not
		// installed", not a server fault — the caller is asking what would be
		// written, not asking us to write it.
		if errors.Is(err, clientconfig.ErrBaseMissing) {
			handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
				"tool":      adapter.ID(),
				"installed": false,
				"files":     files,
				"plan":      []any{},
				"message":   "Config directory does not exist yet; applying settings will create it.",
			})
			return
		}
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"tool":       adapter.ID(),
		"installed":  installedFor(adapter),
		"has9Router": ToolHas9Router(adapter.ID()),
		"files":      files,
		"plan":       planFiles(plan),
	})
}

// planFiles serializes the plan's edits without leaking the credential: the
// API key is a value the plan carries, and this route is served to a caller
// that may only be reading.
func planFiles(plan []clientconfig.FilePlan) []map[string]any {
	out := make([]map[string]any, 0, len(plan))
	for _, fp := range plan {
		edits := make([]map[string]any, 0, len(fp.Edits))
		for _, e := range fp.Edits {
			edits = append(edits, map[string]any{"keyPath": e.KeyPath})
		}
		out = append(out, map[string]any{
			"path":       fp.File.Path,
			"purpose":    fp.File.Purpose,
			"removeFile": fp.RemoveFile,
			"edits":      edits,
		})
	}
	return out
}

// HandleCLIToolPost serves POST /api/cli-tools/{tool}-settings.
func (h *CLIToolHandler) HandleCLIToolPost(w http.ResponseWriter, r *http.Request) {
	adapter, ok := h.resolveTool(w, r)
	if !ok {
		return
	}
	var req cliToolSettingsRequest
	if err := json.UnmarshalRead(r.Body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	baseURL := strings.TrimSpace(req.BaseURL)
	if baseURL == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "baseUrl is required")
		return
	}
	opts := clientconfig.Options{
		BaseURL: baseURL,
		APIKey:  strings.TrimSpace(req.APIKey),
		Model:   strings.TrimSpace(req.Model),
		Tiers:   req.Tiers,
	}
	h.attachModelLimits(r.Context(), &opts, opts.Model, req.Tiers["fable"], req.Tiers["opus"], req.Tiers["sonnet"], req.Tiers["haiku"])
	result, err := adapter.Apply(opts)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"tool":    adapter.ID(),
		"changed": result.Changed,
		"backups": result.Backups,
		"message": "Settings applied",
	})
}

// HandleCLIToolDelete serves DELETE /api/cli-tools/{tool}-settings.
func (h *CLIToolHandler) HandleCLIToolDelete(w http.ResponseWriter, r *http.Request) {
	adapter, ok := h.resolveTool(w, r)
	if !ok {
		return
	}
	result, err := adapter.Reset(clientconfig.Options{})
	if err != nil {
		if errors.Is(err, clientconfig.ErrUnsupportedStructure) {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"tool":    adapter.ID(),
		"changed": result.Changed,
		"backups": result.Backups,
		"message": "Settings reset",
	})
}

// optionsFromQuery builds the read-only options GET needs: a caller may point
// the resolution at another home directory for a preview.
func optionsFromQuery(r *http.Request) clientconfig.Options {
	q := r.URL.Query()
	return clientconfig.Options{
		Home:    strings.TrimSpace(q.Get("home")),
		BaseURL: strings.TrimSpace(q.Get("baseUrl")),
		APIKey:  strings.TrimSpace(q.Get("apiKey")),
		Model:   strings.TrimSpace(q.Get("model")),
	}
}

// attachModelLimits resolves the token capacity the gateway publishes for each
// named model and stores it on the options. The lookup runs against the same
// catalog /v1/models serves (catalog mode: static registry plus custom models,
// no live-catalog network round trips), matched by exact id first and then by
// bare model name, so "glm/glm-5.3" and "glm-5.3" both land. A model the
// catalog does not know simply stays absent: the adapters fall back.
func (h *CLIToolHandler) attachModelLimits(ctx context.Context, opts *clientconfig.Options, models ...string) {
	if h.chat == nil {
		return
	}
	wanted := make([]string, 0, len(models))
	seen := make(map[string]bool, len(models))
	for _, m := range models {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		wanted = append(wanted, m)
	}
	if len(wanted) == 0 {
		return
	}
	result := h.chat.BuildModelsListResult(ctx, chathandlers.ListModeCatalog)
	exact := make(map[string]clientconfig.ModelLimits, len(result.Models))
	bare := make(map[string]clientconfig.ModelLimits, len(result.Models))
	for _, entry := range result.Models {
		limits := clientconfig.ModelLimits{}
		if entry.ContextLength != nil {
			limits.ContextWindow = *entry.ContextLength
		}
		if entry.MaxCompletionTokens != nil {
			limits.MaxOutput = *entry.MaxCompletionTokens
		}
		if limits.ContextWindow == 0 && limits.MaxOutput == 0 {
			continue
		}
		exact[entry.ID] = limits
		if slash := strings.LastIndexByte(entry.ID, '/'); slash >= 0 {
			bareName := entry.ID[slash+1:]
			if _, ok := bare[bareName]; !ok {
				bare[bareName] = limits
			}
		}
	}
	out := make(map[string]clientconfig.ModelLimits, len(wanted))
	for _, m := range wanted {
		if limits, ok := exact[m]; ok {
			out[m] = limits
			continue
		}
		base := trimLevelSuffix(m)
		if base != m {
			if limits, ok := exact[base]; ok {
				out[m] = limits
				continue
			}
		}
		if limits, ok := bare[m]; ok {
			out[m] = limits
			continue
		}
		if base != m {
			if limits, ok := bare[base]; ok {
				out[m] = limits
			}
		}
	}
	if len(out) > 0 {
		opts.ModelLimits = out
	}
}

// trimLevelSuffix removes the "(level)" intent suffix a model id may carry
// ("glm-5.3(high)"): it is a request override, not part of the catalog id.
func trimLevelSuffix(model string) string {
	open := strings.LastIndexByte(model, '(')
	if open <= 0 || !strings.HasSuffix(model, ")") {
		return model
	}
	return model[:open]
}
