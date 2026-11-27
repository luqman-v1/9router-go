package executor

import (
	json "encoding/json/v2"

	"9router/proxy/internal/translator"
)

// shapeOpenCodeFreeBody applies the v2 free-tier gate shaping to a body on one
// of the keyless lanes.
//
// The two request kinds need different shapes and the gate treats them
// differently, which is why this is a decision rather than a passthrough to
// ConcealOpenCodeV2FingerprintTools:
//
//   - An agent turn needs the CLI's tool declarations and the caller's tools
//     described as prompt text.
//   - A title-generation request is one system instruction plus user text. The
//     gate accepts it tool-less and refuses it once agent declarations are
//     attached, so shaping it like an agent turn is exactly what gets a title
//     request refused — after which the router spends a proxy-pool walk
//     retrying a request that should never have carried tools.
//
// flat is true for Responses bodies and false for Chat Completions. The
// returned rename map is empty for a title request: no tool is renamed because
// none is declared.
func shapeOpenCodeFreeBody(body []byte, flat bool, cleanModel string, aliasesOut ...*translator.OpenCodeGateAliases) ([]byte, map[string]string) {
	if translator.IsOpenCodeTitleRequest(body) {
		return translator.ShapeOpenCodeV2TitleRequest(body, flat), map[string]string{}
	}
	shaped, names := translator.ConcealOpenCodeV2FingerprintTools(body, flat, isOpenCodeAutoToolChoiceModel(cleanModel))
	if len(aliasesOut) == 0 || aliasesOut[0] == nil {
		return shaped, names
	}
	var request map[string]any
	if json.Unmarshal(shaped, &request) != nil {
		return shaped, names
	}
	claude := false
	if _, ok := request["system"]; ok {
		claude = true
	}
	flatShape := flat
	if flat || claude {
		flatShape = flat || claude
	}
	*aliasesOut[0] = translator.AliasOpenCodeGateTools(request, flatShape, claude)
	out, err := json.Marshal(request, json.Deterministic(true))
	if err != nil {
		return shaped, names
	}
	return out, names
}
