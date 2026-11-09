package executor

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strconv"
	"strings"
)

// geminiWebInnerSlots is the width of the StreamGenerate request array. The
// site's client sends a fixed-width array with a few named slots and the rest
// left null; the width itself is part of what it validates, so it is named
// rather than written as a literal at each use.
const geminiWebInnerSlots = 81

// Slots the request actually sets. Every other slot stays null, which is what
// the site's own client sends.
const (
	// geminiWebSlotPrompt holds [text, …]. The prompt is the whole flattened
	// conversation; see geminiWebMessagesToPrompt.
	geminiWebSlotPrompt = 0
	// geminiWebSlotLanguage is the requested answer language.
	geminiWebSlotLanguage = 1
	// geminiWebSlotMetadata is the continuation tuple (conversation id, reply
	// id, …). Left as the empty shape: turns are self-contained here.
	geminiWebSlotMetadata = 2
	// geminiWebSlotRequestUUID echoes the uuid also sent in the
	// x-goog-ext-525005358-jspb header, and is what the site correlates the two on.
	geminiWebSlotRequestUUID = 59
	// geminiWebSlotModelNumber names which model of the account's tier answers.
	geminiWebSlotModelNumber = 79
	// geminiWebSlotThinkingLevel selects Gemini's reasoning level.
	geminiWebSlotThinkingLevel = 80
)

// geminiWebThinkingLevels maps a requested effort onto the enum the site uses.
// The values are not ordered: the site's own "standard" level is 1, "extended"
// is 2 and "medium" is 4, so they are named rather than computed.
var geminiWebThinkingLevels = map[string]int{
	"low":    1,
	"medium": 4,
	"high":   2,
}

// geminiWebThinkingLevel resolves the reasoning level for a turn from the body's
// reasoning fields. An absent or unrecognized value means the model's own
// default rather than a guess.
//
// The budget is a client's own unit and the site has no budget field, so it
// is mapped onto the three levels the site does have: a budget under 4096 is
// the site's low, one under 16384 its medium, and anything larger its high.
func geminiWebThinkingLevel(body geminiWebRequestEnvelope, model *geminiWebModel) int {
	if level, ok := geminiWebThinkingLevels[strings.ToLower(strings.TrimSpace(body.ReasoningEffort))]; ok {
		return level
	}
	if body.Thinking != nil {
		if *body.Thinking {
			return geminiWebThinkingLevels["high"]
		}
		return geminiWebThinkingLevels["low"]
	}
	if body.ThinkingBudget != nil {
		switch {
		case *body.ThinkingBudget <= 4096:
			return geminiWebThinkingLevels["low"]
		case *body.ThinkingBudget <= 16384:
			return geminiWebThinkingLevels["medium"]
		default:
			return geminiWebThinkingLevels["high"]
		}
	}
	return geminiWebDefaultThinkingLevel(model)
}

// geminiWebBuildInner renders the request array for one turn.
//
// Slot 7 asks the site to stream tokens, slot 27 to return routing early, and
// slot 11 to strip the consumer extensions (Workspace, Maps, YouTube) so they
// cannot answer in place of the model.
//
// Slot 79 names the model of the account's tier. It used to be hardcoded to
// 1, which is the Flash-Lite number every anonymous session serves; a
// signed-in session has other models and the registry reports their numbers,
// so the resolved model's number goes here.
func geminiWebBuildInner(prompt string, thinkingLevel int, modelNumber int, attachments []geminiWebAttachment) []any {
	if thinkingLevel <= 0 {
		thinkingLevel = geminiWebThinkingLevels["low"]
	}
	if modelNumber <= 0 {
		modelNumber = 1
	}
	inner := make([]any, geminiWebInnerSlots)
	// Slot 0 carries the prompt and, in its fourth member, the attachments
	// the model is asked to look at. The site's own composer sends the
	// uploaded paths there, so that is the shape it accepts.
	inner[geminiWebSlotPrompt] = []any{prompt, 0, nil, geminiWebAttachmentSlot(attachments), nil, nil, 0}
	inner[geminiWebSlotLanguage] = []string{"en"}
	inner[geminiWebSlotMetadata] = []any{"", "", "", nil, nil, nil, nil, nil, nil, ""}
	inner[6] = []int{1}
	inner[7] = 1  // stream tokens
	inner[10] = 1 // request source
	inner[11] = 1 // strip consumer extensions
	inner[17] = [][]int{{0}}
	inner[18] = 0
	inner[27] = 1 // return routing early
	inner[30] = []int{4}
	inner[41] = []int{1}
	inner[53] = 0
	inner[61] = []any{}
	inner[67] = 1 // personal context off
	inner[68] = 1
	inner[geminiWebSlotModelNumber] = modelNumber
	inner[geminiWebSlotThinkingLevel] = thinkingLevel
	return inner
}

