import { describe, expect, it } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type { Service } from "#/api/models";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { ScaleDialog } from "./scale-dialog";
import { makeService, processEntry, scalingOf } from "./test-fixtures";

const GET = "GET /services/:id";

function Harness({ process }: { process?: string }) {
	const [open, setOpen] = useState(true);
	return (
		<>
			<p data-testid="open">{String(open)}</p>
			<ScaleDialog
				serviceId="svc-1"
				open={open}
				onOpenChange={setOpen}
				process={process}
			/>
		</>
	);
}

const api = (service: Service, extra: Parameters<typeof stubApi>[0] = {}) =>
	stubApi({ [GET]: { body: service }, ...extra });

describe("ScaleDialog", () => {
	it("names the service and loads its scaling form", async () => {
		api(
			makeService({
				id: "svc-1",
				name: "api",
				scaling: scalingOf({ replicas: 2, ready: 2 }),
			}),
		);
		await renderWithProviders(<Harness />);
		expect(await screen.findByText("Scale api")).toBeInTheDocument();
		expect(
			await screen.findByText("2 of 2 replicas ready"),
		).toBeInTheDocument();
		expect(screen.queryByLabelText("Process")).not.toBeInTheDocument();
	});

	it("shows a placeholder title while loading", async () => {
		stubApi({ [GET]: () => new Promise<Response>(() => {}) });
		await renderWithProviders(<Harness />);
		expect(await screen.findByText("Scale service")).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Apply" }),
		).not.toBeInTheDocument();
	});

	it("says so when the service cannot be loaded", async () => {
		stubApi({
			[GET]: problem(404, {
				title: "Not found",
				detail: "gone",
			}),
		});
		await renderWithProviders(<Harness />);
		expect(
			await screen.findByText("Could not load this service."),
		).toBeInTheDocument();
	});

	it("does not fetch while closed", async () => {
		const stub = api(makeService({ id: "svc-1" }));
		await renderWithProviders(
			<ScaleDialog serviceId="svc-1" open={false} onOpenChange={() => {}} />,
		);
		expect(stub.calls).toEqual([]);
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
	});

	it("applies and closes the dialog", async () => {
		const user = userEvent.setup();
		const scaled = makeService({
			id: "svc-1",
			name: "api",
			scaling: scalingOf({ replicas: 2, ready: 2 }),
		});
		const stub = api(scaled, {
			"PUT /services/:id/scaling": { body: scaled },
		});
		await renderWithProviders(<Harness />);
		await user.click(
			await screen.findByRole("button", { name: "Increase replicas" }),
		);
		await user.click(screen.getByRole("button", { name: "Apply" }));
		await waitFor(() =>
			expect(screen.getByTestId("open")).toHaveTextContent("false"),
		);
		const put = stub.calls.find((c) => c.method === "PUT");
		expect(put?.path).toBe("/services/svc-1/scaling");
		expect(put?.body).toEqual({ replicas: 3 });
	});

	describe("a service with processes", () => {
		const service = () =>
			makeService({
				id: "svc-1",
				name: "shop",
				processes: [
					processEntry("web", {}, 1),
					processEntry("worker", {}, 4),
					processEntry("cron", { scaling: scalingOf({ eligible: false }) }),
				],
			});

		it("opens on the requested process", async () => {
			api(service());
			await renderWithProviders(<Harness process="worker" />);
			expect(
				await screen.findByRole("group", { name: "Replicas" }),
			).toHaveTextContent("4");
			expect(screen.getByLabelText("Process")).toHaveTextContent("worker");
		});

		it("opens on the default process otherwise", async () => {
			api(service());
			await renderWithProviders(<Harness />);
			expect(
				await screen.findByRole("group", { name: "Replicas" }),
			).toHaveTextContent("1");
			expect(screen.getByLabelText("Process")).toHaveTextContent("web");
		});

		it("switches process from the picker, each with its own settings and endpoint", async () => {
			const user = userEvent.setup();
			const shop = service();
			const stub = api(shop, {
				"PUT /services/:id/processes/:process/scaling": { body: shop },
			});
			await renderWithProviders(<Harness />);
			await screen.findByRole("group", { name: "Replicas" });
			await user.click(screen.getByLabelText("Process"));
			const list = await screen.findByRole("listbox");
			// A process that cannot scale is listed but not selectable.
			const cron = within(list).getByRole("option", { name: /cron/ });
			expect(cron).toHaveAttribute("aria-disabled", "true");
			await user.click(within(list).getByRole("option", { name: /worker/ }));
			await waitFor(() =>
				expect(
					screen.getByRole("group", { name: "Replicas" }),
				).toHaveTextContent("4"),
			);
			await user.click(
				screen.getByRole("button", { name: "Decrease replicas" }),
			);
			await user.click(screen.getByRole("button", { name: "Apply" }));
			await waitFor(() =>
				expect(stub.calls.some((c) => c.method === "PUT")).toBe(true),
			);
			const put = stub.calls.find((c) => c.method === "PUT");
			expect(put?.path).toBe("/services/svc-1/processes/worker/scaling");
			expect(put?.body).toEqual({ replicas: 3 });
		});
	});
});
