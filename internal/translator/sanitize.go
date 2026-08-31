package translator

import (
	json "encoding/json/v2"
	"fmt"
	"regexp"
	"strings"
)

func sanitizeToolArgs(toolName, argsJSON string) string {
	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return argsJSON
	}

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
	case nameLower == "bash" || nameLower == "run_command" || nameLower == "terminal":
		sanitizeBashArgs(args)
	}

	sanitized, err := json.Marshal(args)
	if err != nil {
		return argsJSON
	}
	return string(sanitized)
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
