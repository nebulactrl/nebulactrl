import { describe, expect, test } from "bun:test";
import type { Environment, PreviewSettings } from "#/api/gen/types.gen";
import {
	draftFrom,
	draftToBody,
	groupEnvironments,
	hasErrors,
	isDirty,
	newSeed,
	previewLabel,
	validateDraft,
} from "./model";

function env(partial: Partial<Environment> & { id: string }): Environment {
	return {
		projectId: "p1",
		name: partial.id,
		slug: partial.id,
		isProduction: false,
		createdAt: "2026-10-26T12:00:00Z",
		kind: "standard",
		parentEnvironmentId: null,
		prNumber: null,
		prTitle: "",
		...partial,
	};
}

describe("groupEnvironments", () => {
	const production = env({ id: "prod", isProduction: true });
	const staging = env({ id: "staging" });
	const pr3 = env({
		id: "pr-3",
		kind: "preview",
		parentEnvironmentId: "prod",
		prNumber: 3,
	});
	const pr12 = env({
		id: "pr-12",
		kind: "preview",
		parentEnvironmentId: "prod",
		prNumber: 12,
	});
	const stray = env({
		id: "pr-9",
		kind: "preview",
		parentEnvironmentId: "gone",
		prNumber: 9,
	});

	test("nests previews under their base, newest PR first", () => {
		const { groups, detached } = groupEnvironments([
			production,
			staging,
			pr3,
			pr12,
		]);
		expect(groups.map((g) => g.base.id)).toEqual(["prod", "staging"]);
		expect(groups[0]?.previews.map((p) => p.id)).toEqual(["pr-12", "pr-3"]);
		expect(groups[1]?.previews).toEqual([]);
		expect(detached).toEqual([]);
	});

	test("keeps previews of a missing base visible", () => {
		const { groups, detached } = groupEnvironments([production, stray]);
		expect(groups[0]?.previews).toEqual([]);
		expect(detached.map((p) => p.id)).toEqual(["pr-9"]);
	});
});

describe("previewLabel", () => {
	test("uses the server's name, else the PR number, else the slug", () => {
		expect(previewLabel(env({ id: "x", name: "PR #12 · Add checkout" }))).toBe(
			"PR #12 · Add checkout",
		);
		expect(previewLabel(env({ id: "x", name: "", prNumber: 7 }))).toBe("PR #7");
		expect(previewLabel(env({ id: "x", name: "", slug: "pr-x" }))).toBe("pr-x");
	});
});

describe("settings draft", () => {
	const settings: PreviewSettings = {
		projectId: "p1",
		enabled: true,
		baseEnvironmentId: "prod",
		forkPrs: false,
		maxConcurrent: 5,
		idleDays: 7,
		seedCommands: [
			{ service: "api", command: "npm run migrate", runOn: "create" },
		],
		excludedServices: ["web", "redis"],
		updatedAt: null,
	};

	test("round-trips through the PUT body", () => {
		const body = draftToBody(draftFrom(settings));
		expect(body).toEqual({
			enabled: true,
			baseEnvironmentId: "prod",
			forkPrs: false,
			maxConcurrent: 5,
			idleDays: 7,
			excludedServices: ["redis", "web"],
			seedCommands: [
				{ service: "api", command: "npm run migrate", runOn: "create" },
			],
		});
	});

	test("is dirty only when something changed", () => {
		const draft = draftFrom(settings);
		expect(isDirty(draft, draftFrom(settings))).toBe(false);
		expect(isDirty(draft, { ...draft, forkPrs: true })).toBe(true);
		expect(isDirty(draft, { ...draft, idleDays: " 7 " })).toBe(false);
	});

	test("validates limits, base and seeds", () => {
		const draft = draftFrom(settings);
		expect(hasErrors(validateDraft(draft))).toBe(false);
		expect(validateDraft({ ...draft, maxConcurrent: "0" }).max).toBeDefined();
		expect(validateDraft({ ...draft, maxConcurrent: "51" }).max).toBeDefined();
		expect(validateDraft({ ...draft, idleDays: "x" }).idle).toBeDefined();
		expect(
			validateDraft({ ...draft, baseEnvironmentId: null }).base,
		).toBeDefined();
		// A disabled project needs no base yet.
		expect(
			validateDraft({ ...draft, enabled: false, baseEnvironmentId: null }).base,
		).toBeUndefined();

		const seed = newSeed("web");
		const errors = validateDraft({ ...draft, seeds: [seed] });
		expect(errors.seeds?.[seed.key]).toContain("excluded");
		const empty = newSeed("api");
		expect(
			validateDraft({ ...draft, seeds: [empty] }).seeds?.[empty.key],
		).toContain("command");
		const none = newSeed();
		expect(
			validateDraft({ ...draft, seeds: [none] }).seeds?.[none.key],
		).toContain("service");
	});
});
