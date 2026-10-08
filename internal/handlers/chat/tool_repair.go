package chat

import (
	json "9router/proxy/internal/fastjson"
	"fmt"
)

// repairToolCallIDsInMap ensures every role: "tool" message has a valid tool_call_id (PR #4090).
// Strict upstreams (NVIDIA, OpenAI, Antigravity) reject requests with HTTP 400 when tool_call_id is missing.
func repairToolCallIDsInMap(body map[string]any) {
	msgs, ok := body["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return
	}
	var pendingToolCallIDs []string
	toolSeq := 0
	for i, m := range msgs {
		msgMap, ok := m.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msgMap["role"].(string)
		if role == "assistant" {
			if tcs, ok := msgMap["tool_calls"].([]any); ok && len(tcs) > 0 {
				for k, tcRaw := range tcs {
					if tcMap, ok := tcRaw.(map[string]any); ok {
						id, _ := tcMap["id"].(string)
						if id == "" {
							id = fmt.Sprintf("call_%d_%d", toolSeq, k)
							tcMap["id"] = id
						}
						pendingToolCallIDs = append(pendingToolCallIDs, id)
					}
				}
				toolSeq++
			}
		} else if role == "tool" {
			id, _ := msgMap["tool_call_id"].(string)
			if id != "" {
				for idx, p := range pendingToolCallIDs {
					if p == id {
						pendingToolCallIDs = append(pendingToolCallIDs[:idx], pendingToolCallIDs[idx+1:]...)
						break
					}
				}
			} else {
				if len(pendingToolCallIDs) > 0 {
					msgMap["tool_call_id"] = pendingToolCallIDs[0]
					pendingToolCallIDs = pendingToolCallIDs[1:]
				} else {
					msgMap["tool_call_id"] = fmt.Sprintf("call_tool_%d", i)
				}
			}
		}
	}
}

// repairToolCallIDsInJSON repairs missing tool_call_id directly on raw JSON body if it contains messages.
func repairToolCallIDsInJSON(body []byte) []byte {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	if _, ok := m["messages"]; ok {
		repairToolCallIDsInMap(m)
		if out, err := json.Marshal(m); err == nil {
			return out
		}
	}
	return body
}
