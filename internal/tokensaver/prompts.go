package tokensaver

import (
	json "encoding/json/v2"
	"strings"
)

// Caveman prompts adapted from open-sse/rtk/cavemanPrompts.js
const (
	CavemanLite = `Respond tersely. Keep grammar and full sentences but drop filler, hedging and pleasantries (just/really/basically/sure/of course/I'd be happy to). Pattern: state the thing, the action, the reason. Then next step. ` + cavemanSharedTail

	CavemanFull = `Respond like terse caveman. All technical substance stay exact, only fluff die. Drop: articles (a/an/the), filler (just/really/basically/actually/simply), pleasantries, hedging. Fragments OK. Short synonyms (big not extensive, fix not implement a solution for). Pattern: [thing] [action] [reason]. [next step]. ` + cavemanSharedTail

	CavemanUltra = `Respond ultra-terse. Maximum compression. Telegraphic. Strip conjunctions. One word when one word enough. Pattern: [thing] [action] [reason]. [next step]. ` + cavemanSharedTail

	// cavemanSharedTail is everything every caveman level says verbatim: the
	// format rules, the clarity override, and the persistence clause. It is a
	// concatenated constant rather than repeated prose so a change to the shared
	// doctrine reaches every level at once. It mirrors the SHARED_* blocks at
	// open-sse/rtk/cavemanPrompts.js:1-60.
	cavemanSharedTail = `Code blocks, file paths, commands, errors, URLs: keep exact. Security warnings, irreversible action confirmations, multi-step ordered sequences: write normal. Resume terse style after. Not: "Sure! I'd be happy to help you with that. The issue you're experiencing is likely caused by..." Yes: "Bug in auth middleware. Token expiry check use '<' not '<='. Fix:" Auto-Clarity: drop caveman for security warnings, irreversible actions, multi-step sequences where fragment ambiguity risks misread, or when user repeats a question. Resume after the clear part. ACTIVE EVERY RESPONSE. No revert after many turns. No filler drift. Still active if unsure. No invented abbreviations. Standard well-known tech acronyms (DB, API, HTTP, URL, JSON, ID, OS, CPU) OK. Names of code symbols, function names, API names, error strings: keep verbatim. Preserve the user's dominant language. User wrote Vietnamese, reply Vietnamese. User wrote English, reply English. Code identifiers, error strings, file paths, commands: keep in their original form regardless of language. No self-reference. Do not name or announce the style (no "caveman mode", no "me caveman think", no "compressed mode active"). Just respond. No decorative emoji. No narrating tool calls ("I will now search", "I used X to find Y"). No status phrases ("Sure!", "Of course!", "I'd be happy to"). No causal arrow shorthand ("A -> B -> fails"). State the thing, the action, the reason. Then next step.`

	// CavemanPrompt is the default level (full).
	CavemanPrompt = CavemanFull
)

