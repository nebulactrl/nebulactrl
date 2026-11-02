import { describe, expect, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Service } from "#/api/models";
import { makeService } from "#/features/canvas/lib/fixtures";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { ScalingSection } from "./scaling-section";

const scaling = (
	overrides: Partial<Service["scaling"]> = {},
): Service["scaling"] => ({
	replicas: 2,
	ready: 2,
	autoscaling: { enabled: false, min: 1, max: 3, targetCpuPercent: 70 },
	lastScaleEvent: null,
	blockedReason: null,
	eligible: true,
	ineligibleReason: null,
	...overrides,
});

function process(
	name: string,
	overrides: Partial<Service["processes"][number]> = {},
): Service["processes"][number] {
	return {
		name,
		default: name === "web",
		command: [],
		observed: null,
		pending: false,
		port: 0,
		removed: false,
		scaling: scaling(),
		singleton: false,
		stopGraceSeconds: 30,
		...overrides,
	} as Service["processes"][number];
}

describe("ScalingSection", () => {
	test("says scaling applies now and summarises the service's replicas", async () => {
		stubApi({});
		await renderWithProviders(
			<ScalingSection service={makeService({ scaling: scaling() })} />,
		);
		const heading = screen.getByRole("heading", { name: /Scaling/ });
		expect(heading).toHaveTextContent("Applies now");
		expect(heading).toHaveTextContent("2/2 replicas");
		expect(
			screen.getByText(
				"How many copies of the current release run behind the proxy, without a deploy or a rebuild.",
			),
		).toBeInTheDocument();
		expect(screen.getByRole("group", { name: "Replicas" })).toBeInTheDocument();
	});

	test("the summary names autoscaling bounds when it is on", async () => {
		stubApi({});
		await renderWithProviders(
			<ScalingSection
				service={makeService({
					scaling: scaling({
						autoscaling: {
							enabled: true,
							min: 2,
							max: 6,
							targetCpuPercent: 65,
						},
					}),
				})}
			/>,
		);
		expect(screen.getByRole("heading", { name: /Scaling/ })).toHaveTextContent(
			"2/2 replicas · autoscaling 2–6 @ 65% CPU",
		);
	});

	test("a service that cannot scale shows why, and no summary or Apply", async () => {
		stubApi({});
		await renderWithProviders(
			<ScalingSection
				service={makeService({
					scaling: scaling({
						replicas: 1,
						ready: 1,
						eligible: false,
						ineligibleReason: "It keeps data in a volume.",
					}),
				})}
			/>,
		);
		expect(screen.getByText("Runs as a single replica")).toBeInTheDocument();
		expect(screen.getByText("It keeps data in a volume.")).toBeInTheDocument();
		const heading = screen.getByRole("heading", { name: /Scaling/ });
		expect(heading).toHaveTextContent("Applies now");
		expect(heading).not.toHaveTextContent("replica");
		expect(screen.queryByRole("button", { name: "Apply" })).toBeNull();
	});

	test("Apply sends the new replica count at once, not through the save bar", async () => {
		const user = userEvent.setup();
		const service = makeService({
			id: "s1",
			scaling: scaling({ replicas: 2 }),
		});
		const api = stubApi({
			"PUT /services/s1/scaling": { body: service },
		});
		await renderWithProviders(<ScalingSection service={service} />);
		const apply = screen.getByRole("button", { name: "Apply" });
		expect(apply).toBeDisabled();
		await user.click(screen.getByRole("button", { name: "Increase replicas" }));
		expect(apply).toBeEnabled();
		await user.click(apply);

		expect(
			await screen.findByText(/^Scaling .+ to 3 replicas$/),
		).toBeInTheDocument();
		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]?.method).toBe("PUT");
		expect(api.calls[0]?.path).toBe("/services/s1/scaling");
		expect(api.calls[0]?.body).toEqual({ replicas: 3 });
		expect(api.unmatched).toEqual([]);
	});

	test("several processes are scaled one row each", async () => {
		stubApi({});
		const service = makeService({
			processes: [
				process("web", { scaling: scaling({ replicas: 2, ready: 2 }) }),
				process("worker", {
					scaling: scaling({
						replicas: 1,
						ready: 1,
						eligible: false,
						ineligibleReason: "Workers hold a lock.",
					}),
				}),
			],
		});
		await renderWithProviders(<ScalingSection service={service} />);
		expect(
			screen.getByText(
				"How many copies of each process run, without a deploy or a rebuild.",
			),
		).toBeInTheDocument();
		const list = screen.getByRole("list", { name: "Scaling of each process" });
		const [web, worker] = within(list).getAllByRole("listitem");
		expect(web).toHaveTextContent("web");
		expect(web).toHaveTextContent("2/2 replicas");
		expect(
			within(web as HTMLElement).getByRole("button", { name: "Scale…" }),
		).toBeEnabled();
		expect(worker).toHaveTextContent("Runs one instance");
		expect(worker).toHaveTextContent("Workers hold a lock.");
		expect(
			within(worker as HTMLElement).getByRole("button", { name: "Scale…" }),
		).toBeDisabled();
		// The per-service summary is not repeated in the header when processes scale on their own.
		expect(
			screen.getByRole("heading", { name: /Scaling/ }),
		).not.toHaveTextContent("replicas");
		expect(screen.queryByRole("group", { name: "Replicas" })).toBeNull();
	});

	test("a process that was removed in the draft is not listed", async () => {
		stubApi({});
		const service = makeService({
			processes: [process("web"), process("old", { removed: true })],
		});
		await renderWithProviders(<ScalingSection service={service} />);
		const list = screen.getByRole("list", { name: "Scaling of each process" });
		expect(within(list).getAllByRole("listitem")).toHaveLength(1);
		expect(within(list).queryByText("old")).toBeNull();
	});
});
