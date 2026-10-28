package translator

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"

	"9router/proxy/internal/log"
)

// cleanGeminiSchema recursively removes JSON Schema Draft 7/8 keywords
// that Google's Protobuf parser rejects for function declarations.
func cleanGeminiSchema(schema map[string]any) {
	if schema == nil {
		return
	}

	// Keywords rejected by Gemini
	unsupported := []string{
		"minLength", "maxLength", "exclusiveMinimum", "exclusiveMaximum",
		"minItems", "maxItems", "format", "multipleOf",
		// Array/2020-12 keywords the Gemini schema proto has no field for.
		// Agent tool schemas set these routinely; one occurrence rejects the
		// whole request (Next.js UNSUPPORTED_SCHEMA_CONSTRAINTS parity).
		"uniqueItems", "contains", "unevaluatedProperties", "unevaluatedItems", "contentSchema",
		"default", "examples",
		"$schema", "$defs", "definitions", "const", "$ref", "$comment",
		"deprecated", "readOnly", "writeOnly",
		// additionalItems (draft-07 tuple tail) is rejected like prefixItems:
		// `Unknown name "additionalItems" at functionDeclaration.parameters` 400.
		// Listed here, not deleted locally, so the strip re-runs on keys an
		// anyOf/oneOf branch merges back in.
		"additionalItems", "additionalProperties", "propertyNames", "patternProperties", "enumDescriptions",
		"allOf", "not",
		"dependencies", "dependentSchemas", "dependentRequired",
		"title", "optional", "if", "then", "else", "contentMediaType", "contentEncoding",
		"cornerRadius", "fillColor", "fontFamily", "fontSize", "fontWeight",
		"gap", "padding", "strokeColor", "strokeThickness", "textColor",
		// Non-standard annotation/error keywords some MCP tool schemas emit
		// (upstream #4283). Gemini's schema proto has no field for these and
		// rejects the whole request with "Unknown name X: Cannot find field"
		// if any nested node carries one. Bare names: the x- rule below only
		// catches the vendor-prefixed spellings.
		"errorMessage", "errorMessages", "markdownDescription",
		"doNotSuggest", "suggestSortText", "minProperties", "maxProperties",
	}

	// stripUnsupported removes keywords Gemini rejects. Called again after anyOf/oneOf
	// flattening, which can re-inject them (e.g. const) from a merged branch.
	stripUnsupported := func(s map[string]any) {
		for _, k := range unsupported {
			delete(s, k)
		}
		// Delete all vendor extensions (x- prefixes)
		for k := range s {
			if len(k) > 2 && k[0] == 'x' && k[1] == '-' {
				delete(s, k)
			}
		}
	}
	stripUnsupported(schema)

	// Ensure type="object" if properties exist (Gemini requirement)
	if _, hasProps := schema["properties"]; hasProps {
		if _, hasType := schema["type"]; !hasType {
			schema["type"] = "object"
		}
	}

	// Ensure enum is string array and type="string" (Gemini requirement)
	if enumRaw, hasEnum := schema["enum"]; hasEnum {
		if enumArr, ok := enumRaw.([]any); ok {
			strArr := make([]string, 0, len(enumArr))
			for _, item := range enumArr {
				if s, ok := item.(string); ok {
					strArr = append(strArr, s)
				} else {
					if b, err := json.Marshal(item); err == nil {
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
			if arr, ok := rawArr.([]any); ok && len(arr) > 0 {
				for _, itemRaw := range arr {
					if item, ok := itemRaw.(map[string]any); ok {
						if t, hasT := item["type"]; !hasT || t != "null" {
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

	// anyOf/oneOf merge copies branch keys (including const/$ref/etc.) back in.
	stripUnsupported(schema)

	// Flatten type arrays
	if typeRaw, hasType := schema["type"]; hasType {
		if typeArr, ok := typeRaw.([]any); ok && len(typeArr) > 0 {
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

	// Handle prefixItems (JSON Schema 2020-12 tuple) — Gemini only supports single items.
	if prefixItemsRaw, hasPrefix := schema["prefixItems"]; hasPrefix {
		if _, hasItems := schema["items"]; !hasItems {
			if arr, ok := prefixItemsRaw.([]any); ok && len(arr) > 0 {
				if firstMap, ok := arr[0].(map[string]any); ok {
					schema["items"] = firstMap
				}
			}
		}
		delete(schema, "prefixItems")
	}

	// Ensure every array carries an items schema with a concrete type (Gemini
	// protobuf requires both). Missing items, tuple items, or an untyped/empty
	// items cause: "...items: missing field." / "...items.items: missing field."
	// normalizeArrayItems only normalizes the shape; the recursion below cleans it.
	if t, ok := schema["type"].(string); ok && t == "array" {
		schema["items"] = normalizeArrayItems(schema["items"])
	}

	// Fix misplaced `required` inside `properties` (common generator bug: required as property instead of sibling)
	// e.g. {"properties":{"query":{...},"required":["query"]}} -> {"properties":{"query":{...}},"required":["query"]}
	if propsRaw, hasProps := schema["properties"]; hasProps {
		if props, ok := propsRaw.(map[string]any); ok {
			if reqInProps, hasReqInProps := props["required"]; hasReqInProps {
				// If value is not an object schema, it's misplaced required array
				if _, isObj := reqInProps.(map[string]any); !isObj {
					delete(props, "required")
					if _, hasTopReq := schema["required"]; !hasTopReq {
						// Only promote if top-level required missing
						if reqArr, ok := reqInProps.([]any); ok {
							schema["required"] = reqArr
						} else if reqStrArr, ok := reqInProps.([]string); ok {
							schema["required"] = reqStrArr
						} else {
							// Fallback: marshal and unmarshal to handle mixed types
							if b, err := json.Marshal(reqInProps); err == nil {
								var arr []any
								if err := json.Unmarshal(b, &arr); err == nil {
									schema["required"] = arr
								}
							}
						}
					}
				}
			}
		}
	}

	// Recurse into properties definitions (each value is a property schema, NOT the container map)
	if propsRaw, hasProps := schema["properties"]; hasProps {
		if props, ok := propsRaw.(map[string]any); ok {
			for _, propVal := range props {
				if propSchema, ok := propVal.(map[string]any); ok {
					cleanGeminiSchema(propSchema)
				}
			}
		}
	}

	// Recurse into items (array schema element)
	if itemsRaw, hasItems := schema["items"]; hasItems {
		switch items := itemsRaw.(type) {
		case map[string]any:
			cleanGeminiSchema(items)
			if t, ok := schema["type"].(string); ok && t == "array" {
				schema["items"] = normalizeArrayItems(schema["items"])
			}
		case []any:
			for _, elem := range items {
				if elemMap, ok := elem.(map[string]any); ok {
					cleanGeminiSchema(elemMap)
				}
			}
			if t, ok := schema["type"].(string); ok && t == "array" {
				schema["items"] = normalizeArrayItems(schema["items"])
			}
		}
	}

	// Recurse into additionalProperties if it is a schema (some generators put constraints there)
	if apRaw, hasAP := schema["additionalProperties"]; hasAP {
		if apMap, ok := apRaw.(map[string]any); ok {
			cleanGeminiSchema(apMap)
			// additionalProperties is stripped above via unsupported list, but if
			// it survived (e.g. future list change), still ensure its content is clean.
		}
	}

	// Clean up required array (must only contain keys present in properties)
	if reqRaw, hasReq := schema["required"]; hasReq {
		var reqStrs []string
		switch arr := reqRaw.(type) {
		case []any:
			for _, r := range arr {
				if s, ok := r.(string); ok {
					reqStrs = append(reqStrs, s)
				}
			}
		case []string:
			reqStrs = arr
		}

		if len(reqStrs) > 0 {
			var validReqs []string
			if propsRaw, hasProps := schema["properties"]; hasProps {
				if props, ok := propsRaw.(map[string]any); ok {
					for _, rStr := range reqStrs {
						if _, exists := props[rStr]; exists {
							validReqs = append(validReqs, rStr)
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
			delete(schema, "required")
		}
	}

	// Add placeholder for empty object schemas (Antigravity requirement)
	if t, hasT := schema["type"]; hasT && t == "object" {
		needsPlaceholder := true
		if propsRaw, hasProps := schema["properties"]; hasProps {
			if props, ok := propsRaw.(map[string]any); ok && len(props) > 0 {
				needsPlaceholder = false
			}
		}
		if needsPlaceholder {
			schema["properties"] = map[string]any{
				"reason": map[string]any{
					"type":        "string",
					"description": "Brief explanation of why you are calling this tool",
				},
			}
			schema["required"] = []string{"reason"}
		}
	}

	// Empty schema {} (no type at all) must also become the object placeholder
	if len(schema) == 0 {
		schema["type"] = "object"
		schema["properties"] = map[string]any{
			"reason": map[string]any{
				"type":        "string",
				"description": "Brief explanation of why you are calling this tool",
			},
		}
		schema["required"] = []string{"reason"}
	}
}

// stringItems is the permissive placeholder Gemini accepts for an array whose
// items carry nothing usable. Always a fresh map: the recursion below mutates it.
func stringItems() map[string]any { return map[string]any{"type": "string"} }

// normalizeArrayItems reduces whatever an array carries under `items` to a
// single schema object with a type Gemini can parse: a tuple list collapses to
// one entry, an untyped object infers its type, and an empty or non-object
// value falls back to {"type": "string"} (upstream #4798).
func normalizeArrayItems(raw any) map[string]any {
	switch items := raw.(type) {
	case map[string]any:
		if len(items) == 0 {
			// Empty map, e.g. left over after title/default were stripped:
			// reaching Gemini as-is is the reported "items.items: missing field".
			return stringItems()
		}
		inferItemsType(items)
		return items
	case []any:
		// Tuple form (draft-04/07 items list). Skip empty entries so
		// `[{}, {...}]` keeps the one that describes something instead of
		// collapsing into the empty-object placeholder.
		for _, elem := range items {
			if m, ok := elem.(map[string]any); ok && len(m) > 0 {
				inferItemsType(m)
				return m
			}
		}
		return stringItems()
	default:
		// Missing, null, or a non-schema scalar (items: true).
		return stringItems()
	}
}

// inferItemsType fills in a missing `type` from the keys the item schema does
// carry. anyOf/oneOf are left alone: flattening them may pick a different type.
func inferItemsType(items map[string]any) {
	if _, hasType := items["type"]; hasType {
		return
	}
	if _, hasAnyOf := items["anyOf"]; hasAnyOf {
		return
	}
	if _, hasOneOf := items["oneOf"]; hasOneOf {
		return
	}
	if _, hasProps := items["properties"]; hasProps {
		items["type"] = "object"
		return
	}
	if _, hasNested := items["items"]; hasNested {
		items["type"] = "array"
		return
	}
	// Covers the enum case too: cleanGeminiSchema stringifies enum values and
	// Gemini requires type:"string" alongside them.
	items["type"] = "string"
}

// CleanParametersSchema parses raw JSON schema, cleans it, and returns it.
func CleanParametersSchema(raw jsontext.Value) jsontext.Value {
	if len(raw) == 0 || string(raw) == "null" {
		return jsontext.Value(`{"type":"object","properties":{}}`)
	}

	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return raw // fallback
	}

	cleanGeminiSchema(parsed)

	cleaned, err := json.Marshal(parsed, json.Deterministic(true))
	if err != nil {
		return raw // fallback
	}
	if string(cleaned) != string(raw) {
		// Detailed debug for schema cleaning - helps diagnose Gemini 400s
		log.Debug("translator", "cleaned schema", "before", string(raw[:min(500, len(raw))]), "after", string(cleaned[:min(500, len(cleaned))]))
	}
	return cleaned
}

// SanitizeOpenAITools rewrites every tool's parameters schema through
// CleanParametersSchema in an OpenAI-format request body. Used for OpenAI-compat
// Gemini endpoints whose tool schema validation is as strict as the native
// generateContent path (one unsupported keyword rejects the whole request).
// Returns the body unchanged when there are no tools or nothing to clean.
func SanitizeOpenAITools(body []byte) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	tools, ok := m["tools"].([]any)
	if !ok || len(tools) == 0 {
		return body, nil
	}
	changed := false
	for _, t := range tools {
		tm, ok := t.(map[string]any)
		if !ok {
			continue
		}
		fn, ok := tm["function"].(map[string]any)
		if !ok {
			continue
		}
		params, ok := fn["parameters"]
		if !ok {
			continue
		}
		raw, err := json.Marshal(params, json.Deterministic(true))
		if err != nil {
			continue
		}
		cleaned := CleanParametersSchema(raw)
		var cleanedOut any
		if err := json.Unmarshal(cleaned, &cleanedOut); err != nil {
			continue
		}
		fn["parameters"] = cleanedOut
		changed = true
	}
	if !changed {
		return body, nil
	}
	return json.Marshal(m, json.Deterministic(true))
}
