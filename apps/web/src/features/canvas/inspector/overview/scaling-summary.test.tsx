import { describe, expect, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Service } from "#/api/models";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { makeObserved, makeService } from "../../lib/fixtures";
import { ScalingSummary } from "./scaling-summary";

const baseScaling = makeService().scaling;

function scaling(overrides: Partial<Service["scaling"]>): Service["scaling"] {
	return { ...baseScaling, ...overrides };
}

function processOf(
	name: string,
	overrides: Partial<Service["processes"][number]> = {},
): Service["processes"][number] {
	return {
		name,
		default: name === "web",
		command: [],
		port: 0,
		stopGraceSeconds: 30,
		pending: false,
		removed: false,
		singleton: false,
		observed: {} as Service["processes"][number]["observed"],
		scaling: baseScaling,
		...overrides,
	};
}

describe("ScalingSummary of a service without processes", () => {
	test("shows replicas ready of wanted, autoscaling off and no last event", async () => {
		await renderWithProviders(
			<ScalingSummary
				service={makeService({
					scaling: scaling({ replicas: 2 }),
					observed: makeObserved({ replicasReady: 2, replicasDesired: 2 }),
				})}
			/>,
		);
		expect(screen.getByText("Replicas ready")).toBeInTheDocument();
		expect(screen.getByText("2 / 2")).toBeInTheDocument();
		expect(screen.getByText("Autoscaling")).toBeInTheDocument();
		expect(screen.getByText("Off")).toBeInTheDocument();
		expect(screen.queryByText("Last scaled")).toBeNull();
		expect(screen.queryByText(/running now/)).toBeNull();
		expect(screen.getByRole("button", { name: "Scale" })).toBeInTheDocument();
	});

	test("says how many run now when that differs from the setting", async () => {
		await renderWithProviders(
			<ScalingSummary
				service={makeService({
					scaling: scaling({ replicas: 3, ready: 1 }),
					observed: makeObserved({ replicasReady: 1, replicasDesired: 2 }),
				})}
			/>,
		);
		expect(screen.getByText("1 / 3")).toBeInTheDocument();
		expect(screen.getByText("2 running now")).toBeInTheDocument();
	});

	test("describes autoscaling, the last scale event and what blocks a scale-out", async () => {
		await renderWithProviders(
			<ScalingSummary
				service={makeService({
					scaling: scaling({
						replicas: 2,
						autoscaling: {
							enabled: true,
							min: 2,
							max: 6,
							targetCpuPercent: 65,
						},
						lastScaleEvent: {
							at: "2026-10-31T10:00:00Z",
							from: 2,
							to: 3,
							reason: "cpu above target",
							actor: "autoscaler",
						},
						blockedReason: "No server has room for another replica",
					}),
				})}
			/>,
		);
		expect(
			screen.getByText("On · 2–6 replicas at 65% CPU"),
		).toBeInTheDocument();
		expect(screen.getByText("Last scaled")).toBeInTheDocument();
		expect(
			screen.getByText(/Scaled 2 → 3 · cpu above target · autoscaler/),
		).toBeInTheDocument();
		expect(
			screen.getByText("No server has room for another replica"),
		).toBeInTheDocument();
	});

	test("a service that cannot scale offers no button and says why", async () => {
		await renderWithProviders(
			<ScalingSummary
				service={makeService({
					scaling: scaling({
						eligible: false,
						ineligibleReason: "It has a volume",
					}),
				})}
			/>,
		);
		expect(screen.queryByRole("button", { name: "Scale" })).toBeNull();
		expect(screen.getByText("Not available")).toBeInTheDocument();
		expect(screen.getByText(/Runs as one replica\./)).toHaveTextContent(
			"Runs as one replica. It has a volume",
		);
	});

	test("without a stated reason the generic one is given", async () => {
		await renderWithProviders(
			<ScalingSummary
				service={makeService({
					scaling: scaling({ eligible: false, ineligibleReason: null }),
				})}
			/>,
		);
		expect(
			screen.getByText(/Replicas need a stateless service/),
		).toBeInTheDocument();
	});

	test("Scale opens the scale dialog for this service", async () => {
		const api = stubApi({
			"GET /services/:serviceId": problem(500, { title: "boom" }),
		});
		await renderWithProviders(
			<ScalingSummary service={makeService({ id: "svc-scale" })} />,
		);
		expect(screen.queryByRole("dialog")).toBeNull();
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Scale" }));
		const dialog = await screen.findByRole("dialog");
		expect(within(dialog).getByText("Scale service")).toBeInTheDocument();
		expect(api.calls.map((c) => c.path)).toContain("/services/svc-scale");
	});
});

describe("ScalingSummary of a service with processes", () => {
	test("one row per process, and the reasons a scale-out is blocked", async () => {
		await renderWithProviders(
			<ScalingSummary
				service={makeService({
					processes: [
						processOf("web", {
							scaling: scaling({ replicas: 2, blockedReason: "Out of memory" }),
						}),
						processOf("worker", {
							scaling: scaling({
								eligible: false,
								ineligibleReason: "It is a singleton",
							}),
						}),
						processOf("old", {
							removed: true,
							scaling: scaling({ blockedReason: "ignored, removed" }),
						}),
					],
				})}
			/>,
		);
		const list = screen.getByRole("list", { name: "Scaling of each process" });
		const rows = within(list).getAllByRole("listitem");
		expect(rows).toHaveLength(2);
		expect(rows[0]).toHaveTextContent("web");
		expect(rows[1]).toHaveTextContent("worker");
		expect(rows[1]).toHaveTextContent("It is a singleton");
		expect(
			within(rows[1] as HTMLElement).getByRole("button", { name: "Scale…" }),
		).toBeDisabled();
		expect(screen.getByText("web: Out of memory")).toBeInTheDocument();
		expect(screen.queryByText(/ignored, removed/)).toBeNull();
		// The single-service view is not shown for processes.
		expect(screen.queryByText("Replicas ready")).toBeNull();
	});
});
