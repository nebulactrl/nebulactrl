import { describe, expect, test } from "bun:test";
import { FacetStats } from "./facets";
import { parseLine } from "./line";
import { applySuggestion, type SuggestContext, suggest } from "./suggest";

function context(recent: string[] = []): SuggestContext {
	const stats = new FacetStats();
	const add = (line: string, serviceName: string, times = 1) => {
		for (let i = 0; i < times; i++) stats.add(parseLine({ line, serviceName }));
	};
	add("ERROR boom", "api", 3);
	add("INFO ok", "api", 10);
	add("WARN slow", "web", 1);
	add("INFO fine", "api-worker", 2);
	add('{"duration":512,"user":{"name":"bob"}}', "web", 4);
	add('{"duration":7,"user":{"name":"alice"}}', "web", 1);
	return { facets: stats.facets, attrs: stats.attrs, recent };
}

const labels = (items: { label: string }[]) => items.map((i) => i.label);

describe("suggest: a fresh term", () => {
	test("an empty box offers recent queries, then facet keys that have values", () => {
		const items = suggest("", 0, context(["level:error", "timeout"]));
		expect(labels(items).slice(0, 2)).toEqual(["level:error", "timeout"]);
		expect(items.slice(0, 2).every((i) => i.kind === "recent")).toBe(true);
		const keys = items.filter((i) => i.kind === "key").map((i) => i.label);
		expect(keys).toEqual(["level:", "service:"]);
		// stream, replica, status and deployment have no values yet.
		expect(keys).not.toContain("replica:");
	});

	test("without recents it starts at the keys, then a few attributes", () => {
		const items = suggest("", 0, context());
		expect(items[0]?.kind).toBe("key");
		const attrs = items.filter((i) => i.kind === "attr");
		expect(attrs.length).toBeGreaterThan(0);
		expect(attrs.length).toBeLessThanOrEqual(3);
		expect(attrs[0]?.label).toBe("@duration:");
	});

	test("after a complete term it also offers OR, AND and NOT", () => {
		const items = suggest("level:error ", 12, context());
		const ops = items.filter((i) => i.kind === "operator").map((i) => i.label);
		expect(ops).toEqual(["OR", "AND", "NOT"]);
		expect(items.find((i) => i.label === "OR")).toMatchObject({
			start: 12,
			end: 12,
			insert: "OR ",
		});
	});

	test("at the very start only NOT can open a query", () => {
		const ops = suggest("", 0, context()).filter((i) => i.kind === "operator");
		expect(labels(ops)).toEqual(["NOT"]);
	});

	test("never more than ten", () => {
		expect(
			suggest("", 0, context(Array.from({ length: 10 }, (_, i) => `q${i}`)))
				.length,
		).toBe(10);
	});
});

describe("suggest: a word being typed", () => {
	test("a prefix ranks the matching facet key first and replaces the word", () => {
		const items = suggest("se", 2, context());
		expect(items[0]).toMatchObject({
			kind: "key",
			label: "service:",
			start: 0,
			end: 2,
			insert: "service:",
		});
	});

	test("keys with no values yet still complete, behind those with values", () => {
		const items = suggest("s", 1, context());
		const keys = labels(items.filter((i) => i.kind === "key"));
		expect(keys[0]).toBe("service:");
		expect(keys).toContain("status:");
		expect(keys.indexOf("status:")).toBeGreaterThan(keys.indexOf("service:"));
		expect(items.find((i) => i.label === "status:")?.detail).toBe(
			"no values yet",
		);
	});

	test("a negation prefix stays; only the word is replaced", () => {
		const items = suggest("-lev", 4, context());
		expect(items[0]).toMatchObject({ label: "level:", start: 1, end: 4 });
		expect(applySuggestion("-lev", items[0] as never)).toEqual({
			value: "-level:",
			caret: 7,
		});
	});

	test("@ completes attribute names by count", () => {
		const items = suggest("@", 1, context());
		expect(labels(items)).toEqual(["@duration:", "@user.name:"]);
		expect(suggest("@us", 3, context())[0]).toMatchObject({
			label: "@user.name:",
			start: 0,
			end: 3,
		});
	});

	test("a path fragment matches inside a dotted name", () => {
		expect(labels(suggest("@name", 5, context()))).toEqual(["@user.name:"]);
	});

	test("uppercase O completes OR; lowercase does not", () => {
		const after = suggest("level:error O", 13, context());
		expect(after[0]).toMatchObject({ kind: "operator", label: "OR" });
		expect(labels(after)).toContain("OR");
		expect(labels(suggest("level:error o", 13, context())).includes("OR")).toBe(
			false,
		);
	});

	test("a recent query matches when the box holds one word", () => {
		const items = suggest("err", 3, context(["error timeout", "level:warn"]));
		expect(
			items.some((i) => i.kind === "recent" && i.label === "error timeout"),
		).toBe(true);
		expect(items.some((i) => i.label === "level:warn")).toBe(false);
		const recent = items.find((i) => i.kind === "recent");
		expect(recent).toMatchObject({ start: 0, end: 3, insert: "error timeout" });
	});

	test("nothing matches nothing", () => {
		expect(suggest("zzz", 3, context())).toEqual([]);
	});
});