// geminiWebAttachmentSlot renders the attachments for slot 0.
//
// The slot holds a list of [path, filename] tuples, or nothing at all when
// the turn has no image — the site's own client sends null there, not an
// empty list.
func geminiWebAttachmentSlot(attachments []geminiWebAttachment) any {
	if len(attachments) == 0 {
		return nil
	}
	slot := make([]any, 0, len(attachments))
	for _, attachment := range attachments {
		slot = append(slot, attachment.slot())
	}
	return slot
}

// geminiWebBuildModelHeader renders the x-goog-ext-525001261-jspb header that
// names the model to answer with.
//
// Each recognized model has a different header shape upstream, selected by the
// capacity field the model registry reports. Sending the wrong one is not
// rejected — the site silently answers with a different model — which is why
// the caller verifies the identity that comes back.
func geminiWebBuildModelHeader(model geminiWebModel, thinkingLevel int, fSid string) string {
	if thinkingLevel <= 0 {
		thinkingLevel = geminiWebThinkingLevels["low"]
	}
	trailer := []any{1, nil, nil, nil, model.ID, nil, nil, 0, []int{4, 5, 6, 8}, nil, nil}
	if model.CapacityField == 13 {
		trailer = append(trailer, nil, model.Capacity, nil, nil, model.Number, thinkingLevel, fSid)
	} else {
		capacity := model.Capacity
		if capacity <= 0 {
			capacity = 1
		}
		number := model.Number
		if number <= 0 {
			number = 1
		}
		trailer = append(trailer, capacity, nil, nil, number, thinkingLevel, fSid)
	}
	encoded, err := json.Marshal(trailer)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// geminiWebRequestEnvelope is the subset of the inbound body this executor
// reads. Everything outside it is dropped: the site accepts only the prompt, so
// temperature, top_p and the rest have nowhere to go.
//
// Messages reuse deepseek-web's shape and its transcript builder renders the
// prompt, so both web providers read a client's history the same way instead of
// each inventing a rendering.
type geminiWebRequestEnvelope struct {
	Model           string             `json:"model"`
	Messages        []deepSeekMessage  `json:"messages"`
	Tools           []deepSeekToolSpec `json:"tools"`
	ToolChoice      jsontext.Value     `json:"tool_choice"`
	ReasoningEffort string             `json:"reasoning_effort"`
	Thinking        *bool              `json:"thinking"`
	ThinkingBudget  *int               `json:"thinking_budget"`
}

// geminiWebMessagesToPrompt renders the conversation as the single prompt string
// StreamGenerate accepts.
//
// The auto history window applies: a genuinely multi-turn conversation replays a
// transcript, while a single-turn request collapses to just its user message. This
// provider's turns are self-contained (see geminiWebSlotMetadata), so nothing here
// needs the frozen-transcript rendering that a session-persisting caller does.
//
// The turns are sanitized on the way in. The site takes one flat prompt, so a
// line that claims a role reads as that role's turn, and a replayed assistant
// turn that carries the model's own reasoning makes the model continue that
// reasoning rather than answer.
func geminiWebMessagesToPrompt(messages []deepSeekMessage) string {
	sanitized := make([]deepSeekMessage, 0, len(messages))
	for _, message := range messages {
		text := deepSeekMessageText(message.Content)
		switch message.Role {
		case "user", "tool":
			text = geminiWebSanitizeRoleLine(text)
		case "assistant":
			text = geminiWebStripReasoning(text)
		}
		message.Content = geminiWebTextContent(text)
		sanitized = append(sanitized, message)
	}
	return strings.TrimSpace(deepSeekMessagesToPrompt(sanitized, 0, false))
}

// geminiWebTextContent puts a plain string back into the content slot.
func geminiWebTextContent(text string) jsontext.Value {
	encoded, err := json.Marshal(text)
	if err != nil {
		return nil
	}
	return jsontext.Value(encoded)
}

// geminiWebCatalogAlias maps the catalog model ids this provider advertises onto
// the distinguishing token of the model the site's registry reports.
//
// The catalog names a model by product line ("gemini-3.1-flash-lite"), while the
// registry reports a hex id, a category ("Flash-Lite") and a display name
// ("3.5 Flash-Lite"). There is no shared identifier to join on, so the match is
// on the trailing product tokens — the one part both spellings agree on. A model
// that does not appear in the registry is refused rather than sent: the site
// answers an unknown model with a silent substitution, so sending one and hoping
// returns another model's answer under the requested model's name.
func geminiWebCatalogAlias(model string) string {
	trimmed := strings.TrimSpace(strings.ToLower(model))
	trimmed = strings.TrimPrefix(trimmed, "gemini-")
	tokens := strings.Split(trimmed, "-")
	// Drop the version prefix ("3.1", "3.7") — it moves with every release and
	// never appears in the registry's category.
	for len(tokens) > 1 && tokens[0] != "" && tokens[0][0] >= '0' && tokens[0][0] <= '9' {
		tokens = tokens[1:]
	}
	if len(tokens) == 0 {
		return ""
	}
	return strings.Join(tokens, "-")
}

// geminiWebModelMatches reports whether a registry entry is the model a catalog
// id names.
func geminiWebModelMatches(model geminiWebModel, alias string) bool {
	if alias == "" {
		return false
	}
	for _, candidate := range []string{model.Category, model.DisplayName} {
		slug := strings.FieldsFunc(strings.ToLower(candidate), func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
		})
		for i := range slug {
			if len(slug[i:]) > 0 && strings.HasSuffix(alias, strings.Join(slug[i:], "-")) {
				return true
			}
		}
	}
	return false
}

