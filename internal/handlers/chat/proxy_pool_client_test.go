package chat

import (
	"net/http"
	"testing"
)

func TestGetClientForConnection_PoolsAndReusesClient(t *testing.T) {
	h := &ChatHandler{
		Client: &http.Client{},
	}

	connData := &ConnectionData{
		ConnectionProxyEnabled: true,
		ConnectionProxyURL:     "http://127.0.0.1:8888",
	}

	client1, err1 := h.getClientForConnection(connData)
	if err1 != nil {
		t.Fatalf("first client: %v", err1)
	}
	if client1 == nil {
		t.Fatal("expected non-nil client")
	}

	client2, err2 := h.getClientForConnection(connData)
	if err2 != nil {
		t.Fatalf("second client: %v", err2)
	}
	if client1 != client2 {
		t.Errorf("expected client instance to be reused and identical (pooled), but got different pointers")
	}

	tr1, ok1 := client1.Transport.(*http.Transport)
	tr2, ok2 := client2.Transport.(*http.Transport)
	if !ok1 || !ok2 {
		t.Fatal("expected *http.Transport")
	}
	if tr1 != tr2 {
		t.Errorf("expected *http.Transport to be identical (shared connection pool), but got different transports")
	}
}
