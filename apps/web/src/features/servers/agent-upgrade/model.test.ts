import { describe, expect, test } from "bun:test";
import type {
	InstanceUpgradeView,
	ServerUpgradeView,
} from "#/api/gen/types.gen";
import {
	anyRunning,
	behindSummary,
	preUpgradeBackupLine,
	rolloutSummary,
	serverUpgrade,
	skewLabel,
	upgradePresentation,
	upgradeRunning,
} from "./model";

function server(over: Partial<ServerUpgradeView> = {}): ServerUpgradeView {
	return {
		serverId: "s1",
		name: "hetzner-01",
		status: "online",
		agentVersion: "1.4.0",
		skew: "behind",
		canSelfUpgrade: true,
		upgradable: true,
		...over,
	};
}

function view(over: Partial<InstanceUpgradeView> = {}): InstanceUpgradeView {
	return {
		controlPlaneVersion: "1.5.0",
		agentAutoUpgrade: true,
		behind: 0,
		upgradable: 0,
		servers: [],
		...over,
	};
}

describe("skewLabel", () => {
	test("names the target version when behind and says what is not comparable", () => {
		expect(skewLabel("behind", "1.5.0")).toBe("Behind 1.5.0");
		expect(skewLabel("current", "1.5.0")).toBe("Up to date");
		expect(skewLabel("ahead", "1.5.0")).toBe("Newer than the control plane");
		expect(skewLabel("unknown", "1.5.0")).toBe("Version not comparable");
	});
});

describe("upgradePresentation", () => {
	test("each state has words and an icon kind", () => {
		expect(
			upgradePresentation(server({ state: "queued", targetVersion: "1.5.0" })),
		).toEqual({ kind: "pending", label: "Upgrade to 1.5.0 queued" });
		expect(
			upgradePresentation(
				server({ state: "restarting", targetVersion: "1.5.0" }),
			)?.kind,
		).toBe("deploying");
		expect(
			upgradePresentation(
				server({ state: "succeeded", trigger: "auto", targetVersion: "1.5.0" }),
			)?.label,
		).toBe("Upgraded to 1.5.0 automatically");
		expect(
			upgradePresentation(
				server({
					state: "succeeded",
					trigger: "manual",
					targetVersion: "1.5.0",
				}),
			)?.label,
		).toBe("Upgraded to 1.5.0");
		expect(
			upgradePresentation(server({ state: "failed", targetVersion: "1.5.0" }))
				?.kind,
		).toBe("failed");
		expect(upgradePresentation(server())).toBeNull();
	});
});

describe("running", () => {
	test("queued and restarting count, finished states do not", () => {
		expect(upgradeRunning(server({ state: "queued" }))).toBe(true);
		expect(upgradeRunning(server({ state: "restarting" }))).toBe(true);
		expect(upgradeRunning(server({ state: "failed" }))).toBe(false);
		expect(upgradeRunning(server())).toBe(false);
		expect(anyRunning(undefined)).toBe(false);
		expect(
			anyRunning(view({ servers: [server(), server({ state: "queued" })] })),
		).toBe(true);
	});
});

describe("summaries", () => {
	test("behind count in words", () => {
		expect(behindSummary(view())).toBeNull();
		expect(behindSummary(view({ behind: 1 }))).toBe("1 agent is behind 1.5.0");
		expect(behindSummary(view({ behind: 3 }))).toBe(
			"3 agents are behind 1.5.0",
		);
	});
	test("the rollout confirmation says it is staged and stops at a failure", () => {
		const s = rolloutSummary(view({ upgradable: 2 }));
		expect(s).toContain("2 servers, one at a time");
		expect(s).toContain("first failure stops the rollout");
		expect(rolloutSummary(view({ upgradable: 1 }))).toContain("1 server,");
	});
	test("lookup by server", () => {
		const v = view({
			servers: [server({ serverId: "a" }), server({ serverId: "b" })],
		});
		expect(serverUpgrade(v, "b")?.serverId).toBe("b");
		expect(serverUpgrade(v, "x")).toBeUndefined();
		expect(serverUpgrade(undefined, "a")).toBeUndefined();
	});
});

describe("preUpgradeBackupLine", () => {
	const at = "2026-10-28T10:00:00Z";
	test("succeeded, forced, skipped and failed all say what happened", () => {
		expect(
			preUpgradeBackupLine({
				at,
				status: "succeeded",
				fromVersion: "1.4.0",
				toVersion: "1.5.0",
			}),
		).toEqual({
			kind: "healthy",
			label: "Backed up before the upgrade (1.4.0 → 1.5.0)",
		});
		expect(
			preUpgradeBackupLine({ at, status: "forced", toVersion: "1.5.0" }),
		).toEqual({
			kind: "warning",
			label: "Upgraded without a backup, forced (to 1.5.0)",
		});
		expect(preUpgradeBackupLine({ at, status: "skipped" }).kind).toBe(
			"warning",
		);
		expect(preUpgradeBackupLine({ at, status: "failed" }).kind).toBe("failed");
	});
});
