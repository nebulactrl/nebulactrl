import { describe, expect, it } from "bun:test";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { ScalingForm } from "./scaling-form";
import {
	makeObserved,
	makeService,
	processEntry,
	runningReplica,
	scalingOf,
} from "./test-fixtures";

const SERVICE_PUT = "PUT /services/:id/scaling";
const PROCESS_PUT = "PUT /services/:id/processes/:process/scaling";
const apply = () => screen.getByRole("button", { name: "Apply" });

const single = (over = {}) =>
	makeService({
		id: "svc-1",
		name: "api",
		scaling: scalingOf({ replicas: 2, ready: 2 }),
		...over,
	});

describe("ScalingForm: a service that cannot scale", () => {
	it("explains why and offers no controls", async () => {
		await renderWithProviders(
			<ScalingForm
				service={single({
					scaling: scalingOf({
						eligible: false,
						ineligibleReason: "Has a volume",
					}),
				})}
			/>,
		);
		expect(screen.getByText("Runs as a single replica")).toBeInTheDocument();
		expect(screen.getByText("Has a volume")).toBeInTheDocument();
		expect(
			screen.getByText(/always run exactly one replica/),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Apply" }),
		).not.toBeInTheDocument();
	});

	it("falls back to the stateless-service rule without a reason", async () => {
		await renderWithProviders(
			<ScalingForm
				service={single({ scaling: scalingOf({ eligible: false }) })}
			/>,
		);
		expect(
			screen.getByText(
				"Replicas need a stateless service: no volumes and an HTTP port.",
			),
		).toBeInTheDocument();
	});

	it("speaks of the process, not databases, when the service has processes", async () => {
		await renderWithProviders(
			<ScalingForm
				service={single({
					processes: [
						processEntry("web", {}, 1),
						processEntry("cron", {
							scaling: scalingOf({ eligible: false }),
						}),
					],
				})}
				process="cron"
			/>,
		);
		expect(
			screen.getByText(
				"Replicas need a stateless process: no volumes and no files.",
			),
		).toBeInTheDocument();
		expect(
			screen.queryByText(/always run exactly one replica/),
		).not.toBeInTheDocument();
	});
});

