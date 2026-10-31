import { describe, expect, test } from "bun:test";
import type { EventView } from "#/api/gen/types.gen";
import { foldSteps, stepDuration } from "./steps-model";

let id = 0;
const ev = (
	step: string,
	status: string,
	at: string,
	message = "",
): EventView => ({
	id: ++id,
	step,
	status,
	message,
	createdAt: `2026-10-27T16:20:${at}Z`,
});

describe("foldSteps", () => {
	test("a rolling deploy folds to one row per step, phases dropped", () => {
		const rows = foldSteps([
			ev("status", "created", "52.0"),
			ev("status", "queued", "52.0"),
			ev("status", "preparing", "52.0"),
			ev("pull", "started", "52.0", "Pulling traefik/whoami:latest"),
			ev("pull", "succeeded", "52.4", "Image traefik/whoami@sha256:c47"),
			ev("create", "started", "52.4", "Creating container nc-api-0"),
			ev("status", "releasing", "52.5"),
			ev("create", "succeeded", "52.6"),
			ev("start", "started", "52.6"),
			ev("start", "succeeded", "53.0"),
			ev("health", "started", "53.0", "Checking health (http)"),
			ev("status", "verifying", "53.1"),
			ev("health", "succeeded", "55.0", "Healthy"),
			ev("route", "started", "55.0", "Updating proxy routes"),
			ev("route", "succeeded", "55.2"),
			ev("status", "succeeded", "55.2"),
		]);
		expect(rows.map((r) => [r.step, r.state, r.message])).toEqual([
			["pull", "succeeded", "Image traefik/whoami@sha256:c47"],
			["create", "succeeded", "Creating container nc-api-0"],
			["start", "succeeded", ""],
			["health", "succeeded", "Healthy"],
			["route", "succeeded", "Updating proxy routes"],
		]);
		expect(stepDuration(rows[3] as (typeof rows)[number])).toBe("2.0 s");
	});

	test("a running step stays open; a failure keeps its reason and the outcome row", () => {
		const running = foldSteps([ev("health", "started", "53.0", "Checking")]);
		expect(running[0]?.state).toBe("started");
		expect(running[0]?.endedAt).toBeUndefined();

		const failed = foldSteps([
			ev("health", "started", "53.0", "Checking health (http)"),
			ev("health", "failed", "58.0", "GET /health returned 502"),
			ev("route", "started", "58.1", "Restoring the previous route"),
			ev("route", "succeeded", "58.2"),
			ev("status", "rolled_back", "58.2", "The previous release keeps serving"),
		]);
		expect(failed.map((r) => [r.step, r.state, r.message])).toEqual([
			["health", "failed", "GET /health returned 502"],
			["route", "succeeded", "Restoring the previous route"],
			["status", "rolled_back", "The previous release keeps serving"],
		]);
	});

	test("a step that runs again after finishing opens a new row", () => {
		const rows = foldSteps([
			ev("route", "started", "55.0"),
			ev("route", "succeeded", "55.1"),
			ev("route", "started", "56.0", "Rolling back the route"),
			ev("route", "failed", "56.5", "proxy unreachable"),
		]);
		expect(rows.map((r) => [r.step, r.state])).toEqual([
			["route", "succeeded"],
			["route", "failed"],
		]);
	});
});

describe("stepDuration", () => {
	test("formats seconds and minutes, and is empty until the step ends", () => {
		const base = { key: "k", step: "build", state: "succeeded", message: "" };
		expect(
			stepDuration({
				...base,
				startedAt: "2026-10-27T16:00:00Z",
				endedAt: "2026-10-27T16:01:04Z",
			}),
		).toBe("1 min 4 s");
		expect(stepDuration({ ...base, startedAt: "2026-10-27T16:00:00Z" })).toBe(
			"",
		);
	});
});
