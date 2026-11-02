import { describe, expect, mock, spyOn, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ReactFlow, ReactFlowProvider } from "@xyflow/react";
import type { Service } from "#/api/models";
import { onOpenStagedReview } from "#/features/deploy-button/open-review";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { makeDeployment, makeObserved, makeService } from "./lib/fixtures";
import { NODE_HEIGHT } from "./lib/layout";
import { SelectionDock, type SelectionDockProps } from "./selection-dock";

const DEPLOYMENT = make("DeploymentView", {
	id: "dep-new",
	serviceId: "svc-1",
	trigger: "manual",
	actorType: "user",
	actorLabel: "me",
	status: "queued",
	strategy: "rolling",
});

const running = (over: Partial<Service> = {}) =>
	makeService({
		id: "svc-1",
		name: "Storefront",
		currentDeploymentId: "dep-1",
		latestDeployment: makeDeployment("succeeded"),
		observed: makeObserved(),
		...over,
	});

async function dock(
	service: Service,
	props: Partial<SelectionDockProps> = {},
	routes: Parameters<typeof stubApi>[0] = {},
	withNode = true,
) {
	const handlers = {
		onOpen: mock(() => {}),
		onScale: mock(() => {}),
		onDelete: mock(() => {}),
	};
	const api = stubApi({
		"POST /services/:serviceId/deployments": { status: 202, body: DEPLOYMENT },
		"POST /services/:serviceId/restart": {
			status: 202,
			body: make("ActionResult"),
		},
		"POST /services/:serviceId/stop": {
			status: 202,
			body: make("ActionResult"),
		},
		...routes,
	});
	const view = await renderWithProviders(
		<div style={{ width: 800, height: 600 }}>
			<ReactFlowProvider>
				<ReactFlow
					width={800}
					height={600}
					nodes={
						withNode
							? [
									{
										id: service.id,
										position: { x: 0, y: 0 },
										width: 288,
										height: NODE_HEIGHT,
										data: {},
									},
								]
							: []
					}
					edges={[]}
				>
					<SelectionDock
						service={service}
						isProduction={false}
						rightInset={0}
						compact={false}
						{...handlers}
						{...props}
					/>
				</ReactFlow>
			</ReactFlowProvider>
		</div>,
	);
	return { ...view, api, ...handlers };
}

const toolbar = () =>
	screen.getByRole("toolbar", { name: "Actions for Storefront" });

