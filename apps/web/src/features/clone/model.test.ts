import { describe, expect, test } from "bun:test";
import type { CloneResult, MoveResult } from "#/api/gen/types.gen";
import {
	conflictHosts,
	defaultSuffix,
	domainOutcomeText,
	moveVerdict,
	normalizeSuffix,
	previewHostname,
	referenceText,
	toClonePlan,
	toMovePlan,
} from "./model";

const env = (name: string) => ({
	id: name,
	projectId: "p",
	name,
	slug: name,
	isProduction: false,
});

describe("clone plan", () => {
	test("null slices become empty arrays", () => {
		const plan = toClonePlan({
			dryRun: true,
			links: 0,
			sharedVariables: 0,
			domains: null,
			references: null,
			services: null,
			skipped: null,
			warnings: null,
		} satisfies CloneResult);
		expect(plan.services).toEqual([]);
		expect(plan.domains).toEqual([]);
		expect(plan.warnings).toEqual([]);
	});

	test("conflicts name the hostname that is taken", () => {
		const plan = toClonePlan({
			dryRun: true,
			links: 0,
			sharedVariables: 0,
			services: [],
			skipped: [],
			references: [],
			warnings: [],
			domains: [
				{
					service: "api",
					hostname: "api.example.com",
					to: "api-copy.example.com",
					action: "conflict",
				},
				{ service: "api", hostname: "shop.io", action: "skipped" },
			],
		});
		expect(conflictHosts(plan)).toEqual(["api-copy.example.com"]);
	});

	test("domain outcomes read as words", () => {
		expect(
			domainOutcomeText({ service: "a", hostname: "a.b.c", action: "dropped" }),
		).toBe("not cloned");
		expect(
			domainOutcomeText({
				service: "a",
				hostname: "a.b.c",
				to: "a-x.b.c",
				action: "suffixed",
			}),
		).toBe("a-x.b.c");
		expect(
			domainOutcomeText({
				service: "a",
				hostname: "b.io",
				action: "skipped",
				reason: "An apex domain cannot be suffixed.",
			}),
		).toBe("An apex domain cannot be suffixed.");
	});
});

describe("suffixes", () => {
	test("defaults follow the environment", () => {
		expect(defaultSuffix(true, "production")).toBe("-copy");
		expect(defaultSuffix(false, "staging")).toBe("-staging");
	});

	test("input is normalised like the server does", () => {
		expect(normalizeSuffix("")).toBe("");
		expect(normalizeSuffix(" QA ")).toBe("-qa");
		expect(normalizeSuffix("-qa")).toBe("-qa");
	});

	test("the first label gets the suffix; apex names do not", () => {
		expect(previewHostname("app.example.com", "-copy")).toBe(
			"app-copy.example.com",
		);
		expect(previewHostname("example.com", "-copy")).toBeNull();
	});
});

describe("move plan", () => {
	const base: MoveResult = {
		dryRun: true,
		applied: false,
		blocked: false,
		from: env("production"),
		to: env("staging"),
		slug: "api",
		slugChanged: false,
		outgoing: null,
		incoming: null,
		links: null,
		warnings: null,
	};

	test("a plain move is clean", () => {
		expect(moveVerdict(toMovePlan(base))).toEqual({
			tone: "clean",
			text: "Nothing breaks.",
		});
	});

	test("links and a new slug are notes, not a block", () => {
		expect(moveVerdict(toMovePlan({ ...base, links: ["postgres"] })).tone).toBe(
			"notes",
		);
		expect(moveVerdict(toMovePlan({ ...base, slugChanged: true })).tone).toBe(
			"notes",
		);
	});

	test("broken references block and are counted", () => {
		const plan = toMovePlan({
			...base,
			blocked: true,
			outgoing: [
				{
					service: "api",
					key: "DATABASE_URL",
					ref: "postgres.DATABASE_URL",
					status: "unresolved",
					message: "postgres stays in production",
				},
			],
			incoming: [{ service: "web", keys: ["API_URL"] }],
		});
		expect(moveVerdict(plan)).toEqual({
			tone: "blocked",
			text: "This move breaks 2 references.",
		});
		expect(referenceText(plan.outgoing[0] as never)).toBe(
			"api · DATABASE_URL → postgres.DATABASE_URL",
		);
	});
});
