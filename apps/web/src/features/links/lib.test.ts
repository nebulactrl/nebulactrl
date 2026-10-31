import { describe, expect, test } from "bun:test";
import type { ProposedVariable } from "#/api/gen/types.gen";
import type { ServiceLink } from "#/api/models";
import {
	addedDatabases,
	chosenVariables,
	directionsOf,
	orientLink,
	type ProposalRow,
	rowStates,
	rowsFromProposals,
	wireTargets,
} from "./lib";

const row = (key: string, patch: Partial<ProposalRow> = {}): ProposalRow => ({
	proposed: key,
	key,
	value: "v",
	checked: true,
	...patch,
});

describe("rowStates", () => {
	test("accepts valid unique names", () => {
		const states = rowStates(
			[row("DATABASE_HOST"), row("DATABASE_PORT")],
			new Set(),
		);
		expect(states.map((state) => state.error)).toEqual([undefined, undefined]);
	});
	test("flags an invalid or empty name", () => {
		const states = rowStates(
			[row("1BAD"), row(""), row("HAS-DASH")],
			new Set(),
		);
		expect(states.every((state) => state.error !== undefined)).toBe(true);
	});
	test("flags a name used twice, but only among rows that will be added", () => {
		const states = rowStates(
			[
				row("HOST", { proposed: "A" }),
				row("HOST", { proposed: "B" }),
				row("HOST", { proposed: "C", checked: false }),
			],
			new Set(),
		);
		expect(states[0]?.error).toBeDefined();
		expect(states[1]?.error).toBeDefined();
		expect(states[2]?.error).toBeUndefined();
	});
	test("an unchecked row is never an error", () => {
		expect(
			rowStates([row("", { checked: false })], new Set())[0]?.error,
		).toBeUndefined();
	});
	test("marks a name the service already has as a replacement", () => {
		const states = rowStates(
			[row("DATABASE_HOST"), row("NEW")],
			new Set(["DATABASE_HOST"]),
		);
		expect(states.map((state) => state.replaces)).toEqual([true, false]);
	});
});

describe("chosenVariables", () => {
	test("writes the edited names of the checked rows", () => {
		expect(
			chosenVariables([
				row("DATABASE_HOST", { key: "DB_HOST", value: "postgres" }),
				row("DATABASE_PORT", { checked: false }),
			]),
		).toEqual([{ key: "DB_HOST", value: "postgres" }]);
	});
});

describe("directionsOf", () => {
	const link = (id: string, from: string, to: string): ServiceLink => ({
		id,
		environmentId: "e",
		fromServiceId: from,
		toServiceId: to,
		createdAt: "",
		source: "reference",
		references: null,
		staged: false,
	});
	test("adds the reverse link when the pair is drawn both ways", () => {
		const ab = link("1", "a", "b");
		const ba = link("2", "b", "a");
		expect(
			directionsOf(ab, [ab, ba, link("3", "a", "c")]).map((l) => l.id),
		).toEqual(["1", "2"]);
		expect(directionsOf(ab, [ab]).map((l) => l.id)).toEqual(["1"]);
	});
});

describe("rowsFromProposals", () => {
	const proposal = (
		key: string,
		patch: Partial<ProposedVariable> = {},
	): ProposedVariable => ({
		key,
		value: `\${{db.${key}}}`,
		recommended: true,
		secret: false,
		status: "new",
		...patch,
	});

	test("the recommended set is ticked and the rest is offered unticked", () => {
		const rows = rowsFromProposals([
			proposal("DATABASE_URL", { secret: true }),
			proposal("DATABASE_HOST"),
			proposal("DATABASE_PASSWORD", { recommended: false, secret: true }),
		]);
		expect(rows.map((r) => [r.key, r.checked])).toEqual([
			["DATABASE_URL", true],
			["DATABASE_HOST", true],
			["DATABASE_PASSWORD", false],
		]);
		expect(rows[0]?.secret).toBe(true);
	});

	test("a reference the service already holds is not ticked again", () => {
		const [row] = rowsFromProposals([
			proposal("DATABASE_URL", { status: "unchanged" }),
		]);
		expect(row?.checked).toBe(false);
		expect(row?.status).toBe("unchanged");
	});

	test("a key holding another value is ticked and flagged as a replacement", () => {
		const rows = rowsFromProposals([
			proposal("DATABASE_URL", { status: "replaces" }),
		]);
		expect(rows[0]?.checked).toBe(true);
		expect(rowStates(rows, new Set(["DATABASE_URL"]))[0]?.replaces).toBe(true);
	});

	test("an unchanged row only counts as a replacement once renamed onto a taken name", () => {
		const rows = rowsFromProposals([
			proposal("DATABASE_URL", { status: "unchanged" }),
		]);
		const existing = new Set(["DATABASE_URL", "DB"]);
		expect(rowStates(rows, existing)[0]?.replaces).toBe(false);
		const renamed = rows.map((r) => ({ ...r, key: "DB", checked: true }));
		expect(rowStates(renamed, existing)[0]?.replaces).toBe(true);
	});
});

describe("orientLink", () => {
	test("a database is always the provider", () => {
		expect(orientLink({ kind: "database" }, { kind: "app" })).toBe("reversed");
		expect(orientLink({ kind: "app" }, { kind: "database" })).toBe("forward");
	});
	test("the drawn direction stands between two of a kind", () => {
		expect(orientLink({ kind: "app" }, { kind: "app" })).toBe("forward");
		expect(orientLink({ kind: "app" }, { kind: "worker" })).toBe("forward");
		expect(orientLink({ kind: "database" }, { kind: "database" })).toBe(
			"forward",
		);
	});
});

describe("wireTargets", () => {
	const services = [
		{ id: "web", kind: "app" as const },
		{ id: "api", kind: "app" as const },
		{ id: "jobs", kind: "worker" as const },
		{ id: "pg", kind: "database" as const },
		{ id: "cache", kind: "database" as const },
	];
	test("lists the apps and workers that reach no database yet", () => {
		expect(wireTargets(services, [], { id: "pg" })).toEqual([
			"web",
			"api",
			"jobs",
		]);
	});
	test("an app that already reaches any database is left alone", () => {
		const links = [{ fromServiceId: "api", toServiceId: "cache" }];
		expect(wireTargets(services, links, { id: "pg" })).toEqual(["web", "jobs"]);
	});
	test("a cable between two apps is not a database", () => {
		const links = [{ fromServiceId: "web", toServiceId: "api" }];
		expect(wireTargets(services, links, { id: "pg" })).toEqual([
			"web",
			"api",
			"jobs",
		]);
	});
});

describe("addedDatabases", () => {
	const services = [
		{ id: "web", kind: "app" as const },
		{ id: "pg", kind: "database" as const },
	];
	test("proposes nothing on the first read", () => {
		expect(addedDatabases(null, services)).toEqual([]);
	});
	test("proposes only a database that was not there before", () => {
		expect(addedDatabases(new Set(["web"]), services).map((s) => s.id)).toEqual(
			["pg"],
		);
		expect(addedDatabases(new Set(["web", "pg"]), services)).toEqual([]);
	});
});
