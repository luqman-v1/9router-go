import { test, describe } from "node:test";
import { strictEqual, deepStrictEqual, ok } from "node:assert";
import { parseSearchQuery, matchesSearch } from "./modelSearchFilter";

describe("parseSearchQuery", () => {
  test("empty string", () => {
    deepStrictEqual(parseSearchQuery(""), {
      positiveTerms: [],
      negativeTerms: [],
      isRegex: false,
      regex: null,
      raw: "",
      isEmpty: true,
    });
  });

  test("word", () => {
    deepStrictEqual(parseSearchQuery("opus"), {
      positiveTerms: ["opus"],
      negativeTerms: [],
      isRegex: false,
      regex: null,
      raw: "opus",
      isEmpty: false,
    });
  });

  test("!negation", () => {
    deepStrictEqual(parseSearchQuery("!opus"), {
      positiveTerms: [],
      negativeTerms: ["opus"],
      isRegex: false,
      regex: null,
      raw: "!opus",
      isEmpty: false,
    });
  });

  test("/regex/", () => {
    const result = parseSearchQuery("/opus.*/");
    ok(result.isRegex);
    ok(result.regex instanceof RegExp);
    strictEqual(result.regex.source, "opus.*");
  });

  test("regex with flags", () => {
    const result = parseSearchQuery("/gpt-[45]/i");
    ok(result.isRegex);
    ok(result.regex instanceof RegExp);
    strictEqual(result.regex.source, "gpt-[45]");
    strictEqual(result.regex.flags, "i");
  });

  test("live typing regex without closing slash", () => {
    const result = parseSearchQuery("/gpt-4");
    ok(result.isRegex);
    ok(result.regex instanceof RegExp);
    strictEqual(result.regex.source, "gpt-4");
  });
});

describe("matchesSearch", () => {
  const target = { id: "claude-3-opus", name: "Claude 3 Opus", provider: "anthropic" };

  test("empty query matches", () => {
    ok(matchesSearch(target, ""));
  });

  test("positive term matches id", () => {
    ok(matchesSearch(target, "opus"));
  });

  test("negative term excludes", () => {
    ok(!matchesSearch(target, "!opus"));
  });

  test("combined positive and negative", () => {
    ok(matchesSearch(target, "claude !haiku"));
    ok(!matchesSearch(target, "claude !opus"));
  });

  test("regex matches", () => {
    ok(matchesSearch(target, "/claude-3-.*/"));
    ok(!matchesSearch(target, "/gpt-.*/"));
  });

  test("global regex does not leak lastIndex between candidates", () => {
    ok(matchesSearch({ name: "aaa1", id: "bbb1" }, "/1/g"));
  });

  test("global regex stays independent across repeated searches", () => {
    const parsed = parseSearchQuery("/1/g");
    ok(matchesSearch({ name: "aaa1" }, parsed));
    ok(matchesSearch({ name: "aaa1" }, parsed));
  });
});
