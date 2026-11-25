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

func TestNormalizeAppDescription(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  string
		ok    bool
	}{
		{name: "a plain description passes through", input: "A short description", want: "A short description", ok: true},
		{name: "surrounding space is trimmed", input: "  description  ", want: "description", ok: true},
		{name: "empty string clears the description", input: "  ", want: "", ok: true},
		{name: "a number is not a description", input: 42, ok: false},
		{name: "a list is not a description", input: []any{"a"}, ok: false},
		{name: "over the rune cap is truncated", input: string(make([]rune, maxAppDescriptionRunes+10)), want: string(make([]rune, maxAppDescriptionRunes)), ok: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := normalizeAppDescription(tt.input)
			if ok != tt.ok {
				t.Fatalf("normalizeAppDescription() ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Errorf("normalizeAppDescription() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeAppTagline(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  string
		ok    bool
	}{
		{name: "a plain tagline passes through", input: "Your gateway, your rules", want: "Your gateway, your rules", ok: true},
		{name: "surrounding space is trimmed", input: "  tagline  ", want: "tagline", ok: true},
		{name: "empty string clears the tagline", input: "  ", want: "", ok: true},
		{name: "a number is not a tagline", input: 42, ok: false},
		{name: "over the rune cap is truncated", input: string(make([]rune, maxAppTaglineRunes+10)), want: string(make([]rune, maxAppTaglineRunes)), ok: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := normalizeAppTagline(tt.input)
			if ok != tt.ok {
				t.Fatalf("normalizeAppTagline() ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Errorf("normalizeAppTagline() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeLoginLogoUrl(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  string
		ok    bool
	}{
		{name: "an https URL passes through", input: "https://example.com/login.png", want: "https://example.com/login.png", ok: true},
		{name: "a local path passes through", input: "/login-logo.png", want: "/login-logo.png", ok: true},
		{name: "an empty value clears it", input: "  ", want: "", ok: true},
		{name: "http is refused", input: "http://example.com/login.png", ok: false},
		{name: "protocol-relative is refused", input: "//example.com/login.png", ok: false},
		{name: "javascript scheme is refused", input: "javascript:alert(1)", ok: false},
		{name: "over the length cap is refused", input: "https://example.com/" + string(make([]rune, maxLoginLogoURLLen)), ok: false},
		{name: "a number is not a URL", input: 7, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := normalizeLoginLogoUrl(tt.input)
			if ok != tt.ok {
				t.Fatalf("normalizeLoginLogoUrl() ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Errorf("normalizeLoginLogoUrl() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeTrayAppName(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  string
		ok    bool
	}{
		{name: "a plain name passes through", input: "9router-go", want: "9router-go", ok: true},
		{name: "surrounding space is trimmed", input: "  My Tray  ", want: "My Tray", ok: true},
		{name: "empty string clears the name", input: "  ", want: "", ok: true},
		{name: "a number is not a name", input: 42, ok: false},
		{name: "over the rune cap is truncated", input: string(make([]rune, maxTrayAppNameRunes+10)), want: string(make([]rune, maxTrayAppNameRunes)), ok: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := normalizeTrayAppName(tt.input)
			if ok != tt.ok {
				t.Fatalf("normalizeTrayAppName() ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Errorf("normalizeTrayAppName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeTrayIconUrl(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  string
		ok    bool
	}{
		{name: "an https URL passes through", input: "https://example.com/tray.ico", want: "https://example.com/tray.ico", ok: true},
		{name: "a local path passes through", input: "/tray-icon.png", want: "/tray-icon.png", ok: true},
		{name: "an empty value clears it", input: "  ", want: "", ok: true},
		{name: "http is refused", input: "http://example.com/tray.ico", ok: false},
		{name: "protocol-relative is refused", input: "//example.com/tray.ico", ok: false},
		{name: "javascript scheme is refused", input: "javascript:alert(1)", ok: false},
		{name: "over the length cap is refused", input: "https://example.com/" + string(make([]rune, maxTrayIconURLLen)), ok: false},
		{name: "a number is not a URL", input: 7, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := normalizeTrayIconUrl(tt.input)
			if ok != tt.ok {
				t.Fatalf("normalizeTrayIconUrl() ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Errorf("normalizeTrayIconUrl() = %q, want %q", got, tt.want)
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
		appNameSettingKey:        42,
		appLogoUrlSettingKey:     "http://insecure.example/logo.png",
		appDescriptionSettingKey: 42,
		appTaglineSettingKey:     42,
		loginLogoSettingKey:      "http://insecure.example/login.png",
		trayAppNameSettingKey:    42,
		trayIconSettingKey:       "http://insecure.example/tray.ico",
	}
	applyWhitelabelUpdate(updates)
	if updates[appNameSettingKey] != nil {
		t.Errorf("appName = %v, want nil", updates[appNameSettingKey])
	}
	if updates[appLogoUrlSettingKey] != nil {
		t.Errorf("appLogoUrl = %v, want nil", updates[appLogoUrlSettingKey])
	}
	if updates[appDescriptionSettingKey] != nil {
		t.Errorf("appDescription = %v, want nil", updates[appDescriptionSettingKey])
	}
	if updates[appTaglineSettingKey] != nil {
		t.Errorf("appTagline = %v, want nil", updates[appTaglineSettingKey])
	}
	if updates[loginLogoSettingKey] != nil {
		t.Errorf("loginLogo = %v, want nil", updates[loginLogoSettingKey])
	}
	if updates[trayAppNameSettingKey] != nil {
		t.Errorf("trayAppName = %v, want nil", updates[trayAppNameSettingKey])
	}
	if updates[trayIconSettingKey] != nil {
		t.Errorf("trayIcon = %v, want nil", updates[trayIconSettingKey])
	}
}

// TestApplyWhitelabelUpdateKeepsGoodPayload pins the normal path.
func TestApplyWhitelabelUpdateKeepsGoodPayload(t *testing.T) {
	updates := map[string]any{
		appNameSettingKey:        "  Acme Gateway  ",
		appLogoUrlSettingKey:     "https://example.com/logo.png",
		appDescriptionSettingKey: "  A gateway.  ",
		appTaglineSettingKey:     "  Your rules  ",
		loginLogoSettingKey:      "https://example.com/login.png",
		trayAppNameSettingKey:    "  Acme Tray  ",
		trayIconSettingKey:       "https://example.com/tray.ico",
	}
	applyWhitelabelUpdate(updates)
	if updates[appNameSettingKey] != "Acme Gateway" {
		t.Errorf("appName = %v, want %q", updates[appNameSettingKey], "Acme Gateway")
	}
	if updates[appLogoUrlSettingKey] != "https://example.com/logo.png" {
		t.Errorf("appLogoUrl = %v, want %q", updates[appLogoUrlSettingKey], "https://example.com/logo.png")
	}
	if updates[appDescriptionSettingKey] != "A gateway." {
		t.Errorf("appDescription = %v, want %q", updates[appDescriptionSettingKey], "A gateway.")
	}
	if updates[appTaglineSettingKey] != "Your rules" {
		t.Errorf("appTagline = %v, want %q", updates[appTaglineSettingKey], "Your rules")
	}
	if updates[loginLogoSettingKey] != "https://example.com/login.png" {
		t.Errorf("loginLogo = %v, want %q", updates[loginLogoSettingKey], "https://example.com/login.png")
	}
	if updates[trayAppNameSettingKey] != "Acme Tray" {
		t.Errorf("trayAppName = %v, want %q", updates[trayAppNameSettingKey], "Acme Tray")
	}
	if updates[trayIconSettingKey] != "https://example.com/tray.ico" {
		t.Errorf("trayIcon = %v, want %q", updates[trayIconSettingKey], "https://example.com/tray.ico")
	}
}

// TestSanitizeWhitelabelDropsUnusableStoredValues pins the read path: a row
// written by an older build, or by hand, must not reach the dashboard as a
// value the client has to reject again.
func TestSanitizeWhitelabelDropsUnusableStoredValues(t *testing.T) {
	settings := map[string]any{
		appNameSettingKey:        "   ",
		appLogoUrlSettingKey:     "//evil.example/logo.png",
		appDescriptionSettingKey: "   ",
		appTaglineSettingKey:     "   ",
		loginLogoSettingKey:      "//evil.example/login.png",
		trayAppNameSettingKey:    "   ",
		trayIconSettingKey:       "//evil.example/tray.ico",
		"unrelated":              "kept",
	}
	sanitizeWhitelabel(settings)
	if _, ok := settings[appNameSettingKey]; ok {
		t.Error("a blank appName survived sanitize")
	}
	if _, ok := settings[appLogoUrlSettingKey]; ok {
		t.Error("a protocol-relative appLogoUrl survived sanitize")
	}
	if _, ok := settings[appDescriptionSettingKey]; ok {
		t.Error("a blank appDescription survived sanitize")
	}
	if _, ok := settings[appTaglineSettingKey]; ok {
		t.Error("a blank appTagline survived sanitize")
	}
	if _, ok := settings[loginLogoSettingKey]; ok {
		t.Error("a protocol-relative loginLogo survived sanitize")
	}
	if _, ok := settings[trayAppNameSettingKey]; ok {
		t.Error("a blank trayAppName survived sanitize")
	}
	if _, ok := settings[trayIconSettingKey]; ok {
		t.Error("a protocol-relative trayIcon survived sanitize")
	}
	if settings["unrelated"] != "kept" {
		t.Error("sanitize dropped an unrelated key")
	}
}

// TestSanitizeWhitelabelKeepsUsableStoredValues pins the normal read path.
func TestSanitizeWhitelabelKeepsUsableStoredValues(t *testing.T) {
	settings := map[string]any{
		appNameSettingKey:        "  Acme Gateway  ",
		appLogoUrlSettingKey:     "/logo.png",
		appDescriptionSettingKey: "  A gateway.  ",
		appTaglineSettingKey:     "  Your rules  ",
		loginLogoSettingKey:      "/login.png",
		trayAppNameSettingKey:    "  Acme Tray  ",
		trayIconSettingKey:       "/tray.png",
	}
	sanitizeWhitelabel(settings)
	if settings[appNameSettingKey] != "Acme Gateway" {
		t.Errorf("appName = %v, want %q", settings[appNameSettingKey], "Acme Gateway")
	}
	if settings[appLogoUrlSettingKey] != "/logo.png" {
		t.Errorf("appLogoUrl = %v, want %q", settings[appLogoUrlSettingKey], "/logo.png")
	}
	if settings[appDescriptionSettingKey] != "A gateway." {
		t.Errorf("appDescription = %v, want %q", settings[appDescriptionSettingKey], "A gateway.")
	}
	if settings[appTaglineSettingKey] != "Your rules" {
		t.Errorf("appTagline = %v, want %q", settings[appTaglineSettingKey], "Your rules")
	}
	if settings[loginLogoSettingKey] != "/login.png" {
		t.Errorf("loginLogo = %v, want %q", settings[loginLogoSettingKey], "/login.png")
	}
	if settings[trayAppNameSettingKey] != "Acme Tray" {
		t.Errorf("trayAppName = %v, want %q", settings[trayAppNameSettingKey], "Acme Tray")
	}
	if settings[trayIconSettingKey] != "/tray.png" {
		t.Errorf("trayIcon = %v, want %q", settings[trayIconSettingKey], "/tray.png")
	}
}
