import { describe, expect, test } from "bun:test";
import {
	commandSummary,
	defaultProcessName,
	domainProcessName,
	domainProcessOptions,
	hasProcesses,
	NODE_PROCESS_LIMIT,
	nodeProcessSummary,
	type ProcessLike,
	processCardSize,
	processCountText,
	processFilterOptions,
	processReplicaLabel,
	processRow,
	processRows,
	processState,
	replicasOfProcess,
	replicasText,
	resolveProcessFilter,
	serviceProcesses,
	storedDomainProcess,
} from "./model";

function process(over: Partial<ProcessLike> & { name: string }): ProcessLike {
	return {
		default: false,
		port: 0,
		command: ["bundle", "exec", "sidekiq"],
		singleton: false,
		scaling: { replicas: 2, ready: 2 },
		observed: {
			state: "running",
			health: "healthy",
			replicasReady: 2,
			replicasDesired: 2,
		},
		pending: false,
		removed: false,
		...over,
	};
}

const WEB = process({
	name: "web",
	default: true,
	port: 3000,
	command: ["bin/rails", "server"],
});
const WORKER = process({ name: "worker" });
const CLOCK = process({
	name: "clock",
	singleton: true,
	scaling: { replicas: 1, ready: 1 },
	observed: {
		state: "running",
		health: "healthy",
		replicasReady: 1,
		replicasDesired: 1,
	},
});

describe("processes of a service", () => {
	test("a service without processes has none", () => {
		expect(hasProcesses({ processes: [] })).toBe(false);
		expect(serviceProcesses({})).toEqual([]);
		expect(processRows({ processes: [] })).toEqual([]);
	});

	test("the default process is named by processName, else web", () => {
		expect(defaultProcessName({})).toBe("web");
		expect(defaultProcessName({ processName: "" })).toBe("web");
		expect(defaultProcessName({ processName: "app" })).toBe("app");
	});
});

describe("processState", () => {
	test("reads like a stack component's state", () => {
		expect(processState(undefined)).toEqual({
			kind: "pending",
			label: "Not started",
		});
		expect(processState({ state: "running", health: "healthy" })).toEqual({
			kind: "healthy",
			label: "Healthy",
		});
		expect(processState({ state: "running", health: "unhealthy" })).toEqual({
			kind: "failed",
			label: "Unhealthy",
		});
		expect(processState({ state: "running", health: "degraded" })).toEqual({
			kind: "degraded",
			label: "Degraded",
		});
		expect(processState({ state: "running", health: "starting" })).toEqual({
			kind: "building",
			label: "Starting",
		});
		expect(processState({ state: "running", health: "unknown" })).toEqual({
			kind: "live",
			label: "Running",
		});
		expect(processState({ state: "restarting", health: "unknown" }).kind).toBe(
			"degraded",
		);
		expect(processState({ state: "exited", health: "healthy" }).kind).toBe(
			"failed",
		);
		expect(processState({ state: "missing", health: "unknown" }).label).toBe(
			"Missing",
		);
		expect(processState({ state: "odd", health: "" })).toEqual({
			kind: "unknown",
			label: "odd",
		});
	});
});

describe("commandSummary", () => {
	test("quotes the first three arguments like the settings field", () => {
		expect(commandSummary(["bin/rails", "server"])).toBe("bin/rails server");
		expect(commandSummary(["sh", "-c", "echo hi there"])).toBe(
			'sh -c "echo hi there"',
		);
	});

	test("marks arguments beyond the third", () => {
		expect(commandSummary(["a", "b", "c", "d"])).toBe("a b c …");
	});

	test("cuts at 40 characters with an ellipsis", () => {
		const text = commandSummary([
			"bundle",
			"exec",
			"sidekiq-with-a-very-long-name-indeed",
		]);
		expect(text).toHaveLength(40);
		expect(text.endsWith("…")).toBe(true);
	});

	test("no argv means the image's own command", () => {
		expect(commandSummary([])).toBe("image command");
	});
});

