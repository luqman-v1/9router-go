package proxy_test

import (
	"strings"
	"testing"

	"9router/proxy/internal/proxy"
)

func TestBuildOpenCodeHeaders(t *testing.T) {
	headers := proxy.BuildOpenCodeHeaders(nil, "my-session-123", true)
	if headers["User-Agent"] != "opencode" {
		t.Errorf("expected User-Agent opencode, got %s", headers["User-Agent"])
	}
	if headers["x-opencode-client"] != "desktop" {
		t.Errorf("expected x-opencode-client desktop, got %s", headers["x-opencode-client"])
	}
	if headers["x-opencode-project"] != "global" {
		t.Errorf("expected x-opencode-project global, got %s", headers["x-opencode-project"])
	}
	if headers["x-opencode-session"] != "ses_mysession123" && !strings.HasPrefix(headers["x-opencode-session"], "ses_") {
		t.Errorf("expected x-opencode-session starting with ses_, got %s", headers["x-opencode-session"])
	}
	if !strings.HasPrefix(headers["x-opencode-request"], "msg_") {
		t.Errorf("expected x-opencode-request starting with msg_, got %s", headers["x-opencode-request"])
	}
	if headers["Accept"] != "text/event-stream" {
		t.Errorf("expected Accept text/event-stream, got %s", headers["Accept"])
	}

	// Non-stream accepts */*
	headersNonStream := proxy.BuildOpenCodeHeaders(nil, "", false)
	if headersNonStream["Accept"] != "*/*" {
		t.Errorf("expected Accept */*, got %s", headersNonStream["Accept"])
	}
}
