import { describe, expect, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { makeProcess, makeProcessService, makeScaling } from "./fixtures";
import { ScaleLine, ScaleRows } from "./scale-rows";

const SERVICE = makeProcessService({
	processes: [
		makeProcess({
			name: "web",
			default: true,
			port: 3000,
			scaling: makeScaling({ replicas: 2, ready: 2 }),
		}),
		makeProcess({
			name: "worker",
			scaling: makeScaling({ replicas: 1, ready: 1 }),
		}),
		makeProcess({
			name: "clock",
			singleton: true,
			scaling: makeScaling({
				eligible: false,
				ineligibleReason: "A singleton never runs two instances.",
			}),
		}),
		makeProcess({ name: "old", removed: true }),
	],
});

const row = (name: string) =>
	within(screen.getByRole("list", { name: "Scaling of each process" }))
		.getByText(name)
		.closest("li") as HTMLElement;

describe("ScaleRows", () => {
	test("lists each live process with its scale, in service order, and skips removed ones", async () => {
		stubApi({});
		await renderWithProviders(<ScaleRows service={SERVICE} />);
		const items = within(
			screen.getByRole("list", { name: "Scaling of each process" }),
		).getAllByRole("listitem");
		expect(items).toHaveLength(3);
		expect(items[0]).toHaveTextContent("web");
		expect(items[0]).toHaveTextContent("2/2 replicas");
		expect(items[1]).toHaveTextContent("worker");
		expect(items[1]).toHaveTextContent("1/1 replica");
		expect(items[2]).toHaveTextContent("clock");
		expect(screen.queryByText("old")).not.toBeInTheDocument();
	});

	test("a process that cannot scale says why and its Scale button is disabled", async () => {
		stubApi({});
		await renderWithProviders(<ScaleRows service={SERVICE} />);
		const clock = row("clock");
		expect(clock).toHaveTextContent("Runs one instance");
		expect(clock).toHaveTextContent("A singleton never runs two instances.");
		expect(
			within(clock).getByRole("button", { name: "Scale…" }),
		).toBeDisabled();
		expect(
			within(row("worker")).getByRole("button", { name: "Scale…" }),
		).toBeEnabled();
	});

	test("Scale… opens the dialog on that process and applies to that process only", async () => {
		const api = stubApi({
			"GET /services/:id": { body: SERVICE },
			"PUT /services/:id/processes/:process/scaling": { body: SERVICE },
		});
		const user = userEvent.setup();
		await renderWithProviders(<ScaleRows service={SERVICE} />);
		await user.click(
			within(row("worker")).getByRole("button", { name: "Scale…" }),
		);
		expect(
			await screen.findByRole("heading", { name: "Scale shop" }),
		).toBeInTheDocument();
		// The picker shows the process the row named, not the default one.
		expect(await screen.findByRole("combobox")).toHaveTextContent("worker");
		await user.click(screen.getByRole("button", { name: "Increase replicas" }));
		await user.click(screen.getByRole("button", { name: "Apply" }));
		await screen.findByText("Scaling worker of shop to 2 replicas");
		const put = api.calls.find((call) => call.method === "PUT");
		expect(put?.path).toBe("/services/svc-shop/processes/worker/scaling");
		expect(put?.body).toEqual({ replicas: 2 });
		expect(api.unmatched).toEqual([]);
	});

	test("the default process scales through the service's own endpoint", async () => {
		const api = stubApi({
			"GET /services/:id": { body: SERVICE },
			"PUT /services/:id/scaling": { body: SERVICE },
		});
		const user = userEvent.setup();
		await renderWithProviders(<ScaleRows service={SERVICE} />);
		await user.click(
			within(row("web")).getByRole("button", { name: "Scale…" }),
		);
		await user.click(
			await screen.findByRole("button", { name: "Increase replicas" }),
		);
		await user.click(screen.getByRole("button", { name: "Apply" }));
		await screen.findByText("Scaling web of shop to 3 replicas");
		const put = api.calls.find((call) => call.method === "PUT");
		expect(put?.path).toBe("/services/svc-shop/scaling");
		expect(put?.body).toEqual({ replicas: 3 });
	});
});

describe("ScaleLine", () => {
	test("shows the scale of one process and opens the dialog on it", async () => {
		stubApi({ "GET /services/:id": { body: SERVICE } });
		const user = userEvent.setup();
		await renderWithProviders(<ScaleLine service={SERVICE} process="worker" />);
		expect(screen.getByText("1/1 replica")).toBeInTheDocument();
		expect(screen.queryByText("2/2 replicas")).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Scale…" }));
		expect(await screen.findByRole("combobox")).toHaveTextContent("worker");
	});

	test("a process that cannot scale shows why and disables Scale…", async () => {
		stubApi({});
		await renderWithProviders(<ScaleLine service={SERVICE} process="clock" />);
		expect(screen.getByText("Runs one instance")).toBeInTheDocument();
		expect(
			screen.getByText("A singleton never runs two instances."),
		).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Scale…" })).toBeDisabled();
	});

	test("a process that is not saved yet has no scale to show", async () => {
		stubApi({});
		await renderWithProviders(
			<ScaleLine service={SERVICE} process="unsaved" />,
		);
		expect(
			screen.getByText("Scale appears here once the process is saved."),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Scale…" }),
		).not.toBeInTheDocument();
	});
});