describe("replicasText", () => {
	test("is ready over desired", () => {
		expect(replicasText(WORKER)).toBe("2/2");
		expect(
			replicasText(
				process({
					name: "w",
					observed: {
						state: "running",
						health: "degraded",
						replicasReady: 1,
						replicasDesired: 3,
					},
				}),
			),
		).toBe("1/3");
	});

	test("a singleton says so after the count", () => {
		expect(replicasText(CLOCK)).toBe("1/1 · singleton");
	});

	test("an exited singleton reads as not running", () => {
		expect(
			replicasText(
				process({
					name: "clock",
					singleton: true,
					scaling: { replicas: 1, ready: 0 },
					observed: {
						state: "exited",
						health: "",
						replicasReady: 0,
						replicasDesired: 1,
					},
				}),
			),
		).toBe("0/1 · singleton");
	});

	test("a singleton toggled on a process running three still counts them", () => {
		expect(
			replicasText(
				process({
					name: "w",
					singleton: true,
					observed: {
						state: "running",
						health: "healthy",
						replicasReady: 3,
						replicasDesired: 3,
					},
				}),
			),
		).toBe("3/3 · singleton");
	});

	test("a stopped service reads 0/0", () => {
		expect(
			replicasText(
				process({
					name: "w",
					observed: {
						state: "stopped",
						health: "",
						replicasReady: 0,
						replicasDesired: 0,
					},
				}),
			),
		).toBe("0/0");
	});

	test("autoscaling is marked", () => {
		expect(
			replicasText(
				process({
					name: "w",
					scaling: {
						replicas: 2,
						ready: 2,
						autoscaling: { enabled: true },
					},
				}),
			),
		).toBe("2/2 · auto");
		expect(
			replicasText(
				process({
					name: "w",
					scaling: {
						replicas: 2,
						ready: 2,
						autoscaling: { enabled: false },
					},
				}),
			),
		).toBe("2/2");
		expect(
			processRow(
				process({
					name: "w",
					scaling: { replicas: 2, ready: 2, autoscaling: { enabled: true } },
				}),
			).autoscaling,
		).toBe(true);
	});

	test("falls back to the scale before the agent has reported", () => {
		expect(
			replicasText(
				process({
					name: "w",
					observed: null,
					scaling: { replicas: 3, ready: 0 },
				}),
			),
		).toBe("0/3");
	});
});

describe("replicasOfProcess", () => {
	const replica = (processName: string, index: number) => ({
		process: processName,
		index,
	});
	const rows = processRows({
		processes: [WEB, WORKER],
	});

	test("a replica belongs to the row it is labelled with", () => {
		const replicas = [replica("web", 1), replica("worker", 1)];
		expect(replicasOfProcess(rows, replicas, "worker")).toEqual([
			replica("worker", 1),
		]);
		expect(replicasOfProcess(rows, replicas, "web")).toEqual([
			replica("web", 1),
		]);
	});

	test("an unlabelled replica belongs to the default process", () => {
		const replicas = [replica("", 1)];
		expect(replicasOfProcess(rows, replicas, "web")).toEqual(replicas);
		expect(replicasOfProcess(rows, replicas, "worker")).toEqual([]);
	});

	test("a staged rename of the default process keeps its replicas", () => {
		const renamed = processRows({
			processes: [
				process({ name: "app", default: true, pending: true, port: 3000 }),
				WORKER,
			],
		});
		const replicas = [replica("web", 1), replica("worker", 1)];
		expect(replicasOfProcess(renamed, replicas, "app")).toEqual([
			replica("web", 1),
		]);
		expect(replicasOfProcess(renamed, replicas, "worker")).toEqual([
			replica("worker", 1),
		]);
	});

	test("a removed row still owns its replicas", () => {
		const withRemoved = processRows({
			processes: [
				WEB,
				process({ name: "app", default: false, pending: true }),
				process({ name: "old", removed: true }),
			],
		});
		const replicas = [replica("old", 1)];
		expect(replicasOfProcess(withRemoved, replicas, "old")).toEqual(replicas);
		expect(replicasOfProcess(withRemoved, replicas, "web")).toEqual([]);
	});
});

describe("processRow", () => {
	test("carries the words of a row", () => {
		const row = processRow(WEB);
		expect(row).toMatchObject({
			name: "web",
			isDefault: true,
			port: 3000,
			commandSummary: "bin/rails server",
			replicasText: "2/2",
			kind: "healthy",
			stateLabel: "Healthy",
			badge: null,
		});
	});

	test("the default tag appears only for a default process without a port", () => {
		expect(processRow(WEB).showDefaultTag).toBe(false);
		expect(
			processRow(process({ name: "web", default: true, port: 0 }))
				.showDefaultTag,
		).toBe(true);
		expect(processRow(WORKER).showDefaultTag).toBe(false);
	});

	test("a staged process the agent has not seen says staged", () => {
		const row = processRow(
			process({ name: "queue", pending: true, observed: null }),
		);
		expect(row.badge).toBe("staged");
		expect(row.kind).toBe("pending");
		expect(row.stateLabel).toBe("Staged");
	});

	test("a removed process says removed and keeps its running state", () => {
		const row = processRow(process({ name: "old", removed: true }));
		expect(row.badge).toBe("removed");
		expect(row.stateLabel).toBe("Removed");
		expect(row.kind).toBe("healthy");
		expect(
			processRow(process({ name: "old", removed: true, observed: null })).kind,
		).toBe("stopped");
	});
});

