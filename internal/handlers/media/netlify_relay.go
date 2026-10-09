package media

// Netlify relay helpers: the relay function bundle plus the digest-deploy
// plumbing (upstream src/lib/network/netlifyRelay.js).
//
// Deploy model (https://docs.netlify.com/api-and-cli-guides/api-guides/get-started-with-api):
//  1. POST /api/v1/sites                                       -> { id, ssl_url }
//  2. POST /api/v1/sites/{site_id}/deploys {files, functions}   -> { id, required[], required_functions[] }
//  3. PUT  /api/v1/deploys/{id}/files/{path}                   (only SHAs in `required`)
//     PUT  /api/v1/deploys/{id}/functions/{name}?runtime=js     (only SHAs in `required_functions`)
//  4. Poll GET /api/v1/deploys/{id} until state === "ready".
//
// The relay speaks the same x-relay-target / x-relay-path header spec as the
// Vercel/Cloudflare/Deno relays, so proxyAwareFetch needs no changes. Manual
// digest deploys run no build step, so the function is served from its default
// endpoint: <site>/.netlify/functions/relay.

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/handlerutil"
)

var (
	// netlifyAPI is the API root. A var rather than a const so tests can aim
	// the deploy flow at an httptest fake instead of the real Netlify API.
	netlifyAPI = "https://api.netlify.com/api/v1"

	// Upstream polls every 3s for up to 120s (netlifyRelay.js pollDeployReady).
	// Vars so tests can collapse the wait; the production defaults are what a
	// real Netlify build needs.
	netlifyPollInterval = 3 * time.Second
	netlifyPollAttempts = 40
)

const (
	netlifyFunctionName = "relay"
	netlifyFunctionPath = "/.netlify/functions/relay"

	netlifyRelayUserAgent = "9Router"
)

// Digest-deployed functions run no build step, so the bundle is served by the
// Lambda-compatible runtime: it must use exports.handler(event) with a string
// body, not the modern export-default streaming syntax. Upstream live-verified
// both failure modes — ESM syntax gives Runtime.UserCodeSyntaxError (502) and a
// stream body gives cannot-unmarshal-object-into-Go-struct-field (502).
// Response shape mirrors the other relays: relay headers stripped, status/body
// passed through.
const netlifyRelayCode = `exports.handler = async (event) => {
  const headers = {};
  for (const [k, v] of Object.entries(event.headers || {})) {
    headers[k.toLowerCase()] = v;
  }

  const target = headers["x-relay-target"];
  const relayPath = headers["x-relay-path"] || "/";

  if (!target) {
    return {
      statusCode: 400,
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ error: "Missing x-relay-target header" }),
    };
  }

  const targetUrl = target.replace(/\/$/, "") + relayPath;
  const forwardHeaders = { ...event.headers };
  for (const k of Object.keys(forwardHeaders)) {
    const lower = k.toLowerCase();
    if (lower === "x-relay-target" || lower === "x-relay-path" || lower === "host") {
      delete forwardHeaders[k];
    }
  }

  try {
    const upstream = await fetch(targetUrl, {
      method: event.httpMethod,
      headers: forwardHeaders,
      body: event.httpMethod !== "GET" && event.httpMethod !== "HEAD" && event.body
        ? event.isBase64Encoded
          ? Buffer.from(event.body, "base64")
          : event.body
        : undefined,
    });
    // Lambda response body must be a string: returning the stream object
    // fails with cannot-unmarshal-object-into-Go-struct-field (live 502).
    // So buffer here: text for API payloads (JSON/SSE), base64 for binary.
    // Tradeoff vs the other relays: SSE arrives buffered, stays valid SSE.
    const responseHeaders = {};
    upstream.headers.forEach((value, key) => {
      responseHeaders[key] = value;
    });
    const contentType = upstream.headers.get("content-type") || "";
    const isText = new RegExp("^(text/|[^;]*json|[^;]*event-stream|[^;]*javascript|[^;]*xml|[^;]*urlencoded)", "i").test(contentType);
    const rawBody = await upstream.arrayBuffer();
    const responseBody = isText
      ? Buffer.from(rawBody).toString("utf8")
      : Buffer.from(rawBody).toString("base64");
    return {
      statusCode: upstream.status,
      headers: responseHeaders,
      body: responseBody,
      isBase64Encoded: !isText,
    };
  } catch (error) {
    return {
      statusCode: 502,
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ error: error.message || "Relay fetch failed" }),
    };
  }
};
`

const netlifyIndexHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>9Router Relay</title></head>
<body><p>9Router relay function lives at <code>/.netlify/functions/relay</code>.</p></body></html>
`

// ─── Minimal stored (uncompressed) ZIP writer ───────────────────────────
// No compression dependency needed: the relay bundle is ~1.5 KB. Stored
// entries are plain CRC32 + headers, readable by any unzip tool.

type netlifyZipEntry struct {
	name string
	data []byte
}

var crcTable = sync.OnceValue(func() []uint32 {
	table := make([]uint32, 256)
	for n := range table {
		c := uint32(n)
		for range 8 {
			if c&1 == 1 {
				c = 0xedb88320 ^ (c >> 1)
			} else {
				c >>= 1
			}
		}
		table[n] = c
	}
	return table
})

func crc32Checksum(b []byte) uint32 {
	table := crcTable()
	crc := ^uint32(0)
	for _, c := range b {
		crc = table[byte(crc)^c] ^ (crc >> 8)
	}
	return ^crc
}

func buildStoredZip(files []netlifyZipEntry) []byte {
	type centralRecord struct {
		name   []byte
		crc    uint32
		size   uint32
		offset uint32
	}

	var buf bytes.Buffer
	central := make([]centralRecord, 0, len(files))
	offset := uint32(0)

	for _, f := range files {
		name := []byte(f.name)
		crc := crc32Checksum(f.data)
		size := uint32(len(f.data))

		var local [30]byte
		binary.LittleEndian.PutUint32(local[0:], 0x04034b50) // local file header signature
		binary.LittleEndian.PutUint16(local[4:], 20)         // version needed
		binary.LittleEndian.PutUint16(local[6:], 0x0800)     // UTF-8 filename flag
		binary.LittleEndian.PutUint32(local[14:], crc)
		binary.LittleEndian.PutUint32(local[18:], size)
		binary.LittleEndian.PutUint32(local[22:], size)
		binary.LittleEndian.PutUint16(local[26:], uint16(len(name)))
		buf.Write(local[:])
		buf.Write(name)
		buf.Write(f.data)

		central = append(central, centralRecord{name: name, crc: crc, size: size, offset: offset})
		offset += 30 + uint32(len(name)) + size
	}

	centralStart := offset
	for _, c := range central {
		var header [46]byte
		binary.LittleEndian.PutUint32(header[0:], 0x02014b50) // central directory signature
		binary.LittleEndian.PutUint16(header[4:], 20)         // version made by
		binary.LittleEndian.PutUint16(header[6:], 20)         // version needed
		binary.LittleEndian.PutUint16(header[8:], 0x0800)     // UTF-8 flag
		binary.LittleEndian.PutUint32(header[16:], c.crc)
		binary.LittleEndian.PutUint32(header[20:], c.size)
		binary.LittleEndian.PutUint32(header[24:], c.size)
		binary.LittleEndian.PutUint16(header[28:], uint16(len(c.name)))
		binary.LittleEndian.PutUint32(header[42:], c.offset) // local header offset
		buf.Write(header[:])
		buf.Write(c.name)
	}

	var end [22]byte
	binary.LittleEndian.PutUint32(end[0:], 0x06054b50) // end of central directory signature
	binary.LittleEndian.PutUint16(end[8:], uint16(len(files)))
	binary.LittleEndian.PutUint16(end[10:], uint16(len(files)))
	binary.LittleEndian.PutUint32(end[12:], uint32(buf.Len())-centralStart)
	binary.LittleEndian.PutUint32(end[16:], centralStart)
	buf.Write(end[:])

	return buf.Bytes()
}

// buildRelayFunctionZip returns the single-file zip Netlify expects for a
// `runtime=js` function upload, together with the SHA256 it must announce.
func buildRelayFunctionZip() ([]byte, string) {
	zip := buildStoredZip([]netlifyZipEntry{{name: netlifyFunctionName + ".js", data: []byte(netlifyRelayCode)}})
	sum := sha256.Sum256(zip)
	return zip, hex.EncodeToString(sum[:])
}

// buildIndexFile returns the site index.html content with its SHA1.
func buildIndexFile() ([]byte, string) {
	content := []byte(netlifyIndexHTML)
	sum := sha1.Sum(content)
	return content, hex.EncodeToString(sum[:])
}

func buildRelayURL(siteURL string) string {
	return strings.TrimSuffix(siteURL, "/") + netlifyFunctionPath
}

// ─── Digest-deploy HTTP plumbing ───────────────────────────────────────

// netlifyError carries the HTTP status the dashboard should render, so a
// helper can fail deep in the flow without writing a response itself.
type netlifyError struct {
	status  int
	message string
}

func (e *netlifyError) Error() string { return e.message }

func newNetlifyError(status int, message string) *netlifyError {
	return &netlifyError{status: status, message: message}
}

// netlifyDo issues one authenticated request against the Netlify API.
// contentType is empty for bodyless calls.
func netlifyDo(ctx context.Context, client *http.Client, method, url, token, contentType string, body []byte) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", netlifyRelayUserAgent)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyLen))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, raw, nil
}

// readNetlifyError pulls the message out of a Netlify error payload, falling
// back when the body is not the documented shape.
func readNetlifyError(raw []byte, fallback string) string {
	var e struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	json.Unmarshal(raw, &e)
	if e.Message != "" {
		return e.Message
	}
	if e.Error != "" {
		return e.Error
	}
	return fallback
}

var (
	netlifyInvalidNameChars = regexp.MustCompile(`[^a-z0-9-]`)
	netlifyDashRun          = regexp.MustCompile(`-+`)
)

// sanitizeNetlifySiteName mirrors upstream sanitizeSiteName: a Netlify site name
// becomes a subdomain, so anything outside lowercase alphanumerics and hyphens
// collapses to a single hyphen. An empty result falls back to a random name.
func sanitizeNetlifySiteName(raw string) string {
	random := func() string { return "relay-" + strconv.FormatInt(time.Now().UnixMilli(), 36) }

	name := strings.ToLower(strings.TrimSpace(raw))
	if name == "" {
		return random()
	}
	cleaned := netlifyDashRun.ReplaceAllString(netlifyInvalidNameChars.ReplaceAllString(name, "-"), "-")
	cleaned = strings.Trim(cleaned, "-")
	if cleaned == "" {
		return random()
	}
	return cleaned
}

// createNetlifySite creates the site that will host the relay. Netlify answers
// 422 when the name is taken, which upstream surfaces to the dashboard as 409
// with a hint rather than as a bare 422.
func createNetlifySite(ctx context.Context, client *http.Client, token, siteName string) (siteID, siteURL string, err error) {
	body, _ := json.Marshal(map[string]any{"name": siteName})
	status, raw, err := netlifyDo(ctx, client, http.MethodPost, netlifyAPI+"/sites", token, "application/json", body)
	if err != nil {
		return "", "", fmt.Errorf("createNetlifySite: %w", err)
	}
	if status < 200 || status >= 300 {
		message := readNetlifyError(raw, "Failed to create Netlify site")
		if status == http.StatusUnprocessableEntity {
			return "", "", newNetlifyError(http.StatusConflict, message+". Site name \""+siteName+"\" is taken — choose a different name.")
		}
		return "", "", newNetlifyError(status, message)
	}

	var site struct {
		ID     string `json:"id"`
		SiteID string `json:"site_id"`
		SSLURL string `json:"ssl_url"`
		URL    string `json:"url"`
	}
	json.Unmarshal(raw, &site)
	siteID = site.ID
	if siteID == "" {
		siteID = site.SiteID
	}
	siteURL = site.SSLURL
	if siteURL == "" {
		siteURL = site.URL
	}
	if siteID == "" || siteURL == "" {
		return "", "", newNetlifyError(http.StatusBadGateway, "Netlify site created but no site URL returned")
	}
	return siteID, siteURL, nil
}

// netlifyDeploy is the announced-digest deploy plus the uploads Netlify asks
// for. Uploads are skipped for SHAs already on the account.
type netlifyDeploy struct {
	id           string
	required     []string
	requiredFunc []string
}

func createNetlifyDeploy(ctx context.Context, client *http.Client, token, siteID, indexSHA, relaySHA string) (*netlifyDeploy, error) {
	body, _ := json.Marshal(map[string]any{
		"files":     map[string]any{"/index.html": indexSHA},
		"functions": map[string]any{netlifyFunctionName: relaySHA},
	})
	status, raw, err := netlifyDo(ctx, client, http.MethodPost, netlifyAPI+"/sites/"+siteID+"/deploys", token, "application/json", body)
	if err != nil {
		return nil, fmt.Errorf("createNetlifyDeploy: %w", err)
	}
	if status < 200 || status >= 300 {
		return nil, newNetlifyError(status, readNetlifyError(raw, "Failed to create Netlify deploy"))
	}

	var deploy struct {
		ID                string   `json:"id"`
		Required          []string `json:"required"`
		RequiredFunctions []string `json:"required_functions"`
	}
	json.Unmarshal(raw, &deploy)
	if deploy.ID == "" {
		return nil, newNetlifyError(http.StatusBadGateway, "Netlify deploy created but no deploy ID returned")
	}
	return &netlifyDeploy{id: deploy.ID, required: deploy.Required, requiredFunc: deploy.RequiredFunctions}, nil
}

// awaitNetlifyDeploy polls the deploy status endpoint until it is live or has
// failed. Upstream pollDeployReady authorizes every poll and gives up after
// maxMs; this reuses the package poll with the same 3s × 40 shape.
func awaitNetlifyDeploy(ctx context.Context, client *http.Client, token, deployID string) error {
	url := netlifyAPI + "/deploys/" + deployID
	check := func(data map[string]any) (bool, error) {
		switch state := handlerutil.GetString(data, "state"); state {
		case "ready":
			return true, nil
		case "error":
			message := handlerutil.GetString(data, "error_message")
			if message == "" {
				message = "Netlify deploy failed"
			}
			return false, errors.New(message)
		}
		return false, nil
	}
	for i := range netlifyPollAttempts {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(netlifyPollInterval):
			}
		}
		status, raw, err := netlifyDo(ctx, client, http.MethodGet, url, token, "", nil)
		if err != nil {
			return fmt.Errorf("awaitNetlifyDeploy: %w", err)
		}
		data := map[string]any{}
		if status == http.StatusOK {
			json.Unmarshal(raw, &data) // best-effort parse
		}
		done, err := check(data)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}
	return errors.New("Netlify deploy timed out")
}

func uploadNetlifyAsset(ctx context.Context, client *http.Client, method, url, token string, body []byte, fallback string) error {
	status, raw, err := netlifyDo(ctx, client, method, url, token, "application/octet-stream", body)
	if err != nil {
		return fmt.Errorf("uploadNetlifyAsset: %w", err)
	}
	if status < 200 || status >= 300 {
		return newNetlifyError(status, readNetlifyError(raw, fallback))
	}
	return nil
}

// uploadNetlifyDeployAssets pushes only the digests Netlify reported as
// missing, which is how the digest API deduplicates an unchanged bundle.
func uploadNetlifyDeployAssets(ctx context.Context, client *http.Client, token string, deploy *netlifyDeploy, indexContent []byte, indexSHA, relaySHA string, relayZip []byte) error {
	base := netlifyAPI + "/deploys/" + deploy.id
	if slices.Contains(deploy.required, indexSHA) {
		err := uploadNetlifyAsset(ctx, client, http.MethodPut, base+"/files/index.html", token, indexContent, "Failed to upload site file to Netlify")
		if err != nil {
			return err
		}
	}
	if slices.Contains(deploy.requiredFunc, relaySHA) {
		url := base + "/functions/" + netlifyFunctionName + "?runtime=js"
		err := uploadNetlifyAsset(ctx, client, http.MethodPut, url, token, relayZip, "Failed to upload relay function to Netlify")
		if err != nil {
			return err
		}
	}
	return nil
}

// netlifyDeployRelay runs the whole digest-deploy flow and returns the relay
// URL the gateway must call.
func netlifyDeployRelay(ctx context.Context, client *http.Client, token, siteName string) (string, error) {
	siteID, siteURL, err := createNetlifySite(ctx, client, token, siteName)
	if err != nil {
		return "", err
	}

	relayZip, relaySHA := buildRelayFunctionZip()
	indexContent, indexSHA := buildIndexFile()

	deploy, err := createNetlifyDeploy(ctx, client, token, siteID, indexSHA, relaySHA)
	if err != nil {
		return "", err
	}
	if err := uploadNetlifyDeployAssets(ctx, client, token, deploy, indexContent, indexSHA, relaySHA, relayZip); err != nil {
		return "", err
	}
	if err := awaitNetlifyDeploy(ctx, client, token, deploy.id); err != nil {
		return "", err
	}
	return buildRelayURL(siteURL), nil
}
