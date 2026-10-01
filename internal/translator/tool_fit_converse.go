package translator

// Bedrock Converse is the one wire shape that carries the callable name one
// level deeper than every other: tools[].toolSpec.name, where OpenAI uses
// tools[].function.name and the Responses API uses tools[].name.
//
// Both directions need it, and leaving either side out is the same defect
// mirrored: fit the declaration and the response comes back naming a tool the
// client never declared, so the shape lives in one file instead of being split
// between the request walker and the response walker.

// converseToolSpecName rewrites tools[].toolSpec.name. Both the collect and the
// replace pass call it with the same holder, so a name one records is always a
// name the other writes.
func converseToolSpecName(holder map[string]any, fn func(string) string) {
	spec, ok := holder["toolSpec"].(map[string]any)
	if !ok {
		return
	}
	if n, ok := spec["name"].(string); ok {
		spec["name"] = fn(n)
	}
}

// restoreConverseToolNames maps fitted Converse tool calls back to the names the
// client declared, reporting whether anything changed so the caller can keep the
// original bytes when the upstream never used a fitted name.
//
// Two places carry a name: the streaming contentBlockStart that opens a toolUse
// block, and the non-streaming toolUse inside output.message.content.
func restoreConverseToolNames(m map[string]any, toolNameMap map[string]string) bool {
	if start, ok := m["contentBlockStart"].(map[string]any); ok {
		if restoreConverseToolUse(start["start"], toolNameMap) {
			return true
		}
	}

	output, ok := m["output"].(map[string]any)
	if !ok {
		return false
	}
	message, ok := output["message"].(map[string]any)
	if !ok {
		return false
	}
	content, ok := message["content"].([]any)
	if !ok {
		return false
	}
	for _, block := range content {
		bm, ok := block.(map[string]any)
		if !ok {
			continue
		}
		if restoreConverseToolUse(bm, toolNameMap) {
			return true
		}
	}
	return false
}

// restoreConverseToolUse rewrites holder.toolUse.name when the holder carries one
// and that name is one we fitted.
func restoreConverseToolUse(holder any, toolNameMap map[string]string) bool {
	hm, ok := holder.(map[string]any)
	if !ok {
		return false
	}
	use, ok := hm["toolUse"].(map[string]any)
	if !ok {
		return false
	}
	n, ok := use["name"].(string)
	if !ok || n == "" {
		return false
	}
	orig, found := toolNameMap[n]
	if !found {
		return false
	}
	use["name"] = orig
	return true
}