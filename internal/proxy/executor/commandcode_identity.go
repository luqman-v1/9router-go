package executor

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"regexp"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"golang.org/x/text/unicode/norm"
)

const commandcodeMaxRetries = 2

// commandcodeWorkingDir is the gateway's own working directory, reported as
// config.workingDir and (slugified) as x-project-slug, as upstream does.
var commandcodeWorkingDir = func() string {
	if wd, err := os.Getwd(); err == nil && wd != "" {
		return wd
	}
	return "/"
}()

// commandcodeProjectSlug falls back to "9router-go" when the working dir
// slugifies to nothing (cwd "/" in a container): upstream rejects an empty
// x-project-slug with 400.
var commandcodeProjectSlug = func() string {
	if s := commandcodeSlugify(commandcodeWorkingDir); s != "" {
		return s
	}
	return "9router-go"
}()

var (
	slugNonAlnum = regexp.MustCompile(`[^a-z\d]+`)
	slugCamelA   = regexp.MustCompile(`([A-Z]{2,})(\d+)`)
	slugCamelB   = regexp.MustCompile(`([a-z\d]+)([A-Z]{2,})`)
	slugCamelC   = regexp.MustCompile(`([a-z\d])([A-Z])`)
	slugCamelD   = regexp.MustCompile(`([A-Z]+)([A-Z][a-rt-z\d]+)`)
	slugReplacer = strings.NewReplacer("&", " and ", "ß", "ss", "ẞ", "Ss", "ä", "ae", "Ä", "Ae", "ö", "oe", "Ö", "Oe", "ü", "ue", "Ü", "Ue", "Æ", "AE", "æ", "ae", "Ø", "O", "ø", "o", "Œ", "OE", "œ", "oe", "Ð", "D", "ð", "d", "Đ", "D", "đ", "d", "Þ", "TH", "þ", "th", "Ł", "L", "ł", "l", "ı", "i")
)

// commandcodeSlugify mirrors @sindresorhus/slugify defaults, as used by the
// official CLI for x-project-slug: "/home/u/My Project" → "home-u-my-project".
func commandcodeSlugify(v string) string {
	v = norm.NFD.String(slugReplacer.Replace(v))
	v = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) {
			return -1
		}
		return r
	}, v)
	v = slugCamelA.ReplaceAllString(v, "$1 $2")
	v = slugCamelB.ReplaceAllString(v, "$1 $2")
	v = slugCamelC.ReplaceAllString(v, "$1 $2")
	v = slugCamelD.ReplaceAllString(v, "$1 $2")
	v = slugNonAlnum.ReplaceAllString(strings.ToLower(v), "-")
	return strings.Trim(v, "-")
}

// commandcodeSessionUUID derives a deterministic UUID-shaped id from the
// client session so one conversation keeps one threadId (prefix cache
// friendly). Empty session yields a random UUID.
func commandcodeSessionUUID(sessionID string) string {
	if sessionID == "" {
		return uuid.New().String()
	}
	sum := sha256.Sum256([]byte(sessionID))
	h := hex.EncodeToString(sum[:])
	n := hexNibble(h[16])
	v := "0123456789abcdef"[(n&0x3)|0x8]
	return h[0:8] + "-" + h[8:12] + "-4" + h[13:16] + "-" + string(v) + h[17:20] + "-" + h[20:32]
}

func hexNibble(c byte) int {
	if c >= 'a' {
		return int(c-'a') + 10
	}
	return int(c - '0')
}