// geminiWebResolveModel picks the registry entry a request should be answered
// by. It returns nil when the site does not offer the model, which the caller
// reports with the list it does offer.
func geminiWebResolveModel(models []geminiWebModel, requested string) *geminiWebModel {
	alias := geminiWebCatalogAlias(requested)
	for i := range models {
		if geminiWebModelMatches(models[i], alias) {
			return &models[i]
		}
	}
	return nil
}

// geminiWebAvailableModels renders the registry's model list for an error
// message, so a refusal names what the account can actually answer with.
func geminiWebAvailableModels(models []geminiWebModel) string {
	names := make([]string, 0, len(models))
	for _, model := range models {
		name := model.Category
		if name == "" {
			name = model.DisplayName
		}
		if name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// geminiWebComputeCapacity derives the tier capacity and the field number the
// model header has to carry it in, from the two flag lists the registry returns.
//
// The mapping is the site's: the flag values are tier markers, and upstream's
// own executor reads them the same way.
func geminiWebComputeCapacity(tierFlags, capFlags []int) (capacity, capacityField int) {
	has := func(flags []int, want int) bool {
		for _, flag := range flags {
			if flag == want {
				return true
			}
		}
		return false
	}
	switch {
	case has(tierFlags, 21):
		return 1, 13
	case has(tierFlags, 22):
		return 2, 13
	case has(capFlags, 115):
		return 4, 12
	case has(tierFlags, 16), has(capFlags, 106):
		return 3, 12
	case has(tierFlags, 8), has(capFlags, 19):
		return 2, 12
	default:
		return 1, 12
	}
}

// geminiWebIntList reads a numeric list field, ignoring anything that is not a
// number. A registry that grows a field here must not fail the turn.
func geminiWebIntList(value any) []int {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]int, 0, len(items))
	for _, item := range items {
		if number, ok := item.(float64); ok {
			out = append(out, int(number))
		}
	}
	return out
}

