import { describe, expect, test } from "bun:test";
import {
	fuzzyMatch,
	labelMatchPositions,
	type SearchableCommand,
	searchCommands,
} from "./command-search";

function labels(items: readonly SearchableCommand[]): string[] {
	return items.map((item) => item.label);
}

describe("fuzzyMatch", () => {
	test("finds the characters in order, not side by side", () => {
		const found = fuzzyMatch("dpl", "Deployments");
		expect(found?.positions).toEqual([0, 2, 3]);
	});

	test("rejects a needle whose characters are not all there in order", () => {
		expect(fuzzyMatch("zzz", "Deployments")).toBeNull();
		expect(fuzzyMatch("sd", "Deploy")).toBeNull();
	});

	test("ignores case in both directions", () => {
		expect(fuzzyMatch("DEP", "deploy")?.positions).toEqual([0, 1, 2]);
		expect(fuzzyMatch("dep", "DEPLOY")?.positions).toEqual([0, 1, 2]);
	});

	test("a word start beats the same letters mid-word", () => {
		const start = fuzzyMatch("ar", "api-router");
		const middle = fuzzyMatch("ar", "market");
		expect(start && middle && start.score > middle.score).toBe(true);
	});

	test("a contiguous run beats the same letters spread apart", () => {
		const run = fuzzyMatch("dep", "Deploy");
		const spread = fuzzyMatch("dep", "d-e-p-loy");
		expect(run && spread && run.score > spread.score).toBe(true);
	});

	test("a lone letter deep inside a word is noise, but a plain substring counts", () => {
		expect(fuzzyMatch("x", "checkout")).toBeNull();
		expect(fuzzyMatch("kou", "checkout")?.positions).toEqual([4, 5, 6]);
	});

	test("a needle longer than the text cannot match", () => {
		expect(fuzzyMatch("deployments", "deploy")).toBeNull();
	});
});

describe("searchCommands", () => {
	const items: SearchableCommand[] = [
		{ id: "a", label: "Deployments of api" },
		{ id: "b", label: "Add a pool" },
		{ id: "c", label: "Switch to staging" },
		{ id: "d", label: "Restart api" },
		{ id: "e", label: "api-router" },
		{ id: "f", label: "Redeploy" },
	];

	test("an empty query keeps the list's own order", () => {
		expect(labels(searchCommands(items, "  "))).toEqual(labels(items));
	});

	test("an abbreviation finds the command and ranks it first", () => {
		const found = searchCommands(items, "dpl");
		expect(found[0]?.label).toBe("Deployments of api");
		expect(labels(found)).not.toContain("Switch to staging");
	});

	test("a query that opens the label outranks one that only contains it", () => {
		expect(searchCommands(items, "api")[0]?.label).toBe("api-router");
	});

	test("every word must match, in any order, across label and hint", () => {
		const rows: SearchableCommand[] = [
			{ label: "checkout", hint: "shop · production" },
			{ label: "checkout", hint: "shop · development" },
			{ label: "worker", hint: "shop · production" },
		];
		const found = searchCommands(rows, "prod checkout");
		expect(found).toEqual([rows[0] as SearchableCommand]);
	});

	test("keywords find a command without being its label", () => {
		const rows: SearchableCommand[] = [
			{ label: "Processes", keywords: ["health check", "readiness"] },
			{ label: "Scaling", keywords: ["replicas"] },
		];
		expect(labels(searchCommands(rows, "readiness"))).toEqual(["Processes"]);
		expect(labels(searchCommands(rows, "health"))).toEqual(["Processes"]);
	});

	test("the label counts for more than the hint", () => {
		const rows: SearchableCommand[] = [
			{ label: "Open it", hint: "staging" },
			{ label: "staging", hint: "environment" },
		];
		expect(searchCommands(rows, "staging")[0]?.label).toBe("staging");
	});

	test("the most recent command wins a tie, and an older one loses to it", () => {
		const rows: SearchableCommand[] = [
			{ id: "one", label: "Open logs" },
			{ id: "two", label: "Open logs" },
			{ id: "three", label: "Open logs" },
		];
		const ids = (recent: string[]) =>
			searchCommands(rows, "logs", { recent }).map((row) => row.id);
		expect(ids([])).toEqual(["one", "two", "three"]);
		expect(ids(["three", "two"])).toEqual(["three", "two", "one"]);
	});

	test("recency never lifts a worse match over a better one", () => {
		const rows: SearchableCommand[] = [
			{ id: "exact", label: "Logs" },
			{ id: "scattered", label: "Lots of old gear and stuff" },
		];
		const found = searchCommands(rows, "logs", { recent: ["scattered"] });
		expect(found[0]?.id).toBe("exact");
	});

	test("a word-start answer drops the letters-in-order matches", () => {
		const rows: SearchableCommand[] = [
			{ label: "Install modem port" },
			{ label: "Import from Coolify" },
		];
		expect(labels(searchCommands(rows, "import"))).toEqual([
			"Import from Coolify",
		]);
	});

	test("with no word-start answer the query still reads as an abbreviation", () => {
		const rows: SearchableCommand[] = [{ label: "Dashboard access" }];
		expect(labels(searchCommands(rows, "dsa"))).toEqual(["Dashboard access"]);
	});
});

describe("labelMatchPositions", () => {
	test("names the label characters every word of the query landed on", () => {
		expect(labelMatchPositions("Deployments of api", "dpl api")).toEqual([
			0, 2, 3, 15, 16, 17,
		]);
	});

	test("is empty when the query matched only elsewhere", () => {
		expect(labelMatchPositions("Processes", "readiness")).toEqual([]);
	});
});
