import { describe, expect, test } from "bun:test";
import { readdirSync, readFileSync } from "node:fs";
import { join, relative } from "node:path";

// NebulaCtrl has manual read replicas and a manual promote. It does not detect
// failures and does not switch by itself, so no UI copy, documentation or
// comment may claim high availability. The words that describe what it is
// not are fine ("no automatic failover"); a line that uses them without a
// negation is a claim, and this test fails on it.

const CLAIM_UPPER = /\bHA\b/;
const CLAIM_WORDS = /\b(high[- ]availability|fail-?over|fail over)\b/i;
const NEGATION =
	/\b(no|not|never|without|neither|nor|none|cannot|can't|isn't|aren't|doesn't|don't|won't|nothing)\b/i;

/** The lines of a text that claim availability the product does not give. */
export function findClaims(text: string): { line: number; text: string }[] {
	const claims: { line: number; text: string }[] = [];
	text.split("\n").forEach((line, index) => {
		if (!CLAIM_UPPER.test(line) && !CLAIM_WORDS.test(line)) return;
		if (NEGATION.test(line)) return;
		claims.push({ line: index + 1, text: line.trim() });
	});
	return claims;
}

const ROOT = join(import.meta.dir, "..", "..", "..", "..", "..");
const SELF = join(import.meta.dir, "no-ha-claims.test.ts");

/** Files that list the limits and may name the words freely. */
const EXEMPT = [join("docs", "operations", "limitations.md")];

const SKIP_DIRS = ["node_modules", "gen", "dist", ".git"];

function files(dir: string, extensions: readonly string[]): string[] {
	const found: string[] = [];
	for (const entry of readdirSync(dir, { withFileTypes: true })) {
		if (entry.isDirectory()) {
			if (!SKIP_DIRS.includes(entry.name)) {
				found.push(...files(join(dir, entry.name), extensions));
			}
		} else if (extensions.some((ext) => entry.name.endsWith(ext))) {
			found.push(join(dir, entry.name));
		}
	}
	return found;
}

function scan(dir: string, extensions: readonly string[]): string[] {
	const problems: string[] = [];
	for (const file of files(dir, extensions)) {
		const name = relative(ROOT, file);
		if (file === SELF || EXEMPT.includes(name)) continue;
		for (const claim of findClaims(readFileSync(file, "utf8"))) {
			problems.push(`${name}:${claim.line}: ${claim.text}`);
		}
	}
	return problems;
}

describe("the guard", () => {
	test("catches a claim, lets a denial through", () => {
		expect(findClaims("Automatic failover keeps the app up.")).toHaveLength(1);
		expect(findClaims("Enable HA for production.")).toHaveLength(1);
		expect(findClaims("Built for high availability.")).toHaveLength(1);
		expect(findClaims("Built for High-Availability.")).toHaveLength(1);
		expect(findClaims("There is no automatic failover.")).toHaveLength(0);
		expect(
			findClaims("NebulaCtrl does not do failover or high availability."),
		).toHaveLength(0);
		expect(findClaims("Manual promote of a read replica.")).toHaveLength(0);
		expect(findClaims("const ha = hay.charAt(1);")).toHaveLength(0);
	});
});

describe("no HA claims", () => {
	test("the web app says manual promote, never HA, high availability or failover", () => {
		expect(
			scan(join(ROOT, "apps", "web", "src"), [".ts", ".tsx", ".md"]),
		).toEqual([]);
	});

	test("the docs say manual promote, never HA, high availability or failover", () => {
		expect(scan(join(ROOT, "docs"), [".md", ".mdx"])).toEqual([]);
	});
});
