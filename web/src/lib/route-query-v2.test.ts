import { expect, test } from "vitest";
import {
  parseRouteQueryRules,
  parseRouteSearch,
  serializeRouteQuery,
} from "./route-query-v2";
const rules = [
  { name: "page", type: "integer", min: 1, max: 100 },
  { name: "q", type: "text", max_bytes: 32 },
  { name: "active", type: "boolean" },
  { name: "sort", type: "enum", values: ["newest", "oldest"] },
];
test("typed query round trips Unicode, booleans and numbers in canonical key order", () => {
  const parsed = parseRouteQueryRules(rules);
  expect(parsed).not.toBeNull();
  const result = serializeRouteQuery(parsed!, {
    q: "中文 & test",
    page: 2,
    active: false,
    sort: "newest",
  });
  expect(result?.search).toBe(
    "?active=false&page=2&q=%E4%B8%AD%E6%96%87+%26+test&sort=newest",
  );
  expect(parseRouteSearch(parsed!, result!.search)?.query).toEqual({
    active: false,
    page: 2,
    q: "中文 & test",
    sort: "newest",
  });
  expect(Object.isFrozen(result?.query)).toBe(true);
});
test.each([
  "?page=01",
  "?page=0",
  "?page=101",
  "?page=2&page=3",
  "?page=%32",
  "?q=%ZZ",
  "?q=%ED%A0%80",
  "?active=1",
  "?unknown=x",
  "?sort=invalid",
  "?q=x&page=2",
])("noncanonical or undeclared query refuses %s", (search) => {
  expect(parseRouteSearch(parseRouteQueryRules(rules)!, search)).toBeNull();
});
test("SDK requests require typed values and refuse oversized or malformed text", () => {
  const parsed = parseRouteQueryRules(rules)!;
  for (const value of [
    { page: "2" },
    { active: "true" },
    { page: NaN },
    { q: "x".repeat(33) },
    { q: "\ud800" },
    { q: "\n" },
    { sort: "bad" },
    { unknown: 1 },
  ])
    expect(serializeRouteQuery(parsed, value)).toBeNull();
});
test("query rule variants reject irrelevant bounds, duplicate names and missing constraints", () => {
  for (const value of [
    [{ name: "page", type: "integer", min: 1 }],
    [{ name: "active", type: "boolean", min: 0 }],
    [{ name: "q", type: "text", max_bytes: 0 }],
    [{ name: "sort", type: "enum", values: ["newest", "newest"] }],
    [...rules, rules[0]],
    [{ name: "constructor", type: "boolean" }],
  ])
    expect(parseRouteQueryRules(value)).toBeNull();
});

test("total encoded budget and field count stay bounded independently of per-field limits", () => {
  const rules = parseRouteQueryRules(
    ["a", "b", "c"].map((name) => ({ name, type: "text", max_bytes: 256 })),
  )!;
  expect(
    serializeRouteQuery(rules, {
      a: "中".repeat(85),
      b: "中".repeat(85),
      c: "中".repeat(85),
    }),
  ).toBeNull();
  expect(
    parseRouteQueryRules(
      Array.from({ length: 17 }, (_, n) => ({
        name: "q" + n,
        type: "boolean",
      })),
    ),
  ).toBeNull();
  expect(parseRouteSearch(rules, "?__proto__=polluted")).toBeNull();
});
