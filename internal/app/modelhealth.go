package app

import (
	"context"
	"strings"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/handlers/media"
	"9router/proxy/internal/modelhealth"
)

// startModelHealthScheduler starts the automatic model-health loop.
//
// The loop probes the models an operator has already looked at, counts
// consecutive failures, and disables a model that has failed often enough to
// be considered dead — then re-enables it once a probe succeeds again, when
// auto-enable is on.
//
// It is deliberately narrow. It probes only models that already carry a
// verdict, so a fresh install with a hundred-model provider costs nothing
// until the operator asks about a model. It disables only on repeated
// failure, so one transient 502 does not take a working model off the
// listing. And it re-enables only what it disabled itself, never what an
// operator switched off by hand.
//
// The configuration is read from the settings row on EVERY pass, never
// cached, so a change saved in the dashboard takes effect on the next pass
// without a restart — the same property the auto-ping loop has.
//
// The loop is a no-op without a repository, a chat handler or a media
// handler: there is nothing to probe without a lane to probe through. It is
// bound to the shutdown context, so it stops with the other background loops.
func startModelHealthScheduler(ctx context.Context, repo *db.Repo, chatH *chat.ChatHandler) {
	if repo == nil || chatH == nil {
		return
	}
	// The probe travels the same lane the dashboard's Test button uses, so a
	// second handler is built here rather than reaching into the router's.
	// It is cheap — the constructor only clones a transport — and it keeps
	// this loop independent of the route table it does not own.
	mediaH := media.NewMediaHandler(repo, nil, chatH)
	store := modelhealth.RepoStore{Repo: repo}
	loop := modelhealth.NewLoop(
		store,
		probeFuncFromHandlers(mediaH),
		candidateListerFromRepo(repo),
		func() modelhealth.Config {
			raw, err := repo.GetSettingsRaw()
			if err != nil {
				return modelhealth.Default()
			}
			return modelhealth.ParseConfig(raw["modelHealth"])
		},
	)
	// The dashboard's disabled-models write reports the operator's own
	// enable/disable decisions to this loop, so it needs a handle on it. The
	// router builds the dashboard handler and cannot be threaded the loop
	// through three call sites and every mounting test, so the loop publishes
	// itself here instead.
	modelhealth.PublishLoop(loop)
	go loop.Run(ctx)
}

// probeFuncFromHandlers builds the loop's ProbeFunc.
//
// The probe travels the same endpoint the dashboard's Test button uses, so the
// verdict is the one a real client would see rather than a private copy of the
// forwarding logic. The media handler owns that dispatch because it already
// knows how to reach every kind's lane.
func probeFuncFromHandlers(mediaH *media.MediaHandler) modelhealth.ProbeFunc {
	return func(ctx context.Context, provider, model string) modelhealth.Verdict {
		res := mediaH.ProbeModelForHealth(ctx, provider+"/"+model)
		return modelhealth.Verdict{
			OK:      res.OK,
			Error:   res.Error,
			Blocked: res.Blocked,
			ResetAt: res.ResetAt,
		}
	}
}

// candidateListerFromRepo returns the models a pass should consider.
//
// A candidate is a custom model row, addressed by the provider alias it is
// stored against — which for a compatible node is the node id, the same key
// the disabled list and the dashboard use. Registry models are not listed:
// the loop watches what an operator has imported or pinned, not the whole
// static catalogue, because probing a catalogue nobody asked about is exactly
// the cost this loop exists to avoid.
func candidateListerFromRepo(repo *db.Repo) func() []modelhealth.Candidate {
	return func() []modelhealth.Candidate {
		customs, err := repo.GetCustomModels()
		if err != nil {
			return nil
		}
		out := make([]modelhealth.Candidate, 0, len(customs))
		for _, cm := range customs {
			if cm == nil || cm.ID == "" || cm.ProviderAlias == "" {
				continue
			}
			if !isLLMCustomType(cm.Type) {
				continue
			}
			out = append(out, modelhealth.Candidate{Provider: cm.ProviderAlias, Model: cm.ID})
		}
		return out
	}
}

// isLLMCustomType keeps image/audio/video rows out of a chat probe, matching
// how the model listing decides what is an LLM entry.
func isLLMCustomType(modelType string) bool {
	switch strings.TrimSpace(modelType) {
	case "", "llm", "chat", "completion":
		return true
	}
	return false
}
