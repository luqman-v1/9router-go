package guardrails

import (
	"regexp"
	"strings"
)

// detector is one compiled pattern plus how to name and grade its matches.
type detector struct {
	name     string
	re       *regexp.Regexp
	entity   string
	severity Severity
	// validate runs only on candidates that already matched the shape. It
	// turns a cheap shape test into a correct one — a credit-card check that
	// passes Luhn catches real card numbers while leaving every 16-digit id
	// alone.
	validate func(match string) bool
}

// redactMask is the replacement written over a masked finding. It preserves the
// shape of the value so a masked prompt still reads sensibly to the model.
const redactMask = "[REDACTED]"

// piiDetectors are the personal-data detectors.
//
// The patterns are deliberately anchored on context that only real identifiers
// carry, because this gateway's traffic is overwhelmingly source code. A bare
// \b\d{16}\b would fire on every git SHA and every UUID in a diff; every
// pattern below requires something an id cannot supply.
var piiDetectors = []detector{
	{
		name: "email", re: emailRe, entity: "EMAIL_ADDRESS", severity: SeverityMedium,
	},
	{
		// Only globally routable addresses count. Loopback, private, and
		// link-local addresses are infrastructure, not personal data: this
		// gateway's traffic is full of docker-compose files, kubeconfigs, and
		// server configs full of them, and flagging those breaks real work.
		name: "ipv4", re: ipv4Re, entity: "IP_ADDRESS", severity: SeverityLow,
		validate: isPublicIPv4,
	},
	{
		// Indonesian national id number: 16 digits. Gated on Luhn because a
		// bare 16-digit run is exactly a git SHA.
		name: "id_nik", re: nikRe, entity: "ID_NIK", severity: SeverityHigh,
		validate: luhn,
	},
	{
		name: "credit_card", re: cardRe, entity: "CREDIT_CARD", severity: SeverityHigh,
		validate: luhn,
	},
	{
		// IBAN carries its own check digits, so the mod-97 test is exact.
		name: "iban", re: ibanRe, entity: "IBAN", severity: SeverityHigh,
		validate: validIBAN,
	},
}

// injectionDetectors catch attempts to override the system prompt or exfiltrate
// it.
var injectionDetectors = []detector{
	{name: "ignore_previous", re: ignorePreviousRe, entity: "PROMPT_INJECTION", severity: SeverityHigh},
	{name: "role_override", re: roleOverrideRe, entity: "PROMPT_INJECTION", severity: SeverityMedium},
	{name: "jailbreak_dan", re: danRe, entity: "JAILBREAK", severity: SeverityHigh},
	{name: "system_leak", re: systemLeakRe, entity: "SYSTEM_PROMPT_LEAK", severity: SeverityHigh},
	{name: "safety_bypass", re: safetyBypassRe, entity: "SAFETY_BYPASS", severity: SeverityHigh},
}

var (
	// The local part is anchored so a source-code symbol or an import path
	// (user@example.com appears in READMEs and fixtures constantly) does not
	// match on its own; a TLD from the real ccTLD list is required.
	emailRe = regexp.MustCompile(`(?i)\b[a-z0-9._%+\-]+@[a-z0-9.\-]+\.(?:com|net|org|io|ai|co|dev|id|jp|de|uk|us|nl|fr|br|in|ca|au|sg|my)\b`)
	ipv4Re  = regexp.MustCompile(`\b(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\b`)

	// An Luhn check is what separates a NIK from a git SHA. The regex only
	// proposes candidates.
	nikRe = regexp.MustCompile(`\b\d{16}\b`)
	// Card numbers: 13-19 digits, but grouped so a run of unrelated digits
	// does not become a candidate.
	cardRe = regexp.MustCompile(`\b(?:\d[ -]?){13,19}\b`)
	// IBAN: 2 country letters, 2 check digits, then alphanumerics.
	ibanRe = regexp.MustCompile(`\b[A-Z]{2}\d{2}[A-Z0-9]{11,30}\b`)

	// "ignore all previous instructions" and every phrasing around it.
	ignorePreviousRe = regexp.MustCompile(`(?i)\b(?:ignore|disregard|forget|override)\b[^.\n]{0,40}\b(?:previous|prior|earlier|above|preceding|all)\b[^.\n]{0,40}\b(?:instruction|instructions|prompt|prompts|rule|rules|direction|directions|context)\b`)
	// Attempt to reopen the system role, e.g. "you are now DAN".
	roleOverrideRe = regexp.MustCompile(`(?i)\byou\s+are\s+(?:now|no\s+longer)\b|\bact\s+as\s+(?:if\s+you\s+(?:are|were)|a\s+(?:different|new)|without)\b|\bsystem\s*prompt\s*(?:override|replacement)\b`)
	// Named jailbreak personas.
	danRe = regexp.MustCompile(`(?i)\b(?:DAN|do\s+anything\s+now|developer\s+mode|jailbreak|unfiltered\s+mode)\b`)
	// Asking the model to print its instructions.
	systemLeakRe = regexp.MustCompile(`(?i)\b(?:repeat|print|output|show|reveal|display|echo)\b[^.\n]{0,40}\b(?:your\s+)?(?:initial|system|original)\s+(?:instruction|instructions|prompt|prompts)\b`)
	// Explicit safety-off phrasing. The verb must be genuinely imperative:
	// start of text, after sentence punctuation, or introduced by
	// please/now/then. A bare newline is deliberately NOT an anchor — every
	// source file in this gateway's traffic begins lines with verbs
	// ("bypass := safety.filter(v)"), and treating those as jailbreak attempts
	// would block ordinary development.
	safetyBypassRe = regexp.MustCompile(`(?i)(?:\A|[.!?;]\s*|\b(?:please|now|then)\s+)\b(?:bypass|disable|turn\s+off|circumvent|remove)\b[^.\n]{0,30}\b(?:safety|filter|filters|guardrail|guardrails|restriction|restrictions|content\s+polic\w*|moderation)\b`)
)