describe("nodeProcessSummary", () => {
	test("lists every process in rollout order up to the limit", () => {
		const summary = nodeProcessSummary({ processes: [WEB, WORKER, CLOCK] });
		expect(summary.shown.map((row) => row.name)).toEqual([
			"web",
			"worker",
			"clock",
		]);
		expect(summary.more).toBe(0);
		expect(summary.total).toBe(3);
	});

	test("past the limit failed and degraded processes lead", () => {
		const many = Array.from({ length: NODE_PROCESS_LIMIT + 2 }, (_, index) =>
			process({ name: `p${index}` }),
		);
		const broken = process({
			name: "late",
			observed: {
				state: "running",
				health: "unhealthy",
				replicasReady: 0,
				replicasDesired: 2,
			},
		});
		const summary = nodeProcessSummary({ processes: [...many, broken] });
		expect(summary.shown).toHaveLength(NODE_PROCESS_LIMIT);
		expect(summary.shown[0]?.name).toBe("late");
		expect(summary.more).toBe(3);
		expect(summary.total).toBe(NODE_PROCESS_LIMIT + 3);
	});

	test("at the limit nothing is reordered", () => {
		const exact = Array.from({ length: NODE_PROCESS_LIMIT }, (_, index) =>
			process({ name: `p${index}` }),
		);
		exact[NODE_PROCESS_LIMIT - 1] = process({
			name: "last",
			observed: {
				state: "exited",
				health: "unknown",
				replicasReady: 0,
				replicasDesired: 1,
			},
		});
		const summary = nodeProcessSummary({ processes: exact });
		expect(summary.shown[0]?.name).toBe("p0");
		expect(summary.more).toBe(0);
	});
});

describe("processCardSize", () => {
	test("is header, one row per process and footer", () => {
		expect(processCardSize({ processes: [WEB, WORKER, CLOCK] })).toEqual({
			w: 336,
			h: 52 + 3 * 28 + 44,
		});
	});

	test("an empty card still has its empty line", () => {
		expect(processCardSize({ processes: [] }).h).toBe(52 + 28 + 44);
	});

	test("the more row adds one row's height", () => {
		const many = Array.from({ length: NODE_PROCESS_LIMIT + 1 }, (_, index) =>
			process({ name: `p${index}` }),
		);
		expect(processCardSize({ processes: many }).h).toBe(
			52 + (NODE_PROCESS_LIMIT + 1) * 28 + 44,
		);
	});
});

describe("pickers", () => {
	const all = [WEB, WORKER, process({ name: "next", pending: true })];

	test("the filter lists the processes that exist, default first", () => {
		expect(processFilterOptions({ processes: all })).toEqual([
			{ name: "web", isDefault: true },
			{ name: "worker", isDefault: false },
		]);
	});

	test("a stale name falls back to all processes", () => {
		const options = processFilterOptions({ processes: all });
		expect(resolveProcessFilter(options, "worker")).toBe("worker");
		expect(resolveProcessFilter(options, "gone")).toBeUndefined();
		expect(resolveProcessFilter(options, undefined)).toBeUndefined();
	});

	test("a domain can route to a process with a port", () => {
		expect(domainProcessOptions({ processes: all })).toEqual([
			{ name: "web", port: 3000, isDefault: true },
		]);
	});

	test("a stored empty process is the default process", () => {
		expect(domainProcessName({ processes: all }, "")).toBe("web");
		expect(domainProcessName({ processes: all }, "api")).toBe("api");
		expect(storedDomainProcess({ processes: all }, "web")).toBe("");
		expect(storedDomainProcess({ processes: all }, "api")).toBe("api");
	});
});

describe("labels", () => {
	test("names a replica with its process", () => {
		expect(processReplicaLabel("worker", 1)).toBe("worker · replica 1");
		expect(processReplicaLabel("", 1)).toBe("Replica 1");
	});

	test("counts processes", () => {
		expect(processCountText(1)).toBe("1 process");
		expect(processCountText(3)).toBe("3 processes");
	});
});
