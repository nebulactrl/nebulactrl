import { describe, expect, test } from "bun:test";
import type {
	PromotePlan,
	ReplicaPlan,
	ReplicationView,
} from "#/api/gen/types.gen";
import {
	canConfirmAdd,
	canConfirmPromote,
	cardChip,
	dataLossText,
	defaultReplicaServer,
	exportRole,
	formatLagTime,
	groupConsumers,
	isBusyState,
	isPromotable,
	isRebuildable,
	isSameServerPick,
	lagPercent,
	lagText,
	orderExports,
	PANEL_CAVEAT,
	PANEL_PURPOSE,
	PANEL_TITLE,
	placementLine,
	promotionLine,
	replicationPollInterval,
	SAME_SERVER_NOTE,
	shouldOpenAdvanced,
	shownState,
	stateInfo,
	suggestReplicaName,
	supportsReplicas,
	walInfo,
} from "./model";

const MIB = 1024 * 1024;

const addPlan: ReplicaPlan = {
	serverId: "s2",
	serverName: "hetzner-02",
	sameServer: false,
	devicesAdded: 1,
	links: [{ from: "s2", to: "s1", purpose: "replication" }],
	blockers: null,
	warnings: null,
};

const promotePlan: PromotePlan = {
	mode: "switchover",
	lagBytes: 0,
	replica: { id: "r", slug: "pg-replica" },
	oldPrimary: { id: "p", slug: "pg", serverName: "hetzner-01" },
	consumers: [],
	rebuildReplicas: [],
	schedulesMoving: 0,
	blockers: null,
	warnings: null,
};

describe("states", () => {
	test("every saga and observation state has words, an unknown one reads as unknown", () => {
		expect(stateInfo("streaming").label).toBe("Streaming");
		expect(stateInfo("needs_rebuild").label).toBe("Needs rebuild");
		expect(stateInfo("something-new").label).toBe("Unknown");
	});

	test("an observation older than a minute is shown as unknown, a saga state never is", () => {
		const now = Date.parse("2026-10-27T12:00:00Z");
		const old = "2026-10-27T11:58:00Z";
		const fresh = "2026-10-27T11:59:30Z";
		expect(shownState("streaming", old, now)).toBe("unknown");
		expect(shownState("lagging", fresh, now)).toBe("lagging");
		expect(shownState("cloning", old, now)).toBe("cloning");
		expect(shownState("streaming", null, now)).toBe("streaming");
	});

	test("what the buttons offer follows the state", () => {
		expect(isPromotable("streaming")).toBe(true);
		expect(isPromotable("lagging")).toBe(true);
		expect(isPromotable("broken")).toBe(true);
		expect(isPromotable("needs_rebuild")).toBe(false);
		expect(isRebuildable("needs_rebuild")).toBe(true);
		expect(isRebuildable("streaming")).toBe(false);
		expect(isBusyState("cloning")).toBe(true);
		expect(isBusyState("streaming")).toBe(false);
	});
});

describe("lag", () => {
	test("bytes and time in one phrase, and no number when nothing was measured", () => {
		expect(lagText({ lagBytes: null, replayLagMs: null })).toBe(
			"Lag not measured yet",
		);
		expect(lagText({ lagBytes: 0, replayLagMs: 3 })).toBe("In sync");
		expect(lagText({ lagBytes: 12 * MIB, replayLagMs: 400 })).toBe(
			"12 MiB behind · 400 ms",
		);
		expect(lagText({ lagBytes: 2 * MIB })).toBe("2 MiB behind");
	});

	test("the time reads in ms, s and min", () => {
		expect(formatLagTime(40)).toBe("40 ms");
		expect(formatLagTime(2500)).toBe("2.5 s");
		expect(formatLagTime(120_000)).toBe("2 min");
	});

	test("the bar fills toward the 64 MiB lagging line and stops there", () => {
		expect(lagPercent(null)).toBe(0);
		expect(lagPercent(0)).toBe(0);
		expect(lagPercent(32 * MIB)).toBe(50);
		expect(lagPercent(640 * MIB)).toBe(100);
	});

	test("wal status words come from the slot", () => {
		expect(walInfo(null)).toBeNull();
		expect(walInfo("reserved")?.tone).toBe("ok");
		expect(walInfo("lost")?.tone).toBe("bad");
		expect(walInfo("odd")?.label).toBe("WAL odd");
	});
});

describe("polling", () => {
	const row = {
		id: "r",
		slug: "r",
		serverId: "s",
		serverName: "s",
		state: "streaming",
		message: "",
	};
	const idle: ReplicationView = {
		role: "primary",
		replicas: [row],
		rebuildable: false,
		canPromote: false,
	};
	test("fast while a replica is being created or a promote runs, slow otherwise", () => {
		expect(replicationPollInterval(undefined, 3000, 15000)).toBe(15000);
		expect(replicationPollInterval(idle, 3000, 15000)).toBe(15000);
		const cloning = { ...idle, replicas: [{ ...row, state: "cloning" }] };
		expect(replicationPollInterval(cloning, 3000, 15000)).toBe(3000);
		const promoting: ReplicationView = {
			...idle,
			promotion: {
				id: "x",
				status: "running",
				step: "freeze",
				mode: "switchover",
				message: "",
				startedAt: "2026-10-27T12:00:00Z",
			},
		};
		expect(replicationPollInterval(promoting, 3000, 15000)).toBe(3000);
	});
});

