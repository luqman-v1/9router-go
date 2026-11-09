package executor

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"

	json "encoding/json/v2"
)

// Grok's client-tool bridge, ported from upstream
// open-sse/executors/grok-web-tools.js (38a0c449, part of #624).
//
// Grok's web stream exposes four native tool cards (bash / readFile /
// webSearch / browsePage). When the client declares OpenAI `tools`, this
// bridge (1) injects a CLIENT_TOOLS manifest into the prompt so Grok can also
// call arbitrary tools via <tool_call> markup, and (2) maps native cards onto
// the best-matching declared tool by name/description/schema scoring — in
// both cases emitting standard OpenAI tool_calls back to the client.

// grokClientTool is one declared function tool, kept in the shape the client
// sent it.
type grokClientTool struct {
	name        string
	description string
	parameters  map[string]any
}

// grokToolRegistry is what one request's tool state resolves to.
type grokToolRegistry struct {
	enabled            bool
	toolsByName        map[string]grokClientTool
	lastUserText       string
	executedToolKeys   map[string]bool
	completedToolCalls []string
}

func grokExtractTextContent(msg map[string]any) string {
	switch content := msg["content"].(type) {
	case string:
		return content
	case []any:
		parts := make([]string, 0, len(content))
		for _, part := range content {
			switch p := part.(type) {
			case string:
				parts = append(parts, p)
			case map[string]any:
				if text, ok := p["text"].(string); ok && text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func grokGetLastUserText(messages []any) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg, _ := messages[i].(map[string]any)
		if msg == nil {
			continue
		}
		if role, _ := msg["role"].(string); role == "user" {
			return grokExtractTextContent(msg)
		}
	}
	return ""
}

// grokSemanticToolKey identifies an executed call by name plus sorted args,
// so a replayed card with reordered keys still dedupes.
func grokSemanticToolKey(name string, args map[string]any) string {
	keys := make([]string, 0, len(args))
	for key := range args {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", key, grokNormalizeLike(args[key])))
	}
	return fmt.Sprintf("%s(%s)", name, strings.Join(parts, ","))
}

func grokNormalizeLike(value any) string {
	if s, ok := value.(string); ok {
		lower := regexp.MustCompile(`\s+`).ReplaceAllString(s, " ")
		lower = strings.NewReplacer("\"", "", "'", "", "`", "").Replace(lower)
		return strings.ToLower(lower)
	}
	return fmt.Sprintf("%v", value)
}

func grokSummarizeCompletedToolCall(name string, args map[string]any) string {
	keys := make([]string, 0, len(args))
	for key := range args {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		value := fmt.Sprintf("%v", args[key])
		if len(value) > 60 {
			value = value[:60]
		}
		parts = append(parts, value)
	}
	return fmt.Sprintf("%s(%s)", name, strings.Join(parts, ", "))
}

func grokGetExecutedToolState(messages []any) (map[string]bool, []string) {
	keys := map[string]bool{}
	var summaries []string
	for _, raw := range messages {
		msg, _ := raw.(map[string]any)
		if msg == nil {
			continue
		}
		if role, _ := msg["role"].(string); role != "assistant" {
			continue
		}
		calls, _ := msg["tool_calls"].([]any)
		for _, rawCall := range calls {
			call, _ := rawCall.(map[string]any)
			if call == nil {
				continue
			}
			fn, _ := call["function"].(map[string]any)
			if fn == nil {
				continue
			}
			name, _ := fn["name"].(string)
			args := map[string]any{}
			if encoded, ok := fn["arguments"].(string); ok {
				_ = json.Unmarshal([]byte(encoded), &args)
			}
			keys[grokSemanticToolKey(name, args)] = true
			if name == "" {
				name = "tool"
			}
			summaries = append(summaries, grokSummarizeCompletedToolCall(name, args))
		}
	}
	return keys, summaries
}

func grokForcedToolChoiceName(toolChoice any) string {
	if choice, ok := toolChoice.(map[string]any); ok {
		if fn, ok := choice["function"].(map[string]any); ok {
			if name, ok := fn["name"].(string); ok {
				return name
			}
		}
	}
	return ""
}

func grokSchemaProperties(parameters map[string]any) map[string]any {
	if parameters == nil {
		return map[string]any{}
	}
	props, _ := parameters["properties"].(map[string]any)
	if props == nil {
		return map[string]any{}
	}
	return props
}

func grokSchemaRequired(parameters map[string]any) []string {
	if parameters == nil {
		return nil
	}
	required, _ := parameters["required"].([]any)
	out := make([]string, 0, len(required))
	for _, name := range required {
		if s, ok := name.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func grokFormatToolArgsSummary(parameters map[string]any) string {
	props := grokSchemaProperties(parameters)
	propNames := make([]string, 0, len(props))
	for name := range props {
		propNames = append(propNames, name)
	}
	sort.Strings(propNames)
	required := grokSchemaRequired(parameters)
	var segments []string
	if len(propNames) > 0 {
		segments = append(segments, "args="+strings.Join(propNames, ","))
	}
	if len(required) > 0 {
		segments = append(segments, "required="+strings.Join(required, "; "))
	}
	if len(segments) == 0 {
		return ""
	}
	return " (" + strings.Join(segments, "; ") + ")"
}

func grokToolText(tool grokClientTool) string {
	return strings.ToLower(tool.name + " " + tool.description)
}

func grokHasAnyProperty(tool grokClientTool, names []string) bool {
	lower := map[string]bool{}
	for name := range grokSchemaProperties(tool.parameters) {
		lower[strings.ToLower(name)] = true
	}
	for _, name := range names {
		if lower[strings.ToLower(name)] {
			return true
		}
	}
	return false
}

var (
	grokMetaToolRE    = regexp.MustCompile(`\b(mcp|mcpproxy|upstream|registry|oauth|cache key|token usage|handoff|context management|memory|memories|lsp|language server|plan file|server management|tool discovery)\b`)
	grokContextToolRE = regexp.MustCompile(`\b(ctx_|memory|memories|conversation history|session notes|project memories|context\.db)\b`)
	grokTerminalName  = regexp.MustCompile(`\b(bash|shell|terminal|run_command|execute_command|exec|command)\b`)
	grokTerminalText  = regexp.MustCompile(`\b(?:run|execute).{0,24}\b(?:shell|bash|terminal|command)\b|\b(?:shell|bash|terminal)\b`)
	grokFileReadText  = regexp.MustCompile(`\b(read|file|filesystem|open)\b`)
	grokFileNotReadRE = regexp.MustCompile(`\b(write|edit|patch|delete|remove|grep|search|bash|shell|command)\b`)
	grokUrlFetchName  = regexp.MustCompile(`\b(webfetch|web\.fetch|fetch_url|url_fetch|read_url|browse_page|browsepage)\b`)
	grokUrlFetchText  = regexp.MustCompile(`\b(?:fetch|browse|read).{0,32}\b(?:url|uri|web page|page content)\b|\b(?:url|uri|web page|page content).{0,32}\b(?:fetch|browse|read)\b`)
	grokWebSearchText = regexp.MustCompile(`\b(web|internet|exa|browser|browse|serp)\b`)
	grokSearchFileRE  = regexp.MustCompile(`\b(glob|grep|search files?|file search|content search)\b`)
	grokEditWriteRE   = regexp.MustCompile(`\b(edit|write|patch|modify|apply)\b`)
	grokAgentTaskRE   = regexp.MustCompile(`\b(task|agent|delegate|subagent)\b`)
)

func grokIsMetaOrInfrastructureTool(tool grokClientTool) bool {
	return grokMetaToolRE.MatchString(grokToolText(tool))
}

func grokIsContextMemoryTool(tool grokClientTool) bool {
	return grokContextToolRE.MatchString(grokToolText(tool))
}

func grokIsTerminalTool(tool grokClientTool) bool {
	if grokIsMetaOrInfrastructureTool(tool) {
		return false
	}
	text := grokToolText(tool)
	name := strings.ToLower(tool.name)
	explicitName := grokTerminalName.MatchString(name)
	explicitText := grokTerminalText.MatchString(text)
	return explicitName || (grokHasAnyProperty(tool, []string{"command", "cmd", "shell"}) && explicitText)
}

func grokIsFileReadTool(tool grokClientTool) bool {
	text := grokToolText(tool)
	return grokHasAnyProperty(tool, []string{"filePath", "file_path", "path"}) &&
		grokFileReadText.MatchString(text) &&
		!grokFileNotReadRE.MatchString(text)
}

func grokWantsURLFetch(text string) bool {
	return regexp.MustCompile(`https?://`).MatchString(text)
}

func grokIsUrlFetchTool(tool grokClientTool) bool {
	text := grokToolText(tool)
	name := strings.ToLower(tool.name)
	explicitName := grokUrlFetchName.MatchString(name)
	explicitUrlText := grokUrlFetchText.MatchString(text)
	return explicitName || (!grokIsMetaOrInfrastructureTool(tool) && grokHasAnyProperty(tool, []string{"url", "uri"}) && explicitUrlText)
}

func grokIsWebSearchTool(tool grokClientTool) bool {
	text := grokToolText(tool)
	return grokHasAnyProperty(tool, []string{"query", "search"}) &&
		grokWebSearchText.MatchString(text) &&
		!grokIsMetaOrInfrastructureTool(tool) &&
		!grokIsContextMemoryTool(tool)
}

// grokBuildToolRegistry reads one request's OpenAI tools into the bridge state.
func grokBuildToolRegistry(body map[string]any) grokToolRegistry {
	tools, _ := body["tools"].([]any)
	messages, _ := body["messages"].([]any)
	toolChoice := body["tool_choice"]
	if toolChoice == nil {
		toolChoice = "auto"
	}
	executedKeys, executedSummaries := grokGetExecutedToolState(messages)
	registry := grokToolRegistry{
		toolsByName:        map[string]grokClientTool{},
		lastUserText:       grokGetLastUserText(messages),
		executedToolKeys:   executedKeys,
		completedToolCalls: executedSummaries,
	}
	if choice, ok := toolChoice.(string); ok && choice == "none" {
		return registry
	}
	var functionTools []grokClientTool
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		if tool == nil {
			continue
		}
		if typ, _ := tool["type"].(string); typ != "function" {
			continue
		}
		fn, _ := tool["function"].(map[string]any)
		if fn == nil {
			continue
		}
		name := strings.TrimSpace(fmt.Sprintf("%v", fn["name"]))
		// A missing name formats as "<nil>"; treat only non-empty real names.
		if name == "" || name == "<nil>" {
			continue
		}
		entry := grokClientTool{name: name, parameters: map[string]any{"type": "object", "properties": map[string]any{}}}
		if description, ok := fn["description"].(string); ok {
			entry.description = description
		}
		if parameters, ok := fn["parameters"].(map[string]any); ok && parameters != nil {
			entry.parameters = parameters
		}
		functionTools = append(functionTools, entry)
	}
	if forced := grokForcedToolChoiceName(toolChoice); forced != "" {
		registry.toolsByName[forced] = grokClientTool{name: forced}
		for _, tool := range functionTools {
			if tool.name == forced {
				registry.toolsByName[forced] = tool
			}
		}
		registry.enabled = true
		return registry
	}
	for _, tool := range functionTools {
		registry.toolsByName[tool.name] = tool
	}
	registry.enabled = len(registry.toolsByName) > 0
	return registry
}

func grokBaseToolOrderScore(tool grokClientTool) int {
	switch {
	case grokIsUrlFetchTool(tool):
		return 90
	case grokIsWebSearchTool(tool):
		return 85
	case grokIsFileReadTool(tool):
		return 75
	case grokIsTerminalTool(tool):
		return 70
	case grokSearchFileRE.MatchString(grokToolText(tool)):
		return 60
	case grokEditWriteRE.MatchString(grokToolText(tool)):
		return 50
	case grokAgentTaskRE.MatchString(grokToolText(tool)):
		return 40
	case grokIsContextMemoryTool(tool):
		return 20
	case grokIsMetaOrInfrastructureTool(tool):
		return 10
	}
	return 30
}

// grokOrderedTools lists the declared tools in the manifest's priority order.
func grokOrderedTools(registry grokToolRegistry) []grokClientTool {
	tools := make([]grokClientTool, 0, len(registry.toolsByName))
	for _, tool := range registry.toolsByName {
		tools = append(tools, tool)
	}
	sort.SliceStable(tools, func(i, j int) bool {
		si, sj := grokBaseToolOrderScore(tools[i]), grokBaseToolOrderScore(tools[j])
		if si != sj {
			return si > sj
		}
		return tools[i].name < tools[j].name
	})
	return tools
}

// grokBuildClientToolManifest renders the CLIENT_TOOLS block injected into the
// prompt so Grok can call caller-runtime tools through markup.
func grokBuildClientToolManifest(registry grokToolRegistry, toolChoice any) string {
	if !registry.enabled {
		return ""
	}
	if toolChoice == nil {
		toolChoice = "auto"
	}
	choiceJSON, _ := json.Marshal(toolChoice)
	lines := []string{
		"CLIENT_TOOLS: use this caller-runtime tool list as the tool interface for this request. To call one, respond only with <tool_call>{\"name\":\"exact_tool_name\",\"arguments\":{...}}</tool_call>. After tool results, answer normally.",
		fmt.Sprintf("tool_choice=%s", choiceJSON),
	}
	if len(registry.completedToolCalls) > 0 {
		lines = append(lines, "completed_tool_calls:")
		for _, summary := range registry.completedToolCalls {
			lines = append(lines, "- "+summary)
		}
		lines = append(lines, "Do not repeat completed tool calls unless a different result is required; use their tool results to answer.")
	}
	lines = append(lines, "tools (priority order for this request):")
	for i, tool := range grokOrderedTools(registry) {
		line := fmt.Sprintf("%d. name: %s%s", i+1, tool.name, grokFormatToolArgsSummary(tool.parameters))
		if tool.description != "" {
			line += "\n   description: " + tool.description
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// grokBuildMessage parses OpenAI messages into Grok's single-message format,
// tool-aware: tool results after the last user turn are folded in as caller
// runtime results, earlier ones dropped, and the manifest closes the prompt.
func grokBuildMessage(messages []any, registry grokToolRegistry, toolChoice any) string {
	manifest := grokBuildClientToolManifest(registry, toolChoice)
	lastUserSourceIdx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		msg, _ := messages[i].(map[string]any)
		if msg == nil {
			continue
		}
		if role, _ := msg["role"].(string); role == "user" {
			lastUserSourceIdx = i
			break
		}
	}
	type turn struct {
		role string
		text string
	}
	var extracted []turn
	for i, raw := range messages {
		msg, _ := raw.(map[string]any)
		if msg == nil {
			continue
		}
		role, _ := msg["role"].(string)
		if role == "" {
			role = "user"
		}
		if role == "developer" {
			role = "system"
		}
		content := grokExtractTextContent(msg)
		if role == "tool" {
			if i < lastUserSourceIdx {
				continue
			}
			toolName, _ := msg["name"].(string)
			if toolName == "" {
				toolName = "unknown_tool"
			}
			callID, _ := msg["tool_call_id"].(string)
			if callID == "" {
				callID = "unknown_call"
			}
			content = fmt.Sprintf("CLIENT TOOL RESULT from caller runtime for %s (%s). Use this result to answer; do not call the same tool again:\n%s", toolName, callID, content)
		} else if role == "assistant" {
			if calls, ok := msg["tool_calls"].([]any); ok && len(calls) > 0 {
				if i < lastUserSourceIdx {
					continue
				}
				encoded, _ := json.Marshal(calls)
				content = strings.Join([]string{content, fmt.Sprintf("Previous assistant tool calls: %s", encoded)}, "\n")
			}
		}
		if strings.TrimSpace(content) == "" {
			continue
		}
		extracted = append(extracted, turn{role: role, text: content})
	}
	lastUserIdx := -1
	for i := len(extracted) - 1; i >= 0; i-- {
		if extracted[i].role == "user" {
			lastUserIdx = i
			break
		}
	}
	parts := make([]string, 0, len(extracted)+1)
	for i, t := range extracted {
		if i == lastUserIdx {
			parts = append(parts, t.text)
		} else {
			parts = append(parts, t.role+": "+t.text)
		}
	}
	if strings.TrimSpace(manifest) != "" {
		parts = append(parts, strings.TrimSpace(manifest))
	}
	return strings.Join(parts, "\n\n")
}

func grokFirstString(values ...any) string {
	for _, value := range values {
		if s, ok := value.(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func grokHasValue(value any) bool {
	return value != nil && value != ""
}

func grokArgString(args map[string]any, names ...string) any {
	for _, name := range names {
		if value, ok := args[name]; ok {
			return value
		}
	}
	return nil
}

// grokDefaultRequiredValue fills a required key the model left out from the
// values it did send, by the key's conventional meaning.
func grokDefaultRequiredValue(key string, args map[string]any, intent string) string {
	lower := strings.ToLower(key)
	command := grokFirstString(grokArgString(args, "command", "cmd", "shell", "input"))
	path := grokFirstString(grokArgString(args, "filePath", "file_path", "path", "filename"))
	query := grokFirstString(grokArgString(args, "query", "search", "input"))
	targetURL := grokFirstString(grokArgString(args, "url", "uri"))
	switch lower {
	case "command", "cmd":
		return command
	case "filepath", "file_path", "path":
		return path
	case "query", "search":
		return query
	case "url", "uri":
		return targetURL
	case "input":
		return grokFirstString(grokArgString(args, "query", "url", "command"), path)
	case "description", "reason":
		switch {
		case command != "":
			return "Execute shell command: " + command
		case path != "":
			return "Read file: " + path
		case targetURL != "":
			return "Fetch URL: " + targetURL
		case query != "":
			return "Search: " + query
		}
		return "Grok Web " + intent + " tool call"
	}
	return ""
}

// grokAdaptArguments renames Grok's native-card arguments onto the declared
// tool's schema and drops keys the schema does not know, so the client's
// validator accepts the call.
func grokAdaptArguments(toolName string, args map[string]any, registry grokToolRegistry, intent string) map[string]any {
	tool, ok := registry.toolsByName[toolName]
	if !ok {
		return args
	}
	properties := grokSchemaProperties(tool.parameters)
	required := grokSchemaRequired(tool.parameters)
	out := make(map[string]any, len(args)+len(required))
	for key, value := range args {
		out[key] = value
	}
	aliases := [][3]string{
		{"filePath", "filePath", "file_path"},
		{"file_path", "file_path", "filePath"},
		{"path", "path", "filePath"},
		{"query", "query", "search"},
		{"url", "url", "uri"},
		{"uri", "uri", "url"},
		{"input", "input", "query"},
	}
	for _, alias := range aliases {
		dest, first, second := alias[0], alias[1], alias[2]
		if _, has := properties[dest]; has && !grokHasValue(out[dest]) {
			if value := grokFirstString(grokArgString(args, first, second)); value != "" {
				out[dest] = value
			}
		}
	}
	for _, key := range required {
		if grokHasValue(out[key]) {
			continue
		}
		if value := grokDefaultRequiredValue(key, out, intent); value != "" {
			out[key] = value
		}
	}
	filtered := make(map[string]any, len(out))
	for key, value := range out {
		if _, known := properties[key]; known {
			filtered[key] = value
			continue
		}
		// required keys are kept even when absent from properties
		for _, requiredKey := range required {
			if requiredKey == key {
				filtered[key] = value
				break
			}
		}
	}
	return filtered
}

// grokToolScore scores one declared tool against a native-card intent.
func grokToolScore(tool grokClientTool, intent, lastUserText string) int {
	name := strings.ToLower(tool.name)
	description := strings.ToLower(tool.description)
	propNames := map[string]bool{}
	for propName := range grokSchemaProperties(tool.parameters) {
		propNames[strings.ToLower(propName)] = true
	}
	text := name + " " + description
	userText := strings.ToLower(lastUserText)
	score := 0

	switch intent {
	case "bash":
		if !grokIsTerminalTool(tool) {
			score -= 80
		}
		if name == "bash" {
			score += 100
		}
		switch name {
		case "shell", "terminal", "run_command", "execute_command", "exec", "command":
			score += 80
		}
		if propNames["command"] || propNames["cmd"] {
			score += 60
		}
		if regexp.MustCompile(`bash|shell|terminal|command|execute|run`).MatchString(text) {
			score += 25
		}
		if regexp.MustCompile(`read|search|grep|web|http|browser|context|note|memory`).MatchString(name) {
			score -= 50
		}
	case "readFile":
		if !grokIsFileReadTool(tool) {
			score -= 60
		}
		switch name {
		case "read", "read_file", "readfile", "file_read":
			score += 100
		}
		if propNames["filepath"] || propNames["file_path"] || propNames["path"] {
			score += 50
		}
		if regexp.MustCompile(`read.*file|file.*read|filesystem`).MatchString(text) {
			score += 25
		}
		if regexp.MustCompile(`write|edit|delete|remove|bash|shell|command`).MatchString(text) {
			score -= 50
		}
	case "webSearch", "browsePage":
		if grokIsContextMemoryTool(tool) || grokIsMetaOrInfrastructureTool(tool) {
			score -= 180
		}
		if intent == "browsePage" || grokWantsURLFetch(userText) {
			if !grokIsUrlFetchTool(tool) {
				score -= 60
			}
			if regexp.MustCompile(`webfetch|web_fetch|fetch|browse|browse_page|read_url|url_fetch|page`).MatchString(name) {
				score += 140
			}
			if propNames["url"] || propNames["uri"] {
				score += 90
			}
			if regexp.MustCompile(`fetch|browse|url|web page|page content|extract.*url|read.*url`).MatchString(text) {
				score += 55
			}
			if regexp.MustCompile(`websearch|web_search|search`).MatchString(name) && !propNames["url"] && !propNames["uri"] {
				score -= 80
			}
		}
		if intent == "webSearch" && !grokIsWebSearchTool(tool) {
			score -= 60
		}
		switch name {
		case "web_search", "websearch", "search":
			score += 100
		}
		if propNames["query"] || propNames["search"] {
			score += 50
		}
		if regexp.MustCompile(`web.*search|search.*web|internet|browse`).MatchString(text) {
			score += 25
		}
		if regexp.MustCompile(`file|bash|shell|command|write|edit`).MatchString(text) {
			score -= 50
		}
	}
	return score
}

func grokPickDeclaredTool(intent string, registry grokToolRegistry) string {
	bestName := ""
	bestScore := 0
	for name, tool := range registry.toolsByName {
		score := grokToolScore(tool, intent, registry.lastUserText)
		if score <= 0 {
			continue
		}
		if score > bestScore {
			bestName, bestScore = name, score
		}
	}
	return bestName
}

func grokCallID() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return "call_" + hex.EncodeToString(buf)
}

// grokOpenAIToolCall is one OpenAI tool_calls entry.
type grokOpenAIToolCall struct {
	ID       string                     `json:"id"`
	Type     string                     `json:"type"`
	Function grokOpenAIToolCallFunction `json:"function"`
}

type grokOpenAIToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// grokMapNativeToolCard maps a Grok native toolUsageCard onto a declared
// client tool. Returns nil when nothing matches.
func grokMapNativeToolCard(card map[string]any, cardID string, registry grokToolRegistry) *grokOpenAIToolCall {
	if !registry.enabled || card == nil {
		return nil
	}
	if cardID == "" {
		cardID = grokCallID()
	}

	tryIntent := func(intent string, rawArgs any) *grokOpenAIToolCall {
		args, _ := rawArgs.(map[string]any)
		if args == nil {
			return nil
		}
		name := grokPickDeclaredTool(intent, registry)
		if name == "" {
			return nil
		}
		adapted := grokAdaptArguments(name, args, registry, intent)
		if registry.executedToolKeys[grokSemanticToolKey(name, adapted)] {
			return nil
		}
		encoded, _ := json.Marshal(adapted)
		return &grokOpenAIToolCall{
			ID:   cardID,
			Type: "function",
			Function: grokOpenAIToolCallFunction{
				Name:      name,
				Arguments: string(encoded),
			},
		}
	}

	// bash / readFile / webSearch / browsePage, in upstream's order.
	type cardSlot struct {
		names  []string
		intent string
	}
	for _, slot := range []cardSlot{
		{names: []string{"bash"}, intent: "bash"},
		{names: []string{"readFile", "read_file"}, intent: "readFile"},
		{names: []string{"webSearch"}, intent: "webSearch"},
		{names: []string{"browsePage", "browse_page"}, intent: "browsePage"},
	} {
		for _, cardName := range slot.names {
			entry, _ := card[cardName].(map[string]any)
			if entry == nil {
				continue
			}
			if args, ok := entry["args"].(map[string]any); ok && args != nil {
				if call := tryIntent(slot.intent, args); call != nil {
					return call
				}
			}
		}
	}
	return nil
}

// ——— Markup fallback for arbitrary tools ———

var (
	grokToolCallOpen  = regexp.MustCompile(`<tool_call>`)
	grokToolCallClose = regexp.MustCompile(`</tool_call>`)
	grokMarkupOpenRE  = regexp.MustCompile(`<tool(?:_call)?$|<tool_call[^>]*$`)
)

// grokFindTagBlocks yields every <tool_call>…</tool_call> inner block.
func grokFindTagBlocks(text string) []string {
	var blocks []string
	for {
		open := grokToolCallOpen.FindStringIndex(text)
		if open == nil {
			return blocks
		}
		innerStart := open[0] + len("<tool_call>")
		rest := text[innerStart:]
		close := grokToolCallClose.FindStringIndex(rest)
		if close == nil {
			return blocks
		}
		blocks = append(blocks, rest[:close[0]])
		text = rest[close[0]+len("</tool_call>"):]
		if !strings.Contains(text, "<tool_call>") {
			return blocks
		}
	}
}

// grokNormalizeArbitraryArgs accepts an arguments object or a raw string.
func grokNormalizeArbitraryArgs(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		return typed
	case string:
		parsed := map[string]any{}
		if json.Unmarshal([]byte(typed), &parsed) == nil && parsed != nil {
			return parsed
		}
		return map[string]any{"input": typed}
	}
	return map[string]any{}
}

// grokParseClientToolCallMarkup extracts <tool_call> blocks naming declared
// tools; nil when the reply carries none.
func grokParseClientToolCallMarkup(text string, registry grokToolRegistry) []grokOpenAIToolCall {
	if !registry.enabled || !strings.Contains(text, "<tool_call>") {
		return nil
	}
	var calls []grokOpenAIToolCall
	for _, block := range grokFindTagBlocks(text) {
		parsed := map[string]any{}
		if json.Unmarshal([]byte(strings.TrimSpace(block)), &parsed) != nil || parsed == nil {
			continue
		}
		name, _ := parsed["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, declared := registry.toolsByName[name]; !declared {
			continue
		}
		args := grokAdaptArguments(name, grokNormalizeArbitraryArgs(parsed["arguments"]), registry, "clientTool")
		if registry.executedToolKeys[grokSemanticToolKey(name, args)] {
			continue
		}
		id, _ := parsed["id"].(string)
		if strings.TrimSpace(id) == "" {
			id = grokCallID()
		}
		encoded, _ := json.Marshal(args)
		calls = append(calls, grokOpenAIToolCall{
			ID:   id,
			Type: "function",
			Function: grokOpenAIToolCallFunction{
				Name:      name,
				Arguments: string(encoded),
			},
		})
	}
	return calls
}

// grokHasOpenToolCallMarkup reports whether a <tool_call> block may still be
// streaming in — the flusher holds text back while this is true.
func grokHasOpenToolCallMarkup(text string) bool {
	if grokMarkupOpenRE.MatchString(text) {
		return true
	}
	return strings.Contains(text, "<tool_call>") && !strings.Contains(text, "</tool_call>")
}

// grokStripToolCallMarkup removes tool-call blocks from content destined back
// to the client, the way upstream strips them on the non-streaming path.
func grokStripToolCallMarkup(text string) string {
	for strings.Contains(text, "<tool_call>") {
		open := grokToolCallOpen.FindStringIndex(text)
		if open == nil {
			break
		}
		innerStart := open[0] + len("<tool_call>")
		rest := text[innerStart:]
		close := grokToolCallClose.FindStringIndex(rest)
		if close == nil {
			break
		}
		text = text[:open[0]] + rest[close[0]+len("</tool_call>"):]
	}
	return strings.TrimSpace(text)
}
