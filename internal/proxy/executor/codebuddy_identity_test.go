package executor

import (
	"strings"
	"testing"
)

// TestCodebuddyIdentityStablePerCredential keeps the core anti-ban invariant:
// the same credential yields the same identity, and different credentials never
// share one.
func TestCodebuddyIdentityStablePerCredential(t *testing.T) {
	a := codebuddyCredential{APIKey: "sk-cn-a", AccessToken: "sk-cn-a", ID: "conn-1"}
	b := codebuddyCredential{APIKey: "sk-cn-b", AccessToken: "sk-cn-b", ID: "conn-2"}

	ia1 := getCodebuddyIdentity(a)
	ia2 := getCodebuddyIdentity(a)
	ib := getCodebuddyIdentity(b)

	if ia1.MachineID != ia2.MachineID {
		t.Fatalf("machineId not stable for same credential: %q vs %q", ia1.MachineID, ia2.MachineID)
	}
	if ia1.Qimei36 != ia2.Qimei36 {
		t.Fatalf("qimei36 not stable for same credential: %q vs %q", ia1.Qimei36, ia2.Qimei36)
	}
	if ia1.MachineID == ib.MachineID {
		t.Fatalf("machineId shared across credentials — cross-contamination: %q", ia1.MachineID)
	}
	if ia1.Qimei36 == ib.Qimei36 {
		t.Fatalf("qimei36 shared across credentials — cross-contamination: %q", ia1.Qimei36)
	}
}

// TestCodebuddyIdentityFormats pins the exact shapes Tencent's fingerprint
// parser expects: qimei36 is 36 lowercase hex, machineId/sessionId are UUIDs,
// deviceId is 16 hex, hostId is 32 hex.
func TestCodebuddyIdentityFormats(t *testing.T) {
	id := getCodebuddyIdentity(codebuddyCredential{APIKey: "sk-format"})

	if len(id.Qimei36) != 36 || strings.Trim(id.Qimei36, "0123456789abcdef") != "" {
		t.Errorf("qimei36 = %q, want 36 lowercase hex chars", id.Qimei36)
	}
	if len(id.MachineID) != 36 || id.MachineID[8] != '-' || id.MachineID[13] != '-' {
		t.Errorf("machineId = %q, want UUID shape", id.MachineID)
	}
	if len(id.DeviceID) != 16 || strings.Trim(id.DeviceID, "0123456789abcdef") != "" {
		t.Errorf("deviceId = %q, want 16 hex chars", id.DeviceID)
	}
	if len(id.HostID) != 32 || strings.Trim(id.HostID, "0123456789abcdef") != "" {
		t.Errorf("hostId = %q, want 32 hex chars", id.HostID)
	}
	if id.CLIVersion != "2.133.1" {
		t.Errorf("cliVersion = %q, want 2.133.1", id.CLIVersion)
	}
}

// TestCodebuddyIdentityNoSecretLeak asserts the derived identity never embeds
// the raw credential.
func TestCodebuddyIdentityNoSecretLeak(t *testing.T) {
	secret := "sk-cn-abcdef0123456789<<supersecret>>"
	id := getCodebuddyIdentity(codebuddyCredential{APIKey: secret})
	for _, v := range []string{id.Qimei36, id.MachineID, id.SessionID, id.DeviceID, id.HostID, id.VCS.VCSRepo, id.VCS.VCSRevID} {
		if strings.Contains(v, secret) {
			t.Fatalf("identity leaks the credential: %q in %q", secret, v)
		}
	}
}