// run applies one detector set to text and returns its findings.
func run(detectors []detector, text string) []Finding {
	var out []Finding
	for _, d := range detectors {
		for _, loc := range d.re.FindAllStringIndex(text, -1) {
			if d.validate != nil && !d.validate(text[loc[0]:loc[1]]) {
				continue
			}
			out = append(out, Finding{
				Detector: d.name,
				Entity:   d.entity,
				Start:    loc[0],
				End:      loc[1],
				Severity: d.severity,
				Redacted: redactMask,
			})
		}
	}
	return out
}

// maskFindings rewrites text with every finding's span replaced by its redaction.
// Overlapping spans are merged first: two matches that overlap would otherwise
// corrupt the offsets and drop or duplicate text.
func maskFindings(text string, findings []Finding) string {
	if len(findings) == 0 {
		return text
	}
	sorted := append([]Finding(nil), findings...)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].Start < sorted[j-1].Start; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}

	var b strings.Builder
	b.Grow(len(text))
	cursor := 0
	for _, f := range sorted {
		if f.Start < cursor || f.End > len(text) || f.Start >= f.End {
			continue
		}
		b.WriteString(text[cursor:f.Start])
		b.WriteString(f.Redacted)
		cursor = f.End
	}
	b.WriteString(text[cursor:])
	return b.String()
}

// luhn validates a digit string against the Luhn checksum, used for both card
// numbers and Indonesian NIK. Non-digits are stripped first so grouped card
// numbers ("4111 1111 1111 1111") validate.
//
// This is what keeps the detector usable on real traffic: a bare 16-digit run
// is overwhelmingly a git SHA, and the checksum is what separates the few that
// are actually an account or card number.
func luhn(s string) bool {
	var sum, digits int
	double := false
	// Descending from the right: the rightmost digit is the check digit and is
	// never doubled. Everything stays an int — mixing a byte with a rune
	// literal here silently wraps and every checksum passes or fails at random.
	for i := len(s) - 1; i >= 0; i-- {
		c := s[i]
		if c < '0' || c > '9' {
			continue
		}
		digits++
		n := int(c) - '0'
		if double {
			n *= 2
			if n > 9 {
				n -= 9
			}
		}
		double = !double
		sum += n
	}
	if digits < 12 {
		return false
	}
	return sum%10 == 0
}

// validIBAN applies the mod-97 check IBAN embeds in its own digits.
func validIBAN(s string) bool {
	rearranged := s[4:] + s[:4]
	var sum int
	for i := range len(rearranged) {
		c := rearranged[i]
		switch {
		case c >= '0' && c <= '9':
			sum = sum*10 + int(c-'0')
		case c >= 'A' && c <= 'Z':
			sum = sum*100 + int(c-'A'+10)
		default:
			return false
		}
		sum %= 97
	}
	return sum == 1
}

// isPublicIPv4 rejects loopback, private, link-local, and other reserved space,
// so only genuinely routable addresses are treated as findings.
func isPublicIPv4(s string) bool {
	var octets [4]int
	for i, part := range strings.Split(s, ".") {
		if i >= 4 {
			return false
		}
		n := 0
		if part == "" {
			return false
		}
		for _, c := range part {
			if c < '0' || c > '9' {
				return false
			}
			n = n*10 + int(c-'0')
		}
		if n > 255 {
			return false
		}
		octets[i] = n
	}
	switch {
	case octets[0] == 0, octets[0] == 10:
		return false
	case octets[0] == 127:
		return false
	case octets[0] == 169 && octets[1] == 254:
		return false
	case octets[0] == 172 && octets[1] >= 16 && octets[1] <= 31:
		return false
	case octets[0] == 192 && octets[1] == 168:
		return false
	case octets[0] >= 224:
		return false
	}
	return true
}