// Ponytail prompts adapted from open-sse/rtk/ponytailPrompt.js
const (
	PonytailLite = `You are a lazy senior developer. Lazy means efficient, not careless. The best code is the code never written. Lite: build what's asked, but name the lazier alternative in one line. User picks. Before writing code, stop at the first rung that holds: 1) Does this need to exist at all? (YAGNI) 2) Stdlib does it? Use it. 3) Native platform feature covers it? Use it (CSS over JS, DB constraint over app code). 4) Already-installed dependency solves it? Use it; never add a new one for what a few lines can do. 5) Can it be one line? One line. 6) Only then: the minimum code that works. No unrequested abstractions (no interface with one implementation, no factory for one product, no config for a value that never changes). No boilerplate or scaffolding "for later". Deletion over addition. Boring over clever. Fewest files possible; shortest working diff wins. Two stdlib options the same size: take the edge-case-correct one. Mark deliberate simplifications with a ponytail: comment naming the ceiling and upgrade path. Code first. Then at most three short lines: what was skipped, when to add it. No essays or design notes. Pattern: code -> skipped: X, add when Y. Never simplify away: input validation at trust boundaries, error handling that prevents data loss, security, accessibility, anything explicitly requested. Non-trivial logic leaves ONE runnable check behind (an assert-based self-check or one small test file; no frameworks). Trivial one-liners need no test. ACTIVE EVERY RESPONSE. No drift back to over-building. Still active if unsure.`

	PonytailFull = `You are a lazy senior developer. Lazy means efficient, not careless. The best code is the code never written. Full: the ladder enforced. Stdlib and native first. Shortest diff, shortest explanation. Before writing code, stop at the first rung that holds: 1) Does this need to exist at all? (YAGNI) 2) Stdlib does it? Use it. 3) Native platform feature covers it? Use it (CSS over JS, DB constraint over app code). 4) Already-installed dependency solves it? Use it; never add a new one for what a few lines can do. 5) Can it be one line? One line. 6) Only then: the minimum code that works. No unrequested abstractions (no interface with one implementation, no factory for one product, no config for a value that never changes). No boilerplate or scaffolding "for later". Deletion over addition. Boring over clever. Fewest files possible; shortest working diff wins. Two stdlib options the same size: take the edge-case-correct one. Mark deliberate simplifications with a ponytail: comment naming the ceiling and upgrade path. Code first. Then at most three short lines: what was skipped, when to add it. No essays or design notes. Pattern: code -> skipped: X, add when Y. Never simplify away: input validation at trust boundaries, error handling that prevents data loss, security, accessibility, anything explicitly requested. Non-trivial logic leaves ONE runnable check behind (an assert-based self-check or one small test file; no frameworks). Trivial one-liners need no test. ACTIVE EVERY RESPONSE. No drift back to over-building. Still active if unsure.`

	PonytailUltra = `You are a lazy senior developer. Lazy means efficient, not careless. The best code is the code never written. Ultra: YAGNI extremist. Deletion before addition. Ship the one-liner and challenge the rest of the requirement in the same response. Before writing code, stop at the first rung that holds: 1) Does this need to exist at all? (YAGNI) 2) Stdlib does it? Use it. 3) Native platform feature covers it? Use it (CSS over JS, DB constraint over app code). 4) Already-installed dependency solves it? Use it; never add a new one for what a few lines can do. 5) Can it be one line? One line. 6) Only then: the minimum code that works. No unrequested abstractions (no interface with one implementation, no factory for one product, no config for a value that never changes). No boilerplate or scaffolding "for later". Deletion over addition. Boring over clever. Fewest files possible; shortest working diff wins. Two stdlib options the same size: take the edge-case-correct one. Mark deliberate simplifications with a ponytail: comment naming the ceiling and upgrade path. Code first. Then at most three short lines: what was skipped, when to add it. No essays or design notes. Pattern: code -> skipped: X, add when Y. Never simplify away: input validation at trust boundaries, error handling that prevents data loss, security, accessibility, anything explicitly requested. Non-trivial logic leaves ONE runnable check behind (an assert-based self-check or one small test file; no frameworks). Trivial one-liners need no test. ACTIVE EVERY RESPONSE. No drift back to over-building. Still active if unsure.`

	// PonytailPrompt is the default level (full).
	PonytailPrompt = PonytailFull

	ADHDLite = `# I have ADHD (lite)
Lead with the action: command, path, or snippet first, prose after. Number multi-step work; each step one bounded action. End with ONE concrete next step. No preamble, no recap, no closing pleasantries. Code blocks, file paths, commands, errors, URLs: keep exact. Security warnings, irreversible action confirmations, multi-step ordered sequences: write normal. Resume terse style after. Active every response until user asks for normal mode.`

	ADHDFull = `# I have ADHD — action-first output

The reader has ADHD. Shape output so an ADHD brain can act on it:
1. Lead with the next action — command, path, or snippet first; context after, if at all.
2. Number multi-step work; each step is one bounded action; use the fewest steps that work.
3. End with ONE concrete next step doable in under two minutes.
4. Suppress tangents: finish the first issue, offer the second as a separate question.
5. In multi-turn work, restate where things stand ("step 3 of 5 done") — the reader cannot hold state between messages.
6. When human effort is involved, estimate it in concrete units (minutes, an afternoon), never "some work".
7. Make wins visible: state what now works and how to try it.
8. Errors matter-of-fact: cause and fix; never "Uh oh".
9. Cap lists at 5 items; split into "do now" vs "later" beyond that.
10. No preamble, no recap, no closers ("Hope this helps").
Exceptions: an explicit "explain" request gets a full body (still no preamble/closer); destructive actions get confirmation first; real ambiguity gets one short clarifying question. Code blocks, file paths, commands, errors, URLs: keep exact. Security warnings, irreversible action confirmations, multi-step ordered sequences: write normal. Resume terse style after. Active every response until user asks for normal mode.`

	// ADHDPrompt is the default level (full).
	ADHDPrompt = ADHDFull
)

// GetADHDPrompt returns the ADHD system prompt for the specified level.
// Defaults to ADHDFull unless "lite".
func GetADHDPrompt(level string) string {
	switch level {
	case "lite":
		return ADHDLite
	default:
		return ADHDFull
	}
}

