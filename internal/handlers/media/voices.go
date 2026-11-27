package media

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/handlerutil"
)

// Voice listing for the media-providers dashboard. Mirrors the Next.js
// /api/media-providers/tts/voices route: ?provider= edge-tts (default) |
// local-device | elevenlabs | gemini, ?lang=<code> filter, ?apiKey for elevenlabs.

const (
	elevenVoicesURL   = "https://api.elevenlabs.io/v1/voices"
	deepgramModelsURL = "https://api.deepgram.com/v1/models"
	inworldVoicesURL  = "https://api.inworld.ai/tts/v1/voices"
	minimaxVoiceURL   = "https://api.minimax.io/v1/get_voice"
	minimaxCNVoiceURL = "https://api.minimaxi.com/v1/get_voice"
	edgeUA            = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"
	voicesCacheTTL    = 24 * time.Hour
)

// edgeVoicesURL is a var so tests can stub the upstream.
var edgeVoicesURL = "https://speech.platform.bing.com/consumer/speech/synthesize/readaloud/voices/list?trustedclienttoken=6A5AA1D4EAFF4E9FB37E23D68491D6F4"

type voiceCacheEntry struct {
	voices []map[string]any
	time   time.Time
}

var (
	voiceCacheMu       sync.Mutex
	edgeVoiceCache     voiceCacheEntry
	elevenVoiceCache   = map[string]voiceCacheEntry{} // by API key
	minimaxVoiceCache  = map[string]voiceCacheEntry{} // by apiKey+provider+voiceType
	deepgramVoiceCache voiceCacheEntry
	inworldVoiceCache  voiceCacheEntry
	localVoiceCache    voiceCacheEntry
)

// ponytail: hardcoded subset of Intl.DisplayNames("en"); unknown codes fall
// back to the raw code (the JS catch path). Add codes as needed.
var countryNames = map[string]string{
	"US": "United States", "GB": "United Kingdom", "AU": "Australia", "CA": "Canada",
	"IN": "India", "IE": "Ireland", "NZ": "New Zealand", "PH": "Philippines",
	"SG": "Singapore", "ZA": "South Africa", "FR": "France", "CH": "Switzerland",
	"BE": "Belgium", "DE": "Germany", "AT": "Austria", "ES": "Spain",
	"MX": "Mexico", "AR": "Argentina", "CO": "Colombia", "CL": "Chile",
	"PE": "Peru", "VE": "Venezuela", "IT": "Italy", "PT": "Portugal",
	"BR": "Brazil", "JP": "Japan", "KR": "South Korea", "CN": "China",
	"TW": "Taiwan", "HK": "Hong Kong", "MO": "Macao", "RU": "Russia",
	"NL": "Netherlands", "SE": "Sweden", "NO": "Norway", "DK": "Denmark",
	"FI": "Finland", "PL": "Poland", "TR": "Turkey", "VN": "Vietnam",
	"TH": "Thailand", "ID": "Indonesia", "MY": "Malaysia", "AE": "United Arab Emirates",
	"EG": "Egypt", "IL": "Israel", "GR": "Greece", "CZ": "Czechia",
	"HU": "Hungary", "RO": "Romania", "UA": "Ukraine", "HR": "Croatia",
	"SK": "Slovakia", "SI": "Slovenia", "BG": "Bulgaria", "RS": "Serbia",
	"LT": "Lithuania", "LV": "Latvia", "EE": "Estonia", "IS": "Iceland",
	"KE": "Kenya", "NG": "Nigeria", "TZ": "Tanzania", "PK": "Pakistan",
	"BD": "Bangladesh", "LK": "Sri Lanka", "NP": "Nepal", "KH": "Cambodia",
	"SA": "Saudi Arabia", "QA": "Qatar", "KW": "Kuwait", "CY": "Cyprus",
	"MT": "Malta", "LU": "Luxembourg", "BN": "Brunei", "BY": "Belarus",
	"GE": "Georgia", "AM": "Armenia", "AZ": "Azerbaijan", "KZ": "Kazakhstan",
	"UZ": "Uzbekistan", "MM": "Myanmar", "LA": "Laos",
	"ZW": "Zimbabwe", "GH": "Ghana",
	"CM": "Cameroon", "CI": "Ivory Coast", "SN": "Senegal", "MA": "Morocco",
	"DZ": "Algeria", "TN": "Tunisia", "JO": "Jordan", "LB": "Lebanon",
	"BH": "Bahrain", "OM": "Oman", "YE": "Yemen", "IR": "Iran",
	"AF": "Afghanistan",
}

