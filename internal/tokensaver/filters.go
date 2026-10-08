package tokensaver

import (
	"embed"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

//go:embed filters/*.json
var filtersFS embed.FS

// RawFilterJSON represents the JSON schema of a filter in filters/*.json.
type RawFilterJSON struct {
	ID          string            `json:"id"`
	Label       string            `json:"label"`
	Description string            `json:"description"`
	Category    string            `json:"category"`
	Priority    int               `json:"priority"`
	Match       RawFilterMatch    `json:"match"`
	Rules       RawFilterRules    `json:"rules"`
	Preserve    RawFilterPreserve `json:"preserve"`
}

type RawFilterMatch struct {
	Commands    []string `json:"commands"`
	Patterns    []string `json:"patterns"`
	OutputTypes []string `json:"outputTypes"`
}

type RawFilterRules struct {
	StripAnsi        bool             `json:"stripAnsi"`
	MatchOutput      []RawMatchOutput `json:"matchOutput"`
	IncludePatterns  []string         `json:"includePatterns"`
	DropPatterns     []string         `json:"dropPatterns"`
	CollapsePatterns []string         `json:"collapsePatterns"`
	Deduplicate      bool             `json:"deduplicate"`
	MaxLines         int              `json:"maxLines"`
	HeadLines        int              `json:"headLines"`
	TailLines        int              `json:"tailLines"`
	OnEmpty          string           `json:"onEmpty"`
}

type RawMatchOutput struct {
	Pattern string `json:"pattern"`
	Message string `json:"message"`
	Unless  string `json:"unless,omitempty"`
}

type RawFilterPreserve struct {
	ErrorPatterns   []string `json:"errorPatterns"`
	SummaryPatterns []string `json:"summaryPatterns"`
}

// RTKFilter represents a loaded and compiled RTK filter.
type RTKFilter struct {
	ID               string
	Label            string
	Description      string
	RawCategory      string
	Category         string // Normalized category: git, build, test, package, docker, system
	Priority         int
	Commands         []*regexp.Regexp
	Patterns         []*regexp.Regexp
	IncludePatterns  []*regexp.Regexp
	DropPatterns     []*regexp.Regexp
	CollapsePatterns []*regexp.Regexp
	MatchOutputs     []CompiledMatchOutput
	ErrorPatterns    []*regexp.Regexp
	SummaryPatterns  []*regexp.Regexp
	StripAnsi        bool
	Deduplicate      bool
	MaxLines         int
	HeadLines        int
	TailLines        int
	OnEmpty          string
}

type CompiledMatchOutput struct {
	Pattern *regexp.Regexp
	Message string
	Unless  *regexp.Regexp
}

// FilterInfo represents filter metadata returned by the API.
type FilterInfo struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Category    string `json:"category"`
	Description string `json:"description"`
	Priority    int    `json:"priority"`
	Enabled     bool   `json:"enabled"`
}

var (
	loadedFilters     []*RTKFilter
	loadFiltersOnce   sync.Once
	reAnsi            = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	severityWordsList = []string{
		"error", "failed", "failure", "exception", "traceback", "fail",
		"ts\\d{4}", "✖", "fatal", "panic", "critical",
	}
	reSeverity = regexp.MustCompile(`(?i)` + strings.Join(severityWordsList, "|"))
)

// NormalizeFilterCategory maps raw category to standard categories.
func NormalizeFilterCategory(cat string) string {
	switch strings.ToLower(strings.TrimSpace(cat)) {
	case "git":
		return "git"
	case "build":
		return "build"
	case "test":
		return "test"
	case "package":
		return "package"
	case "docker", "cloud", "infra":
		return "docker"
	case "system", "shell", "generic":
		return "system"
	default:
		return "system"
	}
}

func compileRegexSafe(pat string) *regexp.Regexp {
	if pat == "" {
		return nil
	}
	// Case-insensitive compilation matching upstream TypeScript behavior
	re, err := regexp.Compile("(?i)" + pat)
	if err != nil {
		// Fallback without prefix
		re, err = regexp.Compile(pat)
		if err != nil {
			return nil
		}
	}
	return re
}

func compileRegexList(patterns []string) []*regexp.Regexp {
	res := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		if re := compileRegexSafe(p); re != nil {
			res = append(res, re)
		}
	}
	return res
}

