package tokensaver

import (
	json "encoding/json/v2"
	"regexp"
	"strings"
)

// Caveman prompt translations for Indonesian ("id").
const (
	CavemanLiteID = `Jawab sangat ringkas. Pertahankan tata bahasa dan kalimat lengkap tetapi buang basa-basi, keraguan, dan pemanis (tolong/terima kasih/dengan senang hati/tentu saja). Pola: sebutkan hal, tindakan, alasan. Lalu langkah berikutnya. Blok kode, path file, perintah, error, URL: pertahankan persis. Peringatan keamanan, konfirmasi tindakan destruktif, langkah berurutan: tulis normal. Lanjutkan gaya ringkas setelahnya. Auto-Clarity: matikan gaya ringkas untuk peringatan keamanan atau risiko salah tafsir. Tanpa emoji dekoratif. Tanpa narasi pemanggilan tool. Langsung jawab inti teknis.`

	CavemanFullID = `Jawab seperti manusia purba yang sangat ringkas (caveman mode). Semua substansi teknis tetap persis, hanya basa-basi yang dibuang. Singkat, padat, tanpa kata pengisi. Jangan gunakan kata-kata sopan santun pembuka/penutup. Langsung ke inti teknis. Kode, file path, command, error: tetap utuh dan tepat. Peringatan bahaya/keamanan: tulis jelas normal. Pertahankan bahasa pengguna.`

	CavemanUltraID = `Sangat ringkas. Fragment kata saja jika jelas. Hilangkan semua kata sambung yang tidak perlu. Kode dan error tetap persis.`
)

// GetCavemanPromptWithLang returns the caveman prompt for the specified level and language.
func GetCavemanPromptWithLang(level string, lang string) string {
	if strings.EqualFold(lang, "id") {
		switch strings.ToLower(level) {
		case "lite":
			return CavemanLiteID
		case "ultra":
			return CavemanUltraID
		case "full":
			return CavemanFullID
		default:
			return CavemanFullID
		}
	}
	return GetCavemanPrompt(level)
}

// Regex patterns for detecting dangerous/destructive commands and security risks for Auto-Clarity bypass.
var (
	reDestructiveCmd = regexp.MustCompile(`(?i)(?:rm\s+(-[a-zA-Z]*r[a-zA-Z]*f|-[a-zA-Z]*f[a-zA-Z]*r|\-rf|\-fr)\s+|DROP\s+(?:TABLE|DATABASE|SCHEMA|VIEW)|TRUNCATE\s+TABLE|DELETE\s+FROM\s+\w+|format\s+[a-zA-Z]:|mkfs(?:\.\w+)?\s+|dd\s+if=.*of=|fdisk\s+|parted\s+|shutdown\s+|reboot\s+|init\s+[06]|chmod\s+-R\s+777)`)
	reSecurityRisk   = regexp.MustCompile(`(?i)(?:security\s+(?:warning|alert|vulnerability|issue|flaw|incident)|cve-\d{4}-\d{4,}|privilege\s+escalation|remote\s+code\s+execution|sql\s+injection|cross-site\s+scripting|zero-day|0-day|ransomware|peringatan\s+keamanan|kerentanan\s+keamanan|kebocoran\s+data)`)
	reDetailedReq    = regexp.MustCompile(`(?i)(?:(?:explain|clarify|elaborate|walk\s+me\s+through|break\s+down)\s+in\s+detail|detailed\s+(?:explanation|analysis|breakdown|guide|steps|walkthrough)|explain\s+(?:thoroughly|comprehensively|deeply|step\s+by\s+step)|step\s+by\s+step\s+explanation|comprehensive\s+analysis|deep\s+dive|jelaskan\s+(?:secara\s+detail|secara\s+rinci|dengan\s+jelas|langkah\s+demi\s+langkah)|analisis\s+(?:mendalam|lengkap|komprehensif)|uraikan\s+secara\s+detail)`)
)

// ShouldBypassCaveman checks if the conversation context demands clarity bypass (security, destructive commands, or explicit detail requests).
func ShouldBypassCaveman(input any) bool {
	switch v := input.(type) {
	case string:
		return checkBypassText(v)
	case []byte:
		return checkBypassBytes(v)
	case []map[string]any:
		return checkBypassMessages(v)
	case []any:
		return checkBypassAnySlice(v)
	default:
		return false
	}
}

func checkBypassText(text string) bool {
	return reDestructiveCmd.MatchString(text) ||
		reSecurityRisk.MatchString(text) ||
		reDetailedReq.MatchString(text)
}

func checkBypassBytes(body []byte) bool {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err == nil {
		if msgs, ok := req["messages"].([]any); ok {
			return checkBypassAnySlice(msgs)
		}
		if msgs, ok := req["input"].([]any); ok {
			return checkBypassAnySlice(msgs)
		}
	}
	// Fallback: check raw text
	return checkBypassText(string(body))
}

func checkBypassMessages(messages []map[string]any) bool {
	// Check the last few messages
	start := 0
	if len(messages) > 3 {
		start = len(messages) - 3
	}
	for i := start; i < len(messages); i++ {
		msg := messages[i]
		if content, ok := msg["content"].(string); ok {
			if checkBypassText(content) {
				return true
			}
		}
	}
	return false
}

