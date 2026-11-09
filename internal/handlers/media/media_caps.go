package media

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/middleware"
)

// A note on the total-size cap this file used to carry: MediaMaxTotalBytes was
// 64MB, refused the 100MB recording the transport ceiling was raised to accept,
// and could never fire once raised to the ceiling. The item count and
// MediaMaxBodySize already bound a request's total, so the sum cap is gone
// rather than left as a second number that agrees with nothing.

// Caps for a single media request's input. They are package vars, not consts,
// so a test can lower them instead of allocating a real 25MB part.
var (
	// MediaMaxItems caps how many media items (file parts, or data: URIs in a
	// JSON body) one request may carry. 8 matches the largest fan-out the
	// OpenAI-compatible clients send in practice; beyond it the request is a
	// batching mistake, not a user intent.
	MediaMaxItems = 8
	// MediaMaxItemBytes caps one image item — 25MB is OpenAI's own per-file
	// ceiling for vision input.
	//
	// Audio and video are exempt. The transport ceiling exists for exactly two
	// documented workloads (middleware/max_body.go): /audio/transcriptions
	// takes 30-60 minute recordings that exceed 100MB, and /videos/* takes
	// 15-50MB clips. A single 25MB ceiling refused both — the lanes' own
	// documented input — while adding nothing: those two body kinds are
	// already bounded by MediaMaxBodySize, which is what they were sized for.
	MediaMaxItemBytes int64 = 25 << 20
)

// mediaInputUse is how much media one request actually carried. It is the
// request-level usage the media lanes report back to the caller, and the same
// measurement the caps are checked against — one measurement, two consumers.
//
// Bytes is the decoded content size in both branches: a multipart part is
// measured as the bytes it carries, and a data: URI by the size its payload
// decodes to. Counting the encoded text instead made the two forms of the same
// image disagree by 4/3, so a 19MB image was refused as JSON and accepted as
// multipart.
type mediaInputUse struct {
	Items   int
	Bytes   int64
	MaxItem int64 // largest item that is neither audio nor video
}

// mediaProbeLen bounds how much of a string value is inspected to classify it.
// A data: URI's media type and a base64 payload's alphabet are both settled
// well inside it.
const mediaProbeLen = 300

// measureMediaInput counts the media items in a request body.
//
// A media item is a multipart part with a filename (or a form field the lanes
// read binary input from), or a JSON string value that is an inline data: URI
// or raw base64 in one of the image fields. Everything else — the model field,
// a prompt, JSON scaffolding — is not media and is not counted, so a large
// text body is never refused by a media cap.
//
// A body that cannot be measured is an error, never a zero measurement: see
// guardMediaInput for why that is refused rather than forwarded.
func measureMediaInput(contentType string, body []byte) (mediaInputUse, error) {
	// mime.ParseMediaType lowercases the media type, as RFC 9110 requires.
	// strings.Contains(contentType, "multipart/form-data") did not, so
	// "Multipart/form-data" — which every lane parses fine, because they all
	// call ParseMediaType — fell through to the JSON branch, where a multipart
	// body measured as zero media and no cap fired.
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err == nil && mediaType == "multipart/form-data" {
		return measureMultipartInput(contentType, body)
	}
	return measureJSONInput(body)
}

// isMultipartRequest reports whether the request body is multipart, using the
// same case-insensitive parse every lane uses. The lanes that re-derived this
// with strings.Contains did not, so a "Multipart/form-data" request was
// measured by one branch and dispatched by the other.
func isMultipartRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mediaType == "multipart/form-data"
}

func measureMultipartInput(contentType string, body []byte) (mediaInputUse, error) {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil || params["boundary"] == "" {
		if err == nil {
			err = errors.New("missing boundary")
		}
		return mediaInputUse{}, fmt.Errorf("invalid multipart content type: %w", err)
	}
	var use mediaInputUse
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// The body stopped being parseable. What was counted so far is a
			// lower bound, so it is returned with the error: a cap it already
			// exceeded can still be named.
			return use, err
		}
		// Read every part to completion so the reader advances; only media
		// parts count, and the copy is discarded — the body is already in
		// memory, so this costs no extra allocation.
		n, err := io.Copy(io.Discard, part)
		// A media part is identified the way the lanes themselves identify it,
		// which is not the same as "has a filename". antigravity_stt selects
		// its audio by `FormName() == "file"` and reads FileName() only for a
		// label, so a part named "file" with no `filename=` attribute is the
		// audio sent upstream — and counting by FileName() alone let it
		// through every cap.
		isMedia := part.FileName() != "" || formNameNamesMedia(part.FormName())
		audioVideo := isMedia && partIsAudioVideo(part)
		_ = part.Close()
		if isMedia {
			use.recordItem(n, audioVideo)
		}
		if err != nil {
			return use, err
		}
	}
	return use, nil
}

