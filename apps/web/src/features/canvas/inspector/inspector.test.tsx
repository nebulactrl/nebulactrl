import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import {
	act,
	fireEvent,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type { Service } from "#/api/models";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { makeDeployment, makeObserved, makeService } from "../lib/fixtures";
import { useDomEventTarget } from "./dom-event-target.test-helper";
import {
	INSPECTOR_DOCKED_CSS,
	publishInspectorWidth,
	useInspectorWidthPx,
} from "./geometry";
import { Inspector } from "./inspector";
import type { InspectorTab } from "./tab-ids";

const empty = { items: [] };
const environment = {
	id: "env-1",
	projectId: "p1",
	name: "Staging",
	slug: "staging",
	isProduction: false,
	kind: "standard",
	parentEnvironmentId: null,
	prNumber: null,
	prTitle: "",
	createdAt: "2026-01-01T00:00:00Z",
};
const uptime = {
	serviceId: "svc-i",
	state: "unknown",
	since: null,
	range: "1h",
	granularity: "minute",
	availability: { "24h": null, "7d": null, "30d": null, "90d": null },
	consecutiveFailures: 0,
	failureThreshold: 3,
	incidents: [],
	lastCheckAt: null,
	lastError: "",
	lastOkAt: null,
	latencyMs: null,
	monitor: {
		enabled: true,
		alerts: true,
		failureThreshold: 3,
		recoveryThreshold: 2,
		reminderMinutes: 0,
	},
	series: [],
};

/** Every read the Overview and the header make. */
function inspectorApi(extra: Parameters<typeof stubApi>[0] = {}) {
	return stubApi({
		"GET /projects": { body: empty },
		"GET /projects/:projectId/environments": { body: { items: [environment] } },
		"GET /services/:serviceId/config-source": {
			body: make("ServiceConfigSource", { managed: false }),
		},
		"GET /servers": { body: empty },
		"GET /services/:serviceId/uptime": { body: uptime },
		"GET /services/:serviceId/incidents": { body: empty },
		"GET /services/:serviceId/deployments": { body: empty },
		"GET /services/:serviceId/variables": {
			body: make("VariableList", { items: [] }),
		},
		"GET /services/:serviceId/metrics": {
			body: make("MetricsOutputBody", { points: [] }),
		},
		"GET /services/:serviceId/logs": () =>
			new Response("event: end\ndata: {}\n\n", {
				headers: { "Content-Type": "text/event-stream" },
			}),
		...extra,
	});
}

const deployed = (overrides: Partial<Service> = {}) =>
	makeService({
		id: "svc-i",
		name: "api",
		currentDeploymentId: "dep-1",
		latestDeployment: makeDeployment("succeeded", { id: "dep-1" }),
		observed: makeObserved(),
		...overrides,
	});

interface Handle {
	tabs: InspectorTab[];
	sections: string[];
	closed: number;
}

/** A controlled host: the inspector's tab follows what it asks for, as the route does. */
function Host({
	service,
	initialTab = "overview",
	handle,
}: {
	service: Service | undefined;
	initialTab?: InspectorTab;
	handle: Handle;
}) {
	const [tab, setTab] = useState<InspectorTab>(initialTab);
	return (
		<Inspector
			service={service}
			tab={tab}
			section={undefined}
			onTabChange={(next) => {
				handle.tabs.push(next);
				setTab(next);
			}}
			onSectionChange={(section) => handle.sections.push(section)}
			onClose={() => {
				handle.closed += 1;
			}}
		/>
	);
}

const newHandle = (): Handle => ({ tabs: [], sections: [], closed: 0 });

function renderInspector(
	service: Service | undefined,
	options: { tab?: InspectorTab; handle?: Handle } = {},
) {
	const handle = options.handle ?? newHandle();
	return {
		handle,
		view: renderWithProviders(
			<Host
				service={service}
				initialTab={options.tab ?? "overview"}
				handle={handle}
			/>,
			{ path: "/p/p1/staging", routePath: "/p/$projectId/$envSlug" },
		),
	};
}

function setViewport(width: number) {
	Object.defineProperty(window, "innerWidth", {
		configurable: true,
		writable: true,
		value: width,
	});
}

let restoreEventTarget: () => void;

beforeEach(() => {
	restoreEventTarget = useDomEventTarget();
	setViewport(1440);
	window.localStorage.clear();
});

afterEach(() => {
	restoreEventTarget();
	act(() => publishInspectorWidth(0));
	window.localStorage.clear();
	setViewport(1024);
});

describe("Inspector", () => {
	test("is a panel named for the service with its header and tab strip", async () => {
		inspectorApi();
		const { view } = renderInspector(deployed());
		await view;
		const panel = await screen.findByRole("dialog", { name: "api" });
		expect(
			within(panel).getByRole("heading", { name: "api" }),
		).toBeInTheDocument();
		const tabs = within(panel).getByRole("tablist", {
			name: "Service sections",
		});
		expect(
			within(tabs)
				.getAllByRole("tab")
				.map((tab) => tab.textContent),
		).toEqual([
			"Overview",
			"Deployments",
			"Variables",
			"Metrics",
			"Analytics",
			"Logs",
			"Settings",
		]);
		expect(within(tabs).getByRole("tab", { name: "Overview" })).toHaveAttribute(
			"aria-selected",
			"true",
		);
	});

	test("a service with a volume also has Backups", async () => {
		inspectorApi();
		await renderInspector(
			deployed({ volumes: [{ name: "data", path: "/data" } as never] }),
		).view;
		const tabs = await screen.findByRole("tablist", {
			name: "Service sections",
		});
		expect(
			within(tabs).getByRole("tab", { name: "Backups" }),
		).toBeInTheDocument();
	});

	test("a route service has only Overview, Deployments and Settings", async () => {
		inspectorApi();
		await renderInspector(
			makeService({
				id: "svc-r",
				kind: "route",
				sourceType: "external",
				name: "plex",
			}),
			{ tab: "deployments" },
		).view;
		const tabs = await screen.findByRole("tablist", {
			name: "Service sections",
		});
		expect(
			within(tabs)
				.getAllByRole("tab")
				.map((tab) => tab.textContent),
		).toEqual(["Overview", "Deployments", "Settings"]);
	});

	test("shows staged changes as a count on the tab that holds them", async () => {
		inspectorApi();
		await renderInspector(
			deployed({ pendingChangeCounts: { variables: 2, settings: 0 } }),
		).view;
		const variables = await screen.findByRole("tab", { name: /Variables/ });
		expect(within(variables).getByLabelText("2 staged")).toHaveTextContent("2");
		expect(
			within(screen.getByRole("tab", { name: /Settings/ })).queryByLabelText(
				/staged/,
			),
		).toBeNull();
		expect(
			within(screen.getByRole("tab", { name: /Overview/ })).queryByLabelText(
				/staged/,
			),
		).toBeNull();
	});

	test("shows the settings count on Settings", async () => {
		inspectorApi();
		await renderInspector(
			deployed({ pendingChangeCounts: { variables: 0, settings: 3 } }),
		).view;
		const settings = await screen.findByRole("tab", { name: /Settings/ });
		expect(within(settings).getByLabelText("3 staged")).toBeInTheDocument();
		expect(
			within(screen.getByRole("tab", { name: /Variables/ })).queryByLabelText(
				/staged/,
			),
		).toBeNull();
	});

	test("opens on the Overview body", async () => {
		inspectorApi();
		await renderInspector(deployed()).view;
		expect(
			await screen.findByRole(
				"region",
				{ name: "Overview" },
				{ timeout: 5000 },
			),
		).toHaveAttribute("data-service-id", "svc-i");
	});

	test("clicking a tab asks for it", async () => {
		inspectorApi();
		const { handle, view } = renderInspector(deployed());
		await view;
		await userEvent
			.setup()
			.click(await screen.findByRole("tab", { name: "Metrics" }));
		expect(handle.tabs).toEqual(["metrics"]);
		expect(
			await screen.findByRole("region", { name: "Metrics" }, { timeout: 5000 }),
		).toBeInTheDocument();
	});

	test("g then a letter jumps to that tab while it is open", async () => {
		inspectorApi();
		const { handle, view } = renderInspector(deployed());
		await view;
		await screen.findByRole("region", { name: "Overview" }, { timeout: 5000 });
		fireEvent.keyDown(document.body, { key: "g" });
		fireEvent.keyDown(document.body, { key: "l" });
		expect(handle.tabs).toEqual(["logs"]);
	});

	test("a tab the service does not have resolves to Overview", async () => {
		inspectorApi();
		await renderInspector(deployed(), { tab: "backups" }).view;
		expect(
			await screen.findByRole(
				"region",
				{ name: "Overview" },
				{ timeout: 5000 },
			),
		).toBeInTheDocument();
		expect(screen.getByRole("tab", { name: "Overview" })).toHaveAttribute(
			"aria-selected",
			"true",
		);
	});

	test("the close button closes it", async () => {
		inspectorApi();
		const { handle, view } = renderInspector(deployed());
		await view;
		await userEvent
			.setup()
			.click(await screen.findByRole("button", { name: "Close inspector" }));
		expect(handle.closed).toBe(1);
	});

	test("Escape closes it", async () => {
		inspectorApi();
		const { handle, view } = renderInspector(deployed());
		await view;
		await screen.findByRole("dialog", { name: "api" });
		await userEvent.setup().keyboard("{Escape}");
		expect(handle.closed).toBe(1);
	});

	test("with no service it renders nothing", async () => {
		const api = inspectorApi();
		const { view } = renderInspector(undefined);
		await view;
		expect(screen.queryByRole("dialog")).toBeNull();
		expect(api.calls).toEqual([]);
	});

	test("a tab body can open another tab (Resources' Metrics link)", async () => {
		inspectorApi();
		const { handle, view } = renderInspector(deployed());
		await view;
		await userEvent
			.setup()
			.click(await screen.findByRole("button", { name: "Metrics" }));
		expect(handle.tabs).toEqual(["metrics"]);
	});
});

function WidthProbe() {
	return <output aria-label="reserved">{useInspectorWidthPx()}</output>;
}

/** The inspector beside a probe of the canvas room it reserves. */
function Both({ open, handle }: { open: boolean; handle: Handle }) {
	return (
		<>
			<Host service={open ? deployed() : undefined} handle={handle} />
			<WidthProbe />
		</>
	);
}

/** Like Both, with a button that closes the inspector. */
function Toggle({ handle }: { handle: Handle }) {
	const [open, setOpen] = useState(true);
	return (
		<>
			<Both open={open} handle={handle} />
			<button type="button" onClick={() => setOpen(false)}>
				shut
			</button>
		</>
	);
}

describe("Inspector width", () => {
	test("reserves its docked width from the canvas while open and gives it back on close", async () => {
		inspectorApi();
		setViewport(1440);
		const handle = newHandle();
		const view = await renderWithProviders(<Both open handle={handle} />, {
			path: "/p/p1/staging",
			routePath: "/p/$projectId/$envSlug",
		});
		// docked: clamp(640px, 48vw, 920px) at 1440 → 691
		await waitFor(() =>
			expect(screen.getByLabelText("reserved")).toHaveTextContent("691"),
		);
		view.unmount();
		expect(window.localStorage.length).toBe(0);
	});

	test("on a phone it is a full-screen sheet and reserves no canvas", async () => {
		inspectorApi();
		setViewport(390);
		const handle = newHandle();
		await renderWithProviders(
			<>
				<Host service={deployed()} handle={handle} />
				<WidthProbe />
			</>,
			{ path: "/p/p1/staging", routePath: "/p/$projectId/$envSlug" },
		);
		await screen.findByRole("dialog", { name: "api" });
		expect(screen.getByLabelText("reserved")).toHaveTextContent("0");
	});

	test("a wide tab is wider than a docked one, in px", async () => {
		inspectorApi();
		setViewport(1440);
		const handle = newHandle();
		await renderWithProviders(
			<>
				<Host service={deployed()} initialTab="logs" handle={handle} />
				<WidthProbe />
			</>,
			{ path: "/p/p1/staging", routePath: "/p/$projectId/$envSlug" },
		);
		// wide: min(66vw, vw − 420) at 1440 → 950
		await waitFor(() =>
			expect(screen.getByLabelText("reserved")).toHaveTextContent("950"),
		);
	});

	test("starts from the width the reader saved for the tab's class", async () => {
		inspectorApi();
		setViewport(1440);
		window.localStorage.setItem("nebulactrl.inspector.width.v2.docked", "800");
		const handle = newHandle();
		await renderWithProviders(
			<>
				<Host service={deployed()} handle={handle} />
				<WidthProbe />
			</>,
			{ path: "/p/p1/staging", routePath: "/p/$projectId/$envSlug" },
		);
		await waitFor(() =>
			expect(screen.getByLabelText("reserved")).toHaveTextContent("800"),
		);
		expect(INSPECTOR_DOCKED_CSS).toContain("clamp");
	});

	test("dragging the edge (Home: narrowest) resizes it, reserves that width and remembers it for the tab's class", async () => {
		inspectorApi();
		setViewport(1440);
		const handle = newHandle();
		await renderWithProviders(
			<>
				<Host service={deployed()} handle={handle} />
				<WidthProbe />
			</>,
			{ path: "/p/p1/staging", routePath: "/p/$projectId/$envSlug" },
		);
		const separator = await screen.findByRole("separator", {
			name: "Resize panel",
		});
		fireEvent.keyDown(separator, { key: "Home" });
		await waitFor(() =>
			expect(screen.getByLabelText("reserved")).toHaveTextContent("360"),
		);
		expect(
			window.localStorage.getItem("nebulactrl.inspector.width.v2.docked"),
		).toBe("360");
		// The wide class keeps its own width.
		expect(
			window.localStorage.getItem("nebulactrl.inspector.width.v2.wide"),
		).toBeNull();
	});

	test("a phone sheet cannot be resized", async () => {
		inspectorApi();
		setViewport(390);
		await renderInspector(deployed()).view;
		await screen.findByRole("dialog", { name: "api" });
		expect(
			screen.queryByRole("separator", { name: "Resize panel" }),
		).toBeNull();
	});

	test("the wide tabs leave room for the canvas next to the sidebar", async () => {
		inspectorApi();
		setViewport(1440);
		const canvas = document.createElement("div");
		canvas.setAttribute("data-canvas-root", "");
		canvas.getBoundingClientRect = () =>
			({
				left: 300,
				top: 0,
				right: 1440,
				bottom: 900,
				width: 1140,
				height: 900,
				x: 300,
				y: 0,
			}) as DOMRect;
		document.body.append(canvas);
		const handle = newHandle();
		await renderWithProviders(
			<>
				<Host service={deployed()} initialTab="logs" handle={handle} />
				<WidthProbe />
			</>,
			{ path: "/p/p1/staging", routePath: "/p/$projectId/$envSlug" },
		);
		// min(66vw = 950, vw − left − 420 = 720)
		await waitFor(() =>
			expect(screen.getByLabelText("reserved")).toHaveTextContent("720"),
		);
		canvas.remove();
	});

	test("closing releases the canvas room at once while the panel keeps what it showed", async () => {
		// The setup lets popups unmount at once; this test is about the panel that
		// stays mounted while its exit animation runs.
		globalThis.BASE_UI_ANIMATIONS_DISABLED = false;
		try {
			inspectorApi();
			setViewport(1440);
			const handle = newHandle();
			await renderWithProviders(<Toggle handle={handle} />, {
				path: "/p/p1/staging",
				routePath: "/p/$projectId/$envSlug",
			});
			await waitFor(() =>
				expect(screen.getByLabelText("reserved")).toHaveTextContent("691"),
			);
			// happy-dom does not commit a stopped animation's values the way a browser
			// does; state where the panel rests so the slide-out starts from there.
			const panel = await screen.findByRole("dialog", { name: "api" });
			panel.style.opacity = "1";
			panel.style.transform = "none";
			await userEvent
				.setup()
				.click(screen.getByRole("button", { name: "shut" }));
			await waitFor(() =>
				expect(screen.getByLabelText("reserved")).toHaveTextContent("0"),
			);
			// It slides out showing the last service, not an empty frame.
			expect(screen.getByRole("heading", { name: "api" })).toBeInTheDocument();
		} finally {
			globalThis.BASE_UI_ANIMATIONS_DISABLED = true;
		}
	});
});
