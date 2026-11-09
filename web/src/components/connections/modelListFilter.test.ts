import { test, describe } from "node:test";
import { strictEqual, deepStrictEqual, ok } from "node:assert";
import {
  EMPTY_MODEL_LIST_FILTER,
  applyModelListFilter,
  hasLatencyData,
  isModelListFilterEmpty,
  selectedVisibleIds,
  selectionState,
  toggleSelectAll,
  type ModelListFilter,
  type ModelRowView,
} from "./modelListFilter";

function row(over: Partial<ModelRowView> = {}): ModelRowView {
  return {
    id: "model-a",
    source: "registry",
    caps: { vision: false, reasoning: false, search: false, free: false, jailbreak: false },
    thinkingLevels: [],
    deprecated: false,
    disabled: false,
    ...over,
  };
}

function filter(over: Partial<ModelListFilter> = {}): ModelListFilter {
  return {
    ...EMPTY_MODEL_LIST_FILTER,
    ...over,
    caps: { ...EMPTY_MODEL_LIST_FILTER.caps, ...over.caps },
    status: { ...EMPTY_MODEL_LIST_FILTER.status, ...over.status },
  };
}

const ids = (rows: ModelRowView[]) => rows.map((r) => r.id);

describe("applyModelListFilter — search", () => {
  const rows = [
    row({ id: "claude-opus-4", name: "Claude Opus 4" }),
    row({ id: "claude-haiku-4", name: "Claude Haiku 4" }),
    row({ id: "gpt-4o", name: "GPT-4o" }),
  ];

  test("empty query keeps every row", () => {
    deepStrictEqual(ids(applyModelListFilter(rows, filter())), ["claude-opus-4", "claude-haiku-4", "gpt-4o"]);
  });

  test("positive term matches id or name", () => {
    deepStrictEqual(ids(applyModelListFilter(rows, filter({ query: "opus" }))), ["claude-opus-4"]);
  });

  test("negative term excludes", () => {
    deepStrictEqual(ids(applyModelListFilter(rows, filter({ query: "claude !haiku" }))), ["claude-opus-4"]);
  });

  test("regex matches", () => {
    deepStrictEqual(ids(applyModelListFilter(rows, filter({ query: "/gpt-.*/" }))), ["gpt-4o"]);
  });
});

describe("applyModelListFilter — capability groups", () => {
  const rows = [
    row({ id: "vision-only", caps: { vision: true, reasoning: false, search: false, free: false } }),
    row({ id: "reasoning-only", caps: { vision: false, reasoning: true, search: false, free: false }, thinkingLevels: ["high"] }),
    row({ id: "vision-free", caps: { vision: true, reasoning: false, search: false, free: true } }),
    row({ id: "plain" }),
  ];

  test("single chip narrows to that capability", () => {
    deepStrictEqual(ids(applyModelListFilter(rows, filter({ caps: { thinking: true, vision: false, search: false, free: false } }))), ["reasoning-only"]);
  });

  test("chips within a group OR", () => {
    deepStrictEqual(ids(applyModelListFilter(rows, filter({ caps: { thinking: true, vision: true, search: false, free: false } }))), ["vision-only", "reasoning-only", "vision-free"]);
  });

  test("capability chips OR within the group, not AND", () => {
    // vision + free: vision-only matches the vision chip, so the group
    // passes. Capability chips are one group — narrowing happens by
    // combining groups (caps × status × source), never within one.
    deepStrictEqual(ids(applyModelListFilter(rows, filter({ caps: { thinking: false, vision: true, search: false, free: true } }))), ["vision-only", "vision-free"]);
  });

  test("caps group ANDs with the status group", () => {
    const statusRows = [
      row({ id: "vision-live", caps: { vision: true, reasoning: false, search: false, free: false }, testStatus: "ok" }),
      row({ id: "vision-dead", caps: { vision: true, reasoning: false, search: false, free: false }, testStatus: "error" }),
      row({ id: "plain-live", testStatus: "ok" }),
    ];
    deepStrictEqual(ids(applyModelListFilter(statusRows, filter({ caps: { thinking: false, vision: true, search: false, free: false }, status: { available: false, disabled: false, error: true, untested: false, deprecated: false } }))), ["vision-dead"]);
  });

  test("thinking chip accepts a model with levels but no reasoning flag", () => {
    deepStrictEqual(ids(applyModelListFilter(rows, filter({ caps: { thinking: true, vision: false, search: false, free: false } }))), ["reasoning-only"]);
  });
});

