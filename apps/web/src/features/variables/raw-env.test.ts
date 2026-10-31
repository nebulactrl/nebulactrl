// biome-ignore-all lint/suspicious/noTemplateCurlyInString: the product's own `${{ }}` expression syntax, not a JavaScript template
import { describe, expect, test } from "bun:test";
import type { Variable } from "#/api/models";
import { diffEnv, parseEnv, serializeEnv } from "./raw-env";
import { makeVariable } from "./testing";

const plain = (key: string, value: string): Variable =>
	makeVariable({ key, value });
const secret = (key: string): Variable =>
	makeVariable({ key, value: undefined, isSecret: true });

describe("serializeEnv", () => {
	test("leaves secrets out of the raw view", () => {
		const text = serializeEnv([plain("A", "1"), secret("API_KEY")]);
		expect(text).toBe("A=1");
		expect(text).not.toContain("API_KEY");
	});

	test("quotes values that would not survive a round trip", () => {
		const text = serializeEnv([
			plain("MSG", 'hello "world"\nbye'),
			plain("E", ""),
		]);
		expect(text).toBe('MSG="hello \\"world\\"\\nbye"\nE=');
		const parsed = parseEnv(text);
		expect(parsed.errors).toEqual([]);
		expect(parsed.entries).toEqual([
			{ key: "MSG", value: 'hello "world"\nbye' },
			{ key: "E", value: "" },
		]);
	});
});

describe("parseEnv", () => {
	test("reads comments, export, quotes and trailing comments", () => {
		const { entries, errors } = parseEnv(
			[
				"# comment",
				"",
				"export A=1",
				"B = two words # trailing",
				"C='single # kept'",
				'D="dq" # note',
			].join("\n"),
		);
		expect(errors).toEqual([]);
		expect(entries).toEqual([
			{ key: "A", value: "1" },
			{ key: "B", value: "two words" },
			{ key: "C", value: "single # kept" },
			{ key: "D", value: "dq" },
		]);
	});

	test("reports bad lines with their number", () => {
		const { entries, errors } = parseEnv('OK=1\nnope\n1BAD=2\nOK=3\nQ="open');
		expect(entries).toEqual([{ key: "OK", value: "1" }]);
		expect(errors.map((error) => error.line)).toEqual([2, 3, 4, 5]);
	});
});

describe("diffEnv", () => {
	const existing = [plain("A", "1"), plain("B", "2"), secret("API_KEY")];

	test("keeps secrets that the raw text never mentioned", () => {
		const diff = diffEnv(existing, [{ key: "A", value: "1" }]);
		expect(diff.deletes).toEqual(["B"]);
		expect(diff.deletes).not.toContain("API_KEY");
		expect(diff.upserts).toEqual([]);
	});

	test("upserts only what changed, and replaces a named secret as a secret", () => {
		const diff = diffEnv(existing, [
			{ key: "A", value: "9" },
			{ key: "B", value: "2" },
			{ key: "API_KEY", value: "rotated" },
			{ key: "NEW", value: "x" },
		]);
		expect(diff.deletes).toEqual([]);
		expect(diff.upserts).toEqual([
			{ key: "A", value: "9" },
			{ key: "API_KEY", value: "rotated", isSecret: true },
			{ key: "NEW", value: "x" },
		]);
	});
});

describe("multi-line values", () => {
	test("a double-quoted value may span lines", () => {
		const { entries, errors } = parseEnv(
			'KEY="-----BEGIN KEY-----\nabc\n-----END KEY-----"\nNEXT=1',
		);
		expect(errors).toEqual([]);
		expect(entries).toEqual([
			{ key: "KEY", value: "-----BEGIN KEY-----\nabc\n-----END KEY-----" },
			{ key: "NEXT", value: "1" },
		]);
	});

	test("an unclosed quote at the end is still an error on its first line", () => {
		const { entries, errors } = parseEnv('A=1\nB="never closed\nmore');
		expect(entries).toEqual([{ key: "A", value: "1" }]);
		expect(errors).toEqual([
			{ line: 2, message: "The value of B has an unclosed quote." },
		]);
	});

	test("a newline round-trips through the raw view", () => {
		const text = serializeEnv([plain("PEM", "line1\nline2")]);
		expect(parseEnv(text).entries).toEqual([
			{ key: "PEM", value: "line1\nline2" },
		]);
	});
});

describe("generators in the raw editor", () => {
	test("a new generated key is sent without an explicit secret flag", () => {
		const diff = diffEnv([], parseEnv("TOKEN=${{secret(32)}}").entries);
		expect(diff.upserts).toEqual([{ key: "TOKEN", value: "${{secret(32)}}" }]);
	});
});
