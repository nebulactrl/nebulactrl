import { describe, expect, test } from "bun:test";
import { foldRows, type OriginRow } from "./rows";

function rows(shares: readonly number[]): OriginRow[] {
	return shares.map((share, i) => ({
		key: `c${i}`,
		label: `Country ${i}`,
		requests: Math.round(share * 10_000),
		share,
		placeKeys: [`c${i}`],
	}));
}

// 24 countries: five above 1%, the rest a long tail under it.
const TAIL = Array.from({ length: 19 }, () => 0.004);
const ALL = rows([0.4, 0.2, 0.1, 0.05, 0.02, ...TAIL]);

describe("foldRows", () => {
	test("folded: busiest rows of at least 1%, the tail summed into one line", () => {
		const { rows: shown, other } = foldRows(ALL, "Other countries", false);
		expect(shown.map((r) => r.key)).toEqual(["c0", "c1", "c2", "c3", "c4"]);
		expect(other?.label).toBe("Other countries");
		expect(other?.count).toBe(19);
		expect(other?.requests).toBe(19 * 40);
		expect(other?.share).toBeCloseTo(19 * 0.004, 9);
		expect(other?.placeKeys).toHaveLength(19);
	});

	test("never more than eight rows, and the shares still add up", () => {
		const many = rows(Array.from({ length: 12 }, () => 0.08));
		const { rows: shown, other } = foldRows(many, "Other countries", false);
		expect(shown).toHaveLength(8);
		expect(other?.count).toBe(4);
		const total = shown.reduce((n, r) => n + r.share, 0) + (other?.share ?? 0);
		expect(total).toBeCloseTo(0.96, 9);
	});

	test("expanded: every row, nothing folded, tiny shares included", () => {
		const { rows: shown, other } = foldRows(ALL, "Other countries", true);
		expect(shown).toHaveLength(24);
		expect(other).toBeNull();
	});

	test("a single leftover row keeps its own line", () => {
		const nine = rows([0.2, 0.15, 0.12, 0.1, 0.1, 0.08, 0.08, 0.07, 0.05]);
		const { rows: shown, other } = foldRows(nine, "Other countries", false);
		expect(shown).toHaveLength(9);
		expect(other).toBeNull();
	});

	test("a short list needs no other line", () => {
		const { rows: shown, other } = foldRows(rows([0.6, 0.4]), "Other", false);
		expect(shown).toHaveLength(2);
		expect(other).toBeNull();
	});

	test("a list of nothing but tiny rows still names the busiest", () => {
		const tiny = rows(Array.from({ length: 30 }, () => 0.003));
		const { rows: shown, other } = foldRows(tiny, "Other", false);
		expect(shown).toHaveLength(8);
		expect(other?.count).toBe(22);
	});
});