describe("applyModelListFilter — jailbreak group", () => {
  const rows = [
    row({ id: "plain" }),
    row({ id: "dan-mode", caps: { vision: false, reasoning: false, search: false, free: false, jailbreak: true } }),
    row({ id: "uncensored-7b", caps: { vision: false, reasoning: false, search: false, free: false, jailbreak: true } }),
  ];

  test("chip selects only flagged rows", () => {
    deepStrictEqual(ids(applyModelListFilter(rows, filter({ caps: { thinking: false, vision: false, search: false, free: false, jailbreak: true } }))), ["dan-mode", "uncensored-7b"]);
  });

  test("jailbreak ORs with vision inside the caps group", () => {
    const mixed = [
      row({ id: "vision-only", caps: { vision: true, reasoning: false, search: false, free: false, jailbreak: false } }),
      row({ id: "jb-only", caps: { vision: false, reasoning: false, search: false, free: false, jailbreak: true } }),
      row({ id: "plain" }),
    ];
    deepStrictEqual(ids(applyModelListFilter(mixed, filter({ caps: { thinking: false, vision: true, search: false, free: false, jailbreak: true } }))), ["vision-only", "jb-only"]);
  });

  test("jailbreak ANDs with the status group", () => {
    const mixed = [
      row({ id: "jb-dead", caps: { vision: false, reasoning: false, search: false, free: false, jailbreak: true }, testStatus: "error" }),
      row({ id: "jb-live", caps: { vision: false, reasoning: false, search: false, free: false, jailbreak: true }, testStatus: "ok" }),
    ];
    deepStrictEqual(ids(applyModelListFilter(mixed, filter({ caps: { thinking: false, vision: false, search: false, free: false, jailbreak: true }, status: { available: false, disabled: false, error: true, untested: false, deprecated: false } }))), ["jb-dead"]);
  });
});

describe("applyModelListFilter — status groups", () => {
  const rows = [
    row({ id: "live", testStatus: "ok" }),
    row({ id: "dead", testStatus: "error" }),
    row({ id: "cooling", testStatus: "blocked" }),
    row({ id: "fresh" }),
    row({ id: "off", disabled: true }),
    row({ id: "gone", deprecated: true }),
  ];

  test("available excludes disabled and failed rows", () => {
    deepStrictEqual(ids(applyModelListFilter(rows, filter({ status: { available: true, disabled: false, unavailable: false, error: false, untested: false, deprecated: false } }))), ["live", "fresh"]);
  });

  test("disabled selects only disabled", () => {
    deepStrictEqual(ids(applyModelListFilter(rows, filter({ status: { available: false, disabled: true, error: false, untested: false, deprecated: false } }))), ["off"]);
  });

  test("error covers failed and blocked", () => {
    deepStrictEqual(ids(applyModelListFilter(rows, filter({ status: { available: false, disabled: false, error: true, untested: false, deprecated: false } }))), ["dead", "cooling"]);
  });

  test("untested selects never-probed rows, disabled included", () => {
    deepStrictEqual(ids(applyModelListFilter(rows, filter({ status: { available: false, disabled: false, error: false, untested: true, deprecated: false } }))), ["fresh", "off", "gone"]);
  });

  test("deprecated selects badged rows", () => {
    deepStrictEqual(ids(applyModelListFilter(rows, filter({ status: { available: false, disabled: false, unavailable: false, error: false, untested: false, deprecated: true } }))), ["gone"]);
  });

  test("unavailable covers disabled, failed, blocked and deprecated", () => {
    deepStrictEqual(ids(applyModelListFilter(rows, filter({ status: { available: false, disabled: false, unavailable: true, error: false, untested: false, deprecated: false } }))), ["dead", "cooling", "off", "gone"]);
  });

  test("unavailable excludes untested rows", () => {
    // "fresh" has never been probed: it may well work, and calling it
    // unavailable would send the operator hunting a fault never observed.
    deepStrictEqual(ids(applyModelListFilter(rows, filter({ status: { available: false, disabled: false, unavailable: true, error: false, untested: false, deprecated: false } }))).includes("fresh"), false);
  });

  test("available and unavailable are disjoint", () => {
    const avail = ids(applyModelListFilter(rows, filter({ status: { available: true, disabled: false, unavailable: false, error: false, untested: false, deprecated: false } })));
    const unavail = ids(applyModelListFilter(rows, filter({ status: { available: false, disabled: false, unavailable: true, error: false, untested: false, deprecated: false } })));
    strictEqual(avail.filter((x) => unavail.includes(x)).length, 0);
  });

  test("status chips OR within the group", () => {
    deepStrictEqual(ids(applyModelListFilter(rows, filter({ status: { available: false, disabled: false, unavailable: false, error: true, untested: true, deprecated: false } }))), ["dead", "cooling", "fresh", "off", "gone"]);
  });
});