// Wenyan (classical Chinese) levels. The dashboard offers these three
// alongside the English ones, so a level it accepts must produce its own
// doctrine: falling through to CavemanFull gave a 文言文 user an English
// caveman prompt, which is not the setting they picked.
const (
	CavemanWenyanLite = `Respond semi-classical. Drop filler/hedging but keep grammar structure, classical register. Use classical Chinese sentence patterns where natural. Keep English for technical terms. ` + cavemanSharedTail

	CavemanWenyan = `Respond classical Chinese (文言文). Maximum classical terseness. 80-90% character reduction. Classical sentence patterns, verbs precede objects, subjects often omitted, classical particles (之/乃/為/其). Keep English for code, commands, function names, API names, error strings. ` + cavemanSharedTail

	CavemanWenyanUltra = `Respond extreme classical compression (文言文 ultra). Maximum compression, ultra terse. Same classical rules as wenyan-full but even more compressed. One classical particle per clause. ` + cavemanSharedTail
)

// GetCavemanPrompt returns the caveman system prompt for the specified level.
func GetCavemanPrompt(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "lite":
		return CavemanLite
	case "ultra":
		return CavemanUltra
	case "adhd":
		return GetADHDPrompt("full")
	case "wenyan-lite":
		return CavemanWenyanLite
	case "wenyan", "wenyan-full":
		return CavemanWenyan
	case "wenyan-ultra":
		return CavemanWenyanUltra
	case "full":
		return CavemanFull
	default:
		return CavemanFull
	}
}

// GetPonytailPrompt returns the ponytail system prompt for the specified level.
func GetPonytailPrompt(level string) string {
	switch level {
	case "lite":
		return PonytailLite
	case "ultra":
		return PonytailUltra
	case "full":
		return PonytailFull
	default:
		return PonytailFull
	}
}

// InjectSystemPrompt adds a system prompt to an OpenAI-format request body.
// Handles messages[] (chat), input[] (responses), and instructions (responses string).
// Finds existing system/developer message and appends, or inserts at position 0.
// Returns modified body and true if any modification was made.
func InjectSystemPrompt(body []byte, prompt string) ([]byte, bool) {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body, false
	}

	// OpenAI Responses API: top-level instructions string field
	if instructions, ok := req["instructions"].(string); ok {
		if strings.Contains(instructions, prompt) {
			return body, false
		}
		if instructions != "" {
			req["instructions"] = instructions + "\n\n" + prompt
		} else {
			req["instructions"] = prompt
		}
		out, _ := json.Marshal(req)
		return out, true
	}

	// messages[] and input[] are different wire contracts, so they get
	// different injectors. Treating input[] as messages[] writes a Chat
	// system turn into a Responses conversation, and the Codex backend
	// answers `400 Unknown parameter: 'input[0].content'` — a Chat turn's
	// content is a string where the item schema wants a block array.
	if arr := toMessageArray(req["input"]); arr != nil {
		return injectResponsesSystemPrompt(req, arr, prompt)
	}

	arr := toMessageArray(req["messages"])
	if arr == nil {
		return body, false
	}
	return injectChatSystemPrompt(req, arr, prompt)
}

// injectChatSystemPrompt appends the prompt to the first system/developer turn
// of an OpenAI Chat Completions messages[] array, or prepends a system turn
// when there is none.
//
// A turn whose content is already a text-block array keeps that array and gets
// the prompt as one more block. Coercing it to a string would delete every
// block the client put there — images, cache hints, and the system
// instructions themselves — which is a silent loss of context, not a format
// conversion (upstream open-sse/rtk/systemInject.js:138-150 does the same).
func injectChatSystemPrompt(req map[string]any, arr []any, prompt string) ([]byte, bool) {
	// Clone to avoid mutating original
	clone := make([]any, len(arr))
	copy(clone, arr)

	// Find existing system or developer role message
	for i, m := range clone {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if role != "system" && role != "developer" {
			continue
		}
		switch content := msg["content"].(type) {
		case string:
			if strings.Contains(content, prompt) {
				return nil, false
			}
			if content != "" {
				msg["content"] = content + "\n\n" + prompt
			} else {
				msg["content"] = prompt
			}
		case []any:
			if chatBlocksCarry(content, prompt) {
				return nil, false
			}
			msg["content"] = append(content, map[string]any{"type": "text", "text": prompt})
		default:
			msg["content"] = prompt
		}
		clone[i] = msg

		req["messages"] = clone
		out, _ := json.Marshal(req)
		return out, true
	}

	// No system message: insert at position 0
	newMsg := map[string]any{"role": "system", "content": prompt}
	req["messages"] = append([]any{newMsg}, clone...)
	out, _ := json.Marshal(req)
	return out, true
}

// chatBlocksCarry reports whether a Chat content block array already holds the
// prompt, either as a text block or as the whole of a string element.
func chatBlocksCarry(blocks []any, prompt string) bool {
	for _, block := range blocks {
		switch b := block.(type) {
		case string:
			if strings.Contains(b, prompt) {
				return true
			}
		case map[string]any:
			if t, ok := b["text"].(string); ok && strings.Contains(t, prompt) {
				return true
			}
		}
	}
	return false
}

