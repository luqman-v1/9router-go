// Server-side cache for a connection's live /models catalogue.
//
// The dashboard's import preview asks the same question on every open, and the
// answer is a provider catalogue that changes on the order of hours: a 460-model
// cline fetch took ~2.6 s to answer, and reopening the panel paid it again. Two
// mechanisms here:
//
//   - A short TTL cache per connection, so a reopen (or a second tab) answers
//     from memory instead of the upstream. Entries are bounded and expiring, so
//     the map cannot grow without limit on the small box this gateway serves
//     from.
//   - singleflight, so fetches that arrive while a cache miss is in flight wait
//     for that one upstream call instead of each opening their own. "Import free
//     only" pressed right after "Import" is the case this collapses.
//
// Only successful responses are cached. The cached body is the provider's own
// catalogue; adding a row to the active list does not change what the upstream
// /models returns, so an import never invalidates it. Model sync does, because
// its whole purpose is to re-read the catalogue.
package dashboard

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// modelsCatalogTTL is deliberately short. It exists to absorb a reopen, a
// double-click and the free-only press right after a plain import; it is not
// meant to make a stale catalogue look current. An operator who adds a model
// upstream and reopens the panel expects to see it within the minute.
const modelsCatalogTTL = 60 * time.Second

// modelsCatalogMaxEntries bounds the cache. One entry per connection, so this
// only matters to a node with an unusual number of connections; the cap keeps a
// long-lived process from accumulating entries for deleted connections.
const modelsCatalogMaxEntries = 64

type modelsCatalogEntry struct {
	body      []byte
	expiresAt time.Time
}

// modelsCatalog is the process-wide cache and de-duplicator. Package-level like
// liveCatalogFlight in the chat handler: the dashboard is a singleton, and a
// per-instance cache would miss across route rebuilds.
type modelsCatalog struct {
	mu      sync.Mutex
	entries map[string]modelsCatalogEntry
	flight  singleflight.Group
}

var liveModelsCatalog = &modelsCatalog{entries: make(map[string]modelsCatalogEntry)}

// serve answers a models request from the cache, or runs produce once for every
// concurrent caller. The handler's own ResponseWriter is swapped for a capture,
// so the existing branch chain (antigravity, zed, live aggregators, compatible
// nodes…) is reused unchanged instead of a second copy that could drift.
func (c *modelsCatalog) serve(
	w http.ResponseWriter,
	r *http.Request,
	connID string,
	produce func(http.ResponseWriter, *http.Request),
) {
	c.mu.Lock()
	if entry, ok := c.entries[connID]; ok && time.Now().Before(entry.expiresAt) {
		c.mu.Unlock()
		writeModelsBody(w, entry.body, true)
		return
	}
	c.mu.Unlock()

	value, err, _ := c.flight.Do(connID, func() (any, error) {
		// Re-check inside the flight: a caller that queued behind a completing
		// fetch gets its body from the cache rather than a second upstream call.
		c.mu.Lock()
		if entry, ok := c.entries[connID]; ok && time.Now().Before(entry.expiresAt) {
			c.mu.Unlock()
			return entry.body, nil
		}
		c.mu.Unlock()

		capture := &captureWriter{header: make(http.Header)}
		produce(capture, r)
		if capture.status != http.StatusOK {
			return nil, modelsHTTPError{code: capture.status, body: capture.body.Bytes()}
		}
		body := capture.body.Bytes()
		c.store(connID, body)
		return body, nil
	})

	if err != nil {
		var he modelsHTTPError
		if errors.As(err, &he) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(he.code)
			_, _ = w.Write(he.body)
			return
		}
		http.Error(w, "failed to fetch models", http.StatusBadGateway)
		return
	}
	body, _ := value.([]byte)
	writeModelsBody(w, body, false)
}

// modelsHTTPError carries a non-200 response out of the singleflight so the
// caller replays it unchanged rather than receiving a generic failure.
type modelsHTTPError struct {
	code int
	body []byte
}

func (e modelsHTTPError) Error() string { return http.StatusText(e.code) }

func writeModelsBody(w http.ResponseWriter, body []byte, hit bool) {
	w.Header().Set("Content-Type", "application/json")
	if hit {
		w.Header().Set("X-Models-Cache", "hit")
	} else {
		w.Header().Set("X-Models-Cache", "miss")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// store records a body, evicting expired entries and, when still over the cap,
// the entry nearest expiry.
func (c *modelsCatalog) store(connID string, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if len(c.entries) >= modelsCatalogMaxEntries {
		for key, entry := range c.entries {
			if now.After(entry.expiresAt) {
				delete(c.entries, key)
			}
		}
		if len(c.entries) >= modelsCatalogMaxEntries {
			oldestKey, oldest := "", now.Add(24*time.Hour)
			for key, entry := range c.entries {
				if entry.expiresAt.Before(oldest) {
					oldestKey, oldest = key, entry.expiresAt
				}
			}
			if oldestKey != "" {
				delete(c.entries, oldestKey)
			}
		}
	}
	c.entries[connID] = modelsCatalogEntry{body: body, expiresAt: now.Add(modelsCatalogTTL)}
}

// invalidate drops one connection's cached body. Model sync calls it because
// its purpose is to re-read the catalogue, and it must not be answered from the
// cache it is trying to refresh.
func (c *modelsCatalog) invalidate(connID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, connID)
}

// captureWriter records what a handler writes so the body can be cached and
// replayed. It exists instead of httptest.ResponseRecorder to keep the test
// package out of the shipped binary.
type captureWriter struct {
	header      http.Header
	body        byteSlice
	status      int
	wroteHeader bool
}

// byteSlice is a minimal growable buffer; bytes.Buffer would be fine, but this
// keeps the type local to where it is used.
type byteSlice struct {
	b []byte
}

func (s *byteSlice) Write(p []byte) (int, error) {
	s.b = append(s.b, p...)
	return len(p), nil
}

func (s *byteSlice) Bytes() []byte { return s.b }

func (cw *captureWriter) Header() http.Header { return cw.header }

func (cw *captureWriter) WriteHeader(code int) {
	if cw.wroteHeader {
		return
	}
	cw.status = code
	cw.wroteHeader = true
}

func (cw *captureWriter) Write(p []byte) (int, error) {
	if !cw.wroteHeader {
		cw.WriteHeader(http.StatusOK)
	}
	return cw.body.Write(p)
}
