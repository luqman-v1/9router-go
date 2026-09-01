package translator

import (
	json "encoding/json/v2"
	"fmt"
	"regexp"
	"strings"

	"9router/proxy/internal/log"
)

func sanitizeToolArgs(toolName, argsJSON string) string {
	// Handle "null", empty, or whitespace-only args — common when LLM sends null for web_search
	if argsJSON == "" || argsJSON == "null" || strings.TrimSpace(argsJSON) == "" {
		argsJSON = "{}"
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		// If args is a JSON string (e.g. "\"query\""), try to unwrap
		var s string
		if err2 := json.Unmarshal([]byte(argsJSON), &s); err2 == nil && s != "" {
			args = map[string]any{"query": s}
		} else {
			return argsJSON
		}
	}
	if args == nil {
		args = make(map[string]any)
	}
	origArgsJSON := argsJSON

	name := toolName
	if strings.HasPrefix(name, "proxy_") {
		name = strings.TrimPrefix(name, "proxy_")
	}

	nameLower := strings.ToLower(name)
	switch {
	case name == "Read" || nameLower == "view_file" || nameLower == "read_file":
		sanitizeReadArgs(args)
	case strings.Contains(nameLower, "search") || nameLower == "websearch":
		sanitizeSearchArgs(args)
		// Fallback for web_search when query is still missing/null after normal sanitization
		if _, ok := args["query"]; !ok {
			if qVal, ok := args["query"]; ok && qVal != nil {
				// query exists but is not a string (e.g. null) — will be handled below
			}
			// Try any string value in args as query
			for _, v := range args {
				if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
					args["query"] = strings.TrimSpace(s)
					args["q"] = strings.TrimSpace(s)
					args["queries"] = []string{strings.TrimSpace(s)}
					log.Warn("sanitize", "web_search query fallback from args value", "tool", toolName, "query", s)
					break
				}
			}
			if _, ok := args["query"]; !ok && origArgsJSON != "" && origArgsJSON != "{}" && origArgsJSON != "null" {
				trimmed := strings.Trim(origArgsJSON, "\" \t\n\r")
				if trimmed != "" && trimmed != "{}" && trimmed != "null" {
					// If original was a plain string like "cara mengalahkan bot", use it
					// Also handle case where original was `{"query": null}` — trimmed would be `{"query": null}` not useful
					// So only use if trimmed looks like a query (no braces)
					if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
						args["query"] = trimmed
						args["q"] = trimmed
						args["queries"] = []string{trimmed}
						log.Warn("sanitize", "web_search query fallback from raw string", "tool", toolName, "query", trimmed)
					}
				}
			}
			// Final fallback: if still no query and this is web_search, ensure args is at least a valid object
			// The LLM may have sent null/empty; Jcode's websearch will error "missing field query" — provide a placeholder
			// that will at least not be "null" so the tool can be called (it will search for empty, but better than error)
			if _, ok := args["query"]; !ok {
				// Check if query key exists but is nil/null
				if _, exists := args["query"]; exists {
					// query is present but not a string (likely nil) — remove and set fallback
					delete(args, "query")
				}
				// If still no query, leave args as is — Jcode will return missing field error which will be shown to LLM
				// and LLM will retry. Don't fabricate a query from nothing.
			}
		} else {
			// Query exists but might be not a string or empty — ensure it's a string
			if qStr, ok := args["query"].(string); !ok || strings.TrimSpace(qStr) == "" {
				// Query is present but not a valid string (null, empty, etc.)
				// Try fallback as above
				for _, v := range args {
					if s, ok := v.(string); ok && strings.TrimSpace(s) != "" && s != args["query"] {
						args["query"] = strings.TrimSpace(s)
						args["q"] = strings.TrimSpace(s)
						args["queries"] = []string{strings.TrimSpace(s)}
						log.Warn("sanitize", "web_search query fallback for empty query", "tool", toolName, "query", s)
						break
					}
				}
			}
		}
	case nameLower == "bash" || nameLower == "run_command" || nameLower == "terminal":
		sanitizeBashArgs(args)
	}

	// Final ensure web_search has a string query if it has any query key that is not a string (e.g. null)
	if strings.Contains(nameLower, "search") {
		if qVal, exists := args["query"]; exists {
			if _, ok := qVal.(string); !ok {
				// query exists but is not a string (null, number, etc.) — try to convert or remove
				if qVal == nil {
					delete(args, "query")
					delete(args, "q")
					delete(args, "queries")
					log.Warn("sanitize", "web_search removed null query", "tool", toolName, "before", origArgsJSON)
				}
			}
		}
	}

	sanitized, err := json.Marshal(args)
	if err != nil {
		return argsJSON
	}
	out := string(sanitized)
	if nameLower == "web_search" || nameLower == "websearch" || nameLower == "search_web" || nameLower == "web_search_ide" || nameLower == "search_web_ide" {
		if out != origArgsJSON {
			log.Warn("sanitize", "web_search args sanitized", "tool", toolName, "before", origArgsJSON, "after", out)
		} else {
			log.Info("sanitize", "web_search args", "tool", toolName, "args", out)
		}
	}
	return out
}