describe("SelectionDock", () => {
	test("is a toolbar named for the selected service with Open, Redeploy, Scale and More", async () => {
		await dock(running());
		const bar = toolbar();
		expect(bar).toHaveAttribute("data-dock-for", "svc-1");
		expect(
			within(bar).getByRole("button", { name: /^Open/ }),
		).toHaveTextContent("Open↵");
		expect(within(bar).getByRole("button", { name: "Redeploy" })).toBeEnabled();
		expect(
			within(bar).getByRole("button", { name: "Scale" }),
		).toBeInTheDocument();
		expect(
			within(bar).getByRole("button", { name: "More actions for Storefront" }),
		).toBeInTheDocument();
	});

	test("a service that never ran says Deploy, not Redeploy", async () => {
		await dock(makeService({ id: "svc-1", name: "Storefront" }));
		expect(screen.getByRole("button", { name: "Deploy" })).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Redeploy" }),
		).not.toBeInTheDocument();
	});

	test("Open and Scale call the canvas's handlers", async () => {
		const user = userEvent.setup();
		const h = await dock(running());
		await user.click(screen.getByRole("button", { name: /^Open/ }));
		expect(h.onOpen).toHaveBeenCalledTimes(1);
		await user.click(screen.getByRole("button", { name: "Scale" }));
		expect(h.onScale).toHaveBeenCalledTimes(1);
		expect(h.onDelete).not.toHaveBeenCalled();
	});

	test("Scale is inert for a service that cannot scale, and says why on hover", async () => {
		const user = userEvent.setup();
		const base = running();
		const h = await dock({
			...base,
			scaling: {
				...base.scaling,
				eligible: false,
				ineligibleReason: "Databases scale from their own tab",
			},
		});
		const scale = screen.getByRole("button", { name: "Scale" });
		expect(scale).toHaveAttribute("aria-disabled", "true");
		await user.hover(scale.parentElement as HTMLElement);
		expect(
			await screen.findByText(
				"Databases scale from their own tab",
				{},
				{ timeout: 3000 },
			),
		).toBeInTheDocument();
		await user.click(scale);
		expect(h.onScale).not.toHaveBeenCalled();
	});

	test("without a stated reason an ineligible Scale falls back to a generic one", async () => {
		const user = userEvent.setup();
		const base = running();
		await dock({
			...base,
			scaling: { ...base.scaling, eligible: false, ineligibleReason: null },
		});
		await user.hover(
			screen.getByRole("button", { name: "Scale" })
				.parentElement as HTMLElement,
		);
		expect(
			await screen.findByText(
				"Replicas are not available for this service",
				{},
				{ timeout: 3000 },
			),
		).toBeInTheDocument();
	});

	test("Deploy outside production deploys at once with a POST to the service's deployments", async () => {
		const user = userEvent.setup();
		const h = await dock(running());
		await user.click(screen.getByRole("button", { name: "Redeploy" }));
		await waitFor(() =>
			expect(h.api.calls.map((c) => `${c.method} ${c.path}`)).toContain(
				"POST /services/svc-1/deployments",
			),
		);
		expect(h.api.unmatched).toEqual([]);
	});

	test("in production Deploy opens the review sheet for this service and posts nothing", async () => {
		const user = userEvent.setup();
		const opened = mock((_o: unknown) => {});
		const off = onOpenStagedReview(opened);
		try {
			const h = await dock(running(), { isProduction: true });
			await user.click(screen.getByRole("button", { name: "Redeploy" }));
			expect(opened).toHaveBeenCalledWith({ serviceId: "svc-1" });
			expect(h.api.calls).toEqual([]);
		} finally {
			off();
		}
	});

	test("More lists the service's actions, Delete last and destructive", async () => {
		const user = userEvent.setup();
		await dock(running());
		await user.click(
			screen.getByRole("button", { name: "More actions for Storefront" }),
		);
		const items = (await screen.findAllByRole("menuitem")).map(
			(item) => item.textContent,
		);
		expect(items).toEqual(
			expect.arrayContaining([
				"Redeploy",
				"Restart",
				"Stop",
				"Run command…",
				"Open shell…",
				"Browse files…",
				"Copy private host",
				"Delete…",
			]),
		);
		expect(items.at(-1)).toBe("Delete…");
	});

	test("Delete in More hands off to the canvas's delete handler", async () => {
		const user = userEvent.setup();
		const h = await dock(running());
		await user.click(
			screen.getByRole("button", { name: "More actions for Storefront" }),
		);
		await user.click(await screen.findByRole("menuitem", { name: "Delete…" }));
		expect(h.onDelete).toHaveBeenCalledTimes(1);
	});

	test("Restart in More posts to the service's restart endpoint", async () => {
		const user = userEvent.setup();
		const h = await dock(running());
		await user.click(
			screen.getByRole("button", { name: "More actions for Storefront" }),
		);
		await user.click(await screen.findByRole("menuitem", { name: "Restart" }));
		await waitFor(() =>
			expect(h.api.calls.map((c) => `${c.method} ${c.path}`)).toContain(
				"POST /services/svc-1/restart",
			),
		);
	});

	test("a stopped service offers Start in place of Restart and Stop", async () => {
		const user = userEvent.setup();
		await dock(running({ desiredState: "stopped" }));
		await user.click(
			screen.getByRole("button", { name: "More actions for Storefront" }),
		);
		await screen.findAllByRole("menuitem");
		expect(screen.getByRole("menuitem", { name: "Start" })).toBeInTheDocument();
		expect(
			screen.queryByRole("menuitem", { name: "Restart" }),
		).not.toBeInTheDocument();
		expect(
			screen.queryByRole("menuitem", { name: "Stop" }),
		).not.toBeInTheDocument();
	});

	test("a service that never ran offers no Restart, shell or files, and Restart is disabled", async () => {
		const user = userEvent.setup();
		await dock(makeService({ id: "svc-1", name: "Storefront" }));
		await user.click(
			screen.getByRole("button", { name: "More actions for Storefront" }),
		);
		await screen.findAllByRole("menuitem");
		expect(screen.getByRole("menuitem", { name: "Restart" })).toHaveAttribute(
			"aria-disabled",
			"true",
		);
		expect(
			screen.queryByRole("menuitem", { name: "Open shell…" }),
		).not.toBeInTheDocument();
		expect(
			screen.queryByRole("menuitem", { name: "Browse files…" }),
		).not.toBeInTheDocument();
	});

	test("Copy private host puts the host on the clipboard", async () => {
		const user = userEvent.setup();
		const write = mock(() => Promise.resolve());
		const clipboard = spyOn(
			navigator.clipboard,
			"writeText",
		).mockImplementation(write);
		try {
			await dock(running({ privateHost: "storefront.internal" }));
			await user.click(
				screen.getByRole("button", { name: "More actions for Storefront" }),
			);
			await user.click(
				await screen.findByRole("menuitem", { name: "Copy private host" }),
			);
			expect(write).toHaveBeenCalledWith("storefront.internal");
		} finally {
			clipboard.mockRestore();
		}
	});

	test("compact mode shows icon-only keys that keep their names", async () => {
		await dock(running(), { compact: true });
		const bar = toolbar();
		for (const name of ["Open", "Redeploy", "Scale"]) {
			const button = within(bar).getByRole("button", { name });
			expect(button.textContent).toBe("");
		}
		expect(within(bar).getByRole("button", { name: "Open" })).toHaveAttribute(
			"title",
			"Open (Enter)",
		);
		expect(bar.style.width).toBe("168px");
	});

	test("the roomy dock is 300px wide", async () => {
		await dock(running());
		expect(toolbar().style.width).toBe("300px");
	});

	test("sits under its card: centred on it, clamped to the canvas margin", async () => {
		await dock(running());
		// Card at x 0..288 (centre 144): the 300px dock would start at -6, so it is held at the 12px margin.
		expect(toolbar().style.transform).toBe(
			`translate3d(12px, ${NODE_HEIGHT + 14}px, 0)`,
		);
		expect(toolbar().style.visibility).toBe("");
	});

	test("hides while its card is not on the canvas", async () => {
		const view = await dock(running(), {}, {}, false);
		const { container } = view;
		expect(
			container.querySelector<HTMLElement>("[data-dock-for='svc-1']")?.style
				.visibility,
		).toBe("hidden");
	});

	test("keeps inside the free canvas when the inspector covers the right edge", async () => {
		await dock(running(), { rightInset: 700 });
		// 800 - 700 = 100px free: the dock cannot fit, it still starts at the margin.
		expect(toolbar().style.transform).toMatch(/^translate3d\(12px,/);
	});
});
