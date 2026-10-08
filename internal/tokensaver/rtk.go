package tokensaver

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

// RTKConfig defines settings for RTK tool output compression.
type RTKConfig struct {
	Mode         string          `json:"mode,omitempty"`         // "simple" | "advance"
	Intensity    string          `json:"intensity,omitempty"`    // "minimal" | "standard" | "aggressive"
	MaxLines     int             `json:"maxLines,omitempty"`     // default 100
	MaxChars     int             `json:"maxChars,omitempty"`     // default 8000
	Deduplicate  bool            `json:"deduplicate,omitempty"`  // default true
	Categories   map[string]bool `json:"categories,omitempty"`   // git, build, test, package, docker, system
	Filters      map[string]bool `json:"filters,omitempty"`      // per-filter ID toggle overrides
	RawRetention string          `json:"rawRetention,omitempty"` // "never" | "failures" | "always"
}

// DefaultRTKConfig returns the default configuration for RTK.
func DefaultRTKConfig() RTKConfig {
	return RTKConfig{
		Mode:        "simple",
		Intensity:   "standard",
		MaxLines:    100,
		MaxChars:    8000,
		Deduplicate: true,
		Categories: map[string]bool{
			"git":     true,
			"build":   true,
			"test":    true,
			"package": true,
			"docker":  true,
			"system":  true,
		},
		RawRetention: "never",
	}
}

// RTKCompressResult contains compression metrics and the compressed text.
type RTKCompressResult struct {
	OriginalTokens   int      `json:"originalTokens"`
	CompressedTokens int      `json:"compressedTokens"`
	SavedTokens      int      `json:"savedTokens"`
	SavedPct         float64  `json:"savedPct"`
	Text             string   `json:"text"`
	DetectedCategory string   `json:"detectedCategory"`
	TechniquesUsed   []string `json:"techniquesUsed"`
}

var (
	reGitLogLine       = regexp.MustCompile(`^([a-f0-9]{7,40}\s|commit\s[a-f0-9]{7,})`)
	reTsError          = regexp.MustCompile(`error TS\d+:`)
	reEsLintError      = regexp.MustCompile(`\d+:\d+\s+error\s+`)
	reGoBuildError     = regexp.MustCompile(`(?m)^.*\.go:\d+:\d+: `)
	reCargoCompile     = regexp.MustCompile(`(?m)^\s*Compiling\s+[\w\-]+.*$`)
	rePipRequirement   = regexp.MustCompile(`(?m)^Requirement already satisfied:.*$`)
	reLsLine           = regexp.MustCompile(`^[dcbsp-][rwx-]{9}\s+`)
	rePsAuxHeader      = regexp.MustCompile(`(?i)^USER\s+PID\s+%(?:CPU|MEM)`)
	reDockerPsHeader   = regexp.MustCompile(`(?i)^CONTAINER ID\s+IMAGE`)
	reKubectlGetHeader = regexp.MustCompile(`(?i)^NAME\s+(?:READY\s+STATUS|TYPE\s+CLUSTER-IP)`)
)

func resolveLimits(cfg RTKConfig) (int, int) {
	maxLines := cfg.MaxLines
	if maxLines <= 0 {
		maxLines = 100
	}
	maxChars := cfg.MaxChars
	if maxChars <= 0 {
		maxChars = 8000
	}
	switch strings.ToLower(cfg.Intensity) {
	case "minimal":
		maxLines *= 2
		maxChars *= 2
	case "aggressive":
		maxLines /= 2
		if maxLines < 30 {
			maxLines = 30
		}
		maxChars /= 2
		if maxChars < 2000 {
			maxChars = 2000
		}
	}
	return maxLines, maxChars
}

func isCatEnabled(cfg RTKConfig, cat string) bool {
	if cfg.Categories == nil {
		return true
	}
	enabled, ok := cfg.Categories[cat]
	if !ok {
		return true
	}
	return enabled
}

