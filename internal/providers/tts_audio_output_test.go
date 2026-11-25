package providers

import (
	"sort"
	"strings"
	"testing"
)

// A speech-synthesis model emits audio, and buildModalities in
// internal/handlers/chat derives output_modalities from AudioOutput alone — so
// a TTS model without that flag is published as text-only and never reaches the
// audio lane, while its endpoint answers audio/wav.
//
// ⚠️ Every case here names its provider/model pair literally. An earlier version
// of this file iterated ProviderModels and skipped pairs whose registry kind was
// not "tts", which made it vacuous by construction: it could only ever check the
// pairs the registry rule was guaranteed to reach, and it passed while
// studio/gemini-3.8-flash-tts — the id the bug was reported against — still
// resolved with audioOutput false.

// ttsCase is a provider/model pair, with the reason it is in the list.
type ttsCase struct {
	provider string
	model    string
	why      string
}

// reportedTTSCases are the shapes the request path actually produces. The
// gateway resolves capabilities through three of them: models_list.go:727 calls
// GetCapabilitiesDetailForModel(providerID, modelID), :858 calls it with
// (alias, modelID), and :934 with (prefix, cm.ID) for a custom node — so the
// prefixed id, the bare id and the node id all have to work.
var reportedTTSCases = []ttsCase{
	{provider: "openai-compatible-chat-studio", model: "studio/gemini-3.8-flash-tts", why: "the reported case: custom node id + prefixed model"},
	{provider: "studio", model: "gemini-3.8-flash-tts", why: "custom node resolved by prefix (models_list.go:934)"},
	{provider: "openai-compatible-chat-studio", model: "gemini-3.8-flash-tts", why: "node id + bare model"},
	{provider: "gemini", model: "gemini-3.8-flash-tts", why: "builtin provider, registry-classified"},
	{provider: "vertex", model: "gemini-3.8-flash-tts", why: "builtin provider with no kind declared for this id"},
	{provider: "ag", model: "gemini-3.8-flash-tts", why: "provider alias with no kind declared for this id"},
	{provider: "gemini", model: "gemini-3.8-flash-lite-tts", why: "sibling tts id"},
	{provider: "gemini", model: "gemini-3.1-flash-tts-preview", why: "sibling tts id"},
	{provider: "gemini", model: "gemini-2.5-flash-preview-tts", why: "trailing -tts spelling"},
	{provider: "gemini", model: "gemini-2.5-pro-preview-tts", why: "trailing -tts spelling"},
}

// nameOnlyTTSCases carry no registry classification on their provider, so only
// the name signal can reach them. They are why the rule does not rely on the
// registry alone.
var nameOnlyTTSCases = []ttsCase{
	{provider: "xiaomi-tokenplan", model: "mimo-v2-tts", why: "no kind declared on this provider"},
	{provider: "xiaomi-tokenplan", model: "mimo-v2.5-tts", why: "no kind declared on this provider"},
	{provider: "xiaomi-tokenplan", model: "mimo-v2.5-tts-voiceclone", why: "no kind declared on this provider"},
	{provider: "xiaomi-tokenplan", model: "mimo-v2.5-tts-voicedesign", why: "no kind declared on this provider"},
	{provider: "xmtp", model: "mimo-v2-tts", why: "no kind declared on this provider"},
	{provider: "xmtp", model: "mimo-v2.5-tts", why: "no kind declared on this provider"},
	{provider: "xmtp", model: "mimo-v2.5-tts-voiceclone", why: "no kind declared on this provider"},
	{provider: "xmtp", model: "mimo-v2.5-tts-voicedesign", why: "no kind declared on this provider"},
}

// architectureNamedTTSCases name their architecture or product line instead of
// the modality. The name signal reaches them only because those families are
// listed in ttsName explicitly; the registry kind agrees independently. A
// mutation run confirms the registry source is not load-bearing for any pair
// shipping today — it is kept as the authoritative fallback for a future id
// whose name carries no signal at all, which is a real shape (a classified
// model called "sonic-2" would have nothing for the regex to match).
var architectureNamedTTSCases = []ttsCase{
	{provider: "minimax", model: "speech-2.8-hd", why: "registry kind tts; no 'tts' in the id"},
	{provider: "minimax", model: "speech-2.6-turbo", why: "registry kind tts; no 'tts' in the id"},
	{provider: "minimax", model: "speech-01-hd", why: "registry kind tts; no 'tts' in the id"},
	{provider: "minimax-cn", model: "speech-2.8-turbo", why: "registry kind tts; no 'tts' in the id"},
	{provider: "selfhosted-tts", model: "kokoro", why: "architecture name only"},
	{provider: "nvidia", model: "fastpitch", why: "architecture name only"},
	{provider: "nvidia", model: "tacotron2", why: "architecture name only"},
}