describe("applyModelListFilter — sort", () => {
  const rows = [row({ id: "beta" }), row({ id: "Alpha" }), row({ id: "gamma" })];

  test("registry keeps input order", () => {
    deepStrictEqual(ids(applyModelListFilter(rows, filter({ sort: "registry" }))), ["beta", "Alpha", "gamma"]);
  });

  test("az is case-insensitive", () => {
    deepStrictEqual(ids(applyModelListFilter(rows, filter({ sort: "az" }))), ["Alpha", "beta", "gamma"]);
  });

  test("za reverses", () => {
    deepStrictEqual(ids(applyModelListFilter(rows, filter({ sort: "za" }))), ["gamma", "beta", "Alpha"]);
  });

  test("sort does not mutate the input array", () => {
    const input = [row({ id: "b" }), row({ id: "a" })]
    applyModelListFilter(input, filter({ sort: "az" }))
    deepStrictEqual(ids(input), ["b", "a"]);
  });
});

describe("applyModelListFilter — latency sorts", () => {
  const rows = [
    row({ id: "slow", latencyMs: 900 }),
    row({ id: "fast", latencyMs: 120 }),
    row({ id: "mid", latencyMs: 400 }),
    row({ id: "never-probed" }),
  ];

  test("latency-asc puts fastest first and unmeasured last", () => {
    deepStrictEqual(ids(applyModelListFilter(rows, filter({ sort: "latency-asc" }))), ["fast", "mid", "slow", "never-probed"]);
  });

  test("latency-desc puts slowest first and unmeasured last", () => {
    deepStrictEqual(ids(applyModelListFilter(rows, filter({ sort: "latency-desc" }))), ["slow", "mid", "fast", "never-probed"]);
  });

  test("latency sort does not mutate the input array", () => {
    const input = [row({ id: "slow", latencyMs: 900 }), row({ id: "fast", latencyMs: 100 })];
    applyModelListFilter(input, filter({ sort: "latency-asc" }));
    deepStrictEqual(ids(input), ["slow", "fast"]);
  });

  test("ties keep the incoming order", () => {
    const tied = [row({ id: "b", latencyMs: 200 }), row({ id: "a", latencyMs: 200 })];
    deepStrictEqual(ids(applyModelListFilter(tied, filter({ sort: "latency-asc" }))), ["b", "a"]);
  });

  test("all-unmeasured list is unchanged by a latency sort", () => {
    const none = [row({ id: "x" }), row({ id: "y" })];
    deepStrictEqual(ids(applyModelListFilter(none, filter({ sort: "latency-asc" }))), ["x", "y"]);
  });
});

describe("hasLatencyData", () => {
  test("false when nothing has been probed", () => {
    ok(!hasLatencyData([row({ id: "a" }), row({ id: "b" })]));
  });

  test("true when at least one row carries a measurement", () => {
    ok(hasLatencyData([row({ id: "a" }), row({ id: "b", latencyMs: 250 })]));
  });

  test("false for an empty list", () => {
    ok(!hasLatencyData([]));
  });
});

