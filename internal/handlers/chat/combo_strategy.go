package chat

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"9router/proxy/internal/providers"
)

func NormalizeComboWeights(models []string, rawWeights map[string]int) map[string]int {
	if len(models) == 0 || len(rawWeights) == 0 {
		return nil
	}

	weights := make(map[string]int, len(models))
	hasPositive := false
	allOne := true

	for _, m := range models {
		w, ok := rawWeights[m]
		if !ok {
			w = 1
		} else {
			if w < 0 {
				w = 0
			} else if w > 1000 {
				w = 1000
			}
		}
		weights[m] = w
		if w > 0 {
			hasPositive = true
		}
		if w != 1 {
			allOne = false
		}
	}

	if !hasPositive || allOne {
		return nil
	}
	return weights
}

func (h *ChatHandler) resolveComboWeights(comboName string, explicitWeights ...map[string]int) map[string]int {
	if len(explicitWeights) > 0 && explicitWeights[0] != nil {
		return explicitWeights[0]
	}
	if comboName == "" || h.Repo == nil {
		return nil
	}

	// 1. Settings comboStrategies[comboName].weights
	if settings, err := h.Repo.GetSettings(); err == nil && settings != nil {
		if cs, ok := settings.ComboStrategies[comboName]; ok && len(cs.Weights) > 0 {
			return cs.Weights
		}
	}

	// 2. combo.Models in DB
	if combo, err := h.Repo.GetComboByName(comboName); err == nil && combo != nil {
		if weights := combo.ModelWeights(); len(weights) > 0 {
			return weights
		}
	}

	return nil
}

func advanceWeightedRotation(
	state *comboStickyState,
	models []string,
	weights map[string]int,
	stickyLimit int,
	newTurn bool,
	comboModels []string,
) []string {
	if stickyLimit <= 0 {
		stickyLimit = 1
	}

	seatPool := models
	if len(comboModels) > 0 {
		seatPool = comboModels
	}

	seats := make([]string, 0, len(seatPool))
	for _, m := range seatPool {
		if weights[m] > 0 {
			seats = append(seats, m)
		}
	}
	if len(seats) == 0 {
		out := make([]string, len(models))
		copy(out, models)
		return out
	}

	var sigBuilder strings.Builder
	for i, m := range seats {
		if i > 0 {
			sigBuilder.WriteByte('|')
		}
		sigBuilder.WriteString(m)
	}
	sigBuilder.WriteString("::")
	for i, m := range seats {
		if i > 0 {
			sigBuilder.WriteByte(',')
		}
		sigBuilder.WriteString(strconv.Itoa(weights[m]))
	}
	sig := sigBuilder.String()

	if state.Sig != sig || state.CurrentWeights == nil {
		state.Sig = sig
		state.CurrentWeights = make(map[string]int, len(seats))
		state.LastSeat = ""
		state.ConsecutiveUseCount = 0
		state.ServingRotated = nil
	}

	total := 0
	for _, m := range seats {
		total += weights[m]
	}

	var head string
	if !newTurn && state.LastSeat != "" && slices.Contains(seats, state.LastSeat) {
		if len(state.ServingRotated) > 0 {
			return slices.Clone(state.ServingRotated)
		}
		head = state.LastSeat
	} else if state.LastSeat != "" && state.ConsecutiveUseCount < stickyLimit && slices.Contains(seats, state.LastSeat) {
		head = state.LastSeat
		state.ConsecutiveUseCount++
	} else {
		for _, m := range seats {
			state.CurrentWeights[m] += weights[m]
		}
		head = seats[0]
		for _, m := range seats {
			if state.CurrentWeights[m] > state.CurrentWeights[head] {
				head = m
			}
		}
		state.CurrentWeights[head] -= total
		state.LastSeat = head
		state.ConsecutiveUseCount = 1
	}

	rotated := orderSeatsByWeights(models, weights, state.CurrentWeights, head)
	state.ServingRotated = rotated
	return rotated
}

func orderSeatsByWeights(models []string, weights map[string]int, currentWeights map[string]int, head string) []string {
	score := func(m string) int {
		return currentWeights[m] + weights[m]
	}

	modelIndex := make(map[string]int, len(models))
	for i, m := range models {
		modelIndex[m] = i
	}

	var ranked []string
	var excluded []string
	for _, m := range models {
		if weights[m] > 0 {
			ranked = append(ranked, m)
		} else {
			excluded = append(excluded, m)
		}
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if a == head {
			return true
		}
		if b == head {
			return false
		}
		scoreA := score(a)
		scoreB := score(b)
		if scoreA != scoreB {
			return scoreA > scoreB
		}
		return modelIndex[a] < modelIndex[b]
	})

	result := make([]string, 0, len(models))
	result = append(result, ranked...)
	result = append(result, excluded...)
	return result
}

