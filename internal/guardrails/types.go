// Package guardrails scans request and response content for sensitive data
// and prompt-injection attempts before the text reaches, or leaves, an
// upstream provider.
//
// The MVP is regex-only and entirely offline: no external moderation service,
// no embedding model, no network call of any kind. That is a deliberate
// constraint, not a gap — a gateway cannot ship a feature that silently adds a
// latency- and cost-bearing dependency to every request, and an operator who
// wants Presidio or an embedding classifier can add one behind the same
// policy later.
package guardrails

// Action is what a detector decided to do with the content it matched.
type Action string

const (
	// ActionAllow passes the content through untouched.
	ActionAllow Action = "allow"
	// ActionLogOnly records the decision and changes nothing else.
	ActionLogOnly Action = "log_only"
	// ActionWarn records the decision and warns, but passes the content.
	ActionWarn Action = "warn"
	// ActionMask redacts the matched spans and passes the rewritten content.
	ActionMask Action = "mask"
	// ActionBlock rejects the request or truncates the stream.
	ActionBlock Action = "block"
)

// actionRank orders actions by severity. StrictestAction picks the maximum, so
// two detectors that both fire cannot soften each other's verdict: a warn from
// one detector and a block from another resolve to block.
var actionRank = map[Action]int{
	ActionAllow:   0,
	ActionLogOnly: 1,
	ActionWarn:    2,
	ActionMask:    3,
	ActionBlock:   4,
}

// StrictestAction returns whichever of the two actions is more severe. An empty
// action is treated as allow, so a detector that fires without naming an action
// cannot accidentally become stricter than allow by omission.
func StrictestAction(a, b Action) Action {
	if actionRank[a] >= actionRank[b] {
		return a
	}
	return b
}

// Direction distinguishes the content entering the gateway from the content
// leaving it toward the client.
type Direction string

const (
	DirectionInbound  Direction = "inbound"
	DirectionOutbound Direction = "outbound"
)

// Severity grades a finding.
type Severity string

const (
	SeverityLow    Severity = "low"
	SeverityMedium Severity = "medium"
	SeverityHigh   Severity = "high"
)

// Scope names how widely a policy applies. The resolver walks these from least
// to most specific and lets the most specific win.
type Scope string

const (
	ScopeGlobal   Scope = "global"
	ScopeProvider Scope = "provider"
	ScopeModel    Scope = "model"
	ScopeChain    Scope = "chain"
	ScopeAPIKey   Scope = "apikey"
)

// Finding is one detector match. Start and End are byte offsets into the
// scanned text, so Mask can redact precisely rather than blanking the whole
// message.
type Finding struct {
	Detector string   `json:"detector"`
	Entity   string   `json:"entity"`
	Start    int      `json:"start"`
	End      int      `json:"end"`
	Severity Severity `json:"severity"`
	// Redacted is the replacement text used when the winning action is mask.
	// It never contains the matched value itself.
	Redacted string `json:"redacted,omitempty"`
}

// Decision is the outcome of scanning one piece of content.
type Decision struct {
	Detector string    `json:"detector"`
	Action   Action    `json:"action"`
	Findings []Finding `json:"findings,omitempty"`
	// Mutated is the rewritten content, set only when the winning action is
	// mask and something was actually redacted.
	Mutated string `json:"-"`
	Reason  string `json:"reason,omitempty"`
}

// Blocked reports whether the decision must stop the request.
func (d *Decision) Blocked() bool { return d.Action == ActionBlock }

// Mutated reports whether the content was rewritten.
func (d *Decision) WasMutated() bool { return d.Mutated != "" }