var langNames = map[string]string{
	"en": "English", "fr": "French", "de": "German", "es": "Spanish",
	"it": "Italian", "pt": "Portuguese", "ja": "Japanese", "ko": "Korean",
	"zh": "Chinese", "ru": "Russian", "ar": "Arabic", "nl": "Dutch",
	"pl": "Polish", "tr": "Turkish", "vi": "Vietnamese", "th": "Thai",
	"id": "Indonesian", "ms": "Malay", "hi": "Hindi", "sv": "Swedish",
	"no": "Norwegian", "da": "Danish", "fi": "Finnish", "cs": "Czech",
	"el": "Greek", "he": "Hebrew", "hu": "Hungarian", "ro": "Romanian",
	"sk": "Slovak", "uk": "Ukrainian", "bg": "Bulgarian", "hr": "Croatian",
	"ca": "Catalan", "sr": "Serbian", "sl": "Slovenian", "lt": "Lithuanian",
	"lv": "Latvian", "et": "Estonian", "is": "Icelandic", "fa": "Persian",
	"ur": "Urdu", "bn": "Bengali", "ta": "Tamil", "te": "Telugu",
	"mr": "Marathi", "gu": "Gujarati", "kn": "Kannada", "ml": "Malayalam",
	"pa": "Punjabi", "si": "Sinhala", "ne": "Nepali", "km": "Khmer",
	"my": "Burmese", "mn": "Mongolian", "am": "Amharic", "sw": "Swahili",
	"af": "Afrikaans", "cy": "Welsh", "ga": "Irish", "mt": "Maltese",
	"sq": "Albanian", "az": "Azerbaijani", "kk": "Kazakh", "uz": "Uzbek",
	"bs": "Bosnian", "mk": "Macedonian", "tl": "Filipino", "zu": "Zulu",
	"yo": "Yoruba", "ig": "Igbo", "ha": "Hausa", "so": "Somali",
}

func countryName(code, fallback string) string {
	c := code
	if c == "" {
		c = fallback
	}
	if c != "" {
		if n, ok := countryNames[c]; ok {
			return n
		}
	}
	return c
}

func langName(code string) string {
	if n, ok := langNames[code]; ok {
		return n
	}
	return code
}

// HandleAudioVoices lists available audio voices per provider.
func (h *MediaHandler) HandleAudioVoices(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	provider := q.Get("provider")
	if provider == "" {
		provider = "edge-tts"
	}
	langFilter := q.Get("lang")

	var (
		voices []map[string]any
		err    error
	)
	switch provider {
	case "edge-tts":
		voices, err = fetchEdgeTTSVoices(h.Client, r.Context())
	case "deepgram":
		voices, err = h.fetchDeepgramVoices(r.Context())
	case "inworld":
		voices, err = h.fetchInworldVoices(r.Context())
	case "minimax", "minimax-cn":
		voices, err = h.fetchMinimaxVoices(r.Context(), provider, q.Get("voice_type"))
	case "elevenlabs":
		apiKey := q.Get("apiKey")
		if apiKey == "" && h.Repo != nil {
			if conns, cerr := h.Repo.GetProviderConnections("elevenlabs", true); cerr == nil {
				for _, c := range conns {
					var cd struct {
						APIKey      string `json:"apiKey"`
						AccessToken string `json:"accessToken"`
					}
					if c != nil && c.Data != "" {
						_ = json.Unmarshal([]byte(c.Data), &cd)
					}
					if cd.APIKey != "" {
						apiKey = cd.APIKey
						break
					}
					if cd.AccessToken != "" {
						apiKey = cd.AccessToken
						break
					}
				}
			}
		}
		voices, err = fetchElevenLabsVoices(h.Client, r.Context(), apiKey)
	case "gemini":
		voices = fetchGeminiVoices()
	case "local-device":
		voices = fetchLocalDeviceVoices()
	default:
		handlerutil.WriteJSONError(w, http.StatusBadRequest,
			fmt.Sprintf("Provider '%s' does not support voice listing", provider))
		return
	}
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		return
	}

	if langFilter != "" {
		filtered := make([]map[string]any, 0, len(voices))
		for _, v := range voices {
			if v["lang"] == langFilter {
				filtered = append(filtered, v)
			}
		}
		voices = filtered
	}

	byLang := map[string]any{}
	for _, v := range voices {
		lang, _ := v["lang"].(string)
		group, ok := byLang[lang].(map[string]any)
		if !ok {
			group = map[string]any{"code": lang, "name": v["langName"], "voices": []map[string]any{}}
			byLang[lang] = group
		}
		group["voices"] = append(group["voices"].([]map[string]any), v)
	}

	languages := make([]map[string]any, 0, len(byLang))
	for _, g := range byLang {
		languages = append(languages, g.(map[string]any))
	}
	sort.Slice(languages, func(i, j int) bool {
		return languages[i]["name"].(string) < languages[j]["name"].(string)
	})

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"voices":    voices,
		"languages": languages,
		"byLang":    byLang,
	})
}