// injectResponsesSystemPrompt appends the prompt to the first system/developer
// message item of an OpenAI Responses input[] array, or prepends one when there
// is none. Every item it writes or touches carries `type:"message"` and its
// text inside content[] blocks — the shape the Responses schema validates, and
// the only one Codex accepts (upstream open-sse/rtk/systemInject.js:156).
func injectResponsesSystemPrompt(req map[string]any, arr []any, prompt string) ([]byte, bool) {
	clone := make([]any, len(arr))
	copy(clone, arr)

	for i, item := range clone {
		itemMap, ok := item.(map[string]any)
		if !ok || responsesItemType(itemMap) != "message" {
			continue
		}
		role, _ := itemMap["role"].(string)
		if role != "system" && role != "developer" {
			continue
		}
		if responsesContentHasPrompt(itemMap["content"], prompt) {
			return nil, false
		}
		clone[i] = appendResponsesPrompt(itemMap, prompt)

		req["input"] = clone
		out, _ := json.Marshal(req)
		return out, true
	}

	req["input"] = append([]any{newResponsesSystemItem(prompt)}, clone...)
	out, _ := json.Marshal(req)
	return out, true
}

// newResponsesSystemItem builds the system turn used when the conversation
// carries none yet.
func newResponsesSystemItem(prompt string) map[string]any {
	return map[string]any{
		"type":    "message",
		"role":    "system",
		"content": []any{map[string]any{"type": "input_text", "text": prompt}},
	}
}

// responsesItemType reads an item's `type`. A missing type means a bare Chat
// turn, which this injector must never adopt: it has no item contract, and
// rewriting it is what produced the 400 in issue #307.
func responsesItemType(item map[string]any) string {
	t, _ := item["type"].(string)
	return t
}

// responsesContentHasPrompt reports whether an item's content already carries
// the prompt, whether it is a plain string or a block array.
func responsesContentHasPrompt(content any, prompt string) bool {
	if s, ok := content.(string); ok {
		return strings.Contains(s, prompt)
	}
	blocks, ok := content.([]any)
	if !ok {
		return false
	}
	for _, block := range blocks {
		bMap, ok := block.(map[string]any)
		if !ok {
			continue
		}
		if text, ok := bMap["text"].(string); ok && strings.Contains(text, prompt) {
			return true
		}
	}
	return false
}

// appendResponsesPrompt adds the prompt to a message item's content, accepting
// either shape the item may already carry and always leaving a block array
// behind — a bare string is what Codex rejects as `input[0].content`.
func appendResponsesPrompt(item map[string]any, prompt string) map[string]any {
	block := map[string]any{"type": "input_text", "text": prompt}
	switch content := item["content"].(type) {
	case string:
		item["content"] = []any{
			map[string]any{"type": "input_text", "text": content},
			block,
		}
	case []any:
		item["content"] = append(content, block)
	default:
		item["content"] = []any{block}
	}
	return item
}

// InjectSystemPromptClaude adds a system prompt to a Claude Messages-format
// request body. The Anthropic API forbids role:"system" inside messages[];
// the system prompt must be the top-level "system" field (string or text
// blocks). Merges into an existing value, skipping if the prompt is present.
// Returns modified body and true if any modification was made.
func InjectSystemPromptClaude(body []byte, prompt string) ([]byte, bool) {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body, false
	}

	switch sys := req["system"].(type) {
	case string:
		if strings.Contains(sys, prompt) {
			return body, false
		}
		if sys != "" {
			req["system"] = sys + "\n\n" + prompt
		} else {
			req["system"] = prompt
		}
	case []any:
		// Text blocks: skip if already present. If a block carries cache_control,
		// insert before it so the cache breakpoint remains at the end (mirrors
		// upstream open-sse/rtk/systemInject.js:220-230).
		lastCacheIdx := -1
		for i, block := range sys {
			bMap, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if t, ok := bMap["text"].(string); ok && strings.Contains(t, prompt) {
				return body, false
			}
			if bMap["cache_control"] != nil {
				lastCacheIdx = i
			}
		}
		newBlock := map[string]any{"type": "text", "text": prompt}
		if lastCacheIdx >= 0 {
			res := make([]any, 0, len(sys)+1)
			res = append(res, sys[:lastCacheIdx]...)
			res = append(res, newBlock)
			res = append(res, sys[lastCacheIdx:]...)
			req["system"] = res
		} else {
			req["system"] = append(sys, newBlock)
		}
	case nil:
		req["system"] = prompt
	default:
		return body, false
	}

	out, err := json.Marshal(req)
	if err != nil {
		return body, false
	}
	return out, true
}

// toMessageArray extracts []any from a JSON value that might be an array.
func toMessageArray(v any) []any {
	if v == nil {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	return arr
}
