package translator

import (
	"encoding/json"
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

	if name == "Read" {
		sanitizeReadArgs(args)
	}

	sanitized, err := json.Marshal(args)
	if err != nil {
		return argsJSON
	}
	return string(sanitized)
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
