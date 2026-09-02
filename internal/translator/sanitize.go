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
		// Keep strict: only set `query` (Claude's web_search schema only allows query)
		if _, ok := args["query"]; !ok {
			for _, v := range args {
				if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
					args["query"] = strings.TrimSpace(s)
					log.Warn("sanitize", "web_search query fallback from args value", "tool", toolName, "query", s)
					break
				}
			}
			if _, ok := args["query"]; !ok && origArgsJSON != "" && origArgsJSON != "{}" && origArgsJSON != "null" {
				trimmed := strings.Trim(origArgsJSON, "\" \t\n\r")
				if trimmed != "" && trimmed != "{}" && trimmed != "null" {
					if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
						args["query"] = trimmed
						log.Warn("sanitize", "web_search query fallback from raw string", "tool", toolName, "query", trimmed)
					}
				}
			}
			if _, ok := args["query"]; !ok {
				if _, exists := args["query"]; exists {
					delete(args, "query")
				}
				args["query"] = "search"
				log.Warn("sanitize", "web_search set fallback generic query for missing query", "tool", toolName, "before", origArgsJSON, "after", "search")
			}
		} else {
			if qStr, ok := args["query"].(string); !ok || strings.TrimSpace(qStr) == "" {
				foundFallback := false
				for _, v := range args {
					if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
						if s != args["query"] {
							args["query"] = strings.TrimSpace(s)
							log.Warn("sanitize", "web_search query fallback for empty query", "tool", toolName, "query", s)
							foundFallback = true
							break
						}
					}
				}
				if !foundFallback {
					if qVal, exists := args["query"]; exists && qVal == nil {
						delete(args, "query")
					}
					if _, ok := args["query"]; !ok {
						args["query"] = "search"
						log.Warn("sanitize", "web_search empty query replaced with generic", "tool", toolName, "before", origArgsJSON)
					}
				}
			}
		}
	case strings.Contains(nameLower, "fetch") || nameLower == "web_fetch" || nameLower == "read_url":
		sanitizeFetchArgs(args, origArgsJSON)
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
	if nameLower == "web_search" || nameLower == "websearch" || nameLower == "search_web" || nameLower == "web_search_ide" || nameLower == "search_web_ide" || strings.Contains(nameLower, "fetch") || strings.Contains(nameLower, "read_url") {
		if out != origArgsJSON {
			log.Warn("sanitize", "fetch/search args sanitized", "tool", toolName, "before", origArgsJSON, "after", out)
		} else {
			log.Info("sanitize", "fetch/search args", "tool", toolName, "args", out)
		}
	}
	return out
}

func sanitizeFetchArgs(args map[string]any, origJSON string) {
	// Unwrap wrappers
	for _, wrapperKey := range []string{"input", "params", "arguments", "parameters"} {
		if wrapped, ok := args[wrapperKey].(map[string]any); ok {
			for k, v := range wrapped {
				if _, exists := args[k]; !exists {
					args[k] = v
				}
			}
		}
	}
	// Ensure url is present — try common synonyms
	if _, ok := args["url"]; !ok {
		if u, ok := args["uri"].(string); ok && u != "" {
			args["url"] = u
		} else if u, ok := args["link"].(string); ok && u != "" {
			args["url"] = u
		} else if u, ok := args["href"].(string); ok && u != "" {
			args["url"] = u
		} else if u, ok := args["website"].(string); ok && u != "" {
			args["url"] = u
		} else if u, ok := args["target_url"].(string); ok && u != "" {
			args["url"] = u
		} else {
			// Try any string value that looks like a URL
			for _, v := range args {
				if s, ok := v.(string); ok && (strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")) {
					args["url"] = s
					log.Warn("sanitize", "fetch url fallback from args value", "url", s)
					break
				}
			}
			// If still no url and original was a plain URL string
			if _, ok := args["url"]; !ok && origJSON != "" && origJSON != "{}" && origJSON != "null" {
				trimmed := strings.Trim(origJSON, "\" \t\n\r")
				if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
					args["url"] = trimmed
					log.Warn("sanitize", "fetch url fallback from raw string", "url", trimmed)
				}
			}
		}
	}
	// Ensure url is a string (handle null)
	if urlVal, exists := args["url"]; exists {
		if _, ok := urlVal.(string); !ok {
			if urlVal == nil {
				delete(args, "url")
				log.Warn("sanitize", "fetch removed null url", "before", origJSON)
			}
		}
	}
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

	// Ensure query is a clean string (do not auto-populate q/queries — strict schemas like
	// Claude Code's web_search only allow `query` and will 400 on extra fields)
	if qStr, ok := args["query"].(string); ok && qStr != "" {
		args["query"] = strings.TrimSpace(qStr)
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
