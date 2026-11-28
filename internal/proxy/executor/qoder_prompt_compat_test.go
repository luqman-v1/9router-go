package executor

import (
	"context"
	"strings"
	"testing"
)

func TestQoderAsAgentSDK(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"You are Claude Code, Anthropic's official CLI for Claude. hi", "You are a Claude agent, built on Anthropic's Claude Agent SDK. hi"},
		{"You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK. hi", "You are a Claude agent, built on Anthropic's Claude Agent SDK. hi"},
		{"no change here", "no change here"},
	}
	for _, c := range cases {
		got := qoderAsAgentSDK(c.in)
		if got != c.want {
			t.Errorf("got %q want %q", got, c.want)
		}
	}
}

func TestQoderHarnessComma(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"You are an AI agent powered by DeepSeek Harness.", "You are an AI agent, powered by DeepSeek Harness."},
		{"You are the assistant running on X", "You are the assistant, running on X"},
		{"You are a agent built on Y", "You are a agent, built on Y"},
	}
	for _, c := range cases {
		got := qoderHarnessComma(c.in)
		if got != c.want {
			t.Errorf("got %q want %q", got, c.want)
		}
	}
}

func TestQoderPromptCompatApplied(t *testing.T) {
	// Ensure composition order matches upstream: asAgentSDK then harnessComma.
	in := "You are Claude Code, Anthropic's official CLI for Claude. You are an AI agent powered by DeepSeek Harness."
	out := qoderHarnessComma(qoderAsAgentSDK(in))
	if !strings.Contains(out, "You are a Claude agent, built on Anthropic's Claude Agent SDK.") {
		t.Fatalf("missing agent sdk line: %s", out)
	}
	if !strings.Contains(out, "You are an AI agent, powered by DeepSeek Harness.") {
		t.Fatalf("missing comma: %s", out)
	}
}

func TestQoderPromptCompatInBuiltPayload(t *testing.T) {
	creds := qoderFixture(t)
	body := []byte(`{"model":"qd/qfmodel","messages":[{"role":"system","content":"You are Claude Code, Anthropic's official CLI for Claude. You are an AI agent powered by DeepSeek Harness."},{"role":"user","content":"hi"}],"stream":false}`)
	config := qoderCachedModelConfig(creds, QoderRegionIntl, "qfmodel")
	if config == nil {
		t.Fatal("no model_config")
	}
	built, err := buildQoderChatRequestWithAttachments(context.Background(), body, config, creds, 1700000000000, QoderRegionIntl, nil, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	sys, _ := built.Payload["system"].(string)
	if !strings.Contains(sys, "You are a Claude agent, built on Anthropic's Claude Agent SDK.") {
		t.Fatalf("missing agent sdk line: %s", sys)
	}
	if !strings.Contains(sys, "You are an AI agent, powered by DeepSeek Harness.") {
		t.Fatalf("missing comma: %s", sys)
	}
}
