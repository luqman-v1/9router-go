package app

import (
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"go.uber.org/fx"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers"
	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/middleware"
)

// HandlersModule provides TokenSaverConfig and sets up the server router.
var HandlersModule = fx.Module("handlers",
	fx.Provide(
		ProvideTokenSaverConfig,
		ProvideRouter,
		ProvideHTTPHandler,
	),
)

// ProvideTokenSaverConfig initializes TokenSaverConfig from CLI flags and database settings.
func ProvideTokenSaverConfig(repo *db.Repo, params CLIParams) *handlers.TokenSaverConfig {
	ts := handlers.NewTokenSaverConfig(params.RTK, params.Caveman, params.Ponytail)
	if params.ADHDSet {
		ts.SetADHD(params.ADHD)
	}
	if settings, err := repo.GetSettings(); err == nil && settings != nil {
		ts.UpdateFromSettings(settings)
		rtk := settings.RTKEnabled
		if params.RTKSet {
			rtk = params.RTK
		}
		caveman := settings.CavemanEnabled
		if params.CavemanSet {
			caveman = params.Caveman
		}
		ponytail := settings.PonytailEnabled
		if params.PonytailSet {
			ponytail = params.Ponytail
		}
		adhd := settings.ADHDEnabled
		if params.ADHDSet {
			adhd = params.ADHD
		}
		ts.SetAll(rtk, caveman, ponytail)
		ts.SetCaveman(caveman, settings.CavemanLevel)
		ts.SetPonytail(ponytail, settings.PonytailLevel)
		ts.SetADHD(adhd, settings.ADHDLevel)
	}
	ts.SetInjectionGuard(!params.NoInjectionGuard)
	log.Printf("[config] token savers — rtk=%v caveman=%v (%s) ponytail=%v (%s) adhd=%v (%s)",
		ts.RTKEnabled(), ts.CavemanEnabled(), ts.CavemanLevel(), ts.PonytailEnabled(), ts.PonytailLevel(), ts.ADHDEnabled(), ts.ADHDLevel())
	return ts
}

// ProvideRouter sets up the Chi router with middleware and routes.
//
// It returns the router and the engine's chat handler — the same instance the
// routes are mounted on, not a second one. The auto-ping scheduler needs a chat
// handler, and a second one would be a second PersistentStore over the same
// SQLite file with its own in-memory LRU: the dashboard would report zero
// entries while the engine served hits, and clearing entries from the dashboard
// would leave the engine still serving them (see handlers.SetupRoutes).
func ProvideRouter(repo *db.Repo, ts *handlers.TokenSaverConfig) (*chi.Mux, *chat.ChatHandler) {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	// Body limits are path-aware: JSON APIs keep the 10MB ceiling, the media
	// endpoints that receive multipart video/audio bodies get their own larger
	// one (middleware.MediaBodyLimitRules). One middleware decides the ceiling
	// per request — stacking a second MaxBody above this one would leave the
	// smaller limit still cutting first (#125).
	r.Use(middleware.MaxBodyByPath(middleware.DefaultMaxBodySize, middleware.MediaBodyLimitRules()))
	r.Use(chiMiddleware.Recoverer)
	r.Use(middleware.RequestLogger)

	engineChatH := handlers.SetupServerRouter(r, repo, ts)
	return r, engineChatH
}

// ProvideHTTPHandler provides http.Handler from *chi.Mux.
func ProvideHTTPHandler(r *chi.Mux) http.Handler {
	return r
}
