package dashboard

import "testing"

func TestNormalizeAppName(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  string
		ok    bool
	}{
		{name: "a plain name passes through", input: "Acme Gateway", want: "Acme Gateway", ok: true},
		{name: "surrounding space is trimmed", input: "  Acme Gateway  ", want: "Acme Gateway", ok: true},
		{name: "an empty name clears the branding", input: "   ", want: "", ok: true},
		{name: "a number is not a name", input: 42, ok: false},
		{name: "a list is not a name", input: []any{"a"}, ok: false},
		{name: "a name over the rune cap is truncated", input: string(make([]rune, maxAppNameRunes+10)), want: string(make([]rune, maxAppNameRunes)), ok: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := normalizeAppName(tt.input)
			if ok != tt.ok {
				t.Fatalf("normalizeAppName() ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Errorf("normalizeAppName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeAppLogoUrl(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  string
		ok    bool
	}{
		{name: "an https URL passes through", input: "https://example.com/logo.png", want: "https://example.com/logo.png", ok: true},
		{name: "a local absolute path passes through", input: "/logo.png", want: "/logo.png", ok: true},
		{name: "an empty value clears the logo", input: "  ", want: "", ok: true},
		{name: "a protocol-relative URL is refused", input: "//example.com/logo.png", ok: false},
		{name: "http is refused", input: "http://example.com/logo.png", ok: false},
		{name: "a javascript scheme is refused", input: "javascript:alert(1)", ok: false},
		{name: "a data URL is refused", input: "data:image/svg+xml;base64,AAAA", ok: false},
		{name: "a URL over the length cap is refused", input: "https://example.com/" + string(make([]rune, maxAppLogoURLLen)), ok: false},
		{name: "a number is not a URL", input: 7, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := normalizeAppLogoUrl(tt.input)
			if ok != tt.ok {
				t.Fatalf("normalizeAppLogoUrl() ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Errorf("normalizeAppLogoUrl() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestApplyWhitelabelUpdateClearsOnBadPayload pins that an unusable value
// clears the key rather than leaving the previous branding in place:
// UpdateSettingsRaw merges into the stored row, so dropping the map entry
// would keep showing the name or logo the operator just tried to remove.
func TestApplyWhitelabelUpdateClearsOnBadPayload(t *testing.T) {
	updates := map[string]any{
		appNameSettingKey:    42,
		appLogoUrlSettingKey: "http://insecure.example/logo.png",
	}
	applyWhitelabelUpdate(updates)
	if updates[appNameSettingKey] != nil {
		t.Errorf("appName = %v, want nil", updates[appNameSettingKey])
	}
	if updates[appLogoUrlSettingKey] != nil {
		t.Errorf("appLogoUrl = %v, want nil", updates[appLogoUrlSettingKey])
	}
}

// TestApplyWhitelabelUpdateKeepsGoodPayload pins the normal path.
func TestApplyWhitelabelUpdateKeepsGoodPayload(t *testing.T) {
	updates := map[string]any{
		appNameSettingKey:    "  Acme Gateway  ",
		appLogoUrlSettingKey: "https://example.com/logo.png",
	}
	applyWhitelabelUpdate(updates)
	if updates[appNameSettingKey] != "Acme Gateway" {
		t.Errorf("appName = %v, want %q", updates[appNameSettingKey], "Acme Gateway")
	}
	if updates[appLogoUrlSettingKey] != "https://example.com/logo.png" {
		t.Errorf("appLogoUrl = %v, want %q", updates[appLogoUrlSettingKey], "https://example.com/logo.png")
	}
}

// TestSanitizeWhitelabelDropsUnusableStoredValues pins the read path: a row
// written by an older build, or by hand, must not reach the dashboard as a
// value the client has to reject again.
func TestSanitizeWhitelabelDropsUnusableStoredValues(t *testing.T) {
	settings := map[string]any{
		appNameSettingKey:    "   ",
		appLogoUrlSettingKey: "//evil.example/logo.png",
		"unrelated":          "kept",
	}
	sanitizeWhitelabel(settings)
	if _, ok := settings[appNameSettingKey]; ok {
		t.Error("a blank appName survived sanitize")
	}
	if _, ok := settings[appLogoUrlSettingKey]; ok {
		t.Error("a protocol-relative appLogoUrl survived sanitize")
	}
	if settings["unrelated"] != "kept" {
		t.Error("sanitize dropped an unrelated key")
	}
}

// TestSanitizeWhitelabelKeepsUsableStoredValues pins the normal read path.
func TestSanitizeWhitelabelKeepsUsableStoredValues(t *testing.T) {
	settings := map[string]any{
		appNameSettingKey:    "  Acme Gateway  ",
		appLogoUrlSettingKey: "/logo.png",
	}
	sanitizeWhitelabel(settings)
	if settings[appNameSettingKey] != "Acme Gateway" {
		t.Errorf("appName = %v, want %q", settings[appNameSettingKey], "Acme Gateway")
	}
	if settings[appLogoUrlSettingKey] != "/logo.png" {
		t.Errorf("appLogoUrl = %v, want %q", settings[appLogoUrlSettingKey], "/logo.png")
	}
}