func TestTTSModelsPublishAudioOutput(t *testing.T) {
	InvalidateCapabilitiesCache()
	t.Cleanup(InvalidateCapabilitiesCache)

	groups := map[string][]ttsCase{
		"reported shapes":               reportedTTSCases,
		"reachable by name signal only": nameOnlyTTSCases,
		"named after the architecture":  architectureNamedTTSCases,
	}
	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			for _, tc := range groups[name] {
				// GetCapabilitiesDetailForModel is what models_list calls, so
				// the assertion rides the same path the published JSON does.
				if !GetCapabilitiesDetailForModel(tc.provider, tc.model).AudioOutput {
					t.Errorf("%s on %s: AudioOutput = false, want true (%s)", tc.model, tc.provider, tc.why)
				}
			}
		})
	}
}

// The reported case gets its own test so a regression names it directly rather
// than reporting one failure among twenty-five.
func TestStudioTTSPublishesAudioOutput(t *testing.T) {
	InvalidateCapabilitiesCache()
	t.Cleanup(InvalidateCapabilitiesCache)

	const provider = "openai-compatible-chat-studio"
	const model = "studio/gemini-3.8-flash-tts"

	caps := GetCapabilitiesDetailForModel(provider, model)
	if !caps.AudioOutput {
		t.Fatalf("%s on %s: AudioOutput = false; /v1/models would publish output_modalities [\"text\"] "+
			"for an endpoint that answers audio/wav", model, provider)
	}
	if caps.ImageOutput {
		t.Error("ImageOutput = true on a TTS model")
	}
	// The registry cannot classify a custom node, so this pair is carried by
	// the name signal alone. Pinned so a later narrowing of the rule to the
	// registry fails here instead of silently losing the reported case again.
	if kind := GetProviderModelKind(provider, model); kind == serviceKindTTS {
		t.Errorf("the registry now classifies this pair as %q — update the comment on the rule, "+
			"it no longer depends on the name signal", kind)
	}
	if !looksLikeTTSModel(model) {
		t.Error("looksLikeTTSModel = false for the reported id; nothing else can reach this pair")
	}
}

// The name signal is the only source that reaches a custom provider node, so
// it carries the reported case and the eight mimo pairs on their own. Pinned
// because a narrowing of the rule to the registry would lose exactly these.
func TestTTSNameSignalIsTheOnlySourceForUnclassifiedPairs(t *testing.T) {
	InvalidateCapabilitiesCache()
	t.Cleanup(InvalidateCapabilitiesCache)

	for _, tc := range nameOnlyTTSCases {
		if kind := GetProviderModelKind(tc.provider, tc.model); kind != "" {
			t.Errorf("%s on %s is now classified %q; move it to architectureNamedTTSCases",
				tc.model, tc.provider, kind)
		}
		if !looksLikeTTSModel(tc.model) {
			t.Errorf("%s: looksLikeTTSModel = false, so nothing reaches this pair", tc.model)
		}
	}
}

// The architecture-named pairs are classified by the registry and matched by
// ttsName's explicit family list, so either source alone would carry them. This
// records that overlap rather than claiming the registry is load-bearing: a
// mutation run with the registry source removed leaves every test passing.
func TestTTSArchitectureNamedPairsHaveBothSources(t *testing.T) {
	InvalidateCapabilitiesCache()
	t.Cleanup(InvalidateCapabilitiesCache)

	for _, tc := range architectureNamedTTSCases {
		if strings.Contains(strings.ToLower(tc.model), "tts") {
			t.Errorf("%s contains \"tts\"; move it to a group that reflects that", tc.model)
		}
		if GetProviderModelKind(tc.provider, tc.model) != serviceKindTTS {
			t.Errorf("%s on %s is no longer classified tts", tc.model, tc.provider)
		}
		if !looksLikeTTSModel(tc.model) {
			t.Errorf("%s: ttsName no longer lists this family, so only the registry reaches it — "+
				"the rule's second source is now load-bearing and its comment is stale", tc.model)
		}
	}
}

// Every pair the registry classifies as speech synthesis must publish audio
// output. Unlike the earlier vacuous version, this fails when the classified
// set is unexpectedly small rather than reporting a reassuring count.
func TestRegistryClassifiedTTSPairsAllPublishAudioOutput(t *testing.T) {
	InvalidateCapabilitiesCache()
	t.Cleanup(InvalidateCapabilitiesCache)

	const wantAtLeast = 34
	checked := 0
	for provider, models := range ProviderModels {
		for _, model := range models {
			if GetProviderModelKind(provider, model) != serviceKindTTS {
				continue
			}
			checked++
			if !GetCapabilitiesForModel(provider, model).AudioOutput {
				t.Errorf("%s on %s: registry kind is tts but AudioOutput = false", model, provider)
			}
		}
	}
	if checked < wantAtLeast {
		t.Fatalf("only %d provider/model pairs are classified tts, want at least %d — "+
			"the lookup regressed, so the pass above proves nothing", checked, wantAtLeast)
	}
}