// DeduplicateLines collapses consecutive identical lines into a summary count.
func DeduplicateLines(s string) (string, bool) {
	lines := strings.Split(s, "\n")
	if len(lines) < 2 {
		return s, false
	}
	result := make([]string, 0, len(lines))
	changed := false
	i := 0
	for i < len(lines) {
		current := lines[i]
		trimmed := strings.TrimSpace(current)
		j := i + 1
		for j < len(lines) && (lines[j] == current || (trimmed != "" && strings.TrimSpace(lines[j]) == trimmed)) {
			j++
		}
		count := j - i
		result = append(result, current)
		if count >= 3 {
			result = append(result, fmt.Sprintf("... (repeated %d times)", count-1))
			changed = true
			i = j
		} else if count == 2 {
			result = append(result, lines[i+1])
			i = j
		} else {
			i++
		}
	}
	if !changed {
		return s, false
	}
	return strings.Join(result, "\n"), true
}

// DetectCategory identifies the command or tool domain of the given text.
func DetectCategory(s string) string {
	trimmed := strings.TrimSpace(s)
	// Git
	if strings.HasPrefix(trimmed, "diff --git") || strings.HasPrefix(trimmed, "--- a/") ||
		reGitLogLine.MatchString(trimmed) ||
		strings.Contains(trimmed, "On branch ") ||
		strings.Contains(trimmed, "Changes to be committed:") ||
		strings.Contains(trimmed, "Changes not staged for commit:") ||
		strings.Contains(trimmed, "Untracked files:") {
		return "git"
	}
	// Build / Compiler
	if reTsError.MatchString(trimmed) || strings.Contains(trimmed, "Found ") && strings.Contains(trimmed, " error") ||
		reEsLintError.MatchString(trimmed) ||
		strings.Contains(trimmed, "[vite]") || strings.Contains(trimmed, "[webpack]") ||
		strings.Contains(trimmed, "Module build failed") || strings.Contains(trimmed, "Failed to compile") ||
		reGoBuildError.MatchString(trimmed) {
		return "build"
	}
	// Test Runners
	if (strings.Contains(trimmed, "PASS ") || strings.Contains(trimmed, "FAIL ")) && (strings.Contains(trimmed, "Test Suites:") || strings.Contains(trimmed, "Tests:")) ||
		strings.Contains(trimmed, "expect(received).to") ||
		strings.Contains(trimmed, "=== FAILURES ===") || strings.Contains(trimmed, "=== test session starts ===") ||
		strings.Contains(trimmed, "=== RUN ") || strings.Contains(trimmed, "--- FAIL: ") || strings.Contains(trimmed, "--- PASS: ") ||
		(strings.Contains(trimmed, "running ") && strings.Contains(trimmed, " tests\ntest ")) {
		return "test"
	}
	// Package Managers
	if strings.Contains(trimmed, "npm WARN") || strings.Contains(trimmed, "npm ERR!") ||
		strings.Contains(trimmed, "packages are looking for funding") || strings.Contains(trimmed, "added ") && strings.Contains(trimmed, "packages in") ||
		strings.Contains(trimmed, "Requirement already satisfied:") ||
		strings.Contains(trimmed, "Compiling ") && strings.Contains(trimmed, "crates") ||
		strings.Contains(trimmed, "[bun]") || strings.Contains(trimmed, "bun install") {
		return "package"
	}
	// Docker / System / Containers
	if reDockerPsHeader.MatchString(trimmed) || reKubectlGetHeader.MatchString(trimmed) ||
		strings.Contains(trimmed, "├──") || strings.Contains(trimmed, "└──") ||
		isGrepOutput(trimmed) ||
		reLsLine.MatchString(trimmed) || strings.HasPrefix(trimmed, "total ") ||
		rePsAuxHeader.MatchString(trimmed) ||
		strings.Contains(trimmed, "* Connected to ") || (strings.Contains(trimmed, "> GET ") && strings.Contains(trimmed, "< HTTP/")) {
		return "docker"
	}
	return "general"
}

