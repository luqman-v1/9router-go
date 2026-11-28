package app

import (
	"context"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/log"
)

// liveCatalogPrewarmInterval keeps the /v1/models live cache warm. The cache
// TTL is 5 minutes; refreshing at 4 minutes means an expired catalog is a
// rare event even if no client polls the endpoint. The first pass runs
// immediately on boot (in the goroutine, so startup never waits on a slow
// upstream feed).
const liveCatalogPrewarmInterval = 4 * time.Minute

// startLiveCatalogPrewarm warms each active connection's live catalog before
// the first /v1/models request and keeps it warm afterwards.
//
// It is deliberately bounded: one worker, one pass per interval, each fetch
// with resolveLiveCatalog's own per-fetch timeout. It is a no-op without a
// repository or chat handler, and it is bound to the shutdown context, so it
// stops with the other background loops.
func startLiveCatalogPrewarm(ctx context.Context, repo *db.Repo, chatH *chat.ChatHandler) {
	if repo == nil || chatH == nil {
		return
	}
	go func() {
		chatH.PrewarmLiveCatalogs(ctx)
		ticker := time.NewTicker(liveCatalogPrewarmInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				chatH.PrewarmLiveCatalogs(ctx)
			}
		}
	}()
	log.Info("models", "live catalog prewarm started", "interval", liveCatalogPrewarmInterval.String())
}
