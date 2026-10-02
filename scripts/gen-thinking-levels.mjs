#!/usr/bin/env node
// Regenerate internal/providers/testdata/thinking_levels.json from an upstream
// checkout, so the parity fixture is a capture of the real implementation
// rather than a hand-written subset.
//
//   git clone --branch v0.5.95 https://github.com/decolua/9router /tmp/9router
//   DUMP_CATALOG_PAIRS=testdata/catalog_pairs.json \
//     go test ./internal/providers/ -run TestDumpCatalogPairs -count=1
//   node scripts/gen-thinking-levels.mjs /tmp/9router v0.5.95 \
//     internal/providers/testdata/catalog_pairs.json
//
// The pair list is the Go catalog (providers.ProviderModels) dumped by
// TestDumpCatalogPairs, so the capture always walks exactly the models this
// port serves — a new provider or model cannot silently escape the fixture.
// Bump upstreamLevelsFixtureVersion in thinking_levels_fixture_test.go to the
// same tag.

import { readFileSync, writeFileSync } from "node:fs";
import { pathToFileURL } from "node:url";
import { resolve } from "node:path";

const [checkout, version = "v0.5.95", pairsPath] = process.argv.slice(2);
if (!checkout || !pairsPath) {
  console.error("usage: gen-thinking-levels.mjs <upstream-checkout> <tag> <pairs.json>");
  process.exit(2);
}

const root = resolve(checkout);
const url = (p) => pathToFileURL(resolve(root, p)).href;
const { getThinkingLevels } = await import(url("open-sse/providers/thinkingLevels.js"));

const pairs = JSON.parse(readFileSync(resolve(pairsPath), "utf8"));

const models = {};
for (const [provider, ids] of Object.entries(pairs)) {
  const rows = {};
  for (const model of ids) rows[model] = getThinkingLevels(provider, model) ?? null;
  models[provider] = rows;
}

const out = { upstreamVersion: version, models };
writeFileSync("internal/providers/testdata/thinking_levels.json", JSON.stringify(out));
const count = Object.values(models).reduce((n, rows) => n + Object.keys(rows).length, 0);
console.log(`captured ${count} (provider, model) pairs from ${version} (${root})`);