// fetchEdgeTTSVoices returns edge-tts voices (24h in-process cache).
func fetchEdgeTTSVoices(client *http.Client, ctx context.Context) ([]map[string]any, error) {
	voiceCacheMu.Lock()
	if edgeVoiceCache.voices != nil && time.Since(edgeVoiceCache.time) < voicesCacheTTL {
		voices := edgeVoiceCache.voices
		voiceCacheMu.Unlock()
		return voices, nil
	}
	voiceCacheMu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, edgeVoicesURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", edgeUA)
	resp, err := doDirectOrClient(ctx, client, req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Edge TTS voices fetch failed: %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var raw []struct {
		ShortName    string `json:"ShortName"`
		FriendlyName string `json:"FriendlyName"`
		Locale       string `json:"Locale"`
		Gender       string `json:"Gender"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}

	voices := make([]map[string]any, 0, len(raw))
	for _, v := range raw {
		lang, country, _ := strings.Cut(v.Locale, "-")
		name := v.FriendlyName
		if name == "" {
			name = v.ShortName
		}
		name = strings.ReplaceAll(name, "Microsoft ", "")
		// ponytail: faithful to the reference regex, which consumes the "(Natural)"
		// closing paren, yielding e.g. "Aria (English (United States)".
		name = strings.ReplaceAll(name, " Online (Natural) - ", " (")
		voices = append(voices, map[string]any{
			"id":          v.ShortName,
			"name":        name,
			"locale":      v.Locale,
			"lang":        lang,
			"country":     country,
			"countryName": countryName(country, lang),
			"langName":    langName(lang),
			"gender":      v.Gender,
		})
	}

	voiceCacheMu.Lock()
	edgeVoiceCache = voiceCacheEntry{voices: voices, time: time.Now()}
	voiceCacheMu.Unlock()
	return voices, nil
}

// fetchElevenLabsVoices returns elevenlabs voices for apiKey (per-key 24h cache).
func fetchElevenLabsVoices(client *http.Client, ctx context.Context, apiKey string) ([]map[string]any, error) {
	if apiKey == "" {
		return nil, errors.New("ElevenLabs API key required")
	}
	voiceCacheMu.Lock()
	if e, ok := elevenVoiceCache[apiKey]; ok && time.Since(e.time) < voicesCacheTTL {
		voices := e.voices
		voiceCacheMu.Unlock()
		return voices, nil
	}
	voiceCacheMu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, elevenVoicesURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("xi-api-key", apiKey)
	req.Header.Set(constants.HeaderContentType, constants.ContentTypeJSON)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ElevenLabs voices fetch failed: %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var data struct {
		Voices []map[string]any `json:"voices"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}

	voices := make([]map[string]any, 0, len(data.Voices))
	for _, v := range data.Voices {
		labels, _ := v["labels"].(map[string]any)
		lang := handlerutil.GetString(labels, "language")
		if lang == "" {
			lang = "en"
		}
		baseLang, _, _ := strings.Cut(lang, "-")
		voice := map[string]any{
			"id":          v["voice_id"],
			"name":        v["name"],
			"locale":      lang,
			"lang":        baseLang,
			"country":     "",
			"countryName": "",
			"langName":    langName(baseLang),
			"gender":      handlerutil.GetString(labels, "gender"),
		}
		if cat, ok := v["category"]; ok {
			voice["category"] = cat
		}
		voices = append(voices, voice)
	}

	voiceCacheMu.Lock()
	elevenVoiceCache[apiKey] = voiceCacheEntry{voices: voices, time: time.Now()}
	voiceCacheMu.Unlock()
	return voices, nil
}

// geminiPrebuilt mirrors the reference's prebuilt list (Gemini has no list API).
var geminiPrebuilt = [][2]string{
	{"Zephyr", "Female"}, {"Puck", "Male"}, {"Charon", "Male"}, {"Kore", "Female"},
	{"Fenrir", "Male"}, {"Leda", "Female"}, {"Orus", "Male"}, {"Aoede", "Female"},
	{"Callirrhoe", "Female"}, {"Autonoe", "Female"}, {"Enceladus", "Male"}, {"Iapetus", "Male"},
	{"Umbriel", "Male"}, {"Algieba", "Male"}, {"Despina", "Female"}, {"Erinome", "Female"},
	{"Algenib", "Male"}, {"Rasalgethi", "Male"}, {"Laomedeia", "Female"}, {"Achernar", "Female"},
	{"Alnilam", "Male"}, {"Schedar", "Male"}, {"Gacrux", "Female"}, {"Pulcherrima", "Female"},
	{"Achird", "Male"}, {"Zubenelgenubi", "Male"}, {"Vindemiatrix", "Female"}, {"Sadachbia", "Male"},
	{"Sadaltager", "Male"}, {"Sulafat", "Female"},
}

func fetchGeminiVoices() []map[string]any {
	voices := make([]map[string]any, 0, len(geminiPrebuilt))
	for _, g := range geminiPrebuilt {
		voices = append(voices, map[string]any{
			"id":          g[0],
			"name":        g[0],
			"locale":      "en",
			"lang":        "en",
			"country":     "",
			"countryName": "",
			"langName":    "English",
			"gender":      g[1],
		})
	}
	return voices
}

var localVoiceLine = regexp.MustCompile(`^([^\s].*?)\s{2,}([a-z]{2}_[A-Z]{2})`)

// fetchLocalDeviceVoices lists voices from the local `say` binary (macOS);
// returns an empty list on any other platform or error, like the reference.
func fetchLocalDeviceVoices() []map[string]any {
	voiceCacheMu.Lock()
	if localVoiceCache.voices != nil && time.Since(localVoiceCache.time) < voicesCacheTTL {
		voices := localVoiceCache.voices
		voiceCacheMu.Unlock()
		return voices
	}
	voiceCacheMu.Unlock()

	var voices []map[string]any
	if out, err := exec.Command("say", "-v", "?").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			m := localVoiceLine.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			name := strings.TrimSpace(m[1])
			locale := m[2]
			lang, country, _ := strings.Cut(locale, "_")
			voices = append(voices, map[string]any{
				"id":          name,
				"name":        name,
				"locale":      strings.ReplaceAll(locale, "_", "-"),
				"lang":        lang,
				"country":     country,
				"countryName": countryName(country, lang),
				"langName":    langName(lang),
				"gender":      "",
			})
		}
	}
	if voices == nil {
		voices = []map[string]any{}
	}

	voiceCacheMu.Lock()
	localVoiceCache = voiceCacheEntry{voices: voices, time: time.Now()}
	voiceCacheMu.Unlock()
	return voices
}