// geminiWebStringAt reads a string field with bounds checking.
func geminiWebStringAt(arr []any, index int) string {
	if index < 0 || index >= len(arr) {
		return ""
	}
	value, _ := arr[index].(string)
	return value
}

// geminiWebIntAt reads a numeric field with bounds checking.
func geminiWebIntAt(arr []any, index int) (int, bool) {
	if index < 0 || index >= len(arr) {
		return 0, false
	}
	value, ok := arr[index].(float64)
	return int(value), ok
}

// geminiWebParseModelRegistry reads the otAQ7b payload into the model list, the
// tier flags that decide the capacity field, and the thinking policy that
// says which levels each model offers.
func geminiWebParseModelRegistry(payload any) ([]geminiWebModel, []int, []int, bool) {
	arr, ok := payload.([]any)
	if !ok || len(arr) <= 15 {
		return nil, nil, nil, false
	}
	rawModels, ok := arr[15].([]any)
	if !ok || len(rawModels) == 0 {
		return nil, nil, nil, false
	}
	tierFlags := geminiWebIntList(arr[16])
	capFlags := geminiWebIntList(arr[17])
	capacity, capacityField := geminiWebComputeCapacity(tierFlags, capFlags)
	policy, policyKnown := geminiWebParseThinkingPolicy(geminiWebPayloadAt(arr, 24))

	models := make([]geminiWebModel, 0, len(rawModels))
	for _, entryAny := range rawModels {
		entry, ok := entryAny.([]any)
		if !ok || len(entry) == 0 {
			continue
		}
		id := geminiWebStringAt(entry, 0)
		if id == "" {
			continue
		}
		category := geminiWebStringAt(entry, 1)
		displayName := geminiWebStringAt(entry, 11)
		if displayName == "" {
			displayName = geminiWebStringAt(entry, 19)
		}
		description := geminiWebStringAt(entry, 12)
		if description == "" {
			description = geminiWebStringAt(entry, 2)
		}
		number, ok := geminiWebIntAt(entry, 17)
		if !ok {
			number, _ = geminiWebIntAt(entry, 9)
		}
		levels := policy[number]
		models = append(models, geminiWebModel{
			ID:                  id,
			Category:            category,
			DisplayName:         displayName,
			Description:         description,
			Number:              number,
			Capacity:            capacity,
			CapacityField:       capacityField,
			ThinkingLevels:      levels,
			ThinkingPolicyKnown: policyKnown,
		})
	}
	return models, tierFlags, capFlags, true
}

// geminiWebModel is one entry of the account's live model registry.
type geminiWebModel struct {
	ID            string
	Category      string
	DisplayName   string
	Description   string
	Number        int
	Capacity      int
	CapacityField int
	// ThinkingLevels are the reasoning levels the registry says this model
	// offers. It is empty when the registry published no policy, in which
	// case the level is not constrained by the registry.
	ThinkingLevels []string
	// ThinkingPolicyKnown reports that the registry published a thinking
	// policy and simply did not list this model. That is different from a
	// registry with no policy at all: a listed model genuinely has no
	// thinking levels, while an absent policy means the level cannot be
	// constrained from the registry.
	ThinkingPolicyKnown bool
}

// geminiWebPayloadAt reads one field of a payload array, bounds-checked.
func geminiWebPayloadAt(arr []any, index int) any {
	if index < 0 || index >= len(arr) {
		return nil
	}
	return arr[index]
}

// geminiWebRequestID mints the _reqid the site expects to increase per call.
// It only has to be distinctive and increasing, so a counter seeded from the
// clock is enough.
func geminiWebRequestID() string {
	return strconv.FormatInt(geminiWebReqID.Add(7), 10)
}
