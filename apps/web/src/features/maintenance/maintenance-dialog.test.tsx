import { describe, expect, test } from "bun:test";
import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { MaintenanceHost } from "./maintenance-dialog";
import {
	closeMaintenance,
	openMaintenance,
	useMaintenanceTarget,
} from "./store";

function Target() {
	return (
		<output aria-label="target">{useMaintenanceTarget() ?? "none"}</output>
	);
}

const service = make("ServiceView", { id: "svc-d", name: "Storefront" });

describe("maintenance store", () => {
	test("openMaintenance names the target and closeMaintenance clears it, for every reader", async () => {
		await renderWithProviders(
			<>
				<Target />
				<span data-testid="second">
					<Target />
				</span>
			</>,
		);
		expect(
			screen.getAllByLabelText("target").map((n) => n.textContent),
		).toEqual(["none", "none"]);
		act(() => openMaintenance("svc-1"));
		expect(
			screen.getAllByLabelText("target").map((n) => n.textContent),
		).toEqual(["svc-1", "svc-1"]);
		act(() => openMaintenance("svc-2"));
		expect(
			screen.getAllByLabelText("target").map((n) => n.textContent),
		).toEqual(["svc-2", "svc-2"]);
		act(() => closeMaintenance());
		expect(
			screen.getAllByLabelText("target").map((n) => n.textContent),
		).toEqual(["none", "none"]);
	});

	test("a reader that unmounted is no longer notified", async () => {
		const { unmount } = await renderWithProviders(<Target />);
		unmount();
		expect(() => act(() => openMaintenance("svc-3"))).not.toThrow();
		act(() => closeMaintenance());
	});
});

describe("MaintenanceHost", () => {
	test("is closed until a service asks for it, and fetches nothing", async () => {
		const api = stubApi({});
		await renderWithProviders(<MaintenanceHost />);
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});

	test("opens for the service, titled with its name, with the form", async () => {
		stubApi({ "GET /services/:id": { body: service } });
		await renderWithProviders(<MaintenanceHost />);
		act(() => openMaintenance("svc-d"));
		expect(
			await screen.findByText("Maintenance mode for Storefront"),
		).toBeInTheDocument();
		expect(screen.getByRole("dialog")).toHaveTextContent(
			/maintenance page \(HTTP 503\)/,
		);
		expect(
			screen.getByRole("button", { name: "Turn on maintenance" }),
		).toBeInTheDocument();
		act(() => closeMaintenance());
	});

	test("shows a plain title while the service loads", async () => {
		stubApi({ "GET /services/:id": () => new Promise<Response>(() => {}) });
		await renderWithProviders(<MaintenanceHost />);
		act(() => openMaintenance("svc-d"));
		expect(await screen.findByText("Maintenance mode")).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Turn on maintenance" }),
		).not.toBeInTheDocument();
		act(() => closeMaintenance());
	});

	test("says so when the service cannot be loaded", async () => {
		stubApi({
			"GET /services/:id": problem(500, { title: "Boom", status: 500 }),
		});
		await renderWithProviders(<MaintenanceHost />);
		act(() => openMaintenance("svc-d"));
		expect(
			await screen.findByText("Could not load this service."),
		).toBeInTheDocument();
		act(() => closeMaintenance());
	});

	test("turning maintenance on applies it and closes the dialog", async () => {
		const api = stubApi({
			"GET /services/:id": { body: service },
			"GET /environments/:id/services": {
				body: make("PageServiceView", { items: [service] }),
			},
			"PUT /services/:id/maintenance": { body: make("ServiceView") },
		});
		await renderWithProviders(
			<>
				<MaintenanceHost />
				<Target />
			</>,
		);
		act(() => openMaintenance("svc-d"));
		await userEvent
			.setup()
			.click(
				await screen.findByRole("button", { name: "Turn on maintenance" }),
			);
		await waitFor(() =>
			expect(screen.getByLabelText("target")).toHaveTextContent("none"),
		);
		expect(api.calls.find((c) => c.method === "PUT")).toMatchObject({
			path: "/services/svc-d/maintenance",
			body: { enabled: true },
		});
		await waitFor(() =>
			expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
		);
	});

	test("the close button dismisses the dialog and clears the target", async () => {
		stubApi({ "GET /services/:id": { body: service } });
		await renderWithProviders(
			<>
				<MaintenanceHost />
				<Target />
			</>,
		);
		act(() => openMaintenance("svc-d"));
		await screen.findByText("Maintenance mode for Storefront");
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: /close/i }));
		await waitFor(() =>
			expect(screen.getByLabelText("target")).toHaveTextContent("none"),
		);
	});
});
