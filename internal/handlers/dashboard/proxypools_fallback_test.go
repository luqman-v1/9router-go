package dashboard

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestHandleTestProxyPool_FallbackProbe(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	if _, err := repo.RawDB().Exec(`CREATE TABLE IF NOT EXISTS proxyPools (
		id TEXT PRIMARY KEY,
		isActive INTEGER DEFAULT 1,
		testStatus TEXT,
		data TEXT NOT NULL,
		createdAt TEXT NOT NULL,
		updatedAt TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("failed to create proxyPools table: %v", err)
	}

	router := setupTestRouter(repo)

	var primaryHits int32
	var secondaryHits int32
	var primaryShouldFail int32
	var secondaryShouldFail int32

	probeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/primary" {
			atomic.AddInt32(&primaryHits, 1)
			if atomic.LoadInt32(&primaryShouldFail) == 1 {
				http.Error(w, "simulated google failure", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path == "/secondary" {
			atomic.AddInt32(&secondaryHits, 1)
			if atomic.LoadInt32(&secondaryShouldFail) == 1 {
				http.Error(w, "simulated cloudflare failure", http.StatusBadGateway)
				return
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("fl=1\nh=cloudflare.com\n"))
			return
		}
		http.NotFound(w, r)
	}))
	defer probeServer.Close()

	oldPrimary := proxyPrimaryProbeURL
	oldSecondary := proxySecondaryProbeURL
	defer func() {
		proxyPrimaryProbeURL = oldPrimary
		proxySecondaryProbeURL = oldSecondary
	}()

	proxyPrimaryProbeURL = probeServer.URL + "/primary"
	proxySecondaryProbeURL = probeServer.URL + "/secondary"

	// Helper to create a proxy pool
	createPool := func(name, url string) string {
		createBody, _ := json.Marshal(map[string]any{
			"name":     name,
			"proxyUrl": url,
		})
		createReq := httptest.NewRequest(http.MethodPost, "/api/proxy-pools", bytes.NewReader(createBody))
		createReq.Header.Set("Content-Type", "application/json")
		createRec := httptest.NewRecorder()
		router.ServeHTTP(createRec, createReq)
		if createRec.Code != http.StatusOK {
			t.Fatalf("create proxy pool failed: %s", createRec.Body.String())
		}
		var created map[string]any
		_ = json.Unmarshal(createRec.Body.Bytes(), &created)
		return created["id"].(string)
	}

	// 1. Primary succeeds: secondary should NOT be called
	t.Run("Primary probe succeeds", func(t *testing.T) {
		atomic.StoreInt32(&primaryHits, 0)
		atomic.StoreInt32(&secondaryHits, 0)
		atomic.StoreInt32(&primaryShouldFail, 0)
		atomic.StoreInt32(&secondaryShouldFail, 0)

		poolID := createPool("Primary OK Pool", probeServer.URL)
		testReq := httptest.NewRequest(http.MethodPost, "/api/proxy-pools/"+poolID+"/test", nil)
		testRec := httptest.NewRecorder()
		router.ServeHTTP(testRec, testReq)

		if testRec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", testRec.Code)
		}
		var res map[string]any
		_ = json.Unmarshal(testRec.Body.Bytes(), &res)
		if res["success"] != true || res["status"] != "passed" {
			t.Fatalf("expected passed, got %v", res)
		}
		if atomic.LoadInt32(&primaryHits) != 1 {
			t.Fatalf("expected 1 primary hit, got %d", atomic.LoadInt32(&primaryHits))
		}
		if atomic.LoadInt32(&secondaryHits) != 0 {
			t.Fatalf("expected 0 secondary hits, got %d", atomic.LoadInt32(&secondaryHits))
		}
	})

	// 2. Primary fails, fallback secondary probe succeeds
	t.Run("Primary probe fails, secondary succeeds", func(t *testing.T) {
		atomic.StoreInt32(&primaryHits, 0)
		atomic.StoreInt32(&secondaryHits, 0)
		atomic.StoreInt32(&primaryShouldFail, 1)
		atomic.StoreInt32(&secondaryShouldFail, 0)

		poolID := createPool("Fallback Pool", probeServer.URL)
		testReq := httptest.NewRequest(http.MethodPost, "/api/proxy-pools/"+poolID+"/test", nil)
		testRec := httptest.NewRecorder()
		router.ServeHTTP(testRec, testReq)

		if testRec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", testRec.Code)
		}
		var res map[string]any
		_ = json.Unmarshal(testRec.Body.Bytes(), &res)
		if res["success"] != true || res["status"] != "passed" {
			t.Fatalf("expected passed on secondary probe, got %v", res)
		}
		if atomic.LoadInt32(&primaryHits) != 1 {
			t.Fatalf("expected 1 primary hit, got %d", atomic.LoadInt32(&primaryHits))
		}
		if atomic.LoadInt32(&secondaryHits) != 1 {
			t.Fatalf("expected 1 secondary hit, got %d", atomic.LoadInt32(&secondaryHits))
		}
		latency, ok := res["latency"].(float64)
		if !ok || latency < 0 {
			t.Fatalf("expected latency in response, got %v", res["latency"])
		}

		// Verify DB status
		var testStatus, dataStr string
		err := repo.RawDB().QueryRow("SELECT testStatus, data FROM proxyPools WHERE id = ?", poolID).Scan(&testStatus, &dataStr)
		if err != nil || testStatus != "passed" {
			t.Fatalf("expected testStatus 'passed' in DB, got %s (err: %v)", testStatus, err)
		}
	})

	// 3. Both probes fail
	t.Run("Both probes fail", func(t *testing.T) {
		atomic.StoreInt32(&primaryHits, 0)
		atomic.StoreInt32(&secondaryHits, 0)
		atomic.StoreInt32(&primaryShouldFail, 1)
		atomic.StoreInt32(&secondaryShouldFail, 1)

		poolID := createPool("Dead Pool", probeServer.URL)
		testReq := httptest.NewRequest(http.MethodPost, "/api/proxy-pools/"+poolID+"/test", nil)
		testRec := httptest.NewRecorder()
		router.ServeHTTP(testRec, testReq)

		if testRec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", testRec.Code)
		}
		var res map[string]any
		_ = json.Unmarshal(testRec.Body.Bytes(), &res)
		if res["success"] != false || res["status"] != "failed" {
			t.Fatalf("expected failed when both probes fail, got %v", res)
		}
		if atomic.LoadInt32(&primaryHits) != 1 || atomic.LoadInt32(&secondaryHits) != 1 {
			t.Fatalf("expected both primary and secondary to be attempted")
		}

		// Verify DB status
		var testStatus string
		_ = repo.RawDB().QueryRow("SELECT testStatus FROM proxyPools WHERE id = ?", poolID).Scan(&testStatus)
		if testStatus != "failed" {
			t.Fatalf("expected testStatus 'failed' in DB, got %s", testStatus)
		}
	})

	// 4. Invalid proxy URL
	t.Run("Invalid proxy URL", func(t *testing.T) {
		poolID := createPool("Invalid URL Pool", "://invalid-url")
		testReq := httptest.NewRequest(http.MethodPost, "/api/proxy-pools/"+poolID+"/test", nil)
		testRec := httptest.NewRecorder()
		router.ServeHTTP(testRec, testReq)

		var res map[string]any
		_ = json.Unmarshal(testRec.Body.Bytes(), &res)
		if res["success"] != false || res["status"] != "failed" {
			t.Fatalf("expected failed for invalid URL, got %v", res)
		}
	})
}
