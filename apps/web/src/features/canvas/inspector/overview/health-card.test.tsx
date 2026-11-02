import { afterEach, describe, expect, jest, test } from "bun:test";
import { render, screen, within } from "@testing-library/react";
import type { Service } from "#/api/models";
import { makeObserved, makeService } from "../../lib/fixtures";
import { HealthCard } from "./health-card";

const NOW = Date.parse("2026-11-01T12:00:00Z");

afterEach(() => {
	jest.useRealTimers();
});

function renderCard(service: Service) {
	jest.useFakeTimers();
	jest.setSystemTime(NOW);
	return render(<HealthCard service={service} />);
}

/**
 * The figure under a label ("Replicas", "Uptime", "Restarts"), as a screen
 * reader gets it: an animated number repeats its digits visually, so its
 * screen-reader text is the value.
 */
function figure(label: string): string | null {
	const term = screen.getByText(label, { selector: "dt" });
	const value = term.parentElement?.querySelector("dd");
	return (
		value?.querySelector(".sr-only")?.textContent ?? value?.textContent ?? null
	);
}

function replica(index: number, overrides: Record<string, unknown> = {}) {
	return {
		component: "",
		index,
		process: "",
		containerId: `c-${index}`,
		state: "running",
		health: "healthy",
		startedAt: null,
		restartCount: 0,
		...overrides,
	};
}

describe("HealthCard", () => {
	test("a healthy service: verdict with its probe, replicas, uptime and restarts", () => {
		renderCard(
			makeService({
				currentDeploymentId: "dep-1",
				observed: makeObserved({
					startedAt: new Date(NOW - (3 * 3600 + 12 * 60) * 1000).toISOString(),
					replicasReady: 2,
					replicasDesired: 2,
					replicas: [
						replica(0, { restartCount: 1 }),
						replica(1, { restartCount: 2 }),
					],
				}),
			}),
		);
		expect(screen.getByText("Healthy")).toBeInTheDocument();
		expect(
			screen.getByText("HTTP GET / on :80 · every 2s"),
		).toBeInTheDocument();
		expect(figure("Replicas")).toBe("2 / 2");
		expect(figure("Uptime")).toBe("3h 12m");
		expect(figure("Restarts")).toBe("3");
	});

	test("more than one replica lists each with its state and health", () => {
		renderCard(
			makeService({
				currentDeploymentId: "dep-1",
				observed: makeObserved({
					replicasReady: 1,
					replicasDesired: 3,
					replicas: [
						replica(0),
						replica(1, { state: "exited", health: "none" }),
						replica(2, { health: "starting" }),
					],
				}),
			}),
		);
		const list = screen.getByRole("list", { name: "Replicas" });
		const items = within(list).getAllByRole("listitem");
		expect(items.map((item) => item.textContent)).toEqual([
			"#0 · running, healthy",
			"#1 · exited",
			"#2 · running, starting",
		]);
	});

	test("a single replica gets no per-replica list", () => {
		renderCard(
			makeService({ currentDeploymentId: "dep-1", observed: makeObserved() }),
		);
		expect(screen.queryByRole("list", { name: "Replicas" })).toBeNull();
	});

	test("a service the agent has not reported shows dashes, not zeros", () => {
		renderCard(
			makeService({
				currentDeploymentId: null,
				scaling: { ...makeService().scaling, replicas: 0, ready: 0 },
				observed: null,
			}),
		);
		expect(screen.getByText("Not deployed")).toBeInTheDocument();
		expect(figure("Replicas")).toBe("—");
		expect(figure("Uptime")).toBe("—");
		expect(figure("Restarts")).toBe("—");
	});

	test("before the first report, replicas fall back to the setting", () => {
		renderCard(
			makeService({
				currentDeploymentId: "dep-1",
				observed: null,
				scaling: { ...makeService().scaling, replicas: 2, ready: 1 },
			}),
		);
		expect(screen.getByText("Not reported")).toBeInTheDocument();
		expect(figure("Replicas")).toBe("1 / 2");
		expect(figure("Restarts")).toBe("—");
	});

	test("a stale report says the agent's last word is old", () => {
		renderCard(
			makeService({
				currentDeploymentId: "dep-1",
				observed: makeObserved(),
				stale: true,
			}),
		);
		expect(
			screen.getByText("The agent's last report is more than 30 seconds old."),
		).toBeInTheDocument();
	});

	test("a fresh report says nothing about staleness", () => {
		renderCard(
			makeService({ currentDeploymentId: "dep-1", observed: makeObserved() }),
		);
		expect(screen.queryByText(/last report is more than/)).toBeNull();
	});

	test("a stopped service says Stopped and how to start it", () => {
		renderCard(
			makeService({
				currentDeploymentId: "dep-1",
				desiredState: "stopped",
				observed: null,
			}),
		);
		expect(screen.getByText("Stopped")).toBeInTheDocument();
		expect(
			screen.getByText("Restart or deploy to start it again"),
		).toBeInTheDocument();
	});
});
