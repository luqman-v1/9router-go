package providers

import "testing"

func TestKiroEffectiveProfileARN(t *testing.T) {
	const explicit = "arn:aws:codewhisperer:eu-west-1:1:profile/own"
	tests := []struct {
		name       string
		authMethod string
		profileARN string
		want       string
	}{
		{
			name:       "builder-id without arn uses the builder default",
			authMethod: "builder-id",
			want:       KiroDefaultProfileARNBuilderID,
		},
		{
			name:       "empty auth method falls back to the builder default",
			authMethod: "",
			want:       KiroDefaultProfileARNBuilderID,
		},
		{
			name:       "google sign-in uses the social default",
			authMethod: "google",
			want:       KiroDefaultProfileARNSocial,
		},
		{
			name:       "github sign-in uses the social default",
			authMethod: "github",
			want:       KiroDefaultProfileARNSocial,
		},
		{
			name:       "explicit arn always wins",
			authMethod: "builder-id",
			profileARN: explicit,
			want:       explicit,
		},
		{
			name:       "idc without arn sends nothing",
			authMethod: "idc",
			want:       "",
		},
		{
			name:       "external_idp without arn sends nothing",
			authMethod: "external_idp",
			want:       "",
		},
		{
			name:       "api_key without arn sends nothing",
			authMethod: "api_key",
			want:       "",
		},
		{
			name:       "explicit arn is kept for account-bound auth",
			authMethod: "idc",
			profileARN: explicit,
			want:       explicit,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := KiroEffectiveProfileARN(tt.authMethod, tt.profileARN); got != tt.want {
				t.Errorf("KiroEffectiveProfileARN(%q, %q) = %q, want %q", tt.authMethod, tt.profileARN, got, tt.want)
			}
		})
	}
}
