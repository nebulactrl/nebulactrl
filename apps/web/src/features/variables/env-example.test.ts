import { describe, expect, test } from "bun:test";
import type { SuggestedVariable } from "#/api/gen/types.gen";
import {
	bannerText,
	checkedCount,
	describeValue,
	emptyCount,
	exampleItems,
	initialChoices,
	mergeChoices,
	orderForReview,
	toWrites,
} from "./env-example";

// The product's own reference syntax, kept out of template literals' way.
const OPEN = "$" + "{{";

function item(over: Partial<SuggestedVariable>): SuggestedVariable {
	return {
		key: "KEY",
		value: "",
		source: "env-example",
		reason: "",
		proposedSecret: false,
		needsInput: false,
		warnings: [],
		...over,
	};
}

const items: SuggestedVariable[] = [
	item({ key: "APP_NAME", value: "demo", comment: "Page title" }),
	item({ key: "DATABASE_URL", value: `${OPEN}postgres.DATABASE_URL}}` }),
	item({
		key: "SECRET_KEY",
		value: `${OPEN}secret(32)}}`,
		proposedSecret: true,
	}),
	item({ key: "SENTRY_DSN", value: "", needsInput: true }),
];

describe("exampleItems", () => {
	test("keeps the keys that come from example files", () => {
		const mixed = [...items, item({ key: "REDIS_URL", source: "database" })];
		expect(exampleItems(mixed).map((i) => i.key)).toEqual([
			"APP_NAME",
			"DATABASE_URL",
			"SECRET_KEY",
			"SENTRY_DSN",
		]);
		expect(exampleItems(null)).toEqual([]);
		expect(exampleItems(undefined)).toEqual([]);
	});
});

describe("orderForReview", () => {
	test("puts the keys that need a value first and keeps the file order otherwise", () => {
		expect(orderForReview(items).map((i) => i.key)).toEqual([
			"SENTRY_DSN",
			"APP_NAME",
			"DATABASE_URL",
			"SECRET_KEY",
		]);
	});
});

describe("choices", () => {
	test("every key starts checked with its proposed value", () => {
		const choices = initialChoices(items);
		expect(checkedCount(items, choices)).toBe(4);
		expect(choices.APP_NAME).toEqual({ checked: true, value: "demo" });
		expect(choices.SENTRY_DSN).toEqual({ checked: true, value: "" });
	});

	test("a checked key that needs a value and has none is counted as empty", () => {
		let choices = initialChoices(items);
		expect(emptyCount(items, choices)).toBe(1);
		choices = { ...choices, SENTRY_DSN: { checked: true, value: "  " } };
		expect(emptyCount(items, choices)).toBe(1);
		choices = { ...choices, SENTRY_DSN: { checked: true, value: "https://x" } };
		expect(emptyCount(items, choices)).toBe(0);
		choices = { ...choices, SENTRY_DSN: { checked: false, value: "" } };
		expect(emptyCount(items, choices)).toBe(0);
		expect(checkedCount(items, choices)).toBe(3);
	});

	test("a re-read keeps what the user decided and checks new keys", () => {
		const first = {
			...initialChoices(items),
			APP_NAME: { checked: false, value: "mine" },
		};
		const next = [...items, item({ key: "NEW_FLAG", value: "on" })];
		const merged = mergeChoices(next, first);
		expect(merged.APP_NAME).toEqual({ checked: false, value: "mine" });
		expect(merged.NEW_FLAG).toEqual({ checked: true, value: "on" });
		const fewer = mergeChoices(items.slice(0, 1), first);
		expect(Object.keys(fewer)).toEqual(["APP_NAME"]);
	});
});

describe("toWrites", () => {
	test("stages the checked keys as suggested, secrets as secrets", () => {
		let choices = initialChoices(items);
		choices = {
			...choices,
			DATABASE_URL: { checked: false, value: items[1]?.value ?? "" },
			SENTRY_DSN: { checked: true, value: "https://sentry.example/1" },
		};
		expect(toWrites(items, choices)).toEqual([
			{ key: "APP_NAME", value: "demo", source: "suggested" },
			{
				key: "SECRET_KEY",
				value: `${OPEN}secret(32)}}`,
				source: "suggested",
				isSecret: true,
			},
			{
				key: "SENTRY_DSN",
				value: "https://sentry.example/1",
				source: "suggested",
			},
		]);
	});

	test("a key left empty is still staged, empty", () => {
		const writes = toWrites(items, initialChoices(items));
		expect(writes.find((w) => w.key === "SENTRY_DSN")).toEqual({
			key: "SENTRY_DSN",
			value: "",
			source: "suggested",
		});
	});

	test("nothing checked stages nothing", () => {
		const none = Object.fromEntries(
			items.map((i) => [i.key, { checked: false, value: i.value }]),
		);
		expect(toWrites(items, none)).toEqual([]);
	});
});

describe("describeValue", () => {
	test("tells generators, references and plain text apart", () => {
		expect(describeValue(`${OPEN}secret(64)}}`)).toEqual({ kind: "generated" });
		expect(describeValue(`${OPEN}randomBytes(32)}}`)).toEqual({
			kind: "generated",
		});
		expect(describeValue(`${OPEN}postgres.DATABASE_URL}}`)).toEqual({
			kind: "reference",
			target: "postgres.DATABASE_URL",
		});
		expect(describeValue("demo")).toEqual({ kind: "literal", text: "demo" });
		expect(describeValue("")).toEqual({ kind: "literal", text: "" });
	});
});

describe("bannerText", () => {
	test("names the count and the file, singular and plural", () => {
		expect(bannerText(3, [".env.example"])).toBe(
			"3 keys from .env.example aren't set",
		);
		expect(bannerText(1, [".env.sample"])).toBe(
			"1 key from .env.sample isn't set",
		);
		expect(bannerText(2, [".env.example", "api/.env.sample"])).toBe(
			"2 keys from .env.example aren't set",
		);
		expect(bannerText(2, [])).toBe("2 keys from .env.example aren't set");
	});
});
