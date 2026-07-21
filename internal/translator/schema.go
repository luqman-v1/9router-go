package translator

import (
	"encoding/json"
)

// cleanGeminiSchema recursively removes JSON Schema Draft 7/8 keywords
// that Google's Protobuf parser rejects for function declarations.
func cleanGeminiSchema(schema map[string]interface{}) {
	if schema == nil {
		return
	}

	// Keywords rejected by Gemini
	unsupported := []string{
		"minLength", "maxLength", "exclusiveMinimum", "exclusiveMaximum",
		"minItems", "maxItems", "format",
		"default", "examples",
		"$schema", "$defs", "definitions", "const", "$ref", "$comment",
		"deprecated", "readOnly", "writeOnly",
		"additionalProperties", "propertyNames", "patternProperties", "enumDescriptions",
		"allOf", "not",
		"dependencies", "dependentSchemas", "dependentRequired",
		"title", "optional", "if", "then", "else", "contentMediaType", "contentEncoding",
		"cornerRadius", "fillColor", "fontFamily", "fontSize", "fontWeight",
		"gap", "padding", "strokeColor", "strokeThickness", "textColor",
	}

	for _, k := range unsupported {
		delete(schema, k)
	}

	// Delete all vendor extensions (x- prefixes)
	for k := range schema {
		if len(k) > 2 && k[0] == 'x' && k[1] == '-' {
			delete(schema, k)
		}
	}

	// Ensure type="object" if properties exist (Gemini requirement)
	if _, hasProps := schema["properties"]; hasProps {
		if _, hasType := schema["type"]; !hasType {
			schema["type"] = "object"
		}
	}

	// Ensure enum is string array and type="string" (Gemini requirement)
	if enumRaw, hasEnum := schema["enum"]; hasEnum {
		if enumArr, ok := enumRaw.([]interface{}); ok {
			strArr := make([]string, 0, len(enumArr))
			for _, item := range enumArr {
				if s, ok := item.(string); ok {
					strArr = append(strArr, s)
				} else {
					// Extremely simplistic conversion to string
					if b, err := json.Marshal(item); err == nil {
						// Remove quotes if it's a JSON string, otherwise use raw
						if len(b) >= 2 && b[0] == '"' && b[len(b)-1] == '"' {
							strArr = append(strArr, string(b[1:len(b)-1]))
						} else {
							strArr = append(strArr, string(b))
						}
					}
				}
			}
			schema["enum"] = strArr
			if _, hasType := schema["type"]; !hasType {
				schema["type"] = "string"
			}
		}
	}

	// Flatten anyOf/oneOf (Google doesn't support them)
	for _, key := range []string{"anyOf", "oneOf"} {
		if rawArr, has := schema[key]; has {
			if arr, ok := rawArr.([]interface{}); ok && len(arr) > 0 {
				// Pick first schema that is not just type="null"
				for _, itemRaw := range arr {
					if item, ok := itemRaw.(map[string]interface{}); ok {
						if t, hasT := item["type"]; !hasT || t != "null" {
							// Merge item into schema
							for k, v := range item {
								schema[k] = v
							}
							break
						}
					}
				}
			}
			delete(schema, key)
		}
	}

	// Flatten type arrays
	if typeRaw, hasType := schema["type"]; hasType {
		if typeArr, ok := typeRaw.([]interface{}); ok && len(typeArr) > 0 {
			var firstValid string
			for _, tRaw := range typeArr {
				if t, ok := tRaw.(string); ok && t != "null" {
					firstValid = t
					break
				}
			}
			if firstValid != "" {
				schema["type"] = firstValid
			} else {
				schema["type"] = "string"
			}
		}
	}

	// Clean up required array (must only contain keys present in properties)
	if reqRaw, hasReq := schema["required"]; hasReq {
		if reqArr, ok := reqRaw.([]interface{}); ok {
			var validReqs []string
			if propsRaw, hasProps := schema["properties"]; hasProps {
				if props, ok := propsRaw.(map[string]interface{}); ok {
					for _, rRaw := range reqArr {
						if rStr, ok := rRaw.(string); ok {
							if _, exists := props[rStr]; exists {
								validReqs = append(validReqs, rStr)
							}
						}
					}
				}
			}
			if len(validReqs) > 0 {
				schema["required"] = validReqs
			} else {
				delete(schema, "required")
			}
		} else {
			delete(schema, "required") // invalid format
		}
	}

	// Add placeholder for empty object schemas (Antigravity requirement)
	if t, hasT := schema["type"]; hasT && t == "object" {
		needsPlaceholder := true
		if propsRaw, hasProps := schema["properties"]; hasProps {
			if props, ok := propsRaw.(map[string]interface{}); ok && len(props) > 0 {
				needsPlaceholder = false
			}
		}
		if needsPlaceholder {
			schema["properties"] = map[string]interface{}{
				"reason": map[string]interface{}{
					"type":        "string",
					"description": "Brief explanation of why you are calling this tool",
				},
			}
			schema["required"] = []string{"reason"}
		}
	}

	for _, v := range schema {
		switch child := v.(type) {
		case map[string]interface{}:
			cleanGeminiSchema(child)
		case []interface{}:
			for _, elem := range child {
				if elemMap, ok := elem.(map[string]interface{}); ok {
					cleanGeminiSchema(elemMap)
				}
			}
		}
	}
}

// CleanParametersSchema parses raw JSON schema, cleans it, and returns it.
func CleanParametersSchema(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return raw // fallback
	}

	cleanGeminiSchema(parsed)

	cleaned, err := json.Marshal(parsed)
	if err != nil {
		return raw // fallback
	}
	return cleaned
}