// CompressGitStatus strips verbose explanatory tips from git status output.
func CompressGitStatus(s string) string {
	lines := strings.Split(s, "\n")
	result := make([]string, 0, len(lines))
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "(use \"git add") ||
			strings.HasPrefix(trimmed, "(use \"git restore") ||
			strings.HasPrefix(trimmed, "(use \"git push") ||
			strings.HasPrefix(trimmed, "(commit or discard") {
			continue
		}
		result = append(result, l)
	}
	return strings.Join(result, "\n")
}

// CompressBuildOutput filters verbose build compiler outputs down to errors and summaries.
func CompressBuildOutput(s string, maxLines int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s
	}
	// Keep errors and warnings
	var errors []string
	for _, l := range lines {
		lower := strings.ToLower(l)
		if strings.Contains(lower, "error") || strings.Contains(lower, "failed") || strings.Contains(lower, "warning") {
			errors = append(errors, l)
		}
	}
	if len(errors) > 0 && len(errors) < len(lines) {
		if len(errors) > maxLines {
			head := errors[:maxLines/2]
			tail := errors[len(errors)-maxLines/2:]
			return strings.Join(head, "\n") + fmt.Sprintf("\n... (%d build diagnostics truncated)\n", len(errors)-maxLines) + strings.Join(tail, "\n")
		}
		return strings.Join(errors, "\n")
	}
	return smartTruncateLines(lines, maxLines)
}

// CompressTestOutput filters test output to highlight failures and summarize passes.
func CompressTestOutput(s string, maxLines int) string {
	lines := strings.Split(s, "\n")
	hasFailures := strings.Contains(s, "FAIL") || strings.Contains(s, "failed")
	if !hasFailures && len(lines) > maxLines {
		// Summarize all-passed output
		summaryLines := make([]string, 0, 5)
		for _, l := range lines {
			if strings.Contains(l, "passed") || strings.Contains(l, "PASS") || strings.Contains(l, "Tests:") || strings.Contains(l, "test result:") {
				summaryLines = append(summaryLines, l)
			}
		}
		if len(summaryLines) > 0 {
			return strings.Join(summaryLines, "\n") + fmt.Sprintf("\n... (all tests passed; %d verbose lines omitted)", len(lines)-len(summaryLines))
		}
		return smartTruncateLines(lines, maxLines)
	}

	if hasFailures {
		// Filter out internal node_modules/vendor stack traces and pass lines
		filtered := make([]string, 0, len(lines))
		for _, l := range lines {
			trimmed := strings.TrimSpace(l)
			if strings.HasPrefix(trimmed, "at ") && strings.Contains(l, "node_modules/") {
				continue
			}
			if strings.HasPrefix(trimmed, "PASS ") || strings.HasPrefix(trimmed, "--- PASS:") {
				continue
			}
			filtered = append(filtered, l)
		}
		if len(filtered) > maxLines {
			return smartTruncateLines(filtered, maxLines)
		}
		return strings.Join(filtered, "\n")
	}
	return smartTruncateLines(lines, maxLines)
}