func checkBypassAnySlice(messages []any) bool {
	start := 0
	if len(messages) > 3 {
		start = len(messages) - 3
	}
	for i := start; i < len(messages); i++ {
		if msg, ok := messages[i].(map[string]any); ok {
			if content, ok := msg["content"].(string); ok {
				if checkBypassText(content) {
					return true
				}
			}
			if contentArr, ok := msg["content"].([]any); ok {
				for _, block := range contentArr {
					if bm, ok := block.(map[string]any); ok {
						if text, ok := bm["text"].(string); ok && checkBypassText(text) {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

// Conversational filler patterns
var (
	reGreetingLead = regexp.MustCompile(`(?i)^(?:hello|hi|hey(?:\s+there)?|good\s+(?:morning|afternoon|evening)|halo|hai|selamat\s+(?:pagi|siang|sore|malam))[\s,!.-]+`)
	reRequestLead  = regexp.MustCompile(`(?i)^(?:can\s+you\s+please\s+help\s+me\s+to|could\s+you\s+please\s+help\s+me\s+to|please\s+help\s+me\s+to|can\s+you\s+please\s+help\s+me|could\s+you\s+please\s+help\s+me|please\s+help\s+me|can\s+you\s+please|could\s+you\s+please|would\s+you\s+please|would\s+you\s+mind|please\s+kindly|please|kindly|bisa\s+tolong\s+bantu\s+saya\s+untuk|tolong\s+bantu\s+saya\s+untuk|bisa\s+tolong\s+bantu\s+saya|tolong\s+bantu\s+saya|bisa\s+tolong|tolong|mohon|i\s+want\s+you\s+to|i\s+would\s+like\s+you\s+to|i\s+need\s+you\s+to|i'd\s+like\s+you\s+to|bantu\s+saya\s+untuk|can\s+you|could\s+you)[\s,]+`)
	rePleasantTail = regexp.MustCompile(`(?i)[\s,!.-]*(?:thank\s+you\s+(?:so\s+much|very\s+much|in\s+advance)|thank\s+you|thanks\s+a\s+lot|many\s+thanks|thanks|terima\s+kasih\s+banyak|terima\s+kasih|makasih)[\s,!.-]*$`)
)

// CompressInputPrompt strips conversational filler and pleasantries from a single prompt.
func CompressInputPrompt(text string, preserveKeywords string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}

	preservedList := make([]string, 0)
	if preserveKeywords != "" {
		for _, kw := range strings.Split(preserveKeywords, ",") {
			k := strings.TrimSpace(strings.ToLower(kw))
			if k != "" {
				preservedList = append(preservedList, k)
			}
		}
	}

	isPreserved := func(s string) bool {
		lower := strings.ToLower(s)
		for _, p := range preservedList {
			if strings.Contains(lower, p) {
				return true
			}
		}
		return false
	}

	curr := trimmed

	// Strip greeting lead if not preserved
	if loc := reGreetingLead.FindStringIndex(curr); loc != nil {
		matched := curr[loc[0]:loc[1]]
		if !isPreserved(matched) {
			curr = strings.TrimSpace(curr[loc[1]:])
		}
	}

	// Strip request lead if not preserved
	if loc := reRequestLead.FindStringIndex(curr); loc != nil {
		matched := curr[loc[0]:loc[1]]
		if !isPreserved(matched) {
			curr = strings.TrimSpace(curr[loc[1]:])
		}
	}

	// Strip pleasantries at end if not preserved
	if loc := rePleasantTail.FindStringIndex(curr); loc != nil {
		matched := curr[loc[0]:loc[1]]
		if !isPreserved(matched) {
			curr = strings.TrimSpace(curr[:loc[0]])
		}
	}

	// Clean up leading punctuation
	curr = strings.TrimLeft(curr, ",;:- ")
	if curr == "" {
		return trimmed
	}
	return curr
}

// CompressInputMessages compresses user messages in the request body in-place.
func CompressInputMessages(body []byte, preserveKeywords string) ([]byte, bool) {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body, false
	}

	key := "messages"
	rawArr, ok := req["messages"].([]any)
	if !ok {
		key = "input"
		rawArr, ok = req["input"].([]any)
	}
	if !ok || len(rawArr) == 0 {
		return body, false
	}

	modified := false
	for _, item := range rawArr {
		msg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if role != "user" {
			continue
		}

		if text, ok := msg["content"].(string); ok && text != "" {
			comp := CompressInputPrompt(text, preserveKeywords)
			if comp != text && comp != "" {
				msg["content"] = comp
				modified = true
			}
		} else if contentArr, ok := msg["content"].([]any); ok {
			for _, part := range contentArr {
				block, ok := part.(map[string]any)
				if !ok {
					continue
				}
				if block["type"] == "text" {
					if text, ok := block["text"].(string); ok && text != "" {
						comp := CompressInputPrompt(text, preserveKeywords)
						if comp != text && comp != "" {
							block["text"] = comp
							modified = true
						}
					}
				}
			}
		}
	}

	if !modified {
		return body, false
	}

	out, err := json.Marshal(req)
	if err != nil {
		return body, false
	}
	_ = key
	return out, true
}
