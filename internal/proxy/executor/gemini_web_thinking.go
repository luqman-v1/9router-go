package executor

// Thinking levels on Gemini Web.
//
// The site has three reasoning levels and they are not ordered: 1 is
// "standard", 4 is "medium" and 2 is "extended". The names map onto the
// client's vocabulary as low/medium/high.
//
// The registry publishes which levels each model actually offers, in a policy
// field. That matters because a level the model does not have is not a
// smaller answer — the site substitutes a different model, silently, and the
// answer arrives under the requested model's name. Reading the policy is what
// keeps the request inside what the model can serve.
//
// The same policy is what the reference bridge reads
// (azrinaldi/gemini-stealth-bridge internal/gemini/models.go,
// ParseThinkingPolicy), and its enum mapping is the site's own.
var geminiWebThinkingLevelNames = map[int]string{
	1: "low",
	4: "medium",
	2: "high",
}

// geminiWebThinkingLevelEnums is the inverse of the names above.
var geminiWebThinkingLevelEnums = map[string]int{
	"low":    1,
	"medium": 4,
	"high":   2,
}

// geminiWebParseThinkingPolicy reads the registry's policy field into the
// levels each model advertises.
//
// The field is a two-element array: a policy name, then one entry per model.
// An entry is a descriptor — whose second member is the model number —
// followed by the level rows and the tail.
//
// A level row's first member is the site's enum. An enum this build does not
// know is dropped rather than advertised under a guessed name: the level would
// then be sent as a value the site does not accept.
//
// A registry with no policy returns an empty map and a false "known" flag,
// which the caller reads as "fall back to the name heuristics" — a registry
// that published a policy and left a model out is a different thing, and says
// the model has no levels at all.
func geminiWebParseThinkingPolicy(payload any) (map[int][]string, bool) {
	policy := map[int][]string{}
	rows, ok := payload.([]any)
	if !ok || len(rows) < 2 {
		return policy, false
	}
	entries, ok := rows[1].([]any)
	if !ok {
		return policy, false
	}
	for _, entryAny := range entries {
		entry, ok := entryAny.([]any)
		if !ok || len(entry) < 2 {
			continue
		}
		descriptor, ok := entry[0].([]any)
		if !ok {
			continue
		}
		number, ok := geminiWebIntAt(descriptor, 1)
		if !ok || number <= 0 {
			continue
		}
		levelRows, ok := entry[1].([]any)
		if !ok {
			continue
		}
		var levels []string
		for _, levelAny := range levelRows {
			level, ok := levelAny.([]any)
			if !ok || len(level) == 0 {
				continue
			}
			enum, ok := geminiWebIntAt(level, 0)
			if !ok {
				continue
			}
			if name, known := geminiWebThinkingLevelNames[enum]; known {
				levels = append(levels, name)
			}
		}
		if len(levels) > 0 {
			policy[number] = levels
		}
	}
	return policy, len(policy) > 0
}

// geminiWebSupportsThinkingLevel reports whether a model offers a level.
//
// A model that advertises its levels is the authority: a level it does not
// list is one it cannot serve.
func geminiWebSupportsThinkingLevel(model *geminiWebModel, level string) bool {
	if model == nil || len(model.ThinkingLevels) == 0 {
		return true
	}
	for _, offered := range model.ThinkingLevels {
		if offered == level {
			return true
		}
	}
	return false
}

// geminiWebDefaultThinkingLevel picks the level a turn uses when the client
// named none.
//
// Google's own default is medium where a model offers it, and the model's own
// ordering decides otherwise — so the answer is always a level the model
// actually has.
//
// A registry that published a policy and left this model out of it is a
// different thing: the model has no levels at all, so there is nothing to
// pick and the request goes out at the site's own floor.
func geminiWebDefaultThinkingLevel(model *geminiWebModel) int {
	if model == nil {
		return geminiWebThinkingLevelEnums["low"]
	}
	if len(model.ThinkingLevels) == 0 {
		if model.ThinkingPolicyKnown {
			return geminiWebThinkingLevelEnums["low"]
		}
		return geminiWebThinkingLevelEnums["low"]
	}
	for _, level := range model.ThinkingLevels {
		if level == "medium" {
			return geminiWebThinkingLevelEnums[level]
		}
	}
	return geminiWebThinkingLevelEnums[model.ThinkingLevels[0]]
}

// geminiWebClampThinkingLevel keeps a requested level inside what the model
// offers.
//
// A client asking for a level the model does not have gets the model's own
// default rather than the closest level it does have: sending "high" to a
// model that only thinks at "low" is a request the site answers with a
// different model, and the caller cannot see that it happened.
func geminiWebClampThinkingLevel(model *geminiWebModel, level int) int {
	if level <= 0 {
		return geminiWebDefaultThinkingLevel(model)
	}
	if model == nil || len(model.ThinkingLevels) == 0 {
		return level
	}
	name, known := geminiWebThinkingLevelNames[level]
	if !known {
		return geminiWebDefaultThinkingLevel(model)
	}
	if geminiWebSupportsThinkingLevel(model, name) {
		return level
	}
	return geminiWebDefaultThinkingLevel(model)
}