// The rule only ever turns AudioOutput on, so a chat model must keep its tools
// and publish no audio. stepaudio-2.5-chat is the sharp case: its sibling
// stepaudio-2.5-tts is classified TTS on the same provider.
func TestTTSRuleLeavesChatModelsAlone(t *testing.T) {
	InvalidateCapabilitiesCache()
	t.Cleanup(InvalidateCapabilitiesCache)

	for _, tc := range []ttsCase{
		{provider: "gemini", model: "gemini-3.8-flash"},
		{provider: "gemini", model: "gemini-4-argon"},
		{provider: "gemini", model: "gemini-3.1-pro-preview"},
		{provider: "gemini", model: "gemini-omni-1.2-flash"},
		{provider: "openai", model: "gpt-4o"},
		{provider: "mimo", model: "mimo-v2.5-pro"},
		{provider: "mimo", model: "mimo-v2-omni"},
		{provider: "hc", model: "stepaudio-2.5-chat"},
		{provider: "hc", model: "stepaudio-2.5-realtime"},
	} {
		caps := GetCapabilitiesForModel(tc.provider, tc.model)
		if caps.AudioOutput {
			t.Errorf("%s on %s: AudioOutput = true, want false", tc.model, tc.provider)
		}
		if !caps.Tools {
			t.Errorf("%s on %s: Tools = false, want true", tc.model, tc.provider)
		}
	}
}

// Speech recognition is the opposite direction and must never be caught by the
// name signal — "speech" alone is a synthesis signal (minimax speech-2.8-hd),
// which is exactly why the recognition direction is excluded explicitly.
func TestTTSRuleDoesNotClaimSpeechToText(t *testing.T) {
	InvalidateCapabilitiesCache()
	t.Cleanup(InvalidateCapabilitiesCache)

	t.Run("resolved capabilities", func(t *testing.T) {
		for _, tc := range []ttsCase{
			{provider: "openai", model: "whisper-1"},
			{provider: "hc", model: "stepaudio-2.5-asr"},
			{provider: "assemblyai", model: "universal-3-pro"},
			{provider: "gemini", model: "gemini-2.5-flash-native-audio-preview-09-17"},
		} {
			if GetCapabilitiesForModel(tc.provider, tc.model).AudioOutput {
				t.Errorf("%s on %s: AudioOutput = true on a speech-to-text model", tc.model, tc.provider)
			}
		}
	})

	// Ids that do not ship today but would be misread by a bare "speech" or
	// "tts" substring match. Guards the regex, not the catalog.
	t.Run("name signal", func(t *testing.T) {
		for _, id := range []string{
			"speech-to-text",
			"speech_to_text",
			"speech-to-text-v2",
			"whisper-large-v3",
			"stt-1",
			"asr-pro",
			"voice-recognition",
			"transcribe-v1",
			"transcription-preview",
		} {
			if looksLikeTTSModel(id) {
				t.Errorf("looksLikeTTSModel(%q) = true, want false", id)
			}
		}
		for _, id := range []string{
			"gemini-3.8-flash-tts",
			"studio/gemini-3.8-flash-tts",
			"tts-1",
			"speech-2.8-hd",
			"kokoro",
			"fastpitch",
			"tacotron2",
		} {
			if !looksLikeTTSModel(id) {
				t.Errorf("looksLikeTTSModel(%q) = false, want true", id)
			}
		}
	})
}

// Upstream advertises thinking levels for its TTS models (13 pairs are pinned
// in testdata/thinking_levels.json). An earlier attempt at this fix used a
// first-match pattern row that cleared Reasoning and the thinking format to
// "tidy up" a speech model, and broke parity on all 13. The rule stays an
// audio-output-only switch; this pins that so it cannot regress silently.
func TestTTSRuleKeepsUpstreamThinkingLevels(t *testing.T) {
	InvalidateCapabilitiesCache()
	t.Cleanup(InvalidateCapabilitiesCache)

	for _, tc := range []struct {
		provider, model, wantFormat string
	}{
		{provider: "gemini", model: "gemini-3.1-flash-tts-preview", wantFormat: "gemini-level"},
		{provider: "gemini", model: "gemini-2.5-flash-preview-tts", wantFormat: "gemini-budget"},
		{provider: "gemini", model: "gemini-2.5-pro-preview-tts", wantFormat: "gemini-budget"},
		{provider: "mimo", model: "mimo-v2.5-tts", wantFormat: "deepseek"},
	} {
		caps := GetCapabilitiesForModel(tc.provider, tc.model)
		if !caps.AudioOutput {
			t.Errorf("%s on %s: AudioOutput = false, want true", tc.model, tc.provider)
		}
		if !caps.Reasoning {
			t.Errorf("%s on %s: Reasoning = false, but upstream advertises thinking levels",
				tc.model, tc.provider)
		}
		if caps.ThinkingFormat != tc.wantFormat {
			t.Errorf("%s on %s: ThinkingFormat = %q, want %q",
				tc.model, tc.provider, caps.ThinkingFormat, tc.wantFormat)
		}
		if len(GetThinkingLevels(tc.provider, tc.model)) == 0 {
			t.Errorf("%s on %s: thinking levels are empty, breaking upstream parity",
				tc.model, tc.provider)
		}
	}
}