func sanitizeSearchArgs(args map[string]any) {
	// Unwrap nested argument wrappers (e.g. {"input": {...}} or {"params": {...}})
	for _, wrapperKey := range []string{"input", "params", "arguments", "parameters"} {
		if wrapped, ok := args[wrapperKey].(map[string]any); ok {
			for k, v := range wrapped {
				if _, exists := args[k]; !exists {
					args[k] = v
				}
			}
		}
	}

	// If "query" is missing, extract from common synonyms
	if _, ok := args["query"]; !ok {
		if q, ok := args["q"].(string); ok && q != "" {
			args["query"] = q
		} else if sq, ok := args["search_query"].(string); ok && sq != "" {
			args["query"] = sq
		} else if st, ok := args["search_terms"].(string); ok && st != "" {
			args["query"] = st
		} else if kw, ok := args["keyword"].(string); ok && kw != "" {
			args["query"] = kw
		} else if prompt, ok := args["prompt"].(string); ok && prompt != "" {
			args["query"] = prompt
		} else if text, ok := args["text"].(string); ok && text != "" {
			args["query"] = text
		} else if queries, ok := args["queries"].([]any); ok && len(queries) > 0 {
			if first, ok := queries[0].(string); ok && first != "" {
				args["query"] = first
			} else if firstMap, ok := queries[0].(map[string]any); ok {
				if q, ok := firstMap["q"].(string); ok && q != "" {
					args["query"] = q
				} else if q, ok := firstMap["query"].(string); ok && q != "" {
					args["query"] = q
				}
			}
		} else if sqArr, ok := args["search_queries"].([]any); ok && len(sqArr) > 0 {
			if first, ok := sqArr[0].(string); ok && first != "" {
				args["query"] = first
			}
		}
	}

	// If query was passed as a slice/array, take the first string
	if qArr, ok := args["query"].([]any); ok && len(qArr) > 0 {
		if first, ok := qArr[0].(string); ok {
			args["query"] = first
		}
	}

	// Ensure both "query", "q", and "queries" are populated for different schema expectations
	if qStr, ok := args["query"].(string); ok && qStr != "" {
		if _, ok := args["q"]; !ok {
			args["q"] = qStr
		}
		if _, ok := args["queries"]; !ok {
			args["queries"] = []string{qStr}
		}
	}
}

func sanitizeBashArgs(args map[string]any) {
	if _, ok := args["command"]; !ok {
		if cmd, ok := args["cmd"].(string); ok && cmd != "" {
			args["command"] = cmd
		} else if cl, ok := args["CommandLine"].(string); ok && cl != "" {
			args["command"] = cl
		}
	}
}

func sanitizeReadArgs(args map[string]any) {
	if limitVal, ok := args["limit"]; ok {
		switch v := limitVal.(type) {
		case string:
			var n int
			if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
				args["limit"] = n
			}
		}
		if limitNum, ok := args["limit"].(float64); ok {
			n := int(limitNum)
			if n > 2000 {
				args["limit"] = 2000
			} else if n < 1 {
				delete(args, "limit")
			} else {
				args["limit"] = n
			}
		} else if limitNum, ok := args["limit"].(int); ok {
			if limitNum > 2000 {
				args["limit"] = 2000
			} else if limitNum < 1 {
				delete(args, "limit")
			}
		}
	}

	if offsetVal, ok := args["offset"]; ok {
		switch v := offsetVal.(type) {
		case string:
			var n int
			if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
				args["offset"] = n
			}
		}
		if offsetNum, ok := args["offset"].(float64); ok {
			n := int(offsetNum)
			if n < 0 {
				args["offset"] = 0
			} else {
				args["offset"] = n
			}
		} else if offsetNum, ok := args["offset"].(int); ok {
			if offsetNum < 0 {
				args["offset"] = 0
			}
		}
	}

	if pagesVal, ok := args["pages"]; ok {
		filePath, _ := args["file_path"].(string)
		pages, _ := pagesVal.(string)
		if !isValidPdfPagesArg(filePath, pages) {
			delete(args, "pages")
		}
	}
}

func isValidPdfPagesArg(filePath, pages string) bool {
	if filePath == "" || pages == "" {
		return false
	}
	filePathLower := strings.ToLower(filePath)
	if !strings.HasSuffix(filePathLower, ".pdf") {
		return false
	}
	matched, _ := regexp.MatchString(`^\d+(-\d+)?$`, pages)
	return matched
}