// LoadFilters dynamically loads and parses the 55 embedded filter JSON files.
func LoadFilters() []*RTKFilter {
	loadFiltersOnce.Do(func() {
		entries, err := filtersFS.ReadDir("filters")
		if err != nil {
			return
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			data, err := filtersFS.ReadFile("filters/" + entry.Name())
			if err != nil {
				continue
			}
			var raw RawFilterJSON
			if err := json.Unmarshal(data, &raw); err != nil {
				continue
			}

			normCat := NormalizeFilterCategory(raw.Category)

			matchOutputs := make([]CompiledMatchOutput, 0, len(raw.Rules.MatchOutput))
			for _, mo := range raw.Rules.MatchOutput {
				pat := compileRegexSafe(mo.Pattern)
				if pat == nil {
					continue
				}
				var un *regexp.Regexp
				if mo.Unless != "" {
					un = compileRegexSafe(mo.Unless)
				}
				matchOutputs = append(matchOutputs, CompiledMatchOutput{
					Pattern: pat,
					Message: mo.Message,
					Unless:  un,
				})
			}

			f := &RTKFilter{
				ID:               raw.ID,
				Label:            raw.Label,
				Description:      raw.Description,
				RawCategory:      raw.Category,
				Category:         normCat,
				Priority:         raw.Priority,
				Commands:         compileRegexList(raw.Match.Commands),
				Patterns:         compileRegexList(raw.Match.Patterns),
				IncludePatterns:  compileRegexList(raw.Rules.IncludePatterns),
				DropPatterns:     compileRegexList(raw.Rules.DropPatterns),
				CollapsePatterns: compileRegexList(raw.Rules.CollapsePatterns),
				MatchOutputs:     matchOutputs,
				ErrorPatterns:    compileRegexList(raw.Preserve.ErrorPatterns),
				SummaryPatterns:  compileRegexList(raw.Preserve.SummaryPatterns),
				StripAnsi:        raw.Rules.StripAnsi,
				Deduplicate:      raw.Rules.Deduplicate,
				MaxLines:         raw.Rules.MaxLines,
				HeadLines:        raw.Rules.HeadLines,
				TailLines:        raw.Rules.TailLines,
				OnEmpty:          raw.Rules.OnEmpty,
			}
			loadedFilters = append(loadedFilters, f)
		}

		// Sort by Priority descending, then ID ascending
		sort.Slice(loadedFilters, func(i, j int) bool {
			if loadedFilters[i].Priority != loadedFilters[j].Priority {
				return loadedFilters[i].Priority > loadedFilters[j].Priority
			}
			return loadedFilters[i].ID < loadedFilters[j].ID
		})
	})
	return loadedFilters
}

// GetFilterCatalog returns all filters mapped to FilterInfo with enabled status.
func GetFilterCatalog(cfg RTKConfig) []FilterInfo {
	filters := LoadFilters()
	res := make([]FilterInfo, len(filters))
	for i, f := range filters {
		res[i] = FilterInfo{
			ID:          f.ID,
			Label:       f.Label,
			Category:    f.Category,
			Description: f.Description,
			Priority:    f.Priority,
			Enabled:     IsFilterEnabled(f, cfg),
		}
	}
	return res
}

// IsFilterEnabled checks if a filter is enabled according to category settings and filter ID overrides.
func IsFilterEnabled(f *RTKFilter, cfg RTKConfig) bool {
	// 1. Check explicit per-filter override if present
	if cfg.Filters != nil {
		if enabled, exists := cfg.Filters[f.ID]; exists {
			return enabled
		}
	}
	// 2. Fall back to category enabled status
	return isCatEnabled(cfg, f.Category)
}

// A JSON document must never be rewritten by a prose filter. Every filter
// reduces its input line by line, so applying one to structured output silently
// deletes the data lines and leaves unparseable JSON behind. The per-filter
// command patterns cannot cover it, because MatchFilter is called with an empty
// command (rtk.go:393) and only the content patterns remain — a `gh api` body
// containing a github.com URL matches gh.json's first content pattern and is
// filtered as if it were prose.
//
// Go's RE2 has no negative lookahead, so the two filters that document this
// exclusion upstream (gh, kubectl) cannot express it as a single pattern.
// Detection is therefore done on the payload itself: anything that parses as
// JSON is passed through untouched.

// reStructuredJSON matches a complete JSON document spanning the whole input.
var reStructuredJSON = regexp.MustCompile(`\A\s*[[{][\s\S]*[\]}]\s*\z`)

// isStructuredOutput reports whether text is a single JSON document, i.e. the
// kind of payload a line-based filter would corrupt.
//
// The check stays deliberately permissive. This gate exists so a body v2 would
// reject still reaches the model intact rather than being filtered line by line,
// so duplicate names and invalid UTF-8 must not fail it; IsValid rejects both
// by default. AllowDuplicateNames and AllowInvalidUTF8 restore what a lenient
// reader accepts, leaving only genuine syntax errors to reject the payload.
func isStructuredOutput(text string) bool {
	trimmed := strings.TrimSpace(text)
	if !reStructuredJSON.MatchString(trimmed) {
		return false
	}
	return jsontext.Value(trimmed).IsValid(jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))
}

