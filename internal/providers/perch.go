package providers

// Perch (perchai.app) has no public API endpoint: the official CLI and the web
// client sign in through Perch's own Supabase project and call the model-call
// proxy on app.perchai.app. Everything here is taken from those clients'
// on-the-wire protocol (the perchai-cli bundle, cross-checked against the
// open-source adapters ErfanBagheri404/PerchAI2API, zoeee1125/perchai2api and
// YoungJurry/pi-perchai), not from a published developer contract — Perch
// documents direct API access as not included in any plan, and the gateway
// surfaces that as perch_surface_required rather than working around it.

const (
	// PerchAppURL is the origin the CLI surface lives on.
	PerchAppURL = "https://app.perchai.app"
	// PerchSupabaseURL is the Supabase project Perch auth runs on. It is
	// published by GET /api/perch-terminal/cli-auth/config; pinning the value
	// here keeps credentials from being sent to an overridden endpoint
	// (same guard as pi-perchai's createAuth).
	PerchSupabaseURL = "https://zlfuvsfjtgsdtqcaykia.supabase.co"
	// PerchOAuthCallback is the loopback URI the Supabase PKCE flow uses.
	// Supabase validates redirect_to against the project's allow list, so the
	// value must stay exactly what the official CLI registers.
	PerchOAuthCallback = "http://127.0.0.1:51121/callback"
	// PerchCLIUserAgent is the CLI version the adapters header requests as.
	PerchCLIUserAgent = "perchai-cli/2.4.125"
	// PerchTurnTicketPath mints the signed turn ticket a model-call carries.
	PerchTurnTicketPath = "/api/perch-terminal/turn-ticket"
)

// Perch refreshes its Supabase session 90 seconds before the access token
// expires, the same margin the official CLI uses (EXPIRY_MARGIN_S).
const PerchExpiryMarginSeconds = 90