// connectionAPIKey returns the first active connection apiKey/accessToken for
// a provider, mirroring upstream getProviderConnections({provider, isActive}).
func (h *MediaHandler) connectionAPIKey(provider string) string {
	if h.Repo == nil {
		return ""
	}
	conns, err := h.Repo.GetProviderConnections(provider, true)
	if err != nil {
		return ""
	}
	for _, c := range conns {
		var cd struct {
			APIKey      string `json:"apiKey"`
			AccessToken string `json:"accessToken"`
		}
		if c != nil && c.Data != "" {
			_ = json.Unmarshal([]byte(c.Data), &cd)
		}
		if cd.APIKey != "" {
			return cd.APIKey
		}
		if cd.AccessToken != "" {
			return cd.AccessToken
		}
	}
	return ""
}

// fetchDeepgramVoices mirrors upstream
// src/app/api/media-providers/tts/deepgram/voices/route.js: uses the first
// active deepgram connection key against GET /v1/models (Token scheme) and
// groups tts entries by language.
func (h *MediaHandler) fetchDeepgramVoices(ctx context.Context) ([]map[string]any, error) {
	voiceCacheMu.Lock()
	if deepgramVoiceCache.voices != nil && time.Since(deepgramVoiceCache.time) < voicesCacheTTL {
		voices := deepgramVoiceCache.voices
		voiceCacheMu.Unlock()
		return voices, nil
	}
	voiceCacheMu.Unlock()

	apiKey := h.connectionAPIKey("deepgram")
	if apiKey == "" {
		return nil, errors.New("No Deepgram connection found")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, deepgramModelsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Token "+apiKey)
	resp, err := h.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = "Failed"
		}
		return nil, fmt.Errorf("Deepgram API %d: %s", resp.StatusCode, msg)
	}
	var data struct {
		TTS []struct {
			Name          string   `json:"name"`
			CanonicalName string   `json:"canonical_name"`
			Languages     []string `json:"languages"`
			Metadata      struct {
				Tags []string `json:"tags"`
			} `json:"metadata"`
		} `json:"tts"`
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	voices := []map[string]any{}
	seen := map[string]bool{}
	for _, m := range data.TTS {
		langs := m.Languages
		if len(langs) == 0 {
			canon := m.CanonicalName
			if i := strings.LastIndex(canon, "-"); i >= 0 {
				canon = canon[i+1:]
			}
			if canon == "" {
				canon = "en"
			}
			langs = []string{canon}
		}
		id := m.CanonicalName
		if id == "" {
			id = m.Name
		}
		gender := ""
		for _, t := range m.Metadata.Tags {
			if t == "masculine" || t == "feminine" {
				gender = t
				break
			}
		}
		for _, lang := range langs {
			key := lang + "\x00" + id
			if seen[key] {
				continue
			}
			seen[key] = true
			voices = append(voices, map[string]any{
				"id":          id,
				"name":        firstNonEmpty(m.Name, id),
				"locale":      lang,
				"lang":        lang,
				"country":     "",
				"countryName": "",
				"langName":    langName(lang),
				"gender":      gender,
			})
		}
	}
	voiceCacheMu.Lock()
	deepgramVoiceCache = voiceCacheEntry{voices: voices, time: time.Now()}
	voiceCacheMu.Unlock()
	return voices, nil
}