// ApplyComboStrategy rotates the array of models based on the selected strategy.
// Supports optional explicit weights map for weighted round-robin.
// It treats every call as a new turn (backward-compatible wrapper).
func (h *ChatHandler) ApplyComboStrategy(strategy string, models []string, comboName string, stickyLimit int, weights ...map[string]int) []string {
	var w map[string]int
	if len(weights) > 0 {
		w = weights[0]
	}
	return h.applyComboStrategy(strategy, models, comboName, stickyLimit, true, w)
}

// ApplyComboStrategyWeighted rotates models using weighted round-robin with explicit weights.
func (h *ChatHandler) ApplyComboStrategyWeighted(strategy string, models []string, comboName string, stickyLimit int, weights map[string]int) []string {
	return h.applyComboStrategy(strategy, models, comboName, stickyLimit, true, weights)
}

// applyComboStrategy is ApplyComboStrategy with turn awareness: the rotation
// index advances only on a new turn (newTurn=true), so a mid-turn tool-use
// sequence stays on the same provider/model.
func (h *ChatHandler) applyComboStrategy(strategy string, models []string, comboName string, stickyLimit int, newTurn bool, explicitWeights ...map[string]int) []string {
	return h.applyComboStrategyWithSeats(strategy, models, comboName, stickyLimit, newTurn, nil, explicitWeights...)
}

func (h *ChatHandler) applyComboStrategyWithSeats(strategy string, models []string, comboName string, stickyLimit int, newTurn bool, comboOwnModels []string, explicitWeights ...map[string]int) []string {
	return h.applyComboStrategyForConversation(strategy, models, comboName, stickyLimit, newTurn, "", comboOwnModels, explicitWeights...)
}

