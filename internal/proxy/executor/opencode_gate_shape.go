package executor

import (
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
func shapeOpenCodeFreeBody(body []byte, flat bool, cleanModel string) ([]byte, map[string]string) {
	if translator.IsOpenCodeTitleRequest(body) {
		return translator.ShapeOpenCodeV2TitleRequest(body, flat), map[string]string{}
	}
	return translator.ConcealOpenCodeV2FingerprintTools(body, flat, isOpenCodeAutoToolChoiceModel(cleanModel))
}