// fetchInworldVoices mirrors upstream
// src/app/api/media-providers/tts/inworld/voices/route.js: uses the first
// active inworld connection key against GET /tts/v1/voices (Basic scheme)
// and groups entries by language.
func (h *MediaHandler) fetchInworldVoices(ctx context.Context) ([]map[string]any, error) {
	voiceCacheMu.Lock()
	if inworldVoiceCache.voices != nil && time.Since(inworldVoiceCache.time) < voicesCacheTTL {
		voices := inworldVoiceCache.voices
		voiceCacheMu.Unlock()
		return voices, nil
	}
	voiceCacheMu.Unlock()

	apiKey := h.connectionAPIKey("inworld")
	if apiKey == "" {
		return nil, errors.New("No Inworld connection found")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, inworldVoicesURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Basic "+apiKey)
	resp, err := h.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = "Failed"
		}
		return nil, fmt.Errorf("Inworld API %d: %s", resp.StatusCode, msg)
	}
	var data struct {
		Voices []struct {
			VoiceID   string   `json:"voiceId"`
			VoiceID2  string   `json:"voice_id"`
			Name      string   `json:"name"`
			Languages []string `json:"languages"`
		} `json:"voices"`
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	voices := []map[string]any{}
	for _, v := range data.Voices {
		id := firstNonEmpty(v.VoiceID, v.VoiceID2)
		if id == "" {
			continue
		}
		langs := v.Languages
		if len(langs) == 0 {
			langs = []string{"en"}
		}
		for _, lang := range langs {
			voices = append(voices, map[string]any{
				"id":          id,
				"name":        firstNonEmpty(v.Name, id),
				"locale":      lang,
				"lang":        lang,
				"country":     "",
				"countryName": "",
				"langName":    langName(lang),
				"gender":      "",
			})
		}
	}
	voiceCacheMu.Lock()
	inworldVoiceCache = voiceCacheEntry{voices: voices, time: time.Now()}
	voiceCacheMu.Unlock()
	return voices, nil
}

