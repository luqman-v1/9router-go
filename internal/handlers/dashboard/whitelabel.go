package dashboard

import (
	"net/url"
	"strings"
)

// appNameSettingKey and appLogoUrlSettingKey are the white-label keys the
// Settings > White-label branding card writes and the sidebar reads. The
// sidebar renders whatever they hold, so the stored shape is kept honest here
// rather than trusted from the client.
const (
	appNameSettingKey    = "appName"
	appLogoUrlSettingKey = "appLogoUrl"
)

// maxAppNameRunes caps the stored product name. The sidebar renders it inside a
// fixed-width brand block next to a 36px logo tile, so a longer string would
// either wrap the header or push the version line out of the aside.
const maxAppNameRunes = 60

// maxAppLogoURLLen caps the stored logo URL. A logo is a URL, not an asset:
// the sidebar points an <img> at it, so the value has to stay short enough to
// sit in a settings row and a JSON response.
const maxAppLogoURLLen = 500

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
	logo, ok := value.(string)
	if !ok {
		return "", false
	}
	logo = strings.TrimSpace(logo)
	if len(logo) > maxAppLogoURLLen {
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

// applyWhitelabelUpdate normalizes the two branding keys in
// HandleUpdateSettings' payload.
//
// An unparsable value is stored as an explicit null rather than deleted:
// UpdateSettingsRaw merges into the stored row, so dropping the map entry would
// leave the previous name or logo in place and the sidebar would keep showing
// the branding the operator just tried to clear.
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
}