// conversationFingerprint identifies a conversation across its turns.
//
// The FIRST user message is the anchor: it is written once when the conversation
// starts and never changes as the history grows, whereas hashing the whole
// message list would produce a new key on every turn. Providers that bind a
// conversation to the account that opened it refuse a later turn arriving from a
// different account, so the rotation state has to be keyed by something stable
// for the conversation's life.
//
// Returns "" when the body carries no user message, which leaves the caller on
// the per-combo key rather than inventing an identity.
func conversationFingerprint(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var req struct {
		Messages []struct {
			Role    string         `json:"role"`
			Content jsontext.Value `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}
	for _, m := range req.Messages {
		if m.Role != "user" || len(m.Content) == 0 {
			continue
		}
		sum := sha256.Sum256(m.Content)
		return hex.EncodeToString(sum[:16])
	}
	return ""
}

// applyComboStrategyForConversation is applyComboStrategyWithSeats with the
// rotation state scoped to one conversation.
//
// convKey "" keeps the per-combo behaviour the callers had before; a non-empty
// key gives each conversation its own rotation cursor, so a conversation stays
// on the seat that opened it while round-robin still spreads NEW conversations
// across seats. Without that, sticky=1 advances on every request and turn 2 of a
// conversation lands on a different account — which a provider that binds
// conversations to accounts rejects outright.
func (h *ChatHandler) applyComboStrategyForConversation(strategy string, models []string, comboName string, stickyLimit int, newTurn bool, convKey string, comboOwnModels []string, explicitWeights ...map[string]int) []string {
	if len(models) <= 1 {
		return models
	}

	switch strategy {
	case "round-robin", "roundrobin":
		if stickyLimit <= 0 {
			stickyLimit = 1
		}
		fallthrough
	case "sticky":
		if stickyLimit <= 0 {
			stickyLimit = 1
		}
		h.stickyMu.Lock()
		defer h.stickyMu.Unlock()
		if h.stickyState == nil {
			h.stickyState = make(map[string]*comboStickyState)
		}

		key := comboName
		if key == "" {
			key = "__default__"
		}
		if convKey != "" {
			key += "\x00" + convKey
		}
		state, exists := h.stickyState[key]
		if !exists {
			h.pruneComboStickyLocked()
			state = &comboStickyState{Index: 0, ConsecutiveUseCount: 0}
			h.stickyState[key] = state
		}
		state.LastUsed = time.Now()

		// A conversation-bound provider refuses a later turn arriving from a
		// different account, so the conversation's seat is PINNED rather than
		// counted: every request of this conversation reuses the seat that
		// opened it, and round-robin still spreads new conversations across
		// seats. Without this, sticky=1 advances on the next request and turn 2
		// lands on another account — which is exactly what upstream refuses.
		if convKey != "" && comboBindsConversations(models) {
			newTurn = false
		}

		resolvedWeights := h.resolveComboWeights(comboName, explicitWeights...)
		normWeights := NormalizeComboWeights(models, resolvedWeights)
		if normWeights != nil {
			return advanceWeightedRotation(state, models, normWeights, stickyLimit, newTurn, comboOwnModels)
		}

		if state.Sig != "" {
			state.Sig = ""
			state.CurrentWeights = nil
			state.LastSeat = ""
			state.ServingRotated = nil
		}

		// The model owning this turn. A new turn starts from the rotation
		// pointer; a mid-turn request reuses the model serving the turn even
		// after the pointer has advanced for the next turn.
		//
		// The pointer lives in the combo's initial configured target space, not
		// in the list this turn can serve: a seat that capability or affinity
		// filtering dropped still occupies its rotation slot, so filtering a
		// seat out for one turn no longer skips it for good.
		if len(state.Space) == 0 {
			state.Space = slices.Clone(comboOwnModels)
		}
		if len(state.Space) == 0 {
			state.Space = slices.Clone(models)
		}
		// Advance the pointer at the turn boundary, then read it: `Index` is
		// always the seat the next turn starts from.
		if !newTurn && slices.Equal(state.Models, models) && state.ServingIndex < len(models) {
			rotated := make([]string, len(models))
			for i := range models {
				rotated[i] = models[(state.ServingIndex+i)%len(models)]
			}
			return rotated
		}
		state.Models = slices.Clone(models)

		// Sticky serves the scheduled seat for stickyLimit turns, then
		// advances. True round-robin (stickyLimit<=1) advances every turn.
		if newTurn {
			state.ConsecutiveUseCount++
			if state.ConsecutiveUseCount > stickyLimit {
				state.Index = (state.Index + 1) % len(state.Space)
				state.ConsecutiveUseCount = 1
			}
		}
		scheduled := state.Index % len(state.Space)

		// Map the scheduled seat onto this turn's list: the seat itself, or the
		// first seat after it when a filter dropped it.
		servingIndex := slices.Index(models, state.Space[scheduled])
		if servingIndex < 0 {
			for at := scheduled + 1; servingIndex < 0 && at < scheduled+len(state.Space); at++ {
				servingIndex = slices.Index(models, state.Space[at%len(state.Space)])
			}
		}
		if servingIndex < 0 {
			servingIndex = 0
		}
		state.ServingIndex = servingIndex

		rotated := make([]string, len(models))
		for i := range models {
			rotated[i] = models[(servingIndex+i)%len(models)]
		}

		return rotated
	case "cascade":
		// Escalation is the combo loop's own failure path; cascade's job is
		// only to start that walk at the cheapest member. See
		// OrderModelsByCascade.
		return OrderModelsByCascade(models)
	case "capacity":
		fallthrough
	default:
		out := make([]string, len(models))
		copy(out, models)
		return out
	}
}

// keysString returns a comma-separated list of map keys.

func (h *ChatHandler) ResetComboState(comboName string) {
	h.stickyMu.Lock()
	defer h.stickyMu.Unlock()
	if h.stickyState == nil {
		h.stickyState = make(map[string]*comboStickyState)
	}
	if comboName != "" {
		delete(h.stickyState, comboName)
	} else {
		h.stickyState = make(map[string]*comboStickyState)
	}
}

// comboBindsConversations reports whether any seat of a combo is served by a
// provider that ties a conversation to the account that opened it.
//
// Registry-driven on purpose (AGENTS.md §3.B/§3.C): the capability is declared on
// the provider, never matched by a string comparison against a provider id here.
func comboBindsConversations(seats []string) bool {
	for _, seat := range seats {
		prefix, _, ok := strings.Cut(seat, "/")
		if !ok {
			continue
		}
		if providers.BindsConversations(providers.ResolveAlias(prefix)) {
			return true
		}
	}
	return false
}

// comboStickyMaxConversations bounds the conversation-keyed rotation state. The
// key is derived from a client-supplied conversation, so the map would otherwise
// grow without limit (AGENTS.md §4.G). Sits above smartMaxStickySessions because
// every conversation of every combo lands here, not one entry per session.
const comboStickyMaxConversations = 8192

// comboStickyConversationTTL drops a conversation's cursor once it goes idle.
// Long enough that a conversation paused over lunch keeps its account; short
// enough that abandoned conversations do not hold the map open.
const comboStickyConversationTTL = 6 * time.Hour

// pruneComboStickyLocked evicts idle conversation state. The caller holds
// stickyMu.
//
// A conversation's cursor is only needed while the conversation is live: once it
// has gone quiet, a returning client starting a fresh turn can safely be placed
// on any seat. Deleting the oldest quarter when still at the cap keeps the cost
// amortised instead of pruning on every request.
func (h *ChatHandler) pruneComboStickyLocked() {
	if len(h.stickyState) < comboStickyMaxConversations {
		return
	}
	now := time.Now()
	for k, v := range h.stickyState {
		if !v.LastUsed.IsZero() && now.Sub(v.LastUsed) > comboStickyConversationTTL {
			delete(h.stickyState, k)
		}
	}
	if len(h.stickyState) < comboStickyMaxConversations {
		return
	}
	type aged struct {
		key string
		at  time.Time
	}
	entries := make([]aged, 0, len(h.stickyState))
	for k, v := range h.stickyState {
		entries = append(entries, aged{k, v.LastUsed})
	}
	drop := len(entries) / 4
	for drop > 0 && len(entries) > 0 {
		oldest := 0
		for i := range entries {
			if entries[i].at.Before(entries[oldest].at) {
				oldest = i
			}
		}
		delete(h.stickyState, entries[oldest].key)
		entries = append(entries[:oldest], entries[oldest+1:]...)
		drop--
	}
}