describe("suggest: values", () => {
	test("after key: values by count, with counts", () => {
		const items = suggest("service:", 8, context());
		expect(labels(items)).toEqual(["api", "web", "api-worker"]);
		expect(items[0]).toMatchObject({
			kind: "value",
			detail: "13",
			count: 13,
			start: 8,
			end: 8,
		});
		expect(items[0]?.insert).toBe("api ");
	});

	test("a typed prefix filters and ranks prefix before contains", () => {
		expect(labels(suggest("service:a", 9, context()))).toEqual([
			"api",
			"api-worker",
		]);
		expect(labels(suggest("service:wor", 11, context()))).toEqual([
			"api-worker",
		]);
		expect(suggest("service:zzz", 11, context())).toEqual([]);
	});

	test("an exact value ranks first", () => {
		expect(labels(suggest("service:api", 11, context()))[0]).toBe("api");
	});

	test("completing replaces the whole value and adds a space at the end", () => {
		const items = suggest("service:ap", 10, context());
		expect(applySuggestion("service:ap", items[0] as never)).toEqual({
			value: "service:api ",
			caret: 12,
		});
	});

	test("no trailing space when text follows", () => {
		const input = "service:ap level:error";
		const items = suggest(input, 10, context());
		expect(applySuggestion(input, items[0] as never).value).toBe(
			"service:api level:error",
		);
	});

	test("values with spaces are quoted", () => {
		const stats = new FacetStats();
		stats.add(parseLine({ line: "x", serviceName: "my svc" }));
		const items = suggest("service:", 8, {
			facets: stats.facets,
			attrs: stats.attrs,
			recent: [],
		});
		expect(items[0]?.insert).toBe('"my svc" ');
	});

	test("caret right after the colon of an existing term offers the values, replacing the old one", () => {
		const input = "service:web level:error";
		const items = suggest(input, 8, context());
		expect(items[0]).toMatchObject({ start: 8, end: 11 });
	});

	test("inside a quoted value the typed part filters", () => {
		expect(labels(suggest('service:"web', 12, context()))).toEqual(["web"]);
	});

	test("attribute values come from the attribute's own values", () => {
		expect(labels(suggest("@user.name:", 11, context()))).toEqual([
			"bob",
			"alice",
		]);
	});

	test("inside key:( … ) it keeps offering that key's values", () => {
		const input = "level:(error OR ";
		const items = suggest(input, input.length, context());
		expect(items[0]).toMatchObject({ kind: "value", start: input.length });
		expect(labels(items)).toEqual(["info", "error", "warn"]);
	});

	test("after a group closes it is a fresh term again", () => {
		const input = "level:(error OR warn) ";
		const items = suggest(input, input.length, context());
		expect(items.some((i) => i.kind === "key")).toBe(true);
		expect(items.some((i) => i.kind === "value")).toBe(false);
	});
});

describe("suggest: numeric fields", () => {
	test("a number-like attribute offers comparisons after the colon", () => {
		const items = suggest("@duration:", 10, context());
		expect(
			items.filter((i) => i.kind === "operator").map((i) => i.label),
		).toEqual([">", ">=", "<", "<="]);
		expect(items.filter((i) => i.kind === "value").map((i) => i.label)).toEqual(
			["512", "7"],
		);
	});

	test("status offers comparisons and a range", () => {
		const stats = new FacetStats();
		stats.add(parseLine({ line: 'x - - [t] "GET /a HTTP/1.1" 404 1' }));
		const items = suggest("status:", 7, {
			facets: stats.facets,
			attrs: stats.attrs,
			recent: [],
		});
		expect(labels(items)).toEqual([
			"404",
			">",
			">=",
			"<",
			"<=",
			"[400 TO 499]",
		]);
	});

	test("a text attribute does not", () => {
		const items = suggest("@user.name:", 11, context());
		expect(items.some((i) => i.kind === "operator")).toBe(false);
	});

	test("once a comparison is typed there is nothing to complete", () => {
		expect(suggest("@duration:>5", 12, context())).toEqual([]);
	});
});

describe("suggest: where there is nothing to say", () => {
	test("inside a closed phrase", () => {
		expect(suggest('"hello wor"', 6, context())).toEqual([]);
	});

	test("right after a closing parenthesis or an operator word", () => {
		expect(suggest("(a)", 3, context())).toEqual([]);
		expect(suggest("a OR", 4, context())).toEqual([]);
	});

	test("inside a range", () => {
		expect(suggest("@n:[1 TO 2]", 6, context())).toEqual([]);
	});
});

describe("applySuggestion", () => {
	test("splices the insert over the range and puts the caret after it", () => {
		const out = applySuggestion("a se b", {
			kind: "key",
			label: "service:",
			insert: "service:",
			start: 2,
			end: 4,
		});
		expect(out).toEqual({ value: "a service: b", caret: 10 });
	});

	test("a recent query replaces the whole box", () => {
		const out = applySuggestion("err", {
			kind: "recent",
			label: "error timeout",
			insert: "error timeout",
			start: 0,
			end: 3,
		});
		expect(out).toEqual({ value: "error timeout", caret: 13 });
	});
});
