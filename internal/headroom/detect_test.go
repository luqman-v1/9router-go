package headroom

import "testing"

func TestIsLoopbackHeadroomUrl(t *testing.T) {
	loopback := []string{
		"http://localhost:8787",
		"http://127.0.0.1:8787",
		"http://0.0.0.0:8787",
		"http://[::1]:8787",
		"http://LOCALHOST:8787",
	}
	for _, u := range loopback {
		if !IsLoopbackHeadroomUrl(u) {
			t.Errorf("expected %q to be loopback", u)
		}
	}
	nonLoopback := []string{
		"http://example.com:8787",
		"http://192.168.1.10:8787",
		"https://headroom.mycompany.com",
		"not a url",
		"",
		"localhost:8787", // no scheme → not a URL
	}
	for _, u := range nonLoopback {
		if IsLoopbackHeadroomUrl(u) {
			t.Errorf("expected %q to NOT be loopback", u)
		}
	}
}

func TestRewriteDashboardHTML(t *testing.T) {
	cases := map[string]string{
		"fetch('/stats":                        "fetch('/api/headroom/proxy/stats",
		"fetch('/health'":                      "fetch('/api/headroom/proxy/health'",
		"fetch('/stats-history/recent":         "fetch('/api/headroom/proxy/stats-history/recent",
		"fetch('/transformations/feed":         "fetch('/api/headroom/proxy/transformations/feed",
		"fetch('/other":                        "fetch('/other",
		"fetch('https://x.example/stats":       "fetch('https://x.example/stats", // absolute URL, not a local fetch
		"fetch('/stats'); fetch('/health');":   "fetch('/api/headroom/proxy/stats'); fetch('/api/headroom/proxy/health');",
		`fetch("/stats"):'`: `fetch("/stats"):'`, // double quotes untouched
	}
	for in, want := range cases {
		if got := RewriteDashboardHTML(in); got != want {
			t.Errorf("RewriteDashboardHTML(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParsePipList(t *testing.T) {
	fixture := `[
		{"name":"headroom-ai","version":"0.5.0"},
		{"name":"tree-sitter","version":"0.20.0"},
		{"name":"torch","version":"2.1.0"}
	]`
	st := ParsePipList([]byte(fixture))
	if !st.Installed {
		t.Fatal("expected installed=true")
	}
	if st.Version != "0.5.0" {
		t.Errorf("expected version 0.5.0, got %q", st.Version)
	}
	if !st.Extras.Code {
		t.Error("expected code extra from tree-sitter marker")
	}
	if !st.Extras.ML {
		t.Error("expected ml extra from torch marker")
	}

	// headroom-ai absent → not installed
	notInstalled := ParsePipList([]byte(`[{"name":"numpy","version":"1.0"}]`))
	if notInstalled.Installed {
		t.Error("expected installed=false when headroom-ai missing")
	}

	// Invalid JSON → not installed, no panic
	invalid := ParsePipList([]byte(`not json`))
	if invalid.Installed {
		t.Error("expected installed=false for invalid JSON")
	}
}
