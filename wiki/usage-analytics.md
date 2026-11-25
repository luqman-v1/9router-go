---
title: Usage & Analytics
order: 17
section: Using the Gateway
since: 1.9.14
---

# Usage & Analytics

Every request is written to a usage ledger, and the dashboard reads that ledger
several ways. This page covers what is recorded and how to read it.

## What is recorded per request

| Field | Meaning |
|---|---|
| Provider | Which provider served it — a registry id, or a custom node's id |
| Model | The model that answered |
| Prompt / completion tokens | Both directions |
| Cost | From the pricing table, in your display currency |
| Compression mode | `rtk`, `caveman`, `adhd`, `ponytail`, `stacked`, or none |
| Tokens saved | What RTK actually returned, not an estimate |
| Latency | Time to first token and total |
| Outcome | Success, or the failure reason and HTTP status |
| Fallback chain | Which candidates were tried before one succeeded |

## Live stream

`GET /api/usage/stream` is a Server-Sent Events feed the dashboard's topology
animation and recent-request list read. `GET /api/usage/stats` and
`GET /api/usage/chart` serve the aggregates.

## Dashboard views

The Usage page switches between several views of the same data
(`web/src/components/analytics/UsageDashboard.svelte`):

| View | Component | Shows |
|---|---|---|
| Summary KPIs | `SummaryKpiCards.svelte` | Totals for the selected period |
| Chart | `UsageChart.svelte` | Volume over time |
| Breakdown | `UsageBreakdownTable.svelte` | Grouped by provider, model, or key |
| Provider topology | `ProviderTopologyCard.svelte` | Which providers talk to which |
| Activity heatmap | `ActivityHeatmap.svelte` | When traffic happens |
| Combo flow | `ComboFlowCard.svelte` | How a combo's chain resolved |
| Node health | `NodeHealthTable.svelte` | Per-node errors and cooldowns |
| Request details | `RequestDetailsTab.svelte` | One request's full payload |

## Custom ranges

The period selector is not limited to fixed presets — `14d`, `12h`, `7d` all
work, because the range parser accepts a number plus a unit
(`internal/analyticsrange/range.go`).

## Compression analytics

`GET /api/analytics/compression` reports what the token savers actually did:

- Total requests and total tokens saved
- Average savings ratio
- Average duration
- Mode breakdown, including `stacked` (RTK combined with a style saver)

The mode is resolved on the live pipeline, so a request that ran RTK *and* Caveman
is labelled `stacked` rather than being attributed to one of them.

## Cost display

Costs are computed from the pricing table (`internal/pricing/`) and shown in your
chosen currency — USD, IDR, EUR and ten more. The exchange-rate table is editable
and can be refreshed from a public provider with
`POST /api/settings/currency/refresh`.

## Quota tracking

The Quota Tracker page reads per-provider quota and the reset schedules.
Providers with a tracker include Codex, GLM, ClinePass, Freebuff, and
Cloudflare Workers AI:

- **Cloudflare Workers AI** (`cloudflare-ai`) — today's usage read from
  Cloudflare's GraphQL `viewer.accounts.aiInferenceAdaptiveGroups` for the
  account, showing **Neurons (daily)** against the free-tier allowance of
  10,000 neurons/day (override with `providerSpecificData.dailyNeuronLimit`)
  and **Requests (today)** as unlimited. An auth-shaped failure says the token
  has no Cloudflare AI Analytics access rather than reporting a generic error.
- **Codex reset credits** — `GET /api/usage/{connectionId}/reset-credits`, and
  `POST …/reset-credits/consume` to spend one.
- **GLM reset cards** (Z.ai international) —
  `GET|POST /api/usage/{connectionId}/glm-reset-cards`.
- **Auto-ping schedule** — set a background interval that keeps quota current
  without traffic.

### Daily check-ins

Two providers pay a daily bonus for showing up, and a background loop collects
it every 30 minutes per account (toggle in Settings, or the manual button on
the Quota Tracker page):

- **Qoder** daily credit check-in
- **MiniMax Code** daily sign-in — reads the seven-day panel, claims when
  claimable, and writes a per-account ledger to `DATA_DIR/minimax-checkin.json`
  so the toggle takes effect without a restart

`GET /api/minimax/checkins` returns check-in state keyed by connection id;
`POST /api/minimax/checkins/{connectionId}` triggers a manual check-in.

## Cache analytics

`GET /api/cache` and `GET /api/cache/entries` read the semantic response cache:
hit rate, entry count, and the entries themselves. `DELETE /api/cache` clears it.

The dashboard reports the cache the engine actually serves from. That matters: a
second cache instance would report zero entries while the engine served hits, and
clearing from the dashboard would leave the engine still serving them.

## Retention

Usage history is pruned on a schedule. Pruning keeps a carryover per dimension, so
spend already counted is not billed twice after the rows are gone — the invariant
is covered by tests.

## Tracing

`GET /debug/traces` gives p50/p95 latency per provider+model, which is the
fastest way to find the provider that is actually slow.