describe("ScalingForm: manual", () => {
	it("shows readiness and keeps Apply locked until the count changes", async () => {
		await renderWithProviders(<ScalingForm service={single()} />);
		expect(screen.getByText("2 / 2")).toBeInTheDocument();
		expect(screen.getByText("2 of 2 replicas ready")).toBeInTheDocument();
		expect(screen.getByRole("group", { name: "Replicas" })).toHaveTextContent(
			"2",
		);
		expect(apply()).toBeDisabled();
	});

	it("shows replicas that are still starting", async () => {
		await renderWithProviders(
			<ScalingForm
				service={single({ scaling: scalingOf({ replicas: 3, ready: 1 }) })}
			/>,
		);
		expect(screen.getByText("1 of 3 ready, starting")).toBeInTheDocument();
		expect(screen.getByText("· Waiting for replicas")).toBeInTheDocument();
	});

	it("stops at the server's 1 and 10 limits", async () => {
		const user = userEvent.setup();
		await renderWithProviders(
			<ScalingForm
				service={single({ scaling: scalingOf({ replicas: 1, ready: 1 }) })}
			/>,
		);
		expect(
			screen.getByRole("button", { name: "Decrease replicas" }),
		).toBeDisabled();
		for (let i = 0; i < 12; i += 1)
			await user.click(
				screen.getByRole("button", { name: "Increase replicas" }),
			);
		expect(screen.getByRole("group", { name: "Replicas" })).toHaveTextContent(
			"10",
		);
		expect(
			screen.getByRole("button", { name: "Increase replicas" }),
		).toBeDisabled();
	});

	it("PUTs the new count with an idempotency key, toasts, and reports it applied", async () => {
		const user = userEvent.setup();
		let key: string | null = null;
		const api = stubApi({
			[SERVICE_PUT]: (request) => {
				key = request.headers.get("Idempotency-Key");
				return Response.json(single());
			},
		});
		let applied = 0;
		await renderWithProviders(
			<ScalingForm
				service={single()}
				onApplied={() => {
					applied += 1;
				}}
			/>,
		);
		await user.click(screen.getByRole("button", { name: "Increase replicas" }));
		await user.click(apply());

		expect(
			await screen.findByText("Scaling api to 3 replicas"),
		).toBeInTheDocument();
		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]?.path).toBe("/services/svc-1/scaling");
		expect(api.calls[0]?.body).toEqual({ replicas: 3 });
		expect(key).toMatch(/^[0-9a-f-]{36}$/);
		await waitFor(() => expect(applied).toBe(1));
		expect(api.unmatched).toEqual([]);
	});

	it("shows the server's reason and keeps the edit when applying fails", async () => {
		const user = userEvent.setup();
		stubApi({
			[SERVICE_PUT]: problem(500, { title: "Internal", detail: "no capacity" }),
		});
		await renderWithProviders(<ScalingForm service={single()} />);
		await user.click(screen.getByRole("button", { name: "Increase replicas" }));
		await user.click(apply());
		expect(await screen.findByText("no capacity")).toBeInTheDocument();
		expect(screen.getByRole("group", { name: "Replicas" })).toHaveTextContent(
			"3",
		);
		// Editing again clears the stale error.
		await user.click(screen.getByRole("button", { name: "Decrease replicas" }));
		expect(screen.queryByText("no capacity")).not.toBeInTheDocument();
	});

	it("tells the reader to switch to Manual when autoscaling owns the count", async () => {
		const user = userEvent.setup();
		stubApi({
			[SERVICE_PUT]: problem(409, {
				title: "Conflict",
				code: "autoscaling_enabled",
				detail: "x",
			}),
		});
		await renderWithProviders(<ScalingForm service={single()} />);
		await user.click(screen.getByRole("button", { name: "Increase replicas" }));
		await user.click(apply());
		expect(
			await screen.findByText(
				/Autoscaling is on, so the replica count is managed for you/,
			),
		).toBeInTheDocument();
	});

	it("explains a stateless-only rejection with the service rule", async () => {
		const user = userEvent.setup();
		stubApi({
			[SERVICE_PUT]: problem(422, {
				title: "Unprocessable",
				code: "replicas_require_stateless",
				detail: "x",
			}),
		});
		await renderWithProviders(<ScalingForm service={single()} />);
		await user.click(screen.getByRole("button", { name: "Increase replicas" }));
		await user.click(apply());
		expect(
			await screen.findByText(
				"Replicas need a stateless service: no volumes and an HTTP port.",
			),
		).toBeInTheDocument();
	});

	it("uses a secondary Apply when the surface has its own save key", async () => {
		await renderWithProviders(
			<ScalingForm service={single()} primary={false} />,
		);
		expect(apply()).toBeInTheDocument();
	});

	it("shows the last scale event", async () => {
		await renderWithProviders(
			<ScalingForm
				service={single({
					scaling: scalingOf({
						replicas: 3,
						ready: 3,
						lastScaleEvent: {
							at: new Date().toISOString(),
							from: 2,
							to: 3,
							reason: "CPU 84% > 70% for 2m",
							actor: "system",
						},
					}),
				})}
			/>,
		);
		expect(
			screen.getByText(/Scaled 2 → 3 · CPU 84% > 70% for 2m · autoscaler/),
		).toBeInTheDocument();
	});

	it("warns when scaling is blocked", async () => {
		await renderWithProviders(
			<ScalingForm
				service={single({
					scaling: scalingOf({ blockedReason: "Server is full" }),
				})}
			/>,
		);
		expect(screen.getByText("Scaling blocked")).toBeInTheDocument();
		expect(screen.getByText("Server is full")).toBeInTheDocument();
	});
});

