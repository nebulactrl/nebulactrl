import { describe, expect, test } from "bun:test";
import type { ProcessConfig } from "#/api/models";
import {
	buildProcessesInput,
	buildProcessInput,
	MAX_NAMED_PROCESSES,
	newProcessDraft,
	nextProcessName,
	type ProcessDraft,
	processBlocker,
	processErrors,
	processNameError,
	renameProcesses,
	toProcessDraft,
	toProcessDrafts,
} from "./draft";

const WORKER: ProcessConfig = {
	name: "worker",
	startCommand: ["bundle", "exec", "sidekiq", "-C", "config/my sidekiq.yml"],
	stopGraceSeconds: 120,
	healthcheck: {
		type: "none",
		intervalSeconds: 2,
		timeoutSeconds: 3,
		startPeriodSeconds: 0,
		deadlineSeconds: 120,
		successThreshold: 2,
	},
};

const CONTEXT = { defaultName: "", defaultPort: "3000" };

function draft(over: Partial<ProcessDraft> = {}): ProcessDraft {
	return { ...toProcessDraft(WORKER), ...over };
}

describe("toProcessDraft", () => {
	test("keeps numbers as text and the command quoted", () => {
		const row = toProcessDraft(WORKER);
		expect(row.originalName).toBe("worker");
		expect(row.command).toBe('bundle exec sidekiq -C "config/my sidekiq.yml"');
		expect(row.stopGraceSeconds).toBe("120");
		expect(row.port).toBe("");
		expect(row.memoryMb).toBe("");
		expect(row.health.type).toBe("none");
		expect(row.health.deadlineSeconds).toBe("120");
	});

	test("a saved process round trips to the request shape", () => {
		const [input] = buildProcessesInput(toProcessDrafts([WORKER])) ?? [];
		expect(input).toEqual({
			name: "worker",
			startCommand: [
				"bundle",
				"exec",
				"sidekiq",
				"-C",
				"config/my sidekiq.yml",
			],
			stopGraceSeconds: 120,
			healthcheck: {
				type: "none",
				intervalSeconds: 2,
				timeoutSeconds: 3,
				startPeriodSeconds: 0,
				deadlineSeconds: 120,
				successThreshold: 2,
			},
		});
	});

	test("no processes send nothing", () => {
		expect(buildProcessesInput([])).toBeUndefined();
	});
});

describe("buildProcessInput", () => {
	test("sends the optional fields only when they are set", () => {
		const input = buildProcessInput(
			draft({
				port: "9000",
				singleton: true,
				memoryMb: "256",
				cpus: "0.5",
				stopGraceSeconds: "",
				health: { ...draft().health, type: "http", path: " /up ", port: "" },
			}),
		);
		expect(input.port).toBe(9000);
		expect(input.singleton).toBe(true);
		expect(input.memoryMb).toBe(256);
		expect(input.cpus).toBe(0.5);
		expect(input).not.toHaveProperty("stopGraceSeconds");
		expect(input.healthcheck?.path).toBe("/up");
		expect(input.healthcheck).not.toHaveProperty("port");
	});

	test("an exec health check carries its argv", () => {
		const input = buildProcessInput(
			draft({
				health: {
					...draft().health,
					type: "exec",
					command: 'CMD-SHELL "pg_isready -U app"',
				},
			}),
		);
		expect(input.healthcheck?.command).toEqual([
			"CMD-SHELL",
			"pg_isready -U app",
		]);
	});
});

describe("renameProcesses", () => {
	test("maps old to new for a renamed saved process", () => {
		expect(renameProcesses([draft({ name: "queue" })])).toEqual({
			worker: "queue",
		});
	});

	test("ignores unchanged and new rows", () => {
		expect(
			renameProcesses([draft(), newProcessDraft("clock")]),
		).toBeUndefined();
	});
});

describe("nextProcessName", () => {
	test("prefills worker, then worker-2", () => {
		expect(nextProcessName([])).toBe("worker");
		expect(nextProcessName(["worker"])).toBe("worker-2");
		expect(nextProcessName(["worker", "worker-2"])).toBe("worker-3");
		expect(nextProcessName(["web"])).toBe("worker");
	});
});

