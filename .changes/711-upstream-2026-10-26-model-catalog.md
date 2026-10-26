### Ported upstream 2026-10-26 model catalog fixes

- **opencode-go**: added `kimi-k3.1-pro` (Kimi K3.1 Pro) to the catalog, with
  vision/video capabilities, always-on Kimi thinking and the $2/$10 pricing
  tier (upstream decolua/9router#5cd4a75). The id is visible in `/v1/models`
  and the dashboard picker without affecting the generic `opencode` lane.
- **commandcode**: added `moonshotai/Kimi-K3.1-Pro` to the catalog so the
  model is routable (`cmc/<id>`) and shown in the dashboard (upstream
  decolua/9router#a3bdaf6d), keeping the upstream registry order the
  dashboard tests pin.
- **opencode free lane**: pinned the `oc/deepseek-v4.1-flash-free` context
  window to 330k (max output 128k) because the free backend rejects prompts
  above ~330k tokens; clients now compact instead of hard-failing mid-session
  (upstream decolua/9router#1da5b0d7, issue #5097).

**Verified**: new `internal/providers/upstream_20261026_models_test.go` pins
the catalog, capability and pricing entries; the counted lists updated in
`web/src/lib/models.test.ts` and `internal/handlers/dashboard/model_caps_test.go`.
`go test` (providers, pricing, dashboard, chat, media), `go vet`, `go build`,
and `web` build + 603 unit tests all pass.
