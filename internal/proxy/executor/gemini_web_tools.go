package executor

import (
	"encoding/json/jsontext"
	"fmt"
	"strings"

	json "encoding/json/v2"
)

// Tool calling on Gemini Web.
//
// The site has no tool channel of its own: a tool declaration reaches the
// model as prompt text, and the model answers with a JSON object that this
// file reads back. That is the same contract deepseek-web uses, and the same
// parser recovers the calls, so the two web providers read a client's tool
// list the same way rather than each inventing a rendering.
//
// What this file adds over the shared renderer is the tool_choice. The
// shared one renders a list of tools and says the model may use them; a client
// that asked for a *specific* tool, or required one, needs the prompt to say
// so — otherwise the model may answer in prose and the client never gets the
// call it asked for.
//
// Reference: azrinaldi/gemini-stealth-bridge internal/toolbridge/contract.go
// (BuildToolInstructions), which appends the same two rules.
func geminiWebRenderTools(tools []deepSeekToolSpec, toolChoice jsontext.Value) string {
	// "none" means the client declared tools it does not want called. The
	// declarations are not rendered at all: telling the model about a tool
	// it must not use is how a client ends up with a call it did not ask for.
	if geminiWebToolChoice(toolChoice) == geminiWebToolChoiceNone {
		return ""
	}
	var b strings.Builder
	b.WriteString(deepSeekRenderTools(tools))
	switch geminiWebToolChoice(toolChoice) {
	case geminiWebToolChoiceRequired:
		b.WriteString("\n\nYou MUST call one of the tools above in this reply. " +
			"Answering in prose is a failure — the caller is waiting for a tool call.\n")
	case geminiWebToolChoiceNamed:
		name := geminiWebToolChoiceName(toolChoice)
		fmt.Fprintf(&b, "\n\nYou MUST call the tool %q in this reply. "+
			"Answering in prose is a failure — the caller is waiting for that call.\n", name)
	}
	return b.String()
}

// geminiWebToolChoiceMode is what the client asked the model to do.
type geminiWebToolChoiceMode int

const (
	// geminiWebToolChoiceAuto leaves the choice to the model.
	geminiWebToolChoiceAuto geminiWebToolChoiceMode = iota
	// geminiWebToolChoiceNone forbids any tool call.
	geminiWebToolChoiceNone
	// geminiWebToolChoiceRequired demands a call, of any declared tool.
	geminiWebToolChoiceRequired
	// geminiWebToolChoiceNamed demands one specific tool.
	geminiWebToolChoiceNamed
)

// geminiWebToolChoice reads the client's tool_choice.
//
// Three spellings exist: the plain string ("none", "auto", "required"), the
// OpenAI object ({"type":"function","function":{"name":"x"}}), and the
// Responses spelling ({"type":"function","name":"x"}). Anything else is
// "auto" — an unknown choice is not a reason to refuse the request.
func geminiWebToolChoice(toolChoice jsontext.Value) geminiWebToolChoiceMode {
	if len(toolChoice) == 0 {
		return geminiWebToolChoiceAuto
	}
	var asString string
	if err := json.Unmarshal(toolChoice, &asString); err == nil {
		switch strings.ToLower(strings.TrimSpace(asString)) {
		case "none":
			return geminiWebToolChoiceNone
		case "required", "any":
			return geminiWebToolChoiceRequired
		default:
			return geminiWebToolChoiceAuto
		}
	}
	var asObject struct {
		Type     string `json:"type"`
		Name     string `json:"name"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(toolChoice, &asObject); err != nil {
		return geminiWebToolChoiceAuto
	}
	if strings.EqualFold(asObject.Type, "function") {
		if asObject.Function.Name != "" {
			return geminiWebToolChoiceNamed
		}
		if asObject.Name != "" {
			return geminiWebToolChoiceNamed
		}
		return geminiWebToolChoiceRequired
	}
	return geminiWebToolChoiceAuto
}

// geminiWebToolChoiceName reads the tool a named choice points at.
func geminiWebToolChoiceName(toolChoice jsontext.Value) string {
	var asObject struct {
		Name     string `json:"name"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(toolChoice, &asObject); err != nil {
		return ""
	}
	if asObject.Function.Name != "" {
		return asObject.Function.Name
	}
	return asObject.Name
}

// geminiWebValidToolArguments checks a recovered call's arguments against
// the schema the client declared.
//
// The site has no tool channel, so the model writes the call as text and
// this file reads it back. A model that writes its arguments as a string
// instead of an object, or that leaves out a required property, produces a
// call the client cannot run — and the client only finds out when it tries.
// A call that violates its own schema is dropped instead, so the answer the
// client sees is the model's prose rather than a broken call.
//
// The check is the part of the JSON Schema that bites in practice: the
// arguments have to be an object, and the required properties have to be
// there. A schema that says nothing about either is not enforced.
//
// Reference: azrinaldi/gemini-stealth-bridge internal/toolbridge/schema.go
// (CheckSchema) — the shape of the check, narrowed to what a tool call
// actually needs.
func geminiWebValidToolArguments(tools []deepSeekToolSpec, name string, arguments string) bool {
	schema := geminiWebToolSchema(tools, name)
	if len(schema) == 0 {
		return true
	}
	var declared struct {
		Type       string                    `json:"type"`
		Properties map[string]jsontext.Value `json:"properties"`
		Required   []string                  `json:"required"`
	}
	if err := json.Unmarshal(schema, &declared); err != nil {
		// A schema this build cannot read is not a reason to drop the call.
		return true
	}
	wantsObject := declared.Type == "object" || len(declared.Properties) > 0
	if !wantsObject && len(declared.Required) == 0 {
		return true
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(arguments), &parsed); err != nil {
		return false
	}
	for _, required := range declared.Required {
		if _, present := parsed[required]; !present {
			return false
		}
	}
	return true
}

// geminiWebToolSchema reads the schema a declared tool carries.
func geminiWebToolSchema(tools []deepSeekToolSpec, name string) jsontext.Value {
	for _, tool := range tools {
		if tool.Function.Name == name {
			return tool.Function.Parameters
		}
	}
	return nil
}