describe("processNameError", () => {
	test("accepts DNS-label names of 1 to 31 characters", () => {
		expect(processNameError("worker")).toBeNull();
		expect(processNameError("a")).toBeNull();
		expect(processNameError("queue-2")).toBeNull();
		expect(processNameError("a".repeat(31))).toBeNull();
	});

	test("refuses the rest", () => {
		expect(processNameError("")).toBe("Name is required.");
		expect(processNameError("Worker")).not.toBeNull();
		expect(processNameError("-worker")).not.toBeNull();
		expect(processNameError("worker-")).not.toBeNull();
		expect(processNameError("a_b")).not.toBeNull();
		expect(processNameError("a".repeat(32))).not.toBeNull();
	});
});

describe("processErrors", () => {
	test("a valid process has none", () => {
		expect(processErrors([draft()], CONTEXT)).toEqual({});
		expect(processBlocker([draft()], CONTEXT)).toBeNull();
	});

	test("the name must be valid, unique and not the default process'", () => {
		expect(processErrors([draft({ name: "" })], CONTEXT).worker?.name).toBe(
			"Name is required.",
		);
		const web = processErrors([draft({ name: "web" })], CONTEXT).worker?.name;
		expect(web).toContain("default process");
		const custom = processErrors([draft({ name: "app" })], {
			...CONTEXT,
			defaultName: "app",
		}).worker?.name;
		expect(custom).toContain("default process");
		const twice = processErrors(
			[draft(), draft({ key: "other", originalName: null })],
			CONTEXT,
		);
		expect(twice.worker?.name).toContain("already called worker");
		expect(twice.other?.name).toContain("already called worker");
	});

	test("a start command is required", () => {
		expect(
			processErrors([draft({ command: "  " })], CONTEXT).worker?.command,
		).toContain("start command is required");
	});

	test("ports must be unique across the default and the other processes", () => {
		const clash = processErrors([draft({ port: "3000" })], CONTEXT);
		expect(clash.worker?.port).toContain("also used by web");
		const between = processErrors(
			[
				draft({ port: "4000" }),
				draft({ key: "b", name: "queue", originalName: null, port: "4000" }),
			],
			{ ...CONTEXT, defaultPort: "" },
		);
		expect(between.worker?.port).toContain("also used by queue");
		expect(between.b?.port).toContain("also used by worker");
		expect(
			processErrors([draft({ port: "70000" })], CONTEXT).worker?.port,
		).toContain("between 1 and 65535");
	});

	test("limits keep their ranges", () => {
		expect(
			processErrors([draft({ stopGraceSeconds: "0" })], CONTEXT).worker
				?.stopGraceSeconds,
		).toContain("between 1 and 3600");
		expect(
			processErrors([draft({ memoryMb: "2" })], CONTEXT).worker?.memoryMb,
		).toContain("between 6");
		expect(
			processErrors([draft({ cpus: "-1" })], CONTEXT).worker?.cpus,
		).toContain("0.01 and 256");
		// A blank limit inherits the service's.
		expect(
			processErrors(
				[draft({ stopGraceSeconds: "", memoryMb: "", cpus: "" })],
				CONTEXT,
			),
		).toEqual({});
	});

	test("an HTTP or TCP health check needs a port", () => {
		const http = draft({
			health: { ...draft().health, type: "http" },
		});
		expect(processErrors([http], CONTEXT).worker?.health).toContain("port");
		expect(processErrors([{ ...http, port: "9000" }], CONTEXT)).toEqual({});
		const exec = draft({
			health: { ...draft().health, type: "exec", command: "pg_isready" },
		});
		expect(processErrors([exec], CONTEXT).worker?.health).toContain("CMD");
	});

	test("two saved processes cannot swap names in one save", () => {
		const a = toProcessDraft({ ...WORKER, name: "a" });
		const b = toProcessDraft({ ...WORKER, name: "b" });
		const errors = processErrors(
			[
				{ ...a, name: "b" },
				{ ...b, name: "a" },
			],
			CONTEXT,
		);
		expect(errors.a?.name).toContain("being renamed");
	});
});

describe("processBlocker", () => {
	test("names the process and the first problem", () => {
		expect(processBlocker([draft({ command: "" })], CONTEXT)).toBe(
			"worker: A start command is required: a process has no image command to fall back on.",
		);
	});

	test("refuses more processes than the service may have", () => {
		const rows = Array.from({ length: MAX_NAMED_PROCESSES + 1 }, (_, index) =>
			draft({ key: `p${index}`, name: `p${index}`, originalName: null }),
		);
		expect(processBlocker(rows, CONTEXT)).toContain("at most 10 processes");
	});
});
