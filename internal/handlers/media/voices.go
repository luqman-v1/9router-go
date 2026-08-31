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

	"9router/proxy/internal/handlerutil"
)

// Voice listing for the media-providers dashboard. Mirrors the Next.js
// /api/media-providers/tts/voices route: ?provider= edge-tts (default) |
// local-device | elevenlabs | gemini, ?lang=<code> filter, ?apiKey for elevenlabs.

const (
	elevenVoicesURL = "https://api.elevenlabs.io/v1/voices"
	edgeUA          = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"
	voicesCacheTTL  = 24 * time.Hour
)

// edgeVoicesURL is a var so tests can stub the upstream.
var edgeVoicesURL = "https://speech.platform.bing.com/consumer/speech/synthesize/readaloud/voices/list?trustedclienttoken=6A5AA1D4EAFF4E9FB37E23D68491D6F4"

type voiceCacheEntry struct {
	voices []map[string]any
	time   time.Time
}

var (
	voiceCacheMu     sync.Mutex
	edgeVoiceCache   voiceCacheEntry
	elevenVoiceCache = map[string]voiceCacheEntry{} // by API key
	localVoiceCache  voiceCacheEntry
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
	case "elevenlabs":
		voices, err = fetchElevenLabsVoices(h.Client, r.Context(), q.Get("apiKey"))
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
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
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
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
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
