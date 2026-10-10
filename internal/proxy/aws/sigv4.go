// Package aws implements the AWS protocol primitives 9router needs to speak to
// Amazon Bedrock: Signature Version 4 request signing and AWS credential resolution.
//
// SigV4 is hand-rolled rather than pulled from the AWS SDK for Go because every request
// signed here is a single POST (or GET) with a JSON body and no query string — the
// canonicalisation cases that make SigV4 error-prone (multi-value headers, query ordering,
// S3's single-encoded paths) never arise. The credentials resolver is where the SDK's
// value actually lives: it is what makes `aws sso login --profile X` work.
package aws

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// SigV4 protocol constants. These are fixed by the specification, not by any provider,
// so they live here rather than in a registry entry.
const (
	sigV4Algorithm        = "AWS4-HMAC-SHA256"
	sigV4Terminator       = "aws4_request"
	sigV4KeyPrefix        = "AWS4"
	dateHeader            = "x-amz-date"
	securityTokenHeader   = "x-amz-security-token"
	authorizationHeader   = "Authorization"
	emptyPayloadSHA256Hex = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// Credentials is the subset of an AWS identity the signer needs. SessionToken is set for
// temporary credentials (STS, SSO, assumed roles) and empty for long-lived IAM keys.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

// escapeURI percent-encodes per RFC 3986, leaving only the unreserved set alone.
// url.PathEscape is not enough on its own: it does not escape !'()* , all of which AWS
// escapes.
func escapeURI(value string) string {
	const upperhex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(value))
	for i := range len(value) {
		c := value[i]
		if isURIUnreserved(c) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(upperhex[c>>4])
		b.WriteByte(upperhex[c&0x0f])
	}
	return b.String()
}

func isURIUnreserved(c byte) bool {
	return (c >= 'A' && c <= 'Z') ||
		(c >= 'a' && c <= 'z') ||
		(c >= '0' && c <= '9') ||
		c == '-' || c == '_' || c == '.' || c == '~'
}

// sha256Hex returns the lowercase hex SHA-256 of s.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

// canonicalPath builds the canonical URI. Every service except S3 expects each path
// segment escaped a second time, so a Bedrock model id that reaches the wire as
// "...v1%3A0" is signed as "...v1%253A0". Getting this wrong is the single most common
// cause of a Bedrock SignatureDoesNotMatch.
func canonicalPath(pathname string, singleEncodePath bool) string {
	if singleEncodePath {
		if pathname == "" {
			return "/"
		}
		return pathname
	}
	segments := strings.Split(pathname, "/")
	for i, segment := range segments {
		segments[i] = escapeURI(segment)
	}
	joined := strings.Join(segments, "/")
	if joined == "" {
		return "/"
	}
	return joined
}

// SignOptions describes one request to sign.
type SignOptions struct {
	Method string
	// URL is the absolute request URL. Its path must already be escaped once.
	URL     string
	Headers map[string]string
	Body    string
	Region  string
	Service string
	// Credentials must carry both the key id and the secret.
	Credentials Credentials
	// SingleEncodePath is opt-in and applies only to S3-style services. Every other service
	// double-encodes, which is the safe default: a caller that forgets to opt in gets the
	// behaviour Bedrock needs rather than a SignatureDoesNotMatch.
	SingleEncodePath bool
}

// canonicalHeaders builds the canonical headers block plus the matching SignedHeaders
// list. Names are lowercased, values trimmed with internal whitespace runs collapsed,
// and both sorted by name.
func canonicalHeaders(headers map[string]string) (canonical string, signed string) {
	names := make([]string, 0, len(headers))
	values := make(map[string]string, len(headers))
	for name, value := range headers {
		lower := strings.ToLower(name)
		names = append(names, lower)
		values[lower] = strings.Join(strings.Fields(value), " ")
	}
	sort.Strings(names)

	var b strings.Builder
	for _, name := range names {
		b.WriteString(name)
		b.WriteByte(':')
		b.WriteString(values[name])
		b.WriteByte('\n')
	}
	return b.String(), strings.Join(names, ";")
}

