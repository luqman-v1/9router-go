package providers

import (
	"regexp"
	"strings"
)

// Name-based vision detection — last resort when neither the catalog file nor
// the capability tables know a model. Vendors put the modality in the id
// ("qwen3-vl-plus", "glm-4.6v", "deepseek-v4-flash-vision-exp"), so a custom or
// freshly released model still gets image input instead of silently dropping it.
//
// Only ever turns vision ON. Never used to turn a declared capability off.
//
// Port of open-sse/providers/visionPatterns.js.

// nameSep is the set of separators that can bound a modality word in a model id.
const nameSep = "[-_/:.]"

// notVisionName matches image GENERATION, video generation, and non-chat models
// that also carry modality words but take no image input. Checked first so they
// can never match.
var notVisionName = regexp.MustCompile(`(?i)` + strings.Join([]string{
	`(^|` + nameSep + `)(image|img)(` + nameSep + `|$)`,
	"stable-image", "gen[0-9]_image", "nanobanana", "imagine",
	"t2v", "i2v", "flux", "dall", "sdxl", "diffusion",
	"embed", "rerank", "guard", "moderation",
	"tts", "stt", "whisper", "voice", "speech", "audio",
}, "|"))

// visionName matches explicit modality words plus the "<digit>v" suffix vendors
// use for vision variants (glm-4.6v, glm-5v-turbo). The digit-v branch
// requires a dotted version so the never-shipped `gpt-4v` cannot match.
var visionName = regexp.MustCompile(`(?i)` + strings.Join([]string{
	`(^|` + nameSep + `)(vision|vl|vlm|multimodal|omni|visual)(` + nameSep + `|$)`,
	`[0-9]\.[0-9]+v(` + nameSep + `|$)`,
	`(^|` + nameSep + `)glm-[0-9]+v(` + nameSep + `|$)`,
	"(^|[-_/:.])(llava|pixtral|internvl|cogvlm|minicpm-v|moondream|idefics|fuyu)",
}, "|"))

// looksLikeVisionModel reports whether a model id looks like a vision model by
// name signal alone.
func looksLikeVisionModel(modelID string) bool {
	if modelID == "" {
		return false
	}
	id := strings.ToLower(modelID)
	if notVisionName.MatchString(id) {
		return false
	}
	return visionName.MatchString(id)
}
