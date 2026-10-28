package tokensaver

import "strings"

// maxPreservedDiagnosticLines caps how many diagnostic lines a truncation may
// re-add.
//
// The cap bounds the result: a body where every line mentions "error" would
// otherwise grow instead of shrink, and the point of the truncation is to
// return less than the body it replaced. The first matches in document order
// win, so the outcome is the same on every run.
const maxPreservedDiagnosticLines = 20

// isDiagnosticLine reports whether a line carries a diagnostic the agent needs
// to see: the severity vocabulary the filters already use, or one of the
// matched filter's own error and summary patterns.
//
// The filter's own patterns come first because they are the rules that filter
// was written with: the filter that selected the body is the one that knows
// what its failure looks like. The severity vocabulary is the fallback, for a
// body no filter claimed.
func isDiagnosticLine(line string, f *RTKFilter) bool {
	if f != nil {
		for _, re := range f.ErrorPatterns {
			if re.MatchString(line) {
				return true
			}
		}
		for _, re := range f.SummaryPatterns {
			if re.MatchString(line) {
				return true
			}
		}
	}
	return reSeverity.MatchString(line)
}

// preservedDiagnosticLines returns the diagnostic lines of the middle of a
// body, in their original order and without duplicates.
//
// A head/tail truncation keeps the beginning and the end of a body and drops
// everything between them, which is where a single failure line usually sits:
// the run looked fine for two hundred lines and then broke once. That line is
// the one the agent needs, so it is recovered instead of dropped.
//
// The lines that are kept anyway are passed in, so a line that also appears
// in the head or the tail is not added a second time.
func preservedDiagnosticLines(middle, kept []string, f *RTKFilter) []string {
	seen := make(map[string]bool, len(kept))
	for _, l := range kept {
		seen[strings.TrimSpace(l)] = true
	}
	out := make([]string, 0, maxPreservedDiagnosticLines)
	for _, l := range middle {
		if len(out) >= maxPreservedDiagnosticLines {
			break
		}
		key := strings.TrimSpace(l)
		if key == "" || seen[key] {
			continue
		}
		if !isDiagnosticLine(l, f) {
			continue
		}
		seen[key] = true
		out = append(out, l)
	}
	return out
}

// splitForTruncation splits a body into the head, the tail and the diagnostic
// lines that the middle would otherwise lose.
//
// The caller formats the marker, because the two truncation sites word it
// differently ("... (N lines truncated)" and "... (N lines truncated by
// <filter>)"). The count the caller reports is the number of lines actually
// dropped, so the marker never claims to have dropped a line that is still
// in the result.
func splitForTruncation(lines []string, headCount, tailCount int, f *RTKFilter) (head, preserved, tail []string, dropped int) {
	head = lines[:headCount]
	tail = lines[len(lines)-tailCount:]
	middle := lines[headCount : len(lines)-tailCount]

	kept := make([]string, 0, len(head)+len(tail))
	kept = append(kept, head...)
	kept = append(kept, tail...)

	preserved = preservedDiagnosticLines(middle, kept, f)
	dropped = len(middle) - len(preserved)
	return head, preserved, tail, dropped
}