// MatchFilter finds the highest priority matching enabled filter for the given text or command.
func MatchFilter(text string, command string, cfg RTKConfig) *RTKFilter {
	filters := LoadFilters()
	trimmedCmd := strings.TrimSpace(command)

	// Phase 1: Try command matching if command is provided
	if trimmedCmd != "" {
		for _, f := range filters {
			if !IsFilterEnabled(f, cfg) {
				continue
			}
			for _, cmdRe := range f.Commands {
				if cmdRe.MatchString(trimmedCmd) {
					return f
				}
			}
		}
	}

	// A JSON document is never line-filtered. RE2 has no negative lookahead, so
	// the filters that document this exclusion upstream (gh, kubectl) cannot
	// express it as a pattern; detection is done on the payload itself. Every
	// filter reduces its input line by line, and json-output included — its
	// includePatterns keep structural lines and a few named keys, so applying it
	// deletes every other data line and the result no longer parses.
	if isStructuredOutput(text) {
		return nil
	}

	// Phase 2: Try pattern matching against the content
	for _, f := range filters {
		if !IsFilterEnabled(f, cfg) {
			continue
		}
		// Skip generic-output in first pass so specific filters match first
		if f.ID == "generic-output" {
			continue
		}
		for _, patRe := range f.Patterns {
			if patRe.MatchString(text) {
				return f
			}
		}
	}

	// Fall back to generic-output if enabled
	for _, f := range filters {
		if f.ID == "generic-output" && IsFilterEnabled(f, cfg) {
			return f
		}
	}

	return nil
}

// ApplyRTKFilter applies the filter rules to text.
func ApplyRTKFilter(f *RTKFilter, text string, maxLinesLimit int) (string, []string) {
	techniques := make([]string, 0, 4)
	techniques = append(techniques, f.ID)

	curr := text
	if f.StripAnsi {
		curr = reAnsi.ReplaceAllString(curr, "")
	}

	// matchOutput check
	if len(f.MatchOutputs) > 0 {
		for _, mo := range f.MatchOutputs {
			if mo.Pattern.MatchString(curr) {
				if mo.Unless != nil && mo.Unless.MatchString(curr) {
					continue
				}
				techniques = append(techniques, f.ID+":match-output")
				return mo.Message, techniques
			}
		}
	}

	rawLines := strings.Split(curr, "\n")
	origLineCount := len(rawLines)
	lines := rawLines

	// 1. Drop patterns
	if len(f.DropPatterns) > 0 {
		filtered := make([]string, 0, len(lines))
		for _, line := range lines {
			drop := false
			for _, dp := range f.DropPatterns {
				if dp.MatchString(line) {
					drop = true
					break
				}
			}
			if !drop {
				filtered = append(filtered, line)
			}
		}
		lines = filtered
	}

	// 2. Include patterns (severity lines bypass drop if includePatterns specified)
	if len(f.IncludePatterns) > 0 {
		kept := make([]string, 0, len(lines))
		for _, line := range lines {
			keep := false
			for _, ip := range f.IncludePatterns {
				if ip.MatchString(line) {
					keep = true
					break
				}
			}
			if !keep && reSeverity.MatchString(line) {
				keep = true
			}
			if keep {
				kept = append(kept, line)
			}
		}
		if len(kept) > 0 {
			lines = kept
			techniques = append(techniques, f.ID+":include")
		}
	}

	// 3. Collapse patterns (collapse duplicates matching collapse patterns)
	if len(f.CollapsePatterns) > 0 {
		seen := make(map[string]bool)
		collapsed := make([]string, 0, len(lines))
		for _, line := range lines {
			matchesCollapse := false
			for _, cp := range f.CollapsePatterns {
				if cp.MatchString(line) {
					matchesCollapse = true
					break
				}
			}
			if matchesCollapse {
				key := strings.TrimSpace(line)
				if seen[key] {
					continue
				}
				seen[key] = true
			}
			collapsed = append(collapsed, line)
		}
		lines = collapsed
	}

	// 4. Per-filter deduplicate
	if f.Deduplicate {
		joined := strings.Join(lines, "\n")
		if deduped, did := DeduplicateLines(joined); did {
			lines = strings.Split(deduped, "\n")
			techniques = append(techniques, f.ID+":deduplicate")
		}
	}

	// 5. Line truncation (smart truncation using filter's maxLines or config maxLines)
	effMaxLines := f.MaxLines
	if effMaxLines <= 0 || (maxLinesLimit > 0 && maxLinesLimit < effMaxLines) {
		effMaxLines = maxLinesLimit
	}
	if effMaxLines > 0 && len(lines) > effMaxLines {
		headCount := f.HeadLines
		tailCount := f.TailLines
		if headCount <= 0 {
			headCount = effMaxLines / 2
		}
		if tailCount <= 0 {
			tailCount = effMaxLines / 2
		}
		if headCount+tailCount >= len(lines) {
			headCount = len(lines) / 2
			tailCount = len(lines) - headCount
		}
		head := lines[:headCount]
		tail := lines[len(lines)-tailCount:]
		truncMsg := fmt.Sprintf("... (%d lines truncated by %s)", len(lines)-headCount-tailCount, f.ID)
		result := make([]string, 0, len(head)+len(tail)+1)
		result = append(result, head...)
		result = append(result, truncMsg)
		result = append(result, tail...)
		lines = result
		techniques = append(techniques, f.ID+":truncate")
	}

	out := strings.Join(lines, "\n")
	if strings.TrimSpace(out) == "" && f.OnEmpty != "" {
		out = f.OnEmpty
	}

	if len(lines) < origLineCount {
		techniques = append(techniques, f.ID+":filter")
	}

	return out, techniques
}
