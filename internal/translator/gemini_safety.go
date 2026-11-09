package translator

import "encoding/json"

// ApplyGeminiSafetySettings adds Gemini-native safetySettings with BLOCK_NONE
// to a translated generateContent body. This is the official API switch, so it
// is the one jailbreak lever with deterministic effect. A body that already
// carries safetySettings is returned untouched: the caller knows better.
func ApplyGeminiSafetySettings(body []byte) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body, err
	}
	if _, present := m["safetySettings"]; present {
		return body, nil
	}
	m["safetySettings"] = []map[string]string{
		{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "BLOCK_NONE"},
		{"category": "HARM_CATEGORY_HATE_SPEECH", "threshold": "BLOCK_NONE"},
		{"category": "HARM_CATEGORY_SEXUALLY_EXPLICIT", "threshold": "BLOCK_NONE"},
		{"category": "HARM_CATEGORY_DANGEROUS_CONTENT", "threshold": "BLOCK_NONE"},
	}
	return json.Marshal(m)
}