// SignRequest signs a request using the wall clock and returns the headers to send.
//
// Every field it adds — host, x-amz-date, and the security token when present — is also
// included in SignedHeaders. That is load-bearing for temporary credentials: omitting
// the token from the signed set is the classic cause of SignatureDoesNotMatch on STS
// and SSO identities.
func SignRequest(opts SignOptions) (map[string]string, error) {
	return signRequestAt(opts, time.Now().UTC())
}

// SignRequestAt is SignRequest with an injectable clock, for deterministic tests.
func SignRequestAt(opts SignOptions, now time.Time) (map[string]string, error) {
	return signRequestAt(opts, now.UTC())
}

func signRequestAt(opts SignOptions, now time.Time) (map[string]string, error) {
	if opts.Credentials.AccessKeyID == "" || opts.Credentials.SecretAccessKey == "" {
		return nil, fmt.Errorf("aws: SigV4 signing requires both AccessKeyID and SecretAccessKey")
	}
	if opts.Region == "" {
		return nil, fmt.Errorf("aws: SigV4 signing requires a region")
	}
	if opts.Service == "" {
		return nil, fmt.Errorf("aws: SigV4 signing requires a service name")
	}

	parsed, err := url.Parse(opts.URL)
	if err != nil {
		return nil, fmt.Errorf("aws: parse request URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("aws: request URL %q must be absolute", opts.URL)
	}

	// Query canonicalisation is deliberately NOT implemented. SigV4 requires the query
	// sorted by percent-encoded name with AWS-style escaping, while url.Values preserves
	// insertion order and encodes a space as "+". Every caller here signs a query-less
	// POST, so rather than ship a subtly wrong canonical request for some future caller,
	// refuse the input outright.
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return nil, fmt.Errorf("aws: SigV4 signing does not support query strings (got %q); "+
			"move the parameters into the request body, or add proper query canonicalisation first", parsed.RawQuery)
	}

	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	payloadHash := emptyPayloadSHA256Hex
	if opts.Body != "" {
		payloadHash = sha256Hex(opts.Body)
	}

	headersToSign := make(map[string]string, len(opts.Headers)+3)
	for name, value := range opts.Headers {
		headersToSign[name] = value
	}
	headersToSign["host"] = parsed.Host
	headersToSign[dateHeader] = amzDate
	if opts.Credentials.SessionToken != "" {
		headersToSign[securityTokenHeader] = opts.Credentials.SessionToken
	}
	canonical, signed := canonicalHeaders(headersToSign)

	canonicalRequest := strings.Join([]string{
		strings.ToUpper(opts.Method),
		canonicalPath(parsed.EscapedPath(), opts.SingleEncodePath),
		// Always empty: the guard above rejects any URL carrying a query string.
		"",
		canonical,
		signed,
		payloadHash,
	}, "\n")

	credentialScope := strings.Join([]string{dateStamp, opts.Region, opts.Service, sigV4Terminator}, "/")
	stringToSign := strings.Join([]string{
		sigV4Algorithm,
		amzDate,
		credentialScope,
		sha256Hex(canonicalRequest),
	}, "\n")

	key := hmacSHA256([]byte(sigV4KeyPrefix+opts.Credentials.SecretAccessKey), dateStamp)
	key = hmacSHA256(key, opts.Region)
	key = hmacSHA256(key, opts.Service)
	key = hmacSHA256(key, sigV4Terminator)
	signature := hex.EncodeToString(hmacSHA256(key, stringToSign))

	headersToSign[authorizationHeader] = fmt.Sprintf(
		"%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		sigV4Algorithm, opts.Credentials.AccessKeyID, credentialScope, signed, signature,
	)
	return headersToSign, nil
}

// RedactSignedHeaders returns a copy of the signed headers that is safe to write to a
// request log. The session token is a credential and the signature can replay this exact
// request, so both are removed; the key id and signed-header list stay for debugging.
func RedactSignedHeaders(headers map[string]string) map[string]string {
	redacted := make(map[string]string, len(headers))
	for name, value := range headers {
		redacted[name] = value
	}
	if auth, ok := redacted[authorizationHeader]; ok {
		if idx := strings.Index(auth, "Signature="); idx >= 0 {
			redacted[authorizationHeader] = auth[:idx] + "Signature=<redacted>"
		}
	}
	if _, ok := redacted[securityTokenHeader]; ok {
		redacted[securityTokenHeader] = "<redacted>"
	}
	return redacted
}
