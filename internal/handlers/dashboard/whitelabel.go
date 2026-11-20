package dashboard

import (
	"net/url"
	"strings"
)

// White-label branding keys written by Settings > White-label branding card and
// read by the sidebar / login screen / tray companion.
const (
	appNameSettingKey        = "appName"
	appLogoUrlSettingKey     = "appLogoUrl"
	appDescriptionSettingKey = "appDescription"
	appTaglineSettingKey     = "appTagline"
	loginLogoSettingKey      = "loginLogo"
	trayAppNameSettingKey    = "trayAppName"
	trayIconSettingKey       = "trayIcon"
)

// maxAppNameRunes caps the stored product name.
const maxAppNameRunes = 60

// maxAppLogoURLLen caps the stored logo URL.
const maxAppLogoURLLen = 500

// maxAppDescriptionRunes caps the stored product description.
const maxAppDescriptionRunes = 200

// maxAppTaglineRunes caps the stored tagline.
const maxAppTaglineRunes = 80

// maxLoginLogoURLLen caps the stored login logo URL.
const maxLoginLogoURLLen = 500

// maxTrayAppNameRunes caps the stored tray app name.
const maxTrayAppNameRunes = 60

// maxTrayIconURLLen caps the stored tray icon URL.
const maxTrayIconURLLen = 500

// normalizeAppName coerces a client payload into the stored product name:
// trimmed, and capped at maxAppNameRunes. The second result reports whether the
// payload was a string at all, so a number or a list clears the setting instead
// of being stored as something the sidebar would have to defend against.
func normalizeAppName(value any) (string, bool) {
	name, ok := value.(string)
	if !ok {
		return "", false
	}
	name = strings.TrimSpace(name)
	if len([]rune(name)) > maxAppNameRunes {
		name = string([]rune(name)[:maxAppNameRunes])
	}
	return name, true
}

// normalizeAppLogoUrl coerces a client payload into the stored logo URL:
// trimmed, capped, and restricted to a form the sidebar can actually render.
//
// An https:// URL is the normal case. A local absolute path ("/logo.png") is
// accepted too, because an operator serving the dashboard behind a reverse
// proxy may prefer to host the asset themselves; the scheme-less form is what
// makes that distinguishable from a remote URL. A protocol-relative "//host"
// is refused: it silently re-points the request at whatever host the operator
// typed, which is not the asset they meant to serve.
//
// An empty value clears the logo, which is how the default favicon comes back.
func normalizeAppLogoUrl(value any) (string, bool) {
	return normalizeAppLogoUrlWithMax(value, maxAppLogoURLLen)
}

// normalizeAppDescription coerces a client payload into a stored description.
func normalizeAppDescription(value any) (string, bool) {
	desc, ok := value.(string)
	if !ok {
		return "", false
	}
	desc = strings.TrimSpace(desc)
	if len([]rune(desc)) > maxAppDescriptionRunes {
		desc = string([]rune(desc)[:maxAppDescriptionRunes])
	}
	return desc, true
}

// normalizeAppTagline coerces a client payload into a stored tagline.
func normalizeAppTagline(value any) (string, bool) {
	tag, ok := value.(string)
	if !ok {
		return "", false
	}
	tag = strings.TrimSpace(tag)
	if len([]rune(tag)) > maxAppTaglineRunes {
		tag = string([]rune(tag)[:maxAppTaglineRunes])
	}
	return tag, true
}

// normalizeLoginLogoUrl coerces a client payload into a stored login logo URL.
func normalizeLoginLogoUrl(value any) (string, bool) {
	return normalizeAppLogoUrlWithMax(value, maxLoginLogoURLLen)
}

func normalizeTrayAppName(value any) (string, bool) {
	name, ok := value.(string)
	if !ok {
		return "", false
	}
	name = strings.TrimSpace(name)
	if len([]rune(name)) > maxTrayAppNameRunes {
		name = string([]rune(name)[:maxTrayAppNameRunes])
	}
	return name, true
}

func normalizeTrayIconUrl(value any) (string, bool) {
	return normalizeAppLogoUrlWithMax(value, maxTrayIconURLLen)
}

func normalizeAppLogoUrlWithMax(value any, maxLen int) (string, bool) {
	logo, ok := value.(string)
	if !ok {
		return "", false
	}
	logo = strings.TrimSpace(logo)
	if len(logo) > maxLen {
		return "", false
	}
	if logo == "" {
		return "", true
	}
	parsed, err := url.Parse(logo)
	if err != nil {
		return "", false
	}
	isLocalPath := parsed.Scheme == "" && strings.HasPrefix(logo, "/") && !strings.HasPrefix(logo, "//")
	if parsed.Scheme != "https" && !isLocalPath {
		return "", false
	}
	return logo, true
}

