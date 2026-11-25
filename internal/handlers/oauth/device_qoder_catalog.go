package oauth

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"time"

	"9router/proxy/internal/log"
	"9router/proxy/internal/proxy/executor"
)

// Qoder post-login catalog fill — local compatibility fix.
//
// A successful device login leaves the executor's per-credential catalog
// empty, so the operator's first chat request pays for the model-list fetch
// (and a friendly name cannot resolve from display names until it lands).
// The login flow therefore fills the catalog immediately after the
// connection is saved, the same way the reference gateway refreshes its
// snapshot before answering the poll. The fill is best-effort: a catalog
// failure never fails the login, and chat's own fetch path still covers a
// cold cache.

// qoderPostLoginCatalogTimeout bounds the fill. The login response must not
// wait out the chat transport's longer timeout when the catalog host is slow.
const qoderPostLoginCatalogTimeout = 10 * time.Second

// refreshQoderCatalogAfterDeviceLogin fills the live catalog for a freshly
// authorized Qoder connection. Non-Qoder providers are a no-op.
func refreshQoderCatalogAfterDeviceLogin(ctx context.Context, provider string, t deviceTokens) {
	refreshQoderCatalogWithDoer(ctx, provider, t, qoderDeviceCatalogDoer)
}

// refreshQoderCatalogWithDoer is the testable core: the transport is a seam
// so tests never reach the network.
func refreshQoderCatalogWithDoer(ctx context.Context, provider string, t deviceTokens, do executor.QoderDoer) {
	if provider != "qoder" && provider != "qoder-cn" {
		return
	}
	token := t.access
	// A device login mints a dt- token; a PAT cannot sign COSY requests and
	// has no place in this path.
	if token == "" || executor.IsQoderPAT(token) {
		return
	}
	userID := strVal(t.extra, "userId")
	if userID == "" {
		// The COSY signature is bound to the account's user id. Without one
		// the catalog cannot be fetched; the login itself still stands and
		// chat will surface the re-authorize message on first use.
		return
	}
	creds := executor.QoderCosyCreds{
		UserID:    userID,
		AuthToken: token,
		MachineID: strVal(t.extra, "machineId"),
	}
	ctx, cancel := context.WithTimeout(ctx, qoderPostLoginCatalogTimeout)
	defer cancel()
	if err := executor.RefreshQoderCatalog(ctx, do, creds, executor.QoderRegionOf(provider)); err != nil {
		log.Warn("oauth", "qoder post-login catalog refresh failed", "provider", provider, "error", err)
	}
}

// qoderDeviceCatalogDoer rides the device flow's own client (fallback
// transport included), so the fill works from the same networks the login
// just succeeded on.
var qoderDeviceCatalogDoer executor.QoderDoer = func(ctx context.Context, method, rawURL string, headers map[string]string, body []byte) (int, []byte, error) {
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return 0, nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := deviceClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, out, nil
}
