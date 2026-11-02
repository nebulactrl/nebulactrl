import { describe, expect, it } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithProviders } from "#/test/render";
import { ReplicaStrip } from "./replica-strip";
import {
	makeObserved,
	makeService,
	processEntry,
	runningReplica,
	scalingOf,
} from "./test-fixtures";

function running(index: number) {
	return runningReplica("", index);
}

describe("ReplicaStrip", () => {
	it("draws nothing for a service with no replicas", async () => {
		const { container } = await renderWithProviders(
			<ReplicaStrip
				service={makeService({ scaling: scalingOf({ replicas: 0 }) })}
			/>,
		);
		expect(container).toBeEmptyDOMElement();
	});

	it("draws a pip per replica and says the count in words, with no legend for one state", async () => {
		await renderWithProviders(
			<ReplicaStrip
				service={makeService({
					scaling: scalingOf({ replicas: 3, ready: 3 }),
					observed: makeObserved({
						replicas: [running(0), running(1), running(2)],
						replicasReady: 3,
					}),
				})}
			/>,
		);
		const strip = screen.getByRole("img", { name: "3 of 3 replicas ready" });
		expect(strip.querySelectorAll("[data-state=ready]")).toHaveLength(3);
		expect(screen.queryByRole("list")).not.toBeInTheDocument();
	});

	it("shows a legend with counts when replicas are in different states", async () => {
		await renderWithProviders(
			<ReplicaStrip
				service={makeService({
					scaling: scalingOf({ replicas: 3, ready: 1 }),
					observed: makeObserved({
						replicas: [
							running(0),
							{ ...running(1), health: "starting" },
							{ ...running(2), health: "unhealthy" },
						],
						replicasReady: 1,
					}),
				})}
			/>,
		);
		expect(
			screen.getByRole("img", {
				name: "1 of 3 replicas ready, 1 starting, 1 failed",
			}),
		).toBeInTheDocument();
		const items = within(screen.getByRole("list")).getAllByRole("listitem");
		expect(items.map((item) => item.textContent)).toEqual([
			"Ready1",
			"Starting1",
			"Failed1",
		]);
	});

	it("marks replicas that are not placed as not running", async () => {
		await renderWithProviders(
			<ReplicaStrip
				service={makeService({
					scaling: scalingOf({ replicas: 2, ready: 0 }),
					desiredState: "stopped",
					observed: null,
				})}
			/>,
		);
		expect(
			screen.getByRole("img", { name: "0 of 2 replicas ready, 2 not running" }),
		).toBeInTheDocument();
	});

	it("names a replica, its state and its server in the tooltip", async () => {
		const user = userEvent.setup();
		await renderWithProviders(
			<ReplicaStrip
				service={makeService({
					scaling: scalingOf({ replicas: 1, ready: 1 }),
					observed: makeObserved({ replicas: [running(0)], replicasReady: 1 }),
				})}
			/>,
		);
		const pip = screen
			.getByRole("img", { name: "1 of 1 replica ready" })
			.querySelector("[data-state=ready]") as HTMLElement;
		await user.hover(pip);
		expect(
			await screen.findByText("Replica 0 · Ready, healthy · hetzner-01"),
		).toBeInTheDocument();
	});

	it("collapses to one bar with a legend above 16 replicas", async () => {
		await renderWithProviders(
			<ReplicaStrip
				service={makeService({
					scaling: scalingOf({ replicas: 20, ready: 18 }),
					observed: null,
				})}
			/>,
		);
		expect(screen.queryByRole("img")).not.toBeInTheDocument();
		expect(
			screen.getByRole("progressbar", { name: "Replicas" }),
		).toHaveAttribute(
			"aria-valuetext",
			"18 of 20 replicas ready, 2 not running",
		);
		const items = within(screen.getByRole("list")).getAllByRole("listitem");
		expect(items.map((item) => item.textContent)).toEqual([
			"Ready18",
			"Not running2",
		]);
	});

	describe("with processes", () => {
		const service = makeService({
			processes: [processEntry("web", {}, 1), processEntry("worker", {}, 2)],
			observed: makeObserved({
				replicas: [
					runningReplica("web", 0),
					runningReplica("worker", 0),
					{ ...runningReplica("worker", 1), health: "starting" },
				],
				replicasReady: 2,
			}),
		});

		it("lists every process' replicas and labels them by process", async () => {
			const user = userEvent.setup();
			await renderWithProviders(<ReplicaStrip service={service} />);
			const strip = screen.getByRole("img", {
				name: "2 of 3 replicas ready, 1 starting",
			});
			expect(strip.children).toHaveLength(3);
			await user.hover(strip.children[2] as HTMLElement);
			expect(
				await screen.findByText(
					/worker · replica 1 · Starting, .* · hetzner-01/,
				),
			).toBeInTheDocument();
		});

		it("draws only the named process", async () => {
			await renderWithProviders(
				<ReplicaStrip service={service} process="worker" />,
			);
			const strip = screen.getByRole("img", {
				name: "1 of 2 replicas ready, 1 starting",
			});
			expect(strip.children).toHaveLength(2);
		});
	});
});
