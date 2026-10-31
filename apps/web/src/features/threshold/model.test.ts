import { describe, expect, test } from "bun:test";
import type { Team } from "#/api/gen/types.gen";
import {
	GIVE_UP_MS,
	type UpdateInfo,
	type UpdateRun,
} from "#/features/settings/update-model";
import {
	deletingCurrentTeam,
	deletionOutcome,
	deletionStepLabel,
	deletionStepRows,
	thresholdView,
} from "./model";

const NOW = Date.parse("2026-10-30T12:00:00Z");

function info(updating?: UpdateRun): UpdateInfo {
	return {
		current: "0.1.0",
		available: true,
		checkEnabled: true,
		canUpdate: true,
		isAdmin: true,
		manualCommand: "curl -fsSL https://nebulactrl.com/install.sh | sudo bash",
		...(updating ? { updating } : {}),
	};
}

function run(overrides: Partial<UpdateRun> = {}): UpdateRun {
	return {
		state: "running",
		fromVersion: "0.1.0",
		targetVersion: "0.1.2",
		startedAt: new Date(NOW - 60_000).toISOString(),
		...overrides,
	};
}

function team(overrides: Partial<Team> = {}): Team {
	return {
		id: "t1",
		name: "Acme",
		slug: "acme",
		role: "owner",
		memberCount: 1,
		current: true,
		createdAt: "2026-10-01T00:00:00Z",
		...overrides,
	};
}

const deleting = (step?: "projects" | "servers" | "integrations" | "team") =>
	team({
		deletion: {
			state: "deleting",
			keepDnsRecords: false,
			forgottenServers: null,
			...(step ? { step } : {}),
		},
	});

const view = (over: Partial<Parameters<typeof thresholdView>[0]> = {}) =>
	thresholdView({
		update: undefined,
		updateErrorStatus: undefined,
		now: NOW,
		teams: [],
		...over,
	});

describe("update restart", () => {
	test("a running update takes the screen", () => {
		const v = view({ update: info(run()) });
		expect(v?.kind).toBe("update");
		expect(v?.kind === "update" && v.restarting).toBe(false);
	});

	test("a dead connection or proxy 502 while it runs means restarting", () => {
		for (const status of [0, 502, 503, 504]) {
			const v = view({ update: info(run()), updateErrorStatus: status });
			expect(v?.kind === "update" && v.restarting).toBe(true);
		}
		const other = view({ update: info(run()), updateErrorStatus: 500 });
		expect(other?.kind === "update" && other.restarting).toBe(false);
	});

	test("an update that is not running hands back to the app", () => {
		expect(view({ update: info(run({ state: "failed" })) })).toBeNull();
		expect(view({ update: info() })).toBeNull();
		expect(view({ update: undefined })).toBeNull();
	});

	test("a failed read alone is no restart screen", () => {
		expect(view({ update: info(), updateErrorStatus: 502 })).toBeNull();
	});

	test("a run past the give-up window hands back to the app", () => {
		const stalled = run({
			startedAt: new Date(NOW - GIVE_UP_MS - 1_000).toISOString(),
		});
		expect(view({ update: info(stalled) })).toBeNull();
	});
});

describe("team deletion", () => {
	test("shows only for the current team while it is being deleted", () => {
		const v = view({ teams: [deleting()] });
		expect(v?.kind === "team-deletion" && v.team.id).toBe("t1");
	});

	test("a team that is not the current one stays inline", () => {
		expect(view({ teams: [team(), { ...deleting(), current: false }] })).toBe(
			null,
		);
	});

	test("a failed deletion stays inline", () => {
		const failed = team({
			deletion: {
				state: "failed",
				error: "x",
				keepDnsRecords: false,
				forgottenServers: null,
			},
		});
		expect(view({ teams: [failed] })).toBeNull();
	});

	test("the update wins over a deletion", () => {
		const v = view({ update: info(run()), teams: [deleting()] });
		expect(v?.kind).toBe("update");
	});

	test("a deletion shows once the update has stalled", () => {
		const stalled = run({
			startedAt: new Date(NOW - GIVE_UP_MS - 1_000).toISOString(),
		});
		const v = view({ update: info(stalled), teams: [deleting()] });
		expect(v?.kind).toBe("team-deletion");
	});

	test("deletingCurrentTeam picks the current team only", () => {
		expect(deletingCurrentTeam([team()])).toBeUndefined();
		expect(deletingCurrentTeam([deleting()])?.id).toBe("t1");
	});
});

describe("deletionOutcome", () => {
	test("running while listed and deleting", () => {
		expect(deletionOutcome("t1", [deleting("team")])).toBe("running");
	});
	test("gone once the list no longer has it", () => {
		expect(deletionOutcome("t1", [team({ id: "t2" })])).toBe("gone");
		expect(deletionOutcome("t1", [])).toBe("gone");
	});
	test("stopped when listed but no longer deleting", () => {
		expect(deletionOutcome("t1", [team()])).toBe("stopped");
	});
});

describe("deletion steps", () => {
	test("rows mark done, current and upcoming steps", () => {
		expect(deletionStepRows("servers").map((r) => r.state)).toEqual([
			"done",
			"current",
			"upcoming",
			"upcoming",
		]);
		expect(deletionStepRows("team").map((r) => r.state)).toEqual([
			"done",
			"done",
			"done",
			"current",
		]);
	});

	test("before the job names a step, the first is current", () => {
		expect(deletionStepRows(undefined)[0]?.state).toBe("current");
	});

	test("labels read as words, with a fallback", () => {
		expect(deletionStepLabel("servers")).toBe("Emptying and removing servers");
		expect(deletionStepLabel(undefined)).toBe("Working");
	});
});
