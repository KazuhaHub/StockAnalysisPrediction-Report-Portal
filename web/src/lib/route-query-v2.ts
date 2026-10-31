// Pure signed query validation shared by descriptor tooling, host and SDK.
import {
  DESCRIPTOR_LIMITS as limits,
  DESCRIPTOR_PATTERNS as patterns,
  type RouteQuery,
} from "./descriptor-contract.generated";
export type RouteQueryRule = Readonly<Omit<RouteQuery, "values">> & {
  readonly values?: readonly string[];
};
export type RouteQueryValue = string | number | boolean;
export type RouteQueryValues = Readonly<Record<string, RouteQueryValue>>;
export const ROUTE_SEARCH_MAX_BYTES = 2048;
const own = (value: unknown): value is Record<string, unknown> =>
  !!value && typeof value === "object" && !Array.isArray(value);
const exact = (value: Record<string, unknown>, keys: string[]) =>
  Object.keys(value).length === keys.length &&
  keys.every((key) => Object.prototype.hasOwnProperty.call(value, key));
const matches = (value: unknown, pattern: string): value is string =>
  typeof value === "string" && new RegExp(pattern).exec(value)?.[0] === value;
const integer = (value: unknown, min: number, max: number): value is number =>
  typeof value === "number" &&
  Number.isSafeInteger(value) &&
  value >= min &&
  value <= max;
export function parseRouteQueryRules(
  value: unknown,
): readonly RouteQueryRule[] | null {
  if (value == null) return Object.freeze([]);
  if (!Array.isArray(value) || value.length > limits.maxRouteQueryFields)
    return null;
  const names = new Set<string>(),
    result: RouteQueryRule[] = [];
  for (const rule of value) {
    if (
      !own(rule) ||
      !matches(rule.name, patterns.queryName) ||
      ["constructor", "prototype"].includes(rule.name) ||
      names.has(rule.name)
    )
      return null;
    names.add(rule.name);
    if (rule.type === "text") {
      if (
        !exact(rule, ["name", "type", "max_bytes"]) ||
        !integer(rule.max_bytes, 1, limits.maxRouteQueryTextBytes)
      )
        return null;
      result.push(
        Object.freeze({
          name: rule.name,
          type: rule.type,
          max_bytes: rule.max_bytes,
        }),
      );
    } else if (rule.type === "integer") {
      if (
        !exact(rule, ["name", "type", "min", "max"]) ||
        !integer(
          rule.min,
          -limits.maxRouteQueryInteger,
          limits.maxRouteQueryInteger,
        ) ||
        !integer(rule.max, rule.min, limits.maxRouteQueryInteger)
      )
        return null;
      result.push(
        Object.freeze({
          name: rule.name,
          type: rule.type,
          min: rule.min,
          max: rule.max,
        }),
      );
    } else if (rule.type === "boolean") {
      if (!exact(rule, ["name", "type"])) return null;
      result.push(Object.freeze({ name: rule.name, type: rule.type }));
    } else if (rule.type === "enum") {
      if (
        !exact(rule, ["name", "type", "values"]) ||
        !Array.isArray(rule.values) ||
        rule.values.length < 1 ||
        rule.values.length > limits.maxRouteQueryValues ||
        !rule.values.every((value) => matches(value, patterns.queryEnum)) ||
        new Set(rule.values).size !== rule.values.length
      )
        return null;
      result.push(
        Object.freeze({
          name: rule.name,
          type: rule.type,
          values: Object.freeze([...rule.values]),
        }),
      );
    } else return null;
  }
  return Object.freeze(result);
}
function validValue(
  rule: RouteQueryRule,
  value: unknown,
): value is RouteQueryValue {
  switch (rule.type) {
    case "text":
      return (
        typeof value === "string" &&
        !/[\x00-\x1f\x7f\uD800-\uDFFF]/u.test(value) &&
        new TextEncoder().encode(value).length <= rule.max_bytes!
      );
    case "integer":
      return integer(value, rule.min!, rule.max!);
    case "boolean":
      return typeof value === "boolean";
    case "enum":
      return typeof value === "string" && rule.values!.includes(value);
    default:
      return false;
  }
}
export function serializeRouteQuery(
  rules: readonly RouteQueryRule[],
  value: unknown,
): { search: string; query: RouteQueryValues } | null {
  if (!own(value) || Object.keys(value).length > rules.length) return null;
  const byName = new Map(rules.map((rule) => [rule.name, rule])),
    params = new URLSearchParams();
  const query: Record<string, RouteQueryValue> = Object.create(null);
  for (const [key, item] of Object.entries(value)) {
    const rule = byName.get(key);
    if (!rule || !validValue(rule, item)) return null;
    params.append(key, String(item));
    query[key] = item;
  }
  params.sort();
  const encoded = params.toString(),
    search = encoded ? "?" + encoded : "";
  return search.length <= ROUTE_SEARCH_MAX_BYTES
    ? { search, query: Object.freeze(query) }
    : null;
}
export function parseRouteSearch(
  rules: readonly RouteQueryRule[],
  search: unknown,
): { search: string; query: RouteQueryValues } | null {
  if (
    typeof search !== "string" ||
    search.length > ROUTE_SEARCH_MAX_BYTES ||
    (search !== "" && !search.startsWith("?"))
  )
    return null;
  const byName = new Map(rules.map((rule) => [rule.name, rule])),
    query: Record<string, RouteQueryValue> = Object.create(null);
  for (const [key, raw] of new URLSearchParams(search)) {
    const rule = byName.get(key);
    if (!rule || Object.prototype.hasOwnProperty.call(query, key)) return null;
    let value: RouteQueryValue = raw;
    if (rule.type === "integer") {
      if (/^(0|-?[1-9][0-9]{0,9})$/.exec(raw)?.[0] !== raw) return null;
      value = Number(raw);
    } else if (rule.type === "boolean") {
      if (raw !== "true" && raw !== "false") return null;
      value = raw === "true";
    }
    if (!validValue(rule, value)) return null;
    query[key] = value;
  }
  const normalized = serializeRouteQuery(rules, query);
  return normalized?.search === search ? normalized : null;
}