// applyWhitelabelUpdate normalizes the white-label branding keys in
// HandleUpdateSettings' payload.
//
// An unparsable value is stored as an explicit null rather than deleted:
// UpdateSettingsRaw merges into the stored row, so dropping the map entry
// would leave the previous name or logo in place and the sidebar would keep
// showing the branding the operator just tried to clear.
func applyWhitelabelUpdate(updates map[string]any) {
	if value, ok := updates[appNameSettingKey]; ok {
		name, valid := normalizeAppName(value)
		if !valid {
			updates[appNameSettingKey] = nil
		} else {
			updates[appNameSettingKey] = name
		}
	}
	if value, ok := updates[appLogoUrlSettingKey]; ok {
		logo, valid := normalizeAppLogoUrl(value)
		if !valid {
			updates[appLogoUrlSettingKey] = nil
		} else {
			updates[appLogoUrlSettingKey] = logo
		}
	}
	if value, ok := updates[appDescriptionSettingKey]; ok {
		desc, valid := normalizeAppDescription(value)
		if !valid {
			updates[appDescriptionSettingKey] = nil
		} else {
			updates[appDescriptionSettingKey] = desc
		}
	}
	if value, ok := updates[appTaglineSettingKey]; ok {
		tag, valid := normalizeAppTagline(value)
		if !valid {
			updates[appTaglineSettingKey] = nil
		} else {
			updates[appTaglineSettingKey] = tag
		}
	}
	if value, ok := updates[loginLogoSettingKey]; ok {
		logo, valid := normalizeLoginLogoUrl(value)
		if !valid {
			updates[loginLogoSettingKey] = nil
		} else {
			updates[loginLogoSettingKey] = logo
		}
	}
	if value, ok := updates[trayAppNameSettingKey]; ok {
		name, valid := normalizeTrayAppName(value)
		if !valid {
			updates[trayAppNameSettingKey] = nil
		} else {
			updates[trayAppNameSettingKey] = name
		}
	}
	if value, ok := updates[trayIconSettingKey]; ok {
		icon, valid := normalizeTrayIconUrl(value)
		if !valid {
			updates[trayIconSettingKey] = nil
		} else {
			updates[trayIconSettingKey] = icon
		}
	}
}

// sanitizeWhitelabel re-reads the stored values through the same normalizers, so
// a row written by an older build (or by hand) reaches the dashboard in the
// shape the sidebar expects. A value that normalizes to nothing is dropped
// rather than sent as a logo URL the client would have to reject again.
func sanitizeWhitelabel(settings map[string]any) {
	if value, ok := settings[appNameSettingKey]; ok {
		name, valid := normalizeAppName(value)
		if !valid || name == "" {
			delete(settings, appNameSettingKey)
		} else {
			settings[appNameSettingKey] = name
		}
	}
	if value, ok := settings[appLogoUrlSettingKey]; ok {
		logo, valid := normalizeAppLogoUrl(value)
		if !valid || logo == "" {
			delete(settings, appLogoUrlSettingKey)
		} else {
			settings[appLogoUrlSettingKey] = logo
		}
	}
	if value, ok := settings[appDescriptionSettingKey]; ok {
		desc, valid := normalizeAppDescription(value)
		if !valid || desc == "" {
			delete(settings, appDescriptionSettingKey)
		} else {
			settings[appDescriptionSettingKey] = desc
		}
	}
	if value, ok := settings[appTaglineSettingKey]; ok {
		tag, valid := normalizeAppTagline(value)
		if !valid || tag == "" {
			delete(settings, appTaglineSettingKey)
		} else {
			settings[appTaglineSettingKey] = tag
		}
	}
	if value, ok := settings[loginLogoSettingKey]; ok {
		logo, valid := normalizeLoginLogoUrl(value)
		if !valid || logo == "" {
			delete(settings, loginLogoSettingKey)
		} else {
			settings[loginLogoSettingKey] = logo
		}
	}
	if value, ok := settings[trayAppNameSettingKey]; ok {
		name, valid := normalizeTrayAppName(value)
		if !valid || name == "" {
			delete(settings, trayAppNameSettingKey)
		} else {
			settings[trayAppNameSettingKey] = name
		}
	}
	if value, ok := settings[trayIconSettingKey]; ok {
		icon, valid := normalizeTrayIconUrl(value)
		if !valid || icon == "" {
			delete(settings, trayIconSettingKey)
		} else {
			settings[trayIconSettingKey] = icon
		}
	}
}
