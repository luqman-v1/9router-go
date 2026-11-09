package guardrails

import (
	"strings"
	"testing"
)

func TestCustomPatternBlock(t *testing.T) {
	patterns := []CustomPattern{
		{Name: "project_code", Regex: `\bPROJ-[0-9]{4,}\b`, Replacement: "[CONFIDENTIAL]"},
	}
	e := NewEngineWithCustom([]string{}, ActionBlock, patterns)
	if !e.Enabled() {
		t.Fatal("engine should be enabled with custom patterns")
	}
	d := e.Scan("Refactor PROJ-10294 to use new router")
	if d.Action != ActionBlock {
		t.Errorf("expected ActionBlock, got %v", d.Action)
	}
	if len(d.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(d.Findings))
	}
	if d.Findings[0].Detector != "project_code" {
		t.Errorf("expected detector project_code, got %s", d.Findings[0].Detector)
	}
	if d.Findings[0].Entity != "CUSTOM_PATTERN" {
		t.Errorf("expected entity CUSTOM_PATTERN, got %s", d.Findings[0].Entity)
	}
}

func TestCustomPatternMaskDefault(t *testing.T) {
	patterns := []CustomPattern{
		{Name: "order_id", Regex: `\bORDER-\d+\b`},
	}
	e := NewEngineWithCustom([]string{}, ActionMask, patterns)
	d := e.Scan("Processing ORDER-98124 now")
	if d.Action != ActionMask {
		t.Errorf("expected ActionMask, got %v", d.Action)
	}
	if !strings.Contains(d.Mutated, "[REDACTED]") {
		t.Errorf("expected default redactMask in %q", d.Mutated)
	}
	if strings.Contains(d.Mutated, "ORDER-98124") {
		t.Errorf("expected ORDER-98124 to be redacted, got %q", d.Mutated)
	}
}

func TestCustomPatternMaskCustomReplacement(t *testing.T) {
	patterns := []CustomPattern{
		{Name: "secret_token", Regex: `\bsec_[a-zA-Z0-9]{8}\b`, Replacement: "[SECRET]"},
	}
	e := NewEngineWithCustom([]string{}, ActionMask, patterns)
	d := e.Scan("Token: sec_a1b2c3d4 is active")
	if d.Action != ActionMask {
		t.Errorf("expected ActionMask, got %v", d.Action)
	}
	if !strings.Contains(d.Mutated, "[SECRET]") {
		t.Errorf("expected custom replacement [SECRET] in %q", d.Mutated)
	}
	if strings.Contains(d.Mutated, "sec_a1b2c3d4") {
		t.Errorf("expected secret to be redacted, got %q", d.Mutated)
	}
}

func TestCustomPatternLogOnly(t *testing.T) {
	patterns := []CustomPattern{
		{Name: "internal_kw", Regex: `(?i)\bconfidential\b`},
	}
	e := NewEngineWithCustom([]string{}, ActionLogOnly, patterns)
	d := e.Scan("This document is CONFIDENTIAL.")
	if d.Action != ActionLogOnly {
		t.Errorf("expected ActionLogOnly, got %v", d.Action)
	}
	if d.Mutated != "" {
		t.Errorf("expected no mutation for LogOnly, got %q", d.Mutated)
	}
	if len(d.Findings) != 1 {
		t.Errorf("expected 1 finding, got %d", len(d.Findings))
	}
}