// CompressPackageOutput collapses verbose package manager output.
func CompressPackageOutput(s string, maxLines int) string {
	// Collapse Requirement already satisfied
	if rePipRequirement.MatchString(s) {
		lines := strings.Split(s, "\n")
		var nonReq []string
		reqCount := 0
		for _, l := range lines {
			if strings.HasPrefix(strings.TrimSpace(l), "Requirement already satisfied:") {
				reqCount++
			} else {
				nonReq = append(nonReq, l)
			}
		}
		if reqCount > 2 {
			s = fmt.Sprintf("... (%d packages already satisfied)\n", reqCount) + strings.Join(nonReq, "\n")
		}
	}
	// Collapse cargo Compiling
	if reCargoCompile.MatchString(s) {
		lines := strings.Split(s, "\n")
		var nonComp []string
		compCount := 0
		for _, l := range lines {
			if strings.HasPrefix(strings.TrimSpace(l), "Compiling ") {
				compCount++
			} else {
				nonComp = append(nonComp, l)
			}
		}
		if compCount > 3 {
			s = fmt.Sprintf("... (%d crates compiled)\n", compCount) + strings.Join(nonComp, "\n")
		}
	}
	// Strip funding messages
	lines := strings.Split(s, "\n")
	filtered := make([]string, 0, len(lines))
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.Contains(trimmed, "packages are looking for funding") || strings.Contains(trimmed, "run `npm fund`") {
			continue
		}
		filtered = append(filtered, l)
	}
	if len(filtered) > maxLines {
		return smartTruncateLines(filtered, maxLines)
	}
	return strings.Join(filtered, "\n")
}

// CompressContainerOutput truncates tabular docker/kubectl/system outputs while preserving column headers.
func CompressContainerOutput(s string, maxLines int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s
	}
	header := lines[0]
	rows := lines[1:]
	if len(rows) > maxLines {
		keep := maxLines - 2
		if keep < 5 {
			keep = 5
		}
		result := []string{header}
		result = append(result, rows[:keep]...)
		result = append(result, fmt.Sprintf("... (%d table entries truncated)", len(rows)-keep))
		return strings.Join(result, "\n")
	}
	return smartTruncateLines(lines, maxLines)
}

func smartTruncateLines(lines []string, maxLines int) string {
	if len(lines) <= maxLines {
		return strings.Join(lines, "\n")
	}
	headCount := (maxLines * 6) / 10
	tailCount := maxLines - headCount
	if headCount < 1 {
		headCount = 1
	}
	if tailCount < 1 {
		tailCount = 1
	}
	head := lines[:headCount]
	tail := lines[len(lines)-tailCount:]
	result := append(head, fmt.Sprintf("... (%d lines truncated)", len(lines)-headCount-tailCount))
	return strings.Join(append(result, tail...), "\n")
}

// CompressTextWithConfig compresses text according to RTKConfig settings.
func CompressTextWithConfig(text string, cfg RTKConfig) string {
	return CompressTextDetailed(text, cfg).Text
}

