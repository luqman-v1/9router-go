package translator

import (
	json "encoding/json/v2"
	"os"
	"strings"
	"testing"
	"time"
)

// openCodeCLICapture is the request the shipped CLI actually sent, recorded
// against a local mock endpoint. It is the source of truth for both the
// harness template and the built-in tool definitions: the gate fingerprints
// them, so a version bump has to show up as a diff here, not as a guess.
type openCodeCLICapture struct {
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	Tools []map[string]any `json:"tools"`
}

func loadOpenCodeCLICapture(t *testing.T) openCodeCLICapture {
	t.Helper()
	raw, err := os.ReadFile("testdata/opencode-v232/capture.json")
	if err != nil {
		t.Fatalf("read capture fixture: %v", err)
	}
	var capture openCodeCLICapture
	if err := json.Unmarshal(raw, &capture); err != nil {
		t.Fatalf("decode capture fixture: %v", err)
	}
	if len(capture.Messages) == 0 || len(capture.Tools) == 0 {
		t.Fatalf("capture fixture is incomplete: %d messages, %d tools", len(capture.Messages), len(capture.Tools))
	}
	return capture
}

func (c openCodeCLICapture) system() string {
	var b strings.Builder
	for _, m := range c.Messages {
		if m.Role == "system" {
			b.WriteString(m.Content)
		}
	}
	return b.String()
}

// The rendered harness must reproduce the CLI's own bytes once the per-request
// values are filled in. Anything that does not means the template was edited
// by hand, which is exactly what the gate fingerprints.
func TestOpenCodeHarnessMatchesCLICapture(t *testing.T) {
	capture := loadOpenCodeCLICapture(t)
	// The capture ran on this host, so it carries the host's persona and the
	// date it ran on. Rendering with those values must be byte-identical.
	persona := openCodePersona{
		Platform: "win32",
		CWD:      `E:\project\oc-cap\proj`,
		TmpDir:   `E:\cache\temp\opencode`,
		Git:      "no",
	}
	now := time.Date(2026, time.November, 29, 12, 0, 0, 0, time.Local)

	got := renderOpenCodeHarness(now, "Mock M", "mock", "m", persona)
	want := capture.system()
	if got != want {
		for i := 0; i < len(got) && i < len(want); i++ {
			if got[i] != want[i] {
				t.Fatalf("harness diverges from the CLI capture at byte %d:\n got: %.120q\nwant: %.120q",
					i, got[max(0, i-40):min(len(got), i+80)], want[max(0, i-40):min(len(want), i+80)])
			}
		}
		t.Fatalf("harness length differs from the CLI capture: got %d, want %d", len(got), len(want))
	}
}

// Every declared built-in must be the CLI's own definition. The shell
// description is the one platform-dependent entry: the template claims a Linux
// persona, so the captured Windows wording is swapped for the Linux one.
func TestOpenCodeGenuineToolsMatchCLICapture(t *testing.T) {
	capture := loadOpenCodeCLICapture(t)

	var ours []map[string]any
	if err := json.Unmarshal([]byte(openCodeGenuineToolsJSON), &ours); err != nil {
		t.Fatalf("decode openCodeGenuineToolsJSON: %v", err)
	}
	if len(ours) != len(capture.Tools) {
		t.Fatalf("declared %d built-ins, the CLI sent %d", len(ours), len(capture.Tools))
	}
	if len(ours) != len(openCodeGenuineToolNames) {
		t.Fatalf("declared %d built-ins, %d names listed", len(ours), len(openCodeGenuineToolNames))
	}

	const winShell = "Commands run on Windows using pwsh."
	const linuxShell = "Commands run on Linux using bash."

	for i, def := range ours {
		fn, _ := def["function"].(map[string]any)
		want, _ := capture.Tools[i]["function"].(map[string]any)
		if fn == nil || want == nil {
			t.Fatalf("tool %d is not a chat-shaped function definition", i)
		}
		name, _ := fn["name"].(string)
		if name != want["name"] {
			t.Fatalf("tool %d is %q, the CLI sent %q", i, name, want["name"])
		}
		if name != openCodeGenuineToolNames[i] {
			t.Fatalf("declaration order drifted at %q", name)
		}

		wantDesc, _ := want["description"].(string)
		gotDesc, _ := fn["description"].(string)
		if name == "shell" {
			if !strings.Contains(gotDesc, linuxShell) {
				t.Fatalf("shell description must claim the Linux persona, got %.160q", gotDesc)
			}
			wantDesc = strings.Replace(wantDesc, winShell, linuxShell, 1)
		}
		if gotDesc != wantDesc {
			t.Fatalf("%s description drifted from the CLI capture:\n got: %.200q\nwant: %.200q", name, gotDesc, wantDesc)
		}
		if !jsonEqual(fn["parameters"], want["parameters"]) {
			t.Fatalf("%s parameters drifted from the CLI capture", name)
		}
	}
}

// The two values the previous capture froze are the ones a gate can compare
// against its own clock and the request: the date and the model block.
func TestOpenCodeHarnessRendersCurrentDateAndModel(t *testing.T) {
	now := time.Now()
	harness := renderOpenCodeHarness(now, "", openCodeHarnessProviderID, "hy4-flash-free", openCodeFreePersona)

	if want := "Today's date: " + now.Format("Mon Jan 02 2006"); !strings.Contains(harness, want) {
		t.Fatalf("harness must carry the current local date (%q)", want)
	}
	if strings.Contains(harness, "Oct 25 2026") {
		t.Fatal("harness still carries the frozen v2.1.4 date")
	}
	if want := "# Your Model\n- Name: hy4-flash-free\n- Provider ID: opencode\n- Model ID: hy4-flash-free"; !strings.Contains(harness, want) {
		t.Fatalf("harness must name the requested model, got %.300q", harness)
	}
	if strings.Contains(harness, "GLM-5.3-Flash Free") {
		t.Fatal("harness still carries the frozen v2.1.4 model identity")
	}
}

// Stripping Code Mode must not touch the harness bullets: they are part of
// every genuine CLI prompt, and deleting them made the prefix diverge from a
// real request at the third bullet.
func TestStripOpencodeCodeModeKeepsHarnessBullets(t *testing.T) {
	harness := renderOpenCodeHarness(time.Now(), "", openCodeHarnessProviderID, "m", openCodeFreePersona)
	stripped := stripOpencodeCodeMode(harness)

	for _, want := range []string{
		"- Prefer parallelizing independent tool calls.",
		"- Prefer dedicated tools over shell commands",
		"- Use the write tool",
		"- Use the edit tool",
	} {
		if !strings.Contains(stripped, want) {
			t.Fatalf("stripped harness dropped %q", want)
		}
	}
	if strings.Contains(stripped, "# Code Mode") {
		t.Fatal("stripped harness still carries the Code Mode section")
	}
	if !strings.Contains(stripped, "Skills provide specialized") {
		t.Fatal("stripped harness dropped the skills block that follows Code Mode")
	}
	prefix := openCodeHarnessPrefix(harness)
	if !strings.HasPrefix(stripped, prefix) {
		t.Fatal("stripped harness must keep the byte-exact prefix")
	}
}
