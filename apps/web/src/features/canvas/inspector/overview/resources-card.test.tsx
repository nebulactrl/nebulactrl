import { describe, expect, test } from "bun:test";
import { screen } from "@testing-library/react";
import type { ServiceMetricPoint } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { makeService } from "../../lib/fixtures";
import { ResourcesCard } from "./resources-card";

const MIB = 1024 * 1024;

function point(index: number, overrides: Partial<ServiceMetricPoint> = {}) {
	return {
		ts: new Date(Date.UTC(2026, 9, 31, 10, index)).toISOString(),
		cpuPercent: 10,
		cpuMax: 10,
		cpuPerReplica: 10,
		memBytes: 128 * MIB,
		memLimitBytes: 512 * MIB,
		memMax: 128 * MIB,
		replicaCount: 1,
		...overrides,
	};
}

/** The tile titled `title`: its text and the progress bars inside it. */
function tile(title: string): HTMLElement {
	const label = screen.getByText(title, { selector: ".type-micro" });
	return label.parentElement as HTMLElement;
}

describe("ResourcesCard", () => {
	test("shows CPU against the configured limit and memory against the container limit", async () => {
		const api = stubApi({
			"GET /services/:serviceId/metrics": {
				body: make("MetricsOutputBody", {
					points: [
						point(0),
						point(1, {
							cpuPercent: 50,
							memBytes: 256 * MIB,
							memLimitBytes: 512 * MIB,
						}),
					],
				}),
			},
		});
		await renderWithProviders(
			<ResourcesCard
				service={makeService({
					id: "svc-res",
					currentDeploymentId: "dep-1",
					config: { ...makeService().config, cpus: 1 },
				})}
			/>,
		);
		expect(await screen.findByText("0.5 / 1 vCPU")).toBeInTheDocument();
		expect(screen.getByText("256 MiB / 512 MiB")).toBeInTheDocument();
		expect(tile("CPU")).toHaveTextContent("50%");
		expect(tile("Memory")).toHaveTextContent("50%");
		expect(api.calls[0]?.path).toBe("/services/svc-res/metrics");
		expect(api.unmatched).toEqual([]);
	});

	test("without a CPU limit it shows the vCPU in use and says no limit is set", async () => {
		stubApi({
			"GET /services/:serviceId/metrics": {
				body: make("MetricsOutputBody", {
					points: [
						point(0, { cpuPercent: 42, memLimitBytes: 0, memBytes: 64 * MIB }),
					],
				}),
			},
		});
		await renderWithProviders(
			<ResourcesCard service={makeService({ currentDeploymentId: "dep-1" })} />,
		);
		expect(await screen.findByText("No CPU limit set")).toBeInTheDocument();
		expect(screen.getByText("0.42 vCPU")).toBeInTheDocument();
		expect(screen.getByText("No memory limit set")).toBeInTheDocument();
		expect(screen.getByText("64 MiB")).toBeInTheDocument();
	});

	test("the limit scales with the replicas sampled", async () => {
		stubApi({
			"GET /services/:serviceId/metrics": {
				body: make("MetricsOutputBody", {
					points: [point(0, { cpuPercent: 100, replicaCount: 4 })],
				}),
			},
		});
		await renderWithProviders(
			<ResourcesCard
				service={makeService({
					currentDeploymentId: "dep-1",
					config: { ...makeService().config, cpus: 0.5 },
				})}
			/>,
		);
		expect(await screen.findByText("1 / 2 vCPU")).toBeInTheDocument();
	});

	test("one sample is too few for a line: the tile says it is collecting", async () => {
		stubApi({
			"GET /services/:serviceId/metrics": {
				body: make("MetricsOutputBody", { points: [point(0)] }),
			},
		});
		await renderWithProviders(
			<ResourcesCard service={makeService({ currentDeploymentId: "dep-1" })} />,
		);
		await screen.findByText("No CPU limit set");
		expect(screen.getAllByText("Collecting samples…")).toHaveLength(2);
	});

	test("two samples draw a sparkline per tile", async () => {
		stubApi({
			"GET /services/:serviceId/metrics": {
				body: make("MetricsOutputBody", { points: [point(0), point(1)] }),
			},
		});
		await renderWithProviders(
			<ResourcesCard service={makeService({ currentDeploymentId: "dep-1" })} />,
		);
		await screen.findByText("No CPU limit set");
		expect(screen.queryByText("Collecting samples…")).toBeNull();
		// The chart's label carries the series' summary: the newest value and the range.
		expect(
			screen.getByLabelText(
				"CPU over the last hour: latest 10, low 10, high 10",
			),
		).toBeInTheDocument();
		expect(
			screen.getByLabelText(
				`Memory over the last hour: latest ${128 * MIB}, low ${128 * MIB}, high ${128 * MIB}`,
			),
		).toBeInTheDocument();
	});

	test("a service that was never deployed asks for no metrics and says there are no samples", async () => {
		const api = stubApi({});
		await renderWithProviders(
			<ResourcesCard service={makeService({ currentDeploymentId: null })} />,
		);
		expect(screen.getAllByText("No samples yet")).toHaveLength(2);
		expect(screen.getAllByText("—")).toHaveLength(2);
		expect(api.calls).toEqual([]);
	});
});