// CompressTextDetailed runs RTK compression and returns comprehensive metrics and metadata.
func CompressTextDetailed(text string, cfg RTKConfig) RTKCompressResult {
	origLen := len(text)
	origTokens := (origLen + 3) / 4
	if origLen == 0 {
		return RTKCompressResult{
			OriginalTokens:   0,
			CompressedTokens: 0,
			SavedTokens:      0,
			SavedPct:         0,
			Text:             "",
			DetectedCategory: "general",
			TechniquesUsed:   []string{"none"},
		}
	}

	maxLines, maxChars := resolveLimits(cfg)
	techniques := make([]string, 0, 4)
	curr := text

	// 1. Line deduplication
	if cfg.Deduplicate {
		if deduped, did := DeduplicateLines(curr); did {
			curr = deduped
			techniques = append(techniques, "line-deduplication")
		}
	}

	trimmed := strings.TrimSpace(curr)
	cat := DetectCategory(trimmed)

	// 2. Filter matching (OmniRoute 55 RTK Filters & category filtering)
	matchedFilter := MatchFilter(trimmed, "", cfg)
	if matchedFilter != nil {
		cat = matchedFilter.Category
		filtered, filterTechs := ApplyRTKFilter(matchedFilter, trimmed, maxLines)
		if filtered != trimmed {
			curr = filtered
			techniques = append(techniques, filterTechs...)
		}
	} else if isCatEnabled(cfg, cat) {
		switch cat {
		case "git":
			if isGitDiff(trimmed) {
				c := compressGitDiff(trimmed)
				if c != trimmed {
					curr = c
					techniques = append(techniques, "git-diff-filter")
				}
			} else if isGitLog(trimmed) {
				c := compressGitLog(trimmed)
				if c != trimmed {
					curr = c
					techniques = append(techniques, "git-log-filter")
				}
			} else if strings.Contains(trimmed, "On branch ") || strings.Contains(trimmed, "Changes to be committed:") {
				c := CompressGitStatus(trimmed)
				if c != trimmed {
					curr = c
					techniques = append(techniques, "git-status-filter")
				}
			}
		case "build":
			c := CompressBuildOutput(trimmed, maxLines)
			if c != trimmed {
				curr = c
				techniques = append(techniques, "build-filter")
			}
		case "test":
			c := CompressTestOutput(trimmed, maxLines)
			if c != trimmed {
				curr = c
				techniques = append(techniques, "test-filter")
			}
		case "package":
			c := CompressPackageOutput(trimmed, maxLines)
			if c != trimmed {
				curr = c
				techniques = append(techniques, "package-filter")
			}
		case "docker", "system":
			if isTreeOutput(trimmed) {
				c := compressTree(trimmed)
				if c != trimmed {
					curr = c
					techniques = append(techniques, "tree-filter")
				}
			} else if isGrepOutput(trimmed) {
				c := compressGrep(trimmed)
				if c != trimmed {
					curr = c
					techniques = append(techniques, "grep-filter")
				}
			} else if reDockerPsHeader.MatchString(trimmed) || reKubectlGetHeader.MatchString(trimmed) || rePsAuxHeader.MatchString(trimmed) || reLsLine.MatchString(trimmed) {
				c := CompressContainerOutput(trimmed, maxLines)
				if c != trimmed {
					curr = c
					techniques = append(techniques, "system-table-filter")
				}
			}
		default:
			// General fallback
			if len(trimmed) >= MinCompressSize {
				if isGitDiff(trimmed) {
					curr = compressGitDiff(trimmed)
					techniques = append(techniques, "git-diff-filter")
				} else if isGitLog(trimmed) {
					curr = compressGitLog(trimmed)
					techniques = append(techniques, "git-log-filter")
				} else if isGrepOutput(trimmed) {
					curr = compressGrep(trimmed)
					techniques = append(techniques, "grep-filter")
				} else if isTreeOutput(trimmed) {
					curr = compressTree(trimmed)
					techniques = append(techniques, "tree-filter")
				}
			}
		}
	}

	// 3. Smart line truncation if still over maxLines
	lines := strings.Split(curr, "\n")
	if len(lines) > maxLines && len(curr) >= MinCompressSize {
		curr = smartTruncateLines(lines, maxLines)
		techniques = append(techniques, "smart-truncation")
	}

	// 4. Character truncation if still over maxChars
	if len(curr) > maxChars {
		// Cut at newline boundary near maxChars if possible
		cutIdx := maxChars
		if idx := strings.LastIndex(curr[:maxChars], "\n"); idx > maxChars/2 {
			cutIdx = idx
		}
		curr = curr[:cutIdx] + fmt.Sprintf("\n... (output truncated at %d chars)", maxChars)
		techniques = append(techniques, "char-truncation")
	}

	if len(techniques) == 0 {
		techniques = append(techniques, "none")
	}

	compLen := len(curr)
	compTokens := (compLen + 3) / 4
	savedTokens := origTokens - compTokens
	if savedTokens < 0 {
		savedTokens = 0
	}
	var savedPct float64
	if origTokens > 0 && savedTokens > 0 {
		savedPct = math.Round((float64(savedTokens)/float64(origTokens)*100.0)*10) / 10
	}

	return RTKCompressResult{
		OriginalTokens:   origTokens,
		CompressedTokens: compTokens,
		SavedTokens:      savedTokens,
		SavedPct:         savedPct,
		Text:             curr,
		DetectedCategory: cat,
		TechniquesUsed:   techniques,
	}
}
