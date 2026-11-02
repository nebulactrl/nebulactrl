import { describe, expect, mock, spyOn, test } from "bun:test";
import { act, fireEvent, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import type { Service } from "#/api/models";
import { InspectorContext } from "#/features/canvas/inspector/inspector-context";
import { makeService } from "#/features/canvas/lib/fixtures";
import { type ApiRoutes, stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { DangerZone } from "./danger-zone";

const HOLD_MS = 1300;

/** Mounts the zone with a recording inspector, as the Settings tab does. */
async function mount(
	service: Service,
	routes: ApiRoutes = {},
	options: { path?: string; routePath?: string } = {},
) {
	const api = stubApi(routes);
	const close = mock(() => {});
	const wrap = (ui: ReactNode) => (
		<InspectorContext.Provider value={{ close, openTab: () => {} }}>
			{ui}
		</InspectorContext.Provider>
	);
	const view = await renderWithProviders(
		wrap(<DangerZone service={service} />),
		options,
	);
	return { api, close, ...view };
}

const slugField = () => screen.getByLabelText("Confirm the slug");

/**
 * Presses the hold key with its 1.3 s timer shortened to the next tick (fake
 * clocks would strand react-query's timers) and lets it run.
 */
async function holdDelete(name: string | RegExp) {
	const button = screen.getByRole("button", { name });
	const real = window.setTimeout;
	const rushed = spyOn(window, "setTimeout").mockImplementation(((
		handler: TimerHandler,
		ms?: number,
		...args: unknown[]
	) =>
		real(
			handler,
			ms === HOLD_MS ? 0 : ms,
			...args,
		)) as typeof window.setTimeout);
	try {
		fireEvent.keyDown(button, { key: "Enter" });
	} finally {
		rushed.mockRestore();
	}
	const tick = Promise.withResolvers<void>();
	window.setTimeout(tick.resolve, 0);
	await act(async () => {
		await tick.promise;
	});
}

describe("DangerZone", () => {
	test("asks for the slug before anything can be deleted", async () => {
		const service = makeService({ id: "svc-1", name: "API", slug: "api" });
		const { api } = await mount(service);
		expect(screen.getByText("Type the slug first.")).toBeInTheDocument();
		expect(
			screen.getByText(/Deleting API stops the container/),
		).toBeInTheDocument();
		// The slug is shown as the thing to type, and as the placeholder.
		expect(slugField()).toHaveAttribute("placeholder", "api");

		await holdDelete("Hold to delete service");
		expect(api.calls).toEqual([]);
		expect(screen.queryByText("Deleted API")).toBeNull();
	});

	test("a wrong slug keeps the refusal; the right one, even padded, lifts it", async () => {
		const user = userEvent.setup();
		const service = makeService({ id: "svc-1", slug: "api" });
		await mount(service);
		await user.type(slugField(), "ap");
		expect(screen.getByText("Type the slug first.")).toBeInTheDocument();
		await user.type(slugField(), "i ");
		expect(slugField()).toHaveValue("api ");
		expect(screen.queryByText("Type the slug first.")).toBeNull();
	});

	test("holding with the slug typed deletes the service and keeps its volumes by default", async () => {
		const service = makeService({ id: "svc-1", name: "API", slug: "api" });
		const { api, close } = await mount(service, {
			"DELETE /services/svc-1": { status: 204 },
		});
		fireEvent.change(slugField(), { target: { value: "api" } });
		await holdDelete("Hold to delete service");

		expect(await screen.findByText("Deleted API")).toBeInTheDocument();
		expect(
			screen.getByText("Its volumes were kept on the server."),
		).toBeInTheDocument();
		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]?.method).toBe("DELETE");
		expect(api.calls[0]?.path).toBe("/services/svc-1");
		expect(api.calls[0]?.query.get("confirm")).toBe("api");
		expect(api.calls[0]?.query.get("deleteVolumes")).toBe("false");
		// The inspector was showing the service that is gone.
		expect(close).toHaveBeenCalledTimes(1);
		expect(api.unmatched).toEqual([]);
	});

	test("the volumes checkbox makes the delete take the volumes too", async () => {
		const user = userEvent.setup();
		const service = makeService({ id: "svc-1", name: "API", slug: "api" });
		const { api } = await mount(service, {
			"DELETE /services/svc-1": { status: 204 },
		});
		const box = screen.getByRole("checkbox", {
			name: /Also delete the service's volumes/,
		});
		expect(box).not.toBeChecked();
		await user.click(box);
		expect(box).toBeChecked();
		fireEvent.change(slugField(), { target: { value: "api" } });
		await holdDelete("Hold to delete service");

		expect(
			await screen.findByText("Its volumes were removed."),
		).toBeInTheDocument();
		expect(api.calls[0]?.query.get("deleteVolumes")).toBe("true");
	});

	test("a route has no volumes choice and is deleted through the routes API", async () => {
		const service = makeService({
			id: "rt-1",
			name: "Marketing",
			slug: "marketing",
			kind: "route",
		});
		const { api, close } = await mount(service, {
			"DELETE /routes/rt-1": { status: 204 },
		});
		expect(screen.queryByText("Volumes")).toBeNull();
		expect(screen.queryByRole("checkbox")).toBeNull();
		expect(
			screen.getByText(/Deleting Marketing removes its route from the proxy/),
		).toBeInTheDocument();

		fireEvent.change(slugField(), { target: { value: "marketing" } });
		await holdDelete("Hold to delete route");

		expect(await screen.findByText("Deleted Marketing")).toBeInTheDocument();
		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]?.path).toBe("/routes/rt-1");
		expect(api.calls[0]?.query.size).toBe(0);
		expect(close).toHaveBeenCalledTimes(1);
	});

	test("a refused delete shows the server's reason and leaves the inspector open", async () => {
		const service = makeService({ id: "svc-1", name: "API", slug: "api" });
		const { api, close } = await mount(service, {
			"DELETE /services/svc-1": {
				status: 409,
				body: {
					code: "has_dependents",
					detail: "Worker still depends on API.",
					title: "Conflict",
					status: 409,
				},
			},
		});
		fireEvent.change(slugField(), { target: { value: "api" } });
		await holdDelete("Hold to delete service");

		expect(
			await screen.findByText("Worker still depends on API."),
		).toBeInTheDocument();
		expect(close).not.toHaveBeenCalled();
		expect(screen.queryByText("Deleted API")).toBeNull();
		expect(api.calls).toHaveLength(1);
	});

	test("a service in production says so in the description and on the button", async () => {
		const service = makeService({
			id: "svc-1",
			name: "API",
			slug: "api",
			environmentId: "env-prod",
		});
		await mount(
			service,
			{
				"GET /projects": {
					body: make("PageProject", { items: [make("Project", { id: "p1" })] }),
				},
				"GET /projects/p1/environments": {
					body: make("PageEnvironment", {
						items: [
							make("Environment", {
								id: "env-prod",
								projectId: "p1",
								name: "Production",
								slug: "production",
							}),
						],
					}),
				},
			},
			{ path: "/p/p1/production", routePath: "/p/$projectId/$envSlug" },
		);
		expect(
			await screen.findByRole("button", {
				name: "Hold to delete service from production",
			}),
		).toBeInTheDocument();
		expect(
			screen.getByText(/API is in production \(Production\)\./),
		).toBeInTheDocument();
	});

	test("outside a project the plain wording is used", async () => {
		const service = makeService({ id: "svc-1", name: "API", slug: "api" });
		await mount(service);
		expect(
			screen.getByRole("button", { name: "Hold to delete service" }),
		).toBeInTheDocument();
		expect(screen.queryByText(/is in production/)).toBeNull();
	});
});