describe("ScalingForm: autoscale", () => {
	const autoscaled = () =>
		single({
			scaling: scalingOf({
				replicas: 2,
				ready: 2,
				autoscaling: { enabled: true, min: 1, max: 3, targetCpuPercent: 70 },
			}),
		});

	it("opens in autoscale mode with the service's limits", async () => {
		await renderWithProviders(<ScalingForm service={autoscaled()} />);
		expect(screen.getByLabelText("Minimum replicas")).toHaveValue(1);
		expect(screen.getByLabelText("Maximum replicas")).toHaveValue(3);
		expect(screen.getByLabelText("Target CPU %")).toHaveValue(70);
		expect(apply()).toBeDisabled();
	});

	it("rejects a maximum below the minimum and an out-of-range CPU target, and sends nothing", async () => {
		const user = userEvent.setup();
		const api = stubApi({});
		await renderWithProviders(<ScalingForm service={autoscaled()} />);
		fireEvent.change(screen.getByLabelText("Minimum replicas"), {
			target: { value: "4" },
		});
		expect(
			await screen.findByText("Maximum must be at least the minimum."),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Maximum replicas")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
		fireEvent.change(screen.getByLabelText("Target CPU %"), {
			target: { value: "99" },
		});
		expect(
			await screen.findByText("Target CPU must be a whole number from 20–95%."),
		).toBeInTheDocument();
		expect(apply()).toBeDisabled();
		await user.click(apply());
		expect(api.calls).toEqual([]);
	});

	it("treats a cleared field as invalid instead of sending NaN", async () => {
		await renderWithProviders(<ScalingForm service={autoscaled()} />);
		fireEvent.change(screen.getByLabelText("Minimum replicas"), {
			target: { value: "" },
		});
		expect(
			await screen.findByText("Minimum must be a whole number from 1–10."),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Minimum replicas")).toHaveValue(null);
		expect(apply()).toBeDisabled();
	});

	it("PUTs the autoscaling settings and toasts the range", async () => {
		const user = userEvent.setup();
		const api = stubApi({ [SERVICE_PUT]: { body: autoscaled() } });
		await renderWithProviders(<ScalingForm service={autoscaled()} />);
		fireEvent.change(screen.getByLabelText("Maximum replicas"), {
			target: { value: "5" },
		});
		fireEvent.change(screen.getByLabelText("Target CPU %"), {
			target: { value: "60" },
		});
		await user.click(apply());
		expect(
			await screen.findByText("Autoscaling api between 1 and 5 replicas"),
		).toBeInTheDocument();
		expect(api.calls[0]?.body).toEqual({
			autoscaling: { enabled: true, min: 1, max: 5, targetCpuPercent: 60 },
		});
	});

	it("switching to Manual turns autoscaling off in one request", async () => {
		const user = userEvent.setup();
		const api = stubApi({ [SERVICE_PUT]: { body: autoscaled() } });
		await renderWithProviders(<ScalingForm service={autoscaled()} />);
		await user.click(screen.getByRole("button", { name: "Manual" }));
		expect(screen.getByRole("group", { name: "Replicas" })).toBeInTheDocument();
		await user.click(apply());
		await waitFor(() => expect(api.calls).toHaveLength(1));
		expect(api.calls[0]?.body).toEqual({ autoscaling: { enabled: false } });
	});

	it("switching a manual service to Autoscale offers the autoscale fields", async () => {
		const user = userEvent.setup();
		const api = stubApi({ [SERVICE_PUT]: { body: single() } });
		await renderWithProviders(<ScalingForm service={single()} />);
		await user.click(screen.getByRole("button", { name: "Autoscale" }));
		expect(screen.getByLabelText("Maximum replicas")).toBeInTheDocument();
		await user.click(apply());
		await waitFor(() => expect(api.calls).toHaveLength(1));
		expect(api.calls[0]?.body).toEqual({
			autoscaling: { enabled: true, min: 1, max: 3, targetCpuPercent: 70 },
		});
	});
});

describe("ScalingForm: processes", () => {
	const processes = () =>
		single({
			processes: [
				processEntry("web", {}, 1),
				processEntry("worker", {}, 3),
				processEntry("staged", { pending: true }, 2),
			],
			observed: makeObserved({
				replicas: [runningReplica("web", 0)],
				replicasReady: 1,
			}),
		});

	it("scales the default process through the service's own endpoint", async () => {
		const user = userEvent.setup();
		const api = stubApi({ [SERVICE_PUT]: { body: processes() } });
		await renderWithProviders(
			<ScalingForm service={processes()} process="web" />,
		);
		await user.click(screen.getByRole("button", { name: "Increase replicas" }));
		await user.click(apply());
		expect(
			await screen.findByText("Scaling web of api to 2 replicas"),
		).toBeInTheDocument();
		expect(api.calls[0]?.path).toBe("/services/svc-1/scaling");
	});

	it("scales another process through its own endpoint, from its own count", async () => {
		const user = userEvent.setup();
		const api = stubApi({ [PROCESS_PUT]: { body: processes() } });
		await renderWithProviders(
			<ScalingForm service={processes()} process="worker" />,
		);
		expect(screen.getByRole("group", { name: "Replicas" })).toHaveTextContent(
			"3",
		);
		await user.click(screen.getByRole("button", { name: "Decrease replicas" }));
		await user.click(apply());
		expect(
			await screen.findByText("Scaling worker of api to 2 replicas"),
		).toBeInTheDocument();
		expect(api.calls[0]?.path).toBe("/services/svc-1/processes/worker/scaling");
		expect(api.calls[0]?.body).toEqual({ replicas: 2 });
	});

	it("says a staged process starts with the next deploy", async () => {
		await renderWithProviders(
			<ScalingForm service={processes()} process="staged" />,
		);
		expect(screen.getByText("Starts with the next deploy")).toBeInTheDocument();
		expect(
			screen.queryByText("· Waiting for replicas"),
		).not.toBeInTheDocument();
	});

	it("falls back to the process rule when a stateless rejection names no reason", async () => {
		const user = userEvent.setup();
		stubApi({
			[PROCESS_PUT]: problem(422, {
				title: "Unprocessable",
				code: "replicas_require_stateless",
				detail: "x",
			}),
		});
		await renderWithProviders(
			<ScalingForm service={processes()} process="worker" />,
		);
		await user.click(screen.getByRole("button", { name: "Increase replicas" }));
		await user.click(apply());
		expect(
			await screen.findByText(
				"Replicas need a stateless process: no volumes and no files.",
			),
		).toBeInTheDocument();
	});
});