// fetchMinimaxVoices mirrors upstream
// src/app/api/media-providers/tts/minimax/voices/route.js: POSTs
// {voice_type} to /v1/get_voice with the first active minimax/minimax-cn
// connection key and groups voices by language bucket (system voices keep
// their language prefix, clones/generated go to Custom).
func (h *MediaHandler) fetchMinimaxVoices(ctx context.Context, provider, voiceType string) ([]map[string]any, error) {
	if voiceType == "" {
		voiceType = "all"
	}
	apiKey := h.connectionAPIKey(provider)
	if apiKey == "" {
		return nil, fmt.Errorf("No %s connection found", provider)
	}
	cacheKey := apiKey + "\x00" + provider + "\x00" + voiceType
	voiceCacheMu.Lock()
	if e, ok := minimaxVoiceCache[cacheKey]; ok && time.Since(e.time) < voicesCacheTTL {
		voices := e.voices
		voiceCacheMu.Unlock()
		return voices, nil
	}
	voiceCacheMu.Unlock()

	endpoint := minimaxVoiceURL
	if provider == "minimax-cn" {
		endpoint = minimaxCNVoiceURL
	}
	payload := map[string]string{"voice_type": voiceType}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(raw)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set(constants.HeaderContentType, constants.ContentTypeJSON)
	resp, err := h.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	var data map[string]any
	if len(body) > 0 {
		_ = json.Unmarshal(body, &data)
	}
	baseResp, _ := data["base_resp"].(map[string]any)
	if baseResp == nil {
		baseResp, _ = data["baseResp"].(map[string]any)
	}
	statusCode := 0.0
	statusMsg := ""
	if baseResp != nil {
		statusCode, _ = toFloat(baseResp["status_code"])
		if statusCode == 0 {
			statusCode, _ = toFloat(baseResp["statusCode"])
		}
		statusMsg, _ = baseResp["status_msg"].(string)
		if statusMsg == "" {
			statusMsg, _ = baseResp["statusMsg"].(string)
		}
	}
	if resp.StatusCode != http.StatusOK {
		if statusMsg == "" {
			if m, _ := data["message"].(string); m != "" {
				statusMsg = m
			} else {
				statusMsg = strings.TrimSpace(string(body))
			}
			if statusMsg == "" {
				statusMsg = "Failed"
			}
		}
		return nil, fmt.Errorf("MiniMax API %d: %s", resp.StatusCode, statusMsg)
	}
	if statusCode != 0 {
		if statusMsg == "" {
			statusMsg = "MiniMax voice API error"
		}
		return nil, errors.New(statusMsg)
	}
	voices := []map[string]any{}
	seen := map[string]bool{}
	groups := []struct {
		key   string
		label string
	}{
		{"system_voice", "System"},
		{"voice_cloning", "Cloned"},
		{"voice_generation", "Generated"},
		{"music_generation", "Music"},
	}
	for _, g := range groups {
		list, _ := data[g.key].([]any)
		for _, item := range list {
			m, _ := item.(map[string]any)
			if m == nil {
				continue
			}
			id, _ := m["voice_id"].(string)
			if id == "" {
				id, _ = m["voiceId"].(string)
			}
			if id == "" {
				continue
			}
			name, _ := m["voice_name"].(string)
			if name == "" {
				name, _ = m["voiceName"].(string)
			}
			if name == "" {
				name = id
			}
			lang := "Custom"
			display := name + " · " + g.label
			if g.key == "system_voice" {
				lang = id
				if i := strings.Index(id, "_"); i > 0 {
					lang = id[:i]
				}
				if strings.TrimSpace(lang) == "" {
					lang = "Custom"
				}
				display = name
			}
			key := lang + "\x00" + id
			if seen[key] {
				continue
			}
			seen[key] = true
			voices = append(voices, map[string]any{
				"id":          id,
				"name":        display,
				"locale":      lang,
				"lang":        lang,
				"country":     "",
				"countryName": "",
				"langName":    lang,
				"gender":      "",
				"category":    g.key,
			})
		}
	}
	voiceCacheMu.Lock()
	minimaxVoiceCache[cacheKey] = voiceCacheEntry{voices: voices, time: time.Now()}
	voiceCacheMu.Unlock()
	return voices, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case string:
		var f float64
		_, err := fmt.Sscanf(strings.TrimSpace(n), "%g", &f)
		return f, err == nil
	}
	return 0, false
}
