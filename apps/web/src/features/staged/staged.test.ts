import { describe, expect, test } from "bun:test";
import type { DeploymentStatus, LatestDeployment } from "#/api/models";
import {
	changesOf,
	hasInFlightDeployment,
	reviewGroups,
	SECRET_MASK,
	staleStaged,
} from "./staged";

const web = { id: "a", name: "Web", slug: "web" };
const db = { id: "b", name: "DB", slug: "db" };

describe("counting", () => {
	test("review lines carry a sign and the API field per kind of change", () => {
		const [group] = reviewGroups([
			{
				service: web,
				changes: [
					{ field: "variables.A", from: "", to: "1" },
					{ field: "variables.B", from: "2", to: "" },
					{ field: "config.port", from: "80", to: "81" },
					{ field: "variables.KEY", from: SECRET_MASK, to: SECRET_MASK },
				],
			},
		]);
		expect(group?.name).toBe("Web");
		expect(group?.lines.map((line) => line.sign)).toEqual(["+", "−", "~", "~"]);
		expect(group?.lines.map((line) => line.field)).toEqual([
			"variables.A",
			"variables.B",
			"config.port",
			"variables.KEY",
		]);
		expect(group?.lines[3]?.note).toBe("secret");
		expect(reviewGroups([{ service: db, changes: [] }])).toEqual([]);
	});

	test("a never-deployed service reads as a first deployment", () => {
		const [group] = reviewGroups([
			{ service: web, changes: [{ field: "deployment", from: "", to: "" }] },
		]);
		expect(group?.lines[0]?.text).toBe("First deployment");
	});
});

describe("hasInFlightDeployment", () => {
	const withStatus = (status: DeploymentStatus | undefined) => ({
		latestDeployment:
			status === undefined ? null : ({ status } as LatestDeployment),
	});
	test("is true until the latest deployment reaches a terminal status", () => {
		expect(hasInFlightDeployment(withStatus("building"))).toBe(true);
		expect(hasInFlightDeployment(withStatus("verifying"))).toBe(true);
		expect(hasInFlightDeployment(withStatus("succeeded"))).toBe(false);
		expect(hasInFlightDeployment(withStatus("failed"))).toBe(false);
		expect(hasInFlightDeployment(withStatus(undefined))).toBe(false);
	});
});

describe("volume swaps", () => {
	test("a restore reads as one line with its label, no arrow", () => {
		const [group] = reviewGroups([
			{
				service: web,
				changes: [
					{
						field: "volumes.data",
						from: "/data",
						to: "/data · restored from Jan 12, 03:30",
					},
					{ field: "volumes.logs", from: "", to: "/var/log" },
				],
			},
		]);
		expect(group?.lines[0]).toMatchObject({
			sign: "~",
			text: "volumes.data",
			value: "/data · restored from Jan 12, 03:30",
		});
		expect(group?.lines[1]).toMatchObject({ sign: "+", value: "/var/log" });
	});
});

describe("source switch", () => {
	test("reads as one Source line from the old type to the new", () => {
		const [group] = reviewGroups([
			{
				service: web,
				changes: [
					{
						field: "source",
						from: "image",
						to: "Git (github.com/acme/shop, main, Railpack)",
					},
				],
			},
		]);
		expect(group?.lines).toHaveLength(1);
		expect(group?.lines[0]).toMatchObject({
			field: "source",
			sign: "~",
			text: "Source",
			value: "image → Git (github.com/acme/shop, main, Railpack)",
		});
	});
});

describe("a staged flag with no change left", () => {
	const counts = (variables: number, settings: number) => ({
		variables,
		settings,
	});

	test("a service's changes are its variable and setting counts", () => {
		expect(changesOf({ pendingChangeCounts: counts(2, 1) })).toBe(3);
		expect(changesOf({ pendingChangeCounts: counts(0, 0) })).toBe(0);
	});

	test("a flagged service with zero changes is stale, not a change", () => {
		const real = { hasStagedChanges: true, pendingChangeCounts: counts(0, 1) };
		const ghost = { hasStagedChanges: true, pendingChangeCounts: counts(0, 0) };
		const clean = {
			hasStagedChanges: false,
			pendingChangeCounts: counts(0, 0),
		};
		expect(staleStaged([real, ghost, clean])).toEqual([ghost]);
	});
});

describe("process changes", () => {
	const lines = (changes: { field: string; from: string; to: string }[]) =>
		reviewGroups([{ service: web, changes }])[0]?.lines ?? [];

	test("a process added or removed reads as a sentence with what it runs", () => {
		const [added, removed] = lines([
			{
				field: "config.processes.worker",
				from: "",
				to: '{"name":"worker","startCommand":["bin/jobs","--queue","default"],"port":0}',
			},
			{
				field: "config.processes.clock",
				from: '{"name":"clock","startCommand":["bin/clock"],"port":9000}',
				to: "",
			},
		]);
		expect(added).toMatchObject({
			sign: "+",
			text: "Process worker added",
			value: "bin/jobs --queue default",
			field: "config.processes.worker",
		});
		expect(removed).toMatchObject({
			sign: "−",
			text: "Process clock removed",
			value: "bin/clock · port 9000",
		});
	});

	test("a process that cannot be parsed still reads as added", () => {
		const [line] = lines([
			{ field: "config.processes.worker", from: "", to: "not json" },
		]);
		expect(line?.text).toBe("Process worker added");
		expect(line?.value).toBeUndefined();
	});

	test("a changed setting names the process and the setting", () => {
		const result = lines([
			{ field: "config.processes.worker.port", from: "3000", to: "3001" },
			{
				field: "config.processes.worker.stopGraceSeconds",
				from: "",
				to: "90",
			},
			{ field: "config.processes.worker.singleton", from: "false", to: "true" },
			{ field: "config.processes.worker.memoryMb", from: "512", to: "" },
			{ field: "config.processes.worker.cpus", from: "1", to: "2" },
			{
				field: "config.processes.worker.healthcheck.path",
				from: "/",
				to: "/up",
			},
		]);
		expect(result.map((line) => [line.sign, line.text, line.value])).toEqual([
			["~", "Port of worker", "3000 → 3001"],
			["+", "Stop grace period of worker", "90"],
			["~", "Singleton of worker", "false → true"],
			["−", "Memory (MB) of worker", "512"],
			["~", "CPUs of worker", "1 → 2"],
			["~", "Health check path of worker", "/ → /up"],
		]);
	});

	test("a start command is shown the way the settings field types it", () => {
		const [line] = lines([
			{
				field: "config.processes.worker.startCommand",
				from: '["bin/jobs"]',
				to: '["bin/jobs","--name","my app"]',
			},
		]);
		expect(line).toMatchObject({
			text: "Start command of worker",
			value: 'bin/jobs → bin/jobs --name "my app"',
		});
	});

	test("the default process' own fields read in words", () => {
		const [name, singleton] = lines([
			{ field: "config.processName", from: "", to: "api" },
			{ field: "config.singleton", from: "", to: "true" },
		]);
		expect(name).toMatchObject({
			text: "Default process name",
			value: "web → api",
		});
		expect(singleton).toMatchObject({
			text: "Default process singleton",
			value: "false → true",
		});
	});

	test("the API field stays on the line, so it can be discarded", () => {
		const [line] = lines([
			{ field: "config.processes.worker.port", from: "1", to: "2" },
		]);
		expect(line?.field).toBe("config.processes.worker.port");
		expect(line?.id).toBe("a:config.processes.worker.port");
	});
});