describe("add a replica", () => {
	test("blockers lock it, a mesh device needs the consent", () => {
		expect(canConfirmAdd(undefined, true)).toBe(false);
		expect(canConfirmAdd(addPlan, false)).toBe(false);
		expect(canConfirmAdd(addPlan, true)).toBe(true);
		expect(
			canConfirmAdd(
				{
					...addPlan,
					blockers: [{ code: "mesh_server_required", message: "x" }],
				},
				true,
			),
		).toBe(false);
		expect(
			canConfirmAdd({ ...addPlan, sameServer: true, devicesAdded: 0 }, false),
		).toBe(true);
	});

	test("the placement line says what the mesh costs", () => {
		expect(
			placementLine({ ...addPlan, sameServer: true, devicesAdded: 0 }),
		).toContain("Same server");
		expect(placementLine(addPlan)).toContain("adds 1 mesh device");
		expect(placementLine({ ...addPlan, devicesAdded: 2 })).toContain(
			"2 mesh devices",
		);
	});

	test("the suggested name skips taken ones", () => {
		expect(suggestReplicaName("pg", [])).toBe("pg-replica");
		expect(suggestReplicaName("pg", ["pg-replica"])).toBe("pg-replica-2");
		expect(suggestReplicaName("pg", ["pg-replica", "pg-replica-2"])).toBe(
			"pg-replica-3",
		);
	});
});

describe("promote", () => {
	test("a switchover needs the slug; a forced promote also needs the data-loss tick", () => {
		expect(canConfirmPromote(undefined, "pg-replica", "pg-replica", true)).toBe(
			false,
		);
		expect(canConfirmPromote(promotePlan, "pg", "pg-replica", false)).toBe(
			false,
		);
		expect(
			canConfirmPromote(promotePlan, "pg-replica", "pg-replica", false),
		).toBe(true);
		const forced = { ...promotePlan, mode: "forced" as const };
		expect(canConfirmPromote(forced, "pg-replica", "pg-replica", false)).toBe(
			false,
		);
		expect(canConfirmPromote(forced, "pg-replica", "pg-replica", true)).toBe(
			true,
		);
		expect(
			canConfirmPromote(
				{
					...forced,
					blockers: [{ code: "promote_in_progress", message: "x" }],
				},
				"pg-replica",
				"pg-replica",
				true,
			),
		).toBe(false);
	});

	test("the data-loss sentence states the lag or that it is unknown", () => {
		expect(
			dataLossText({ ...promotePlan, mode: "forced", lagBytes: 3 * MIB }),
		).toContain("3 MiB");
		const { lagBytes: _lag, ...withoutLag } = promotePlan;
		expect(dataLossText({ ...withoutLag, mode: "forced" })).toContain(
			"unknown",
		);
		expect(
			dataLossText({ ...promotePlan, dataLossWarning: "Server says so." }),
		).toBe("Server says so.");
	});

	test("consumers are grouped per server and their devices summed", () => {
		const groups = groupConsumers([
			{
				serviceId: "a",
				slug: "api",
				serverName: "b-host",
				keys: ["DATABASE_WRITE_URL"],
				devicesAdded: 1,
				warnings: null,
			},
			{
				serviceId: "b",
				slug: "worker",
				serverName: "a-host",
				keys: null,
				devicesAdded: 0,
				warnings: null,
			},
			{
				serviceId: "c",
				slug: "cron",
				serverName: "b-host",
				keys: null,
				devicesAdded: 1,
				warnings: null,
			},
		]);
		expect(groups.map((g) => g.server)).toEqual(["a-host", "b-host"]);
		expect(groups[1]?.devicesAdded).toBe(2);
		expect(groupConsumers(null)).toEqual([]);
	});

	test("the promotion line", () => {
		expect(promotionLine(undefined)).toBeNull();
		const base = {
			id: "x",
			step: "freeze",
			mode: "forced" as const,
			message: "",
			startedAt: "2026-10-27T12:00:00Z",
		};
		expect(promotionLine({ ...base, status: "running" })?.text).toContain(
			"forced promote",
		);
		expect(promotionLine({ ...base, status: "failed" })?.tone).toBe("failed");
		expect(
			promotionLine({ ...base, status: "succeeded", message: "Done." })?.text,
		).toBe("Done.");
	});
});

describe("exports", () => {
	test("writer and reader keys are recognised and sorted after the service's own", () => {
		expect(exportRole("DATABASE_WRITE_URL")).toBe("writer");
		expect(exportRole("DATABASE_READ_HOSTS")).toBe("reader");
		expect(exportRole("DATABASE_URL")).toBeNull();
		const ordered = orderExports([
			{ key: "DATABASE_READ_URL" },
			{ key: "DATABASE_URL" },
			{ key: "DATABASE_WRITE_URL" },
			{ key: "PGHOST" },
			{ key: "DATABASE_READ_HOST" },
		]).map((entry) => entry.key);
		expect(ordered).toEqual([
			"DATABASE_URL",
			"PGHOST",
			"DATABASE_WRITE_URL",
			"DATABASE_READ_URL",
			"DATABASE_READ_HOST",
		]);
	});
});