describe("applyModelListFilter — register date sorts", () => {
  const rows = [
    row({ id: "oldest", addedAt: 1000 }),
    row({ id: "newest", addedAt: 3000 }),
    row({ id: "middle", addedAt: 2000 }),
    row({ id: "registry-model" }),
  ];

  test("added-desc puts the newest first and undated rows last", () => {
    deepStrictEqual(ids(applyModelListFilter(rows, filter({ sort: "added-desc" }))), ["newest", "middle", "oldest", "registry-model"]);
  });

  test("added-asc puts the oldest first and undated rows last", () => {
    deepStrictEqual(ids(applyModelListFilter(rows, filter({ sort: "added-asc" }))), ["oldest", "middle", "newest", "registry-model"]);
  });

  test("undated rows keep their incoming order at the end", () => {
    const mixed = [
      row({ id: "dated", addedAt: 500 }),
      row({ id: "plain-b" }),
      row({ id: "plain-a" }),
    ];
    deepStrictEqual(ids(applyModelListFilter(mixed, filter({ sort: "added-desc" }))), ["dated", "plain-b", "plain-a"]);
  });

  test("a list with no dates is unchanged", () => {
    const none = [row({ id: "x" }), row({ id: "y" })];
    deepStrictEqual(ids(applyModelListFilter(none, filter({ sort: "added-desc" }))), ["x", "y"]);
  });

  test("sort does not mutate the input array", () => {
    const input = [row({ id: "old", addedAt: 100 }), row({ id: "new", addedAt: 900 })];
    applyModelListFilter(input, filter({ sort: "added-desc" }));
    deepStrictEqual(ids(input), ["old", "new"]);
  });
});

describe("isModelListFilterEmpty", () => {
  test("default filter is empty", () => {
    ok(isModelListFilterEmpty(EMPTY_MODEL_LIST_FILTER));
  });

  test("whitespace query counts as empty", () => {
    ok(isModelListFilterEmpty(filter({ query: "   " })));
  });

  test("any chip makes it non-empty", () => {
    ok(!isModelListFilterEmpty(filter({ caps: { thinking: true, vision: false, search: false, free: false } })));
    ok(!isModelListFilterEmpty(filter({ sort: "az" })));
    ok(!isModelListFilterEmpty(filter({ query: "x" })));
  });
});

describe("selectionState", () => {
  const rows = [row({ id: "a" }), row({ id: "b" }), row({ id: "c" })];

  test("none selected", () => {
    deepStrictEqual(selectionState(new Set(), rows), { selected: 0, total: 3, allSelected: false, indeterminate: false });
  });

  test("some selected is indeterminate", () => {
    deepStrictEqual(selectionState(new Set(["a"]), rows), { selected: 1, total: 3, allSelected: false, indeterminate: true });
  });

  test("all selected", () => {
    deepStrictEqual(selectionState(new Set(["a", "b", "c"]), rows), { selected: 3, total: 3, allSelected: true, indeterminate: false });
  });

  test("empty list is not all-selected", () => {
    deepStrictEqual(selectionState(new Set(), []), { selected: 0, total: 0, allSelected: false, indeterminate: false });
  });

  test("selection outside the visible rows does not count", () => {
    deepStrictEqual(selectionState(new Set(["a", "zzz"]), rows), { selected: 1, total: 3, allSelected: false, indeterminate: true });
  });
});

describe("toggleSelectAll", () => {
  const rows = [row({ id: "a" }), row({ id: "b" })];

  test("select all adds every visible id", () => {
    strictEqual(toggleSelectAll(new Set(), rows, true).size, 2);
  });

  test("select all preserves ids outside the filter", () => {
    const next = toggleSelectAll(new Set(["kept"]), rows, true);
    ok(next.has("kept"));
    ok(next.has("a"));
    ok(next.has("b"));
  });

  test("deselect all removes only visible ids", () => {
    const next = toggleSelectAll(new Set(["a", "b", "kept"]), rows, false);
    deepStrictEqual([...next], ["kept"]);
  });
});

describe("selectedVisibleIds", () => {
  const rows = [row({ id: "a" }), row({ id: "b" })];

  test("returns selected ids in visible order", () => {
    deepStrictEqual(selectedVisibleIds(new Set(["b", "a"]), rows), ["a", "b"]);
  });

  test("drops ids not in the visible list", () => {
    deepStrictEqual(selectedVisibleIds(new Set(["a", "gone"]), rows), ["a"]);
  });
});