// mediaFormNames are the form field names the media lanes read binary input
// from. A part carrying one of these is media whether or not the client set a
// filename on it.
var mediaFormNames = map[string]bool{
	"file":   true, // stt, tts reference audio, image edit
	"image":  true,
	"audio":  true,
	"video":  true,
	"input":  true,
	"source": true,
}

func formNameNamesMedia(name string) bool {
	return mediaFormNames[strings.ToLower(name)]
}

// audioVideoExts are the extensions the media lanes treat as audio or video,
// read whether or not the client labelled the part.
var audioVideoExts = map[string]bool{
	".mp3": true, ".wav": true, ".m4a": true, ".ogg": true, ".opus": true,
	".flac": true, ".webm": true, ".aac": true, ".weba": true,
	".mp4": true, ".mov": true, ".mkv": true, ".avi": true,
}

// partIsAudioVideo reports whether a multipart part is audio or video — the
// two kinds MediaMaxItemBytes leaves out, because the documented workload for
// each is a single large file.
func partIsAudioVideo(part *multipart.Part) bool {
	if mediaType, _, err := mime.ParseMediaType(part.Header.Get("Content-Type")); err == nil {
		if isAudioVideoMIME(mediaType) {
			return true
		}
	}
	return audioVideoExts[strings.ToLower(filepath.Ext(part.FileName()))]
}

func isAudioVideoMIME(mediaType string) bool {
	lower := strings.ToLower(mediaType)
	return strings.HasPrefix(lower, "audio/") || strings.HasPrefix(lower, "video/")
}

func (use *mediaInputUse) recordItem(n int64, audioVideo bool) {
	use.Items++
	use.Bytes += n
	if !audioVideo && n > use.MaxItem {
		use.MaxItem = n
	}
}

// capViolation returns the 413 message when the request exceeds a cap, or ""
// when it is within them. The message names the cap and the actual figure: a
// caller told only "too large" cannot tell which limit it hit or by how much.
//
// Size is checked before count. A request that carries both problems used to
// be answered with the item count, the less actionable of the two: the count
// says "send fewer", while the size says which file to shrink.
func (use mediaInputUse) capViolation() string {
	switch {
	case use.MaxItem > MediaMaxItemBytes:
		return fmt.Sprintf("media input too large: one item is %s, the per-item limit is %s",
			middleware.HumanBytes(use.MaxItem), middleware.HumanBytes(MediaMaxItemBytes))
	case use.Items > MediaMaxItems:
		return fmt.Sprintf("media input too large: request carries %d items, the limit is %d",
			use.Items, MediaMaxItems)
	}
	return ""
}

// copyUpstreamHeaders relays the provider's response headers to the client
// without letting the provider overwrite what the gateway measured.
//
// X-Media-Items / X-Media-Bytes are set by guardMediaInput from the request the
// gateway actually parsed. Copying every upstream header straight into the
// response map let a provider that emits either name replace the measured usage
// with its own number — meaning the usage the gateway claims to own was
// writable by the party it measures.
func copyUpstreamHeaders(dst, src http.Header) {
	for k, v := range src {
		if isGatewayOwnedHeader(k) {
			continue
		}
		dst[k] = v
	}
}

func isGatewayOwnedHeader(name string) bool {
	switch http.CanonicalHeaderKey(name) {
	case "X-Media-Items", "X-Media-Bytes":
		return true
	}
	return false
}

// guardMediaInput measures the request, reports the usage on the response, and
// answers itself when the request must not be forwarded — reporting whether it
// answered. Callers run it before model resolution and before any upstream
// call, so an oversized request is never billed.
//
// Usage headers are set here, before any write, because the media lanes later
// stream the upstream body straight to the client and headers can no longer
// change once the status line is out.
func guardMediaInput(w http.ResponseWriter, contentType string, body []byte) (mediaInputUse, bool) {
	use, err := measureMediaInput(contentType, body)
	// A cap a partial measurement already exceeded is still real: what was
	// counted before the body stopped parsing is a lower bound.
	if msg := use.capViolation(); msg != "" {
		handlerutil.WriteJSONError(w, http.StatusRequestEntityTooLarge, msg)
		return use, true
	}
	if err != nil {
		// Fail closed. A body the gateway cannot measure is a body it cannot
		// cap, and the media lanes tolerate a partial parse: antigravity_stt
		// and elevenlabs_stt break out of their own loop on the same error and
		// forward the file they already read. Measured before this guard: a
		// 30MB audio part followed by one malformed header arrived upstream
		// with items=0, bytes=0, status 200. The 400 names the real problem —
		// a malformed body — rather than blaming a size that was never known.
		handlerutil.WriteJSONError(w, http.StatusBadRequest,
			fmt.Sprintf("malformed media request body: %v", err))
		return use, true
	}
	w.Header().Set("X-Media-Items", strconv.Itoa(use.Items))
	w.Header().Set("X-Media-Bytes", strconv.FormatInt(use.Bytes, 10))
	return use, false
}