describe("card chips", () => {
	test("primary counts, replica names its primary, fenced warns", () => {
		expect(cardChip({ role: "primary", replicaCount: 1 }).label).toBe(
			"Primary · 1 replica",
		);
		expect(cardChip({ role: "primary", replicaCount: 3 }).label).toBe(
			"Primary · 3 replicas",
		);
		const replica = cardChip({
			role: "replica",
			replicaCount: 0,
			primarySlug: "pg",
			state: "lagging",
		});
		expect(replica.label).toBe("Replica of pg");
		expect(replica.tone).toBe("warn");
		expect(cardChip({ role: "fenced", replicaCount: 0 }).tone).toBe("bad");
	});
});

describe("the Advanced disclosure", () => {
	const view = (
		role: ReplicationView["role"],
		replicas: ReplicationView["replicas"] = null,
		promotion?: ReplicationView["promotion"],
	) => ({ role, replicas, ...(promotion ? { promotion } : {}) });

	test("a standalone database starts collapsed, and so does one still loading", () => {
		expect(shouldOpenAdvanced(view("standalone"))).toBe(false);
		expect(shouldOpenAdvanced(view("standalone", []))).toBe(false);
		expect(shouldOpenAdvanced(undefined)).toBe(false);
		expect(shouldOpenAdvanced(null)).toBe(false);
	});

	test("existing state is never hidden: replicas, a replica, a fenced primary, a running promote", () => {
		const member = { id: "r", slug: "pg-replica" } as NonNullable<
			ReplicationView["replicas"]
		>[number];
		expect(shouldOpenAdvanced(view("primary", [member]))).toBe(true);
		expect(shouldOpenAdvanced(view("replica"))).toBe(true);
		expect(shouldOpenAdvanced(view("fenced"))).toBe(true);
		expect(
			shouldOpenAdvanced(
				view("standalone", null, {
					status: "running",
				} as NonNullable<ReplicationView["promotion"]>),
			),
		).toBe(true);
	});

	test("only Postgres offers the panel", () => {
		expect(supportsReplicas("postgres")).toBe(true);
		for (const engine of ["mysql", "mariadb", "redis", "mongodb", ""]) {
			expect(supportsReplicas(engine)).toBe(false);
		}
	});
});

describe("the standby framing", () => {
	test("says what it is for and what it is not", () => {
		expect(PANEL_TITLE).toBe("Standby replica");
		expect(PANEL_PURPOSE).toContain("another server");
		expect(PANEL_PURPOSE).toContain("promote by hand");
		expect(PANEL_PURPOSE).toContain("read-only queries");
		expect(PANEL_CAVEAT).toBe(
			"Not a backup, and it does not take over by itself: keep your scheduled backups.",
		);
	});
});

describe("same-server placement", () => {
	test("the note is calm and says it only helps with reads", () => {
		expect(SAME_SERVER_NOTE).toBe(
			"On the same server a replica shares the primary's disk and host. It only helps with read load, not with losing the server.",
		);
	});

	test("the plan decides once it has arrived, the ids before that", () => {
		const other = { ...addPlan, serverId: "s2", sameServer: false };
		const same = { ...addPlan, serverId: "s1", sameServer: true };
		expect(isSameServerPick(same, "s1", "s1")).toBe(true);
		expect(isSameServerPick(other, "s2", "s1")).toBe(false);
		// No plan yet: compare the ids, so the note does not appear late.
		expect(isSameServerPick(undefined, "s1", "s1")).toBe(true);
		expect(isSameServerPick(undefined, "s2", "s1")).toBe(false);
		// A plan for a server that is no longer picked is not used.
		expect(isSameServerPick(other, "s1", "s1")).toBe(true);
		expect(isSameServerPick(undefined, "", "s1")).toBe(false);
		expect(isSameServerPick(undefined, "s1", null)).toBe(false);
	});

	test("the default pick is another meshed server, else the primary's own", () => {
		const online = [{ id: "s1" }, { id: "s2" }, { id: "s3" }];
		expect(defaultReplicaServer(online, "s1", new Set(["s3"]))).toBe("s3");
		expect(defaultReplicaServer(online, "s1", new Set(["s1", "s2"]))).toBe(
			"s2",
		);
		// Another server that is not in the mesh cannot host it yet.
		expect(defaultReplicaServer(online, "s1", new Set())).toBe("s1");
		expect(defaultReplicaServer(online, "s1", new Set(["s1"]))).toBe("s1");
		// The primary's server is offline and nothing is meshed: first online.
		expect(defaultReplicaServer([{ id: "s2" }], "s1", new Set())).toBe("s2");
		expect(defaultReplicaServer([], "s1", new Set(["s2"]))).toBe("");
	});
});
