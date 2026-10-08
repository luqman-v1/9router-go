package executor

import "testing"

// The model id is read straight out of a client request body, so a body that
// spells the key with different casing used to work and must keep working:
// museModel returning "" fails ForwardMuse outright.
func TestMuseModel_LenientAboutClientBodies(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "canonical key", body: `{"model":"muse/muse-spark-1.3"}`, want: "muse-spark-1.3"},
		{name: "capitalized key", body: `{"Model":"muse/muse-spark-1.3"}`, want: "muse-spark-1.3"},
		{name: "upper-case key", body: `{"MODEL":"muse/muse-spark-1.3"}`, want: "muse-spark-1.3"},
		{name: "repeated member name", body: `{"model":"muse/a","model":"muse/muse-spark-1.3"}`, want: "muse-spark-1.3"},
		{name: "no model key", body: `{"stream":true}`, want: ""},
		{name: "not an object", body: `"muse/muse-spark-1.3"`, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := museModel([]byte(tt.body)); got != tt.want {
				t.Fatalf("museModel(%s) = %q, want %q", tt.body, got, tt.want)
			}
		})
	}
}
