# Combo Fusion — Parallel Multi-Model + Judge Synthesis

> **Design doc for combo fusion feature in 9router-go.**
> Reference: 9router-js `open-sse/services/combo.js` `handleFusionChat()`

## Goal

Add parallel-multi-model fusion to existing combo fallback. Fan request out to N panel models simultaneously, collect responses with quorum-grace timing, then a judge model synthesizes one final answer.

## Architecture

```
Request —→ detect combo, strategy="fusion"
                │
                ▼
       flattenToolHistory(messages)  — strip tool turns → prose
       force stream=false
                │
                ▼
       Fan-out: goroutine per panel model
       ┌────┬────┬────┬────┐
       │ M1 │ M2 │ M3 │ M4 │  ← parallel, non-streaming
       └─┬──┴─┬──┴─┬──┴─┬──┘
         │    │    │    │
         ▼    ▼    ▼    ▼
       collectPanel():
         • minPanel=2 answers → start grace timer (8s)
         • hard timeout 90s
         • return whatever arrived
                │
                ▼
       0 answers ──→ 503
       1 answer  ──→ return directly (no fusion)
       2+ answers ──→ judge synthesizes
                │
                ▼
       judgeModel(buildJudgePrompt(answers))
         • panel[0] if no judgeModel configured
         • streaming jika original stream=true
         • tools re-enabled (dari original body)
                │
                ▼
       Response → client (streaming or JSON)
```

## File Changes

### `internal/handlers/combo.go` — additions

| Symbol | Lines | Description |
|--------|-------|-------------|
| `handleFusion()` | ~80 | Main fusion orchestrator |
| `collectPanel()` | ~40 | Goroutine fan-out with quorum-grace |
| `flattenToolHistory()` | ~30 | Strip tool_calls → prose |
| `buildJudgePrompt()` | ~30 | Build judge system prompt |
| `handleFusionDefault()` | ~10 | Fusion tuning defaults |

### `internal/handlers/chat.go` — routing

- Di `HandleChatCompletions()`: setelah detect combo, kalo `strategy="fusion"` → panggil `handleFusion()`
- Di `HandleMessages()`: sama

### Test

- `internal/handlers/combo_test.go`: test flattenToolHistory, buildJudgePrompt, handleFusion dengan mock calls

## Key Design Decisions

### Panel calls: non-streaming
Panel model calls forced `stream=false`. Judge butuh complete prose buat synthesis. Streaming partial results ke client gak berguna tanpa konteks penuh.

### Tools stripped from panel
`flattenToolHistory()` converts assistant tool_calls + tool_results → prose. Panel models can't emit tool_calls back (they'd conflict). Judge output keeps tools from original request.

### collectPanel quorum-grace
- `minPanel`: 2 (default). Berapa banyak sukses sebelum grace timer mulai.
- `stragglerGraceMs`: 8000. Tunggu 8s setelah quorum tercapai buat stragglers.
- `panelHardTimeoutMs`: 90000. Safety net — 1 model hung gak ngehold selamanya.
- Overridable via combo config `fusionTuning`.

### Judge model fallback
- Jika `judgeModel` diset di combo config → pake itu.
- Jika tidak → pake panel[0] (model pertama di daftar combo).
- Sama kaya JS reference.

### Degradation
- 0 panel answers → `{"error":{"message":"All fusion panel models failed"}}` → 503
- 1 panel answer → return langsung (no fusion overhead)
- 2+ → judge synthesizes

### Streaming preservation
- Panel calls: always non-streaming
- Judge call: streaming=true jika original request `stream=true`. Client gets SSE dari judge output.
- Judge call non-streaming jika original `stream=false`.

### Error handling
- Panel call error (timeout/HTTP error/parse error) → model di-skip, gak nge-fail total.
- Judge call error → propagate ke client dengan status code dari upstream.
- All panel fail → client gets 503, bukan 500.

## Fusion Tuning Defaults

```go
type FusionTuning struct {
    MinPanel          int `json:"minPanel"`          // default 2
    StragglerGraceMs  int `json:"stragglerGraceMs"`  // default 8000
    PanelHardTimeoutMs int `json:"panelHardTimeoutMs"` // default 90000
}

var fusionDefaults = FusionTuning{
    MinPanel:          2,
    StragglerGraceMs:  8000,
    PanelHardTimeoutMs: 90000,
}
```

Overridable per-combo via `comboStrategies[name].fusionTuning` — di-read dari settings DB.

## Wire Protocol Changes

None. Fusion respon identik dengan single-model respon (OpenAI chat format atau Claude messages format). Client gak perlu tau bedanya.

## Testing

1. **flattenToolHistory**: input dengan tool_calls → output prose, text messages gak berubah
2. **buildJudgePrompt**: 2 answers → prompt contains "Source 1" + "Source 2"
3. **handleFusion with 0 calls** → 503
4. **handleFusion with 1 success** → return langsung (skip judge)
5. **handleFusion with 2+ success** → judge dipanggil
6. **collectPanel timeout**: 1 slow model → grace timer triggers, result partial
7. **collectPanel hard timeout**: model hung >90s → forced finish
