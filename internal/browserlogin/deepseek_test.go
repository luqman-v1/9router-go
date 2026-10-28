package browserlogin

import "testing"

// The site stores the token as a JSON OBJECT, not as a bare string: the
// value in localStorage is `{"value":"…","__version":"0"}`. A reader that
// assumed a bare string would read nothing and report "not signed in" for a
// browser that is signed in.
//
// This is the shape the live page was measured to hold, and it is the shape
// a signed-in profile's leveldb holds.
func TestDeepSeekUserTokenReadsEveryStoredShape(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "the site's own object form",
			raw:  `{"value":"tfWDPBARqgRC/+KfPOmhLFeW78PdKOKWiI3ro8MaP9hvRd1IoKZrMSd2L5jEaUZN","__version":"0"}`,
			want: "tfWDPBARqgRC/+KfPOmhLFeW78PdKOKWiI3ro8MaP9hvRd1IoKZrMSd2L5jEaUZN",
		},
		{
			name: "an unsigned-in page holds a null",
			raw:  `{"value":null,"__version":"0"}`,
			want: "",
		},
		{
			name: "a bare JSON string",
			raw:  `"the-token"`,
			want: "the-token",
		},
		{
			name: "a plain value",
			raw:  "the-token",
			want: "the-token",
		},
		{
			name: "a padded value",
			raw:  "  the-token\n",
			want: "the-token",
		},
		{
			name: "an empty value",
			raw:  "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DeepSeekUserTokenFromLocalStorage(tt.raw); got != tt.want {
				t.Errorf("token = %q, want %q", got, tt.want)
			}
		})
	}
}
