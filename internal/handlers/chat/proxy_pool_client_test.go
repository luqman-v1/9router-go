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

func TestGetClientForConnection_Socks5ProxyPool(t *testing.T) {
	h := &ChatHandler{
		Client: &http.Client{},
	}

	tests := []struct {
		name      string
		proxyURL  string
		proxyType string
	}{
		{
			name:      "socks5h scheme with socks5 type",
			proxyURL:  "socks5h://192.168.22.206:1080",
			proxyType: "socks5",
		},
		{
			name:      "socks5 scheme with socks type",
			proxyURL:  "socks5://192.168.22.206:1080",
			proxyType: "socks",
		},
		{
			name:      "socks5 scheme with default type",
			proxyURL:  "socks5://10.0.0.1:1080",
			proxyType: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			connData := &ConnectionData{
				ConnectionProxyEnabled: true,
				ConnectionProxyURL:     tt.proxyURL,
				ProviderSpecificData: map[string]any{
					"connectionProxyEnabled": true,
					"connectionProxyUrl":     tt.proxyURL,
				},
			}

			client, err := h.getClientForConnection(connData)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if client == nil {
				t.Fatal("expected non-nil client")
			}
			if client == h.Client {
				t.Errorf("expected SOCKS5 proxy client, but got direct fallback client h.Client")
			}

			tr, ok := client.Transport.(*http.Transport)
			if !ok {
				t.Fatalf("expected *http.Transport, got %T", client.Transport)
			}
			if tr.Proxy == nil {
				t.Fatal("expected tr.Proxy to be set for SOCKS5 proxy")
			}
			dummyReq, _ := http.NewRequest("GET", "https://api.openai.com", nil)
			resolvedProxy, err := tr.Proxy(dummyReq)
			if err != nil {
				t.Fatalf("tr.Proxy returned error: %v", err)
			}
			if resolvedProxy == nil || resolvedProxy.String() != tt.proxyURL {
				t.Errorf("tr.Proxy = %v, want %s", resolvedProxy, tt.proxyURL)
			}
		})
	}
}
