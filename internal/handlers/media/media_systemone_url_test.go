package media

import "testing"

// Port of the upstream buildSystemoneUrl vitest table: a saved host joins the
// registry path without doubling a "/v1" or "/v1/systemone" it already
// includes.
func TestBuildSystemoneURL(t *testing.T) {
	const localURL = "http://localhost:11434/v1/systemone"
	const localPath = "/v1/systemone"

	cases := []struct {
		override, cfgURL, cfgPath, want string
	}{
		{"", localURL, localPath, localURL},
		{"http://localhost:11434", localURL, localPath, "http://localhost:11434/v1/systemone"},
		{"http://localhost:11434/", localURL, localPath, "http://localhost:11434/v1/systemone"},
		{"http://localhost:11434/v1", localURL, localPath, "http://localhost:11434/v1/systemone"},
		{"http://localhost:11434/v1/", localURL, localPath, "http://localhost:11434/v1/systemone"},
		{"http://localhost:11434/v1/systemone", localURL, localPath, "http://localhost:11434/v1/systemone"},
		{"http://localhost:11434/v1/systemone/", localURL, localPath, "http://localhost:11434/v1/systemone"},
		// No registry path: the lane's "/systemone" suffix, unless already named.
		{"https://proxy.example/custom", "https://x/y", "", "https://proxy.example/custom/systemone"},
		{"https://proxy.example/custom/systemone", "https://x/y", "", "https://proxy.example/custom/systemone"},
	}
	for _, tc := range cases {
		if got := buildSystemoneURL(tc.override, tc.cfgURL, tc.cfgPath); got != tc.want {
			t.Errorf("buildSystemoneURL(%q, %q, %q) = %q, want %q", tc.override, tc.cfgURL, tc.cfgPath, got, tc.want)
		}
	}
}
