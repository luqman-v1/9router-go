package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"9router/proxy/internal/handlerutil"
)

// DefaultMaxBodySize caps request bodies for the JSON APIs (chat, embeddings,
// dashboard CRUD). Media uploads are far larger than any JSON payload, so they
// carry their own ceiling instead — see MediaMaxBodySize (#125).
const DefaultMaxBodySize = 10 * 1024 * 1024 // 10MB

// MediaMaxBodySize caps multipart uploads. Video generation accepts clips of
// 15-50MB and /audio/transcriptions accepts 30-60 minute recordings that can
// exceed 100MB, so the media ceiling is a separate, deliberately larger limit
// rather than a raised global: raising the global would let every JSON endpoint
// buffer 128MB and reintroduce the OOM risk DefaultMaxBodySize exists to stop.
const MediaMaxBodySize = 128 * 1024 * 1024 // 128MB

// MaxJSONBodySizeEnv names the operator override for DefaultMaxBodySize, in MiB.
const MaxJSONBodySizeEnv = "MAX_JSON_BODY_SIZE_MB"

// MaxJSONBodySizeCeilingMB bounds MaxJSONBodySizeEnv. Handlers buffer the whole
// JSON body, so each in-flight request may hold up to this much memory; the
// ceiling keeps a typo from turning the limit into an effectively unlimited one.
const MaxJSONBodySizeCeilingMB = 1024

// JSONBodyLimitFromEnv resolves the JSON body ceiling: DefaultMaxBodySize when
// MaxJSONBodySizeEnv is unset or empty, otherwise the configured whole number of
// MiB. Zero, negative, non-numeric and above-ceiling values are an error rather
// than a silent fallback, so a misconfiguration fails the boot instead of
// running with a limit the operator did not ask for.
func JSONBodyLimitFromEnv() (int64, error) {
	raw := strings.TrimSpace(os.Getenv(MaxJSONBodySizeEnv))
	if raw == "" {
		return DefaultMaxBodySize, nil
	}
	mb, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || mb < 1 || mb > MaxJSONBodySizeCeilingMB {
		return 0, fmt.Errorf("invalid %s=%q: want a whole number of MiB between 1 and %d",
			MaxJSONBodySizeEnv, raw, MaxJSONBodySizeCeilingMB)
	}
	return mb << 20, nil
}

// BodyLimitRule raises the body limit for a request path suffix.
type BodyLimitRule struct {
	// Suffix matches the end of the request path, so a route registered both
	// bare (/videos/generations) and behind a prefix (/v1/videos/generations,
	// /api/v1/videos/generations) is covered by one rule.
	Suffix string
	// MaxBytes is the ceiling for matching requests. A rule lower than the
	// default is ignored — overrides may only raise, never silently shrink the
	// protection the default provides.
	MaxBytes int64
}

// MediaBodyLimitRules returns the per-path overrides for endpoints that
// legitimately receive large multipart bodies.
func MediaBodyLimitRules() []BodyLimitRule {
	return []BodyLimitRule{
		{Suffix: "/videos/generations", MaxBytes: MediaMaxBodySize},
		{Suffix: "/videos/edits", MaxBytes: MediaMaxBodySize},
		{Suffix: "/videos/extensions", MaxBytes: MediaMaxBodySize},
		{Suffix: "/audio/transcriptions", MaxBytes: MediaMaxBodySize},
	}
}

// MaxBody returns a middleware that limits request body size.
func MaxBody(maxBytes int64) func(http.Handler) http.Handler {
	return MaxBodyByPath(maxBytes, nil)
}

// MaxBodyByPath applies defaultBytes to every request whose path matches no
// rule, and the rule's ceiling to the ones that do.
//
// It replaces the global limit rather than stacking above it: two MaxBody
// layers would each wrap r.Body, and the smaller one would still cut first —
// which is exactly why the media endpoints could not be given a higher limit
// before (#125). The applied ceiling is recorded on the request context so a
// handler that trips it can name the number instead of guessing.
func MaxBodyByPath(defaultBytes int64, rules []BodyLimitRule) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			limit := BodyLimitForPath(r.URL.Path, defaultBytes, rules)
			r = r.WithContext(WithBodyLimit(r.Context(), limit))
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			next.ServeHTTP(w, r)
		})
	}
}

// BodyLimitForPath resolves the ceiling for a path: the default unless a rule
// suffix matches and asks for more.
func BodyLimitForPath(path string, defaultBytes int64, rules []BodyLimitRule) int64 {
	if len(rules) == 0 {
		return defaultBytes
	}
	trimmed := strings.TrimRight(path, "/")
	for _, rule := range rules {
		if strings.HasSuffix(trimmed, rule.Suffix) && rule.MaxBytes > defaultBytes {
			return rule.MaxBytes
		}
	}
	return defaultBytes
}

type bodyLimitKey struct{}

// WithBodyLimit records the ceiling MaxBodyByPath applied to a request.
func WithBodyLimit(ctx context.Context, limit int64) context.Context {
	if ctx == nil {
		return nil
	}
	return context.WithValue(ctx, bodyLimitKey{}, limit)
}

// BodyLimitFromContext returns the ceiling applied to this request, or
// (0, false) when no limit middleware ran (unit tests that call a handler
// directly, for instance).
func BodyLimitFromContext(ctx context.Context) (int64, bool) {
	if ctx == nil {
		return 0, false
	}
	limit, ok := ctx.Value(bodyLimitKey{}).(int64)
	return limit, ok
}

// WriteBodyReadError turns a failed r.Body read into a response and reports
// whether it wrote one. A body that tripped http.MaxBytesReader is a 413 naming
// the ceiling; anything else (a corrupt body, a dropped connection) stays a
// 400. Without the split the two were indistinguishable, so a 25MB video upload
// was answered "failed to read request body" — which reads as "your request is
// malformed", not "your file is too large" (#125).
func WriteBodyReadError(w http.ResponseWriter, r *http.Request, err error) {
	if r != nil && r.Body != nil {
		defer r.Body.Close()
	}

	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		limit := maxErr.Limit
		if ctxLimit, ok := BodyLimitFromContext(requestContext(r)); ok && ctxLimit > 0 {
			limit = ctxLimit
		}
		handlerutil.WriteJSONError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf(
			"request body too large: this endpoint accepts up to %s", HumanBytes(limit)))
		return
	}

	handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read request body")
}

func requestContext(r *http.Request) context.Context {
	if r == nil {
		return nil
	}
	return r.Context()
}

// HumanBytes renders a byte ceiling the way the limit constants are declared,
// so the message says "10MB" rather than "10485760 bytes".
func HumanBytes(n int64) string {
	const (
		mb = 1024 * 1024
		kb = 1024
	)
	switch {
	case n >= mb && n%mb == 0:
		return fmt.Sprintf("%dMB", n/mb)
	case n >= kb && n%kb == 0:
		return fmt.Sprintf("%dKB", n/kb)
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}
