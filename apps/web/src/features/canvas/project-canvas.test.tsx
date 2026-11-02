import {
	afterEach,
	expect,
	mock,
	setDefaultTimeout,
	spyOn,
	test,
} from "bun:test";
import {
	act,
	fireEvent,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type {
	CanvasDocument,
	CanvasLayout,
	Service,
	ServiceLink,
} from "#/api/models";
import { useCreateState } from "#/features/create/store";
import { useLinkState } from "#/features/links/store";
import {
	templatesActions,
	useTemplatesState,
} from "#/features/templates/store";
import { type ApiRoutes, stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { landOnCanvas } from "./canvas-landing";
import { makeNoConfigSource, makeObserved, makeService } from "./lib/fixtures";
import { emptyLayout } from "./lib/layout";
import { ProjectCanvas, type ProjectCanvasProps } from "./project-canvas";
import { placeNode } from "./use-layout-persistence";

setDefaultTimeout(30_000);

/*
 * The inspector's sheet slides with `motion`, whose "is this an element" check
 * (`instanceof EventTarget`) fails under happy-dom: the global EventTarget is
 * not the class DOM nodes derive from. A test that opens the inspector swaps
 * in the DOM's own class for its duration.
 */
const DomEventTarget = Object.getPrototypeOf(Node.prototype).constructor;
const realEventTarget = globalThis.EventTarget;
function withDomEventTarget() {
	globalThis.EventTarget = DomEventTarget;
}
afterEach(() => {
	globalThis.EventTarget = realEventTarget;
	templatesActions.close();
});

const CANVAS_ROUTE = "/projects/:p/environments/:e/canvas";

const a = makeService({
	id: "a",
	name: "Alpha",
	currentDeploymentId: "dep-a",
	observed: makeObserved(),
});
const b = makeService({ id: "b", name: "Beta" });
const c = makeService({ id: "c", name: "Gamma" });

function layout(
	nodes: CanvasLayout["nodes"],
	extra: Partial<CanvasLayout> = {},
): CanvasLayout {
	return { nodes, groups: [], viewport: null, ...extra };
}

const saved: CanvasDocument = {
	version: 2,
	layout: layout({
		a: { x: 0, y: 0 },
		b: { x: 400, y: 0 },
		c: { x: 800, y: 0 },
	}),
};

/** The deployment a deploy request is answered with. */
const queued = () => make("DeploymentView", { id: "d1", status: "queued" });

/** The canvas's PUTs are recorded; everything else a card or dock asks is answered empty. */
function canvasApi(extra: ApiRoutes = {}) {
	const puts: Array<{ layout: CanvasLayout; ifMatch: string | null }> = [];
	const api = stubApi({
		[`PUT ${CANVAS_ROUTE}`]: async (request) => {
			const body = (await request.clone().json()) as { layout: CanvasLayout };
			puts.push({
				layout: body.layout,
				ifMatch: request.headers.get("If-Match"),
			});
			return Response.json({ version: 10 + puts.length, layout: body.layout });
		},
		"GET /environments/:e/config-source": { body: makeNoConfigSource() },
		"GET /services/:s/metrics": {
			body: { points: [], retentionDays: 7, stepSeconds: 5 },
		},
		...extra,
	});
	return { api, puts };
}

function props(
	overrides: Partial<ProjectCanvasProps> = {},
): ProjectCanvasProps {
	return {
		projectId: "p1",
		environmentId: "env-1",
		isProduction: false,
		environmentName: "Staging",
		services: [a, b, c],
		links: [],
		servers: [],
		document: saved,
		inspectedServiceId: undefined,
		tab: "overview",
		section: undefined,
		onInspect: () => {},
		onTabChange: () => {},
		onSectionChange: () => {},
		...overrides,
	};
}

function ui(p: ProjectCanvasProps) {
	return (
		<div style={{ width: 1200, height: 800 }}>
			<ProjectCanvas {...p} />
		</div>
	);
}

/** Holds the canvas's props in state, so a test can change them like the route does. */
const seenStores: {
	create: ReturnType<typeof useCreateState> | undefined;
	templates: boolean;
	references: ServiceLink | null;
	deleting: string | null;
} = { create: undefined, templates: false, references: null, deleting: null };

/** Reads the stores the canvas's menus and dialogs write to. */
function StoreProbe() {
	seenStores.create = useCreateState();
	const templates = useTemplatesState();
	seenStores.templates = templates.open;
	seenStores.deleting = templates.deleting?.id ?? null;
	seenStores.references = useLinkState().references;
	return null;
}

function Host({
	initial,
	setter,
}: {
	initial: ProjectCanvasProps;
	setter: {
		current: ((next: Partial<ProjectCanvasProps>) => void) | undefined;
	};
}) {
	const [current, setCurrent] = useState(initial);
	setter.current = (next) =>
		setCurrent((previous) => ({ ...previous, ...next }));
	return (
		<>
			{ui(current)}
			<StoreProbe />
		</>
	);
}

async function mountCanvas(
	overrides: Partial<ProjectCanvasProps> = {},
	ready = "Alpha",
) {
	const setter: Parameters<typeof Host>[0]["setter"] = { current: undefined };
	const view = await renderWithProviders(
		<Host initial={props(overrides)} setter={setter} />,
	);
	await screen.findAllByText(ready);
	return {
		...view,
		rerenderCanvas: async (next: Partial<ProjectCanvasProps>) => {
			await act(async () => {
				setter.current?.(next);
			});
		},
	};
}

function node(id: string): HTMLElement {
	const el = document.querySelector<HTMLElement>(
		`.react-flow__node[data-id="${id}"]`,
	);
	if (!el) throw new Error(`no node ${id}`);
	return el;
}

const position = (id: string) => node(id).style.transform;
const key = (name: string, init: KeyboardEventInit = {}) =>
	act(() => {
		fireEvent.keyDown(document.body, { key: name, ...init });
	});
const selected = () =>
	[...document.querySelectorAll(".react-flow__node.selected")].map((el) =>
		el.getAttribute("data-id"),
	);

test("every service is a card at the place the saved layout gave it", async () => {
	canvasApi();
	await mountCanvas();
	expect(screen.getByText("Beta")).toBeInTheDocument();
	expect(screen.getByText("Gamma")).toBeInTheDocument();
	expect(position("a")).toBe("translate(0px,0px)");
	expect(position("b")).toBe("translate(400px,0px)");
	expect(position("c")).toBe("translate(800px,0px)");
});

test("an empty environment shows the invitation and no card", async () => {
	canvasApi();
	await renderWithProviders(
		ui(
			props({ services: [], document: { version: 0, layout: emptyLayout() } }),
		),
	);
	expect(
		await screen.findByText("Create your first service"),
	).toBeInTheDocument();
	expect(document.querySelector(".react-flow__node")).toBeNull();
	// With nothing to arrange the toolbar's Arrange is off.
	expect(screen.getByRole("button", { name: /Arrange/ })).toBeDisabled();
});

test("the private network caption names the environment once there are services", async () => {
	canvasApi();
	await mountCanvas({ environmentName: "Staging" });
	expect(screen.getByText(/Staging/)).toBeInTheDocument();
});

// --- first layout: new and deleted services --------------------------------

test("services the saved layout has no place for get one, and the layout is saved when the canvas closes", async () => {
	const { puts } = canvasApi();
	const view = await mountCanvas({
		document: { version: 4, layout: layout({ a: { x: 0, y: 0 } }) },
	});
	// All three are drawn, the two new ones somewhere of their own.
	expect(position("a")).toBe("translate(0px,0px)");
	const placed = [position("b"), position("c")];
	expect(placed).not.toContain("");
	expect(placed[0]).not.toBe(placed[1]);
	view.unmount();
	await waitFor(() => expect(puts).toHaveLength(1));
	expect(Object.keys(puts[0]?.layout.nodes ?? {}).sort()).toEqual([
		"a",
		"b",
		"c",
	]);
	expect(puts[0]?.layout.nodes.a).toEqual({ x: 0, y: 0 });
	expect(puts[0]?.ifMatch).toBe('"4"');
});

test("a saved place of a service that no longer exists is dropped on the next save", async () => {
	const { puts } = canvasApi();
	const view = await mountCanvas({
		services: [a, b],
		document: {
			version: 6,
			layout: layout({
				a: { x: 0, y: 0 },
				b: { x: 400, y: 0 },
				ghost: { x: 9, y: 9 },
			}),
		},
	});
	view.unmount();
	await waitFor(() => expect(puts).toHaveLength(1));
	expect(Object.keys(puts[0]?.layout.nodes ?? {}).sort()).toEqual(["a", "b"]);
});

test("a layout that already fits the services is not written back", async () => {
	const { api } = canvasApi();
	const view = await mountCanvas();
	view.unmount();
	await Promise.resolve();
	expect(api.calls.filter((call) => call.method === "PUT")).toEqual([]);
});

test("a service created while the canvas is open gets a card and the layout is saved", async () => {
	const { puts } = canvasApi();
	const view = await mountCanvas({
		services: [a, b],
		document: {
			version: 2,
			layout: layout({ a: { x: 0, y: 0 }, b: { x: 400, y: 0 } }),
		},
	});
	await view.rerenderCanvas({ services: [a, b, c] });
	expect(await screen.findByText("Gamma")).toBeInTheDocument();
	view.unmount();
	await waitFor(() => expect(puts).toHaveLength(1));
	expect(Object.keys(puts[0]?.layout.nodes ?? {})).toContain("c");
});

// --- keyboard ---------------------------------------------------------------

test("an arrow key with nothing selected selects the top-left card and shows its dock", async () => {
	canvasApi();
	await mountCanvas();
	await key("ArrowRight");
	expect(selected()).toEqual(["a"]);
	expect(
		await screen.findByRole("toolbar", { name: "Actions for Alpha" }),
	).toBeInTheDocument();
});

test("arrows walk the selection to the neighbour in that direction", async () => {
	canvasApi();
	await mountCanvas();
	await key("ArrowRight");
	await key("ArrowRight");
	expect(selected()).toEqual(["b"]);
	await key("ArrowRight");
	expect(selected()).toEqual(["c"]);
	await key("ArrowLeft");
	expect(selected()).toEqual(["b"]);
});

test("Enter opens the selected card's inspector", async () => {
	canvasApi();
	const onInspect = mock(
		(_id?: string, _tab?: string, _section?: string) => {},
	);
	await mountCanvas({
		onInspect: onInspect as ProjectCanvasProps["onInspect"],
	});
	await key("Enter");
	expect(onInspect).not.toHaveBeenCalled();
	await key("ArrowRight");
	await key(" ");
	expect(onInspect).toHaveBeenCalledWith("a", undefined, undefined);
});

test("Delete on one selected card goes to the danger zone of its settings", async () => {
	canvasApi();
	const onInspect = mock(
		(_id?: string, _tab?: string, _section?: string) => {},
	);
	await mountCanvas({
		onInspect: onInspect as ProjectCanvasProps["onInspect"],
	});
	await key("Delete");
	expect(onInspect).not.toHaveBeenCalled();
	await key("ArrowRight");
	await key("Backspace");
	expect(onInspect).toHaveBeenCalledWith("a", "settings", "danger");
});

test("Delete with several cards selected refuses and says to pick one", async () => {
	canvasApi();
	const onInspect = mock(
		(_id?: string, _tab?: string, _section?: string) => {},
	);
	await mountCanvas({
		onInspect: onInspect as ProjectCanvasProps["onInspect"],
	});
	await key("a", { metaKey: true });
	expect(selected().sort()).toEqual(["a", "b", "c"]);
	await key("Delete");
	expect(
		await screen.findByText("Delete one service at a time"),
	).toBeInTheDocument();
	expect(
		screen.getByText("Select a single service, then press Delete."),
	).toBeInTheDocument();
	expect(onInspect).not.toHaveBeenCalledWith("a", "settings", "danger");
});

test("⌘A selects every card; Escape clears the selection", async () => {
	canvasApi();
	await mountCanvas();
	await key("a", { ctrlKey: true });
	expect(selected().sort()).toEqual(["a", "b", "c"]);
	await key("Escape");
	expect(selected()).toEqual([]);
});

test("Escape closes an open inspector first, and leaves the selection alone", async () => {
	withDomEventTarget();
	canvasApi();
	const onInspect = mock(
		(_id?: string, _tab?: string, _section?: string) => {},
	);
	await mountCanvas({
		inspectedServiceId: "b",
		onInspect: onInspect as ProjectCanvasProps["onInspect"],
	});
	expect(selected()).toEqual(["b"]);
	await key("Escape");
	expect(onInspect).toHaveBeenCalledWith(undefined);
	expect(selected()).toEqual(["b"]);
});

test("⌘F opens the search field, Escape in it clears and closes it", async () => {
	canvasApi();
	await mountCanvas();
	expect(
		screen.queryByRole("searchbox", { name: "Search services" }),
	).toBeNull();
	await key("f", { metaKey: true });
	const field = await screen.findByRole("searchbox", {
		name: "Search services",
	});
	await userEvent.setup().type(field, "alp");
	expect(await screen.findByText("1 of 3")).toBeInTheDocument();
	fireEvent.keyDown(field, { key: "Escape" });
	await waitFor(() =>
		expect(
			screen.queryByRole("searchbox", { name: "Search services" }),
		).toBeNull(),
	);
});

test("a search that matches nothing says so", async () => {
	canvasApi();
	await mountCanvas();
	await key("f", { ctrlKey: true });
	await userEvent
		.setup()
		.type(await screen.findByRole("searchbox"), "zzz-nothing");
	expect(await screen.findByText("No matches")).toBeInTheDocument();
});

test("the Search button in the toolbar opens the field too", async () => {
	canvasApi();
	await mountCanvas();
	await userEvent
		.setup()
		.click(screen.getByRole("button", { name: "Search services" }));
	expect(
		await screen.findByRole("searchbox", { name: "Search services" }),
	).toBeInTheDocument();
	await userEvent
		.setup()
		.click(screen.getByRole("button", { name: "Close search" }));
	await waitFor(() => expect(screen.queryByRole("searchbox")).toBeNull());
});

test("? opens the keyboard shortcuts", async () => {
	canvasApi();
	await mountCanvas();
	await key("?");
	expect(await screen.findByText("Keyboard shortcuts")).toBeInTheDocument();
});

test("keys typed in a text field never reach the canvas shortcuts", async () => {
	canvasApi();
	await mountCanvas();
	await key("f", { metaKey: true });
	const field = await screen.findByRole("searchbox");
	field.focus();
	fireEvent.keyDown(field, { key: "?" });
	fireEvent.keyDown(field, { key: "ArrowRight" });
	expect(screen.queryByText("Keyboard shortcuts")).toBeNull();
	expect(selected()).toEqual([]);
});

test("canvas keys are ignored while a dialog is open", async () => {
	canvasApi();
	await mountCanvas();
	await key("?");
	await screen.findByRole("dialog");
	await key("ArrowRight");
	expect(selected()).toEqual([]);
});

test("+ zooms the camera in and 0 puts it back to 100%", async () => {
	canvasApi();
	await mountCanvas();
	const percent = () =>
		Number(
			document
				.querySelector('[title^="Reset to 100%"]')
				?.getAttribute("aria-label")
				?.match(/\d+/)?.[0],
		);
	const start = percent();
	await key("+");
	await waitFor(() => expect(percent()).toBeGreaterThan(start), {
		timeout: 4000,
	});
	const zoomedIn = percent();
	await key("0");
	await waitFor(() => expect(percent()).toBe(100), { timeout: 4000 });
	expect(zoomedIn).toBeGreaterThan(100);
});

test("- zooms the camera out", async () => {
	canvasApi();
	await mountCanvas();
	const percent = () =>
		Number(
			document
				.querySelector('[title^="Reset to 100%"]')
				?.getAttribute("aria-label")
				?.match(/\d+/)?.[0],
		);
	await key("-");
	await waitFor(() => expect(percent()).toBeLessThan(100), { timeout: 4000 });
});

// --- the context menus --------------------------------------------------------

function menu() {
	return screen.findByRole("menu");
}
async function rightClick(target: Element) {
	await act(async () => {
		fireEvent.contextMenu(target, { clientX: 300, clientY: 200 });
	});
}
async function pick(name: string | RegExp) {
	const items = await within(await menu()).findAllByRole("menuitem", { name });
	const item = items[0];
	if (!item) throw new Error(`no menu item ${String(name)}`);
	await act(async () => {
		fireEvent.click(item);
	});
}

test("right-clicking the empty pane offers Create here and the canvas actions", async () => {
	canvasApi();
	await mountCanvas();
	const pane = document.querySelector(".react-flow__pane");
	if (!pane) throw new Error("no pane");
	await rightClick(pane);
	const open = await menu();
	expect(open).toHaveAccessibleName("Canvas");
	for (const name of [
		"Git repository",
		"Docker image",
		"Database",
		"Arrange",
		"Fit view",
		"Search",
		"Keyboard shortcuts",
	]) {
		expect(
			within(open).getByRole("menuitem", { name: new RegExp(name) }),
		).toBeInTheDocument();
	}
});

test("Create here from the pane menu opens that flow at the click point", async () => {
	canvasApi();
	await mountCanvas();
	const pane = document.querySelector(".react-flow__pane");
	if (!pane) throw new Error("no pane");
	await rightClick(pane);
	await pick(/Docker image/);
	expect(seenStores.create?.request).toMatchObject({ kind: "image" });
	expect(seenStores.create?.request?.position).toEqual({
		x: expect.any(Number),
		y: expect.any(Number),
	});
});

test("Templates from the pane menu opens the gallery", async () => {
	canvasApi();
	await mountCanvas();
	const pane = document.querySelector(".react-flow__pane");
	if (!pane) throw new Error("no pane");
	await rightClick(pane);
	await pick(/Templates/);
	expect(seenStores.templates).toBe(true);
});

test("Search and Keyboard shortcuts from the pane menu open the search field and the dialog", async () => {
	canvasApi();
	await mountCanvas();
	const pane = document.querySelector(".react-flow__pane");
	if (!pane) throw new Error("no pane");
	await rightClick(pane);
	await pick(/^Search/);
	expect(await screen.findByRole("searchbox")).toBeInTheDocument();
	await rightClick(pane);
	await pick(/Keyboard shortcuts/);
	expect(
		await screen.findByRole("dialog", { name: "Keyboard shortcuts" }),
	).toBeInTheDocument();
});

test("Fit view from the menu refits the camera", async () => {
	canvasApi();
	await mountCanvas();
	const pane = document.querySelector(".react-flow__pane");
	if (!pane) throw new Error("no pane");
	await rightClick(pane);
	await pick(/Fit view/);
	await waitFor(() =>
		expect(
			document.querySelector(".react-flow__viewport")?.getAttribute("style"),
		).toContain("scale("),
	);
});

test("right-clicking a card selects it and opens its actions", async () => {
	canvasApi();
	await mountCanvas();
	await rightClick(node("a"));
	const open = await menu();
	expect(open).toHaveAccessibleName("Actions for Alpha");
	expect(selected()).toEqual(["a"]);
	expect(
		within(open).getByRole("menuitem", { name: "Open" }),
	).toBeInTheDocument();
	expect(
		within(open).getByRole("menuitem", { name: /Scale/ }),
	).toBeInTheDocument();
});

test("Open in a card's menu opens its inspector", async () => {
	canvasApi();
	const onInspect = mock(
		(_id?: string, _tab?: string, _section?: string) => {},
	);
	await mountCanvas({
		onInspect: onInspect as ProjectCanvasProps["onInspect"],
	});
	await rightClick(node("b"));
	await pick("Open");
	expect(onInspect).toHaveBeenCalledWith("b", undefined, undefined);
});

test("Delete… in a card's menu goes to the danger zone of its settings", async () => {
	canvasApi();
	const onInspect = mock(
		(_id?: string, _tab?: string, _section?: string) => {},
	);
	await mountCanvas({
		onInspect: onInspect as ProjectCanvasProps["onInspect"],
	});
	await rightClick(node("b"));
	await pick(/Delete/);
	expect(onInspect).toHaveBeenCalledWith("b", "settings", "danger");
});

test("Scale… in a card's menu opens the scale dialog of that service", async () => {
	stubApi({
		"GET /environments/:e/config-source": { body: makeNoConfigSource() },
		"GET /services/:s/metrics": {
			body: { points: [], retentionDays: 7, stepSeconds: 5 },
		},
		"GET /services/:s": { body: b },
	});
	await mountCanvas();
	await rightClick(node("b"));
	await pick(/Scale/);
	expect(await screen.findByRole("dialog")).toBeInTheDocument();
});

test("Deploy in a card's menu deploys that service", async () => {
	const { api } = canvasApi({
		"POST /services/:s/deployments": { status: 202, body: queued() },
	});
	await mountCanvas();
	await rightClick(node("b"));
	await pick(/^Deploy/);
	await waitFor(() =>
		expect(
			api.calls.some(
				(call) =>
					call.method === "POST" && call.path === "/services/b/deployments",
			),
		).toBe(true),
	);
});

test("a selection of cards has its own menu: group, arrange, clear", async () => {
	canvasApi();
	await mountCanvas();
	await key("a", { metaKey: true });
	await rightClick(node("a"));
	const open = await menu();
	expect(open).toHaveAccessibleName("Actions for 3 selected services");
	await pick(/Clear selection/);
	await waitFor(() => expect(selected()).toEqual([]));
});

test("Group from a selection's menu makes a named frame, and says so", async () => {
	canvasApi();
	await mountCanvas();
	await key("a", { metaKey: true });
	await rightClick(node("a"));
	await pick(/Group 3 services/);
	expect(await screen.findByText("Group created")).toBeInTheDocument();
	expect(
		screen.getByText("Type a name, or Esc to keep “Group 1”."),
	).toBeInTheDocument();
	expect(
		document.querySelector('.react-flow__node[data-id^="group:"]'),
	).not.toBeNull();
});

test("the toolbar's Group button groups the selected cards and is off with fewer than two", async () => {
	canvasApi();
	await mountCanvas();
	const group = screen.getByRole("button", { name: /^Group/ });
	expect(group).toBeDisabled();
	await key("a", { metaKey: true });
	await waitFor(() =>
		expect(screen.getByRole("button", { name: /^Group/ })).toBeEnabled(),
	);
	await userEvent.setup().click(screen.getByRole("button", { name: /^Group/ }));
	expect((await screen.findAllByText("Group created")).length).toBeGreaterThan(
		0,
	);
});

test("Deploy services in a selection's menu posts a deployment for each, with its own toast", async () => {
	const { api } = canvasApi({
		"POST /services/:s/deployments": { status: 202, body: queued() },
	});
	await mountCanvas();
	await key("a", { metaKey: true });
	await rightClick(node("a"));
	await pick(/^Deploy 3 services/);
	expect(await screen.findByText("Deploying 3 services")).toBeInTheDocument();
	expect(
		api.calls
			.filter((call) => call.method === "POST")
			.map((call) => call.path)
			.sort(),
	).toEqual([
		"/services/a/deployments",
		"/services/b/deployments",
		"/services/c/deployments",
	]);
});

test("Restart in a selection's menu restarts only the deployed services", async () => {
	const { api } = canvasApi({
		"POST /services/:s/restart": { status: 202, body: make("ActionResult") },
	});
	await mountCanvas();
	await key("a", { metaKey: true });
	await rightClick(node("a"));
	await pick(/^Restart 1 service/);
	expect(await screen.findByText("Restarting 1 service")).toBeInTheDocument();
	expect(
		api.calls.filter((call) => call.method === "POST").map((call) => call.path),
	).toEqual(["/services/a/restart"]);
});

test("Combine… is off for services that cannot be combined, and says why", async () => {
	canvasApi();
	await mountCanvas();
	await key("a", { metaKey: true });
	await rightClick(node("a"));
	const item = await within(await menu()).findByRole("menuitem", {
		name: /Combine/,
	});
	expect(item).toBeDisabled();
	expect(
		screen.getByText(/deploys an image, not a Git repository/),
	).toBeInTheDocument();
});

test("Combine… on services from one repository opens the combine dialog", async () => {
	const git = (id: string, name: string) =>
		makeService({
			id,
			name,
			sourceType: "git",
			config: {
				...a.config,
				gitUrl: "https://github.com/acme/mono",
				gitBranch: "main",
			},
		});
	canvasApi({
		"POST /environments/:e/services/combine/plan": {
			body: make("CombinePlan", { targetServiceId: "g1" }),
		},
	});
	await mountCanvas(
		{
			services: [git("g1", "Web"), git("g2", "Worker")],
			document: {
				version: 1,
				layout: layout({ g1: { x: 0, y: 0 }, g2: { x: 400, y: 0 } }),
			},
		},
		"Web",
	);
	await key("a", { metaKey: true });
	await rightClick(node("g1"));
	await pick(/Combine/);
	expect(
		await screen.findByText("Combine into one service"),
	).toBeInTheDocument();
});

test("Arrange selected services rearranges them and offers to undo", async () => {
	const { puts } = canvasApi();
	await mountCanvas({
		document: {
			version: 2,
			layout: layout({
				a: { x: 0, y: 0 },
				b: { x: 0, y: 40 },
				c: { x: 0, y: 80 },
			}),
		},
	});
	const before = [position("a"), position("b"), position("c")];
	await key("a", { metaKey: true });
	await rightClick(node("a"));
	await pick(/Arrange 3 services/);
	expect(await screen.findByText("Services arranged")).toBeInTheDocument();
	expect(
		screen.getByRole("button", { name: "Undo arrange" }),
	).toBeInTheDocument();
	// Stacked cards are spread out, and the new layout is saved after the debounce.
	await waitFor(() =>
		expect([position("a"), position("b"), position("c")]).not.toEqual(before),
	);
	await waitFor(() => expect(puts).toHaveLength(1), { timeout: 3000 });
	expect(puts[0]?.layout.nodes).not.toEqual({
		a: { x: 0, y: 0 },
		b: { x: 0, y: 40 },
		c: { x: 0, y: 80 },
	});
});

test("Arrange in the toolbar moves the cards to a layered layout and Undo puts every card back", async () => {
	canvasApi();
	await mountCanvas();
	const before = [position("a"), position("b"), position("c")];
	await userEvent
		.setup()
		.click(screen.getByRole("button", { name: /Arrange/ }));
	expect(await screen.findByText("Canvas arranged")).toBeInTheDocument();
	await userEvent
		.setup()
		.click(screen.getByRole("button", { name: "Undo arrange" }));
	await waitFor(() =>
		expect([position("a"), position("b"), position("c")]).toEqual(before),
	);
});

// --- cards, dock, inspector ---------------------------------------------------

test("clicking a card opens its inspector; Shift-click only extends the selection", async () => {
	canvasApi();
	const onInspect = mock(
		(_id?: string, _tab?: string, _section?: string) => {},
	);
	await mountCanvas({
		onInspect: onInspect as ProjectCanvasProps["onInspect"],
	});
	fireEvent.click(node("b"), { shiftKey: true });
	expect(onInspect).not.toHaveBeenCalled();
	fireEvent.click(node("b"));
	expect(onInspect).toHaveBeenCalledWith("b", undefined, undefined);
});

test("clicking the empty pane closes the inspector, and does nothing when none is open", async () => {
	withDomEventTarget();
	canvasApi();
	const onInspect = mock(
		(_id?: string, _tab?: string, _section?: string) => {},
	);
	const view = await mountCanvas({
		onInspect: onInspect as ProjectCanvasProps["onInspect"],
	});
	const pane = document.querySelector(".react-flow__pane");
	if (!pane) throw new Error("no pane");
	fireEvent.click(pane);
	expect(onInspect).not.toHaveBeenCalled();
	await view.rerenderCanvas({ inspectedServiceId: "a" });
	fireEvent.click(pane);
	expect(onInspect).toHaveBeenCalledWith(undefined);
});

test("the selected card shows a dock with Open, Deploy, Scale and More", async () => {
	canvasApi();
	const onInspect = mock(
		(_id?: string, _tab?: string, _section?: string) => {},
	);
	await mountCanvas({
		onInspect: onInspect as ProjectCanvasProps["onInspect"],
	});
	await key("ArrowRight");
	const dock = await screen.findByRole("toolbar", {
		name: "Actions for Alpha",
	});
	expect(
		within(dock).getByRole("button", { name: /^Redeploy/ }),
	).toBeInTheDocument();
	expect(
		within(dock).getByRole("button", { name: "More actions for Alpha" }),
	).toBeInTheDocument();
	await userEvent
		.setup()
		.click(within(dock).getByRole("button", { name: /^Open/ }));
	expect(onInspect).toHaveBeenCalledWith("a", undefined, undefined);
});

test("the dock's Delete… goes to the danger zone of the service", async () => {
	canvasApi();
	const onInspect = mock(
		(_id?: string, _tab?: string, _section?: string) => {},
	);
	await mountCanvas({
		onInspect: onInspect as ProjectCanvasProps["onInspect"],
	});
	await key("ArrowRight");
	const dock = await screen.findByRole("toolbar", {
		name: "Actions for Alpha",
	});
	const user = userEvent.setup();
	await user.click(
		within(dock).getByRole("button", { name: "More actions for Alpha" }),
	);
	await user.click(await screen.findByRole("menuitem", { name: /Delete/ }));
	expect(onInspect).toHaveBeenCalledWith("a", "settings", "danger");
});

test("the dock's Scale opens the scale dialog", async () => {
	stubApi({
		"GET /environments/:e/config-source": { body: makeNoConfigSource() },
		"GET /services/:s/metrics": {
			body: { points: [], retentionDays: 7, stepSeconds: 5 },
		},
		"GET /services/:s": { body: a },
	});
	await mountCanvas();
	await key("ArrowRight");
	const dock = await screen.findByRole("toolbar", {
		name: "Actions for Alpha",
	});
	await userEvent
		.setup()
		.click(within(dock).getByRole("button", { name: "Scale" }));
	expect(await screen.findByRole("dialog")).toBeInTheDocument();
});

test("opening an inspector selects its service and moves the selection with it", async () => {
	withDomEventTarget();
	canvasApi();
	const view = await mountCanvas();
	await key("a", { metaKey: true });
	expect(selected().sort()).toEqual(["a", "b", "c"]);
	await view.rerenderCanvas({ inspectedServiceId: "c" });
	expect(selected()).toEqual(["c"]);
	await view.rerenderCanvas({ inspectedServiceId: "b" });
	expect(selected()).toEqual(["b"]);
});

test("an inspected service that no longer exists closes the inspector", async () => {
	canvasApi();
	const onInspect = mock(
		(_id?: string, _tab?: string, _section?: string) => {},
	);
	await mountCanvas({
		inspectedServiceId: "gone",
		onInspect: onInspect as ProjectCanvasProps["onInspect"],
	});
	await waitFor(() => expect(onInspect).toHaveBeenCalledWith(undefined));
});

// --- the Internet node and cables ---------------------------------------------

const web = makeService({
	id: "web",
	name: "Web",
	domains: [{ hostname: "app.example.com" }] as Service["domains"],
});
const db = makeService({ id: "db", name: "Pg", kind: "database" });

function link(
	id: string,
	from: string,
	to: string,
	extra: Record<string, unknown> = {},
) {
	return {
		id,
		environmentId: "env-1",
		fromServiceId: from,
		toServiceId: to,
		references: [],
		source: "manual",
		staged: false,
		createdAt: "2026-01-01T00:00:00Z",
		...extra,
	} as ServiceLink;
}

test("a service with a public domain gets an Internet node whose menu lists the domain", async () => {
	canvasApi();
	await mountCanvas(
		{
			services: [web, db],
			document: {
				version: 1,
				layout: layout({ web: { x: 0, y: 0 }, db: { x: 400, y: 0 } }),
			},
		},
		"Web",
	);
	const internet = document.querySelector(
		'.react-flow__node[data-id="internet:public"]',
	);
	if (!internet) throw new Error("no internet node");
	await rightClick(internet);
	const open = await menu();
	expect(open).toHaveAccessibleName(
		/Actions for the Internet, 1 public domain/,
	);
	expect(
		within(open).getByRole("menuitem", { name: "app.example.com" }),
	).toBeInTheDocument();
});

test("Open on a domain in the Internet menu opens the site in a new tab", async () => {
	canvasApi();
	const open = spyOn(window, "open").mockImplementation(() => null);
	await mountCanvas(
		{
			services: [web, db],
			document: {
				version: 1,
				layout: layout({ web: { x: 0, y: 0 }, db: { x: 400, y: 0 } }),
			},
		},
		"Web",
	);
	const internet = document.querySelector(
		'.react-flow__node[data-id="internet:public"]',
	);
	if (!internet) throw new Error("no internet node");
	await rightClick(internet);
	await pick("app.example.com");
	expect(open).toHaveBeenCalledWith(
		"https://app.example.com",
		"_blank",
		"noopener,noreferrer",
	);
});

test("Add domain to a service in the Internet menu opens its Networking settings", async () => {
	canvasApi();
	const onInspect = mock(
		(_id?: string, _tab?: string, _section?: string) => {},
	);
	await mountCanvas(
		{
			services: [web, db],
			onInspect: onInspect as ProjectCanvasProps["onInspect"],
			document: {
				version: 1,
				layout: layout({ web: { x: 0, y: 0 }, db: { x: 400, y: 0 } }),
			},
		},
		"Web",
	);
	const internet = document.querySelector(
		'.react-flow__node[data-id="internet:public"]',
	);
	if (!internet) throw new Error("no internet node");
	await rightClick(internet);
	await pick("Web");
	expect(onInspect).toHaveBeenCalledWith("web", "settings", "networking");
	expect(
		await screen.findByText("Add the domain under Networking"),
	).toBeInTheDocument();
});

test("a card's menu lists the cables it ends and Review link opens its references", async () => {
	canvasApi();
	await mountCanvas(
		{
			services: [web, db],
			links: [link("l1", "web", "db")],
			document: {
				version: 1,
				layout: layout({ web: { x: 0, y: 0 }, db: { x: 400, y: 0 } }),
			},
		},
		"Web",
	);
	await rightClick(node("web"));
	await pick(/Review link to Pg/);
	expect(seenStores.references?.id).toBe("l1");
});

// --- groups ---------------------------------------------------------------------

const grouped: CanvasDocument = {
	version: 5,
	layout: layout(
		{ a: { x: 40, y: 80 }, b: { x: 380, y: 80 }, c: { x: 1200, y: 0 } },
		{
			groups: [
				{
					id: "g1",
					name: "Backend",
					x: 0,
					y: 0,
					w: 700,
					h: 260,
					collapsed: false,
					serviceIds: ["a", "b"],
				},
			],
		},
	),
};

function groupFrame() {
	const frame = document.querySelector<HTMLElement>(
		'.react-flow__node[data-id="group:g1"]',
	);
	if (!frame) throw new Error("no group frame");
	return frame;
}

test("a saved group is drawn as a frame with its name", async () => {
	canvasApi();
	await mountCanvas({ document: grouped });
	expect(within(groupFrame()).getByText("Backend")).toBeInTheDocument();
});

test("Collapse on the frame folds the group, saves it, and Expand opens it again", async () => {
	const { puts } = canvasApi();
	await mountCanvas({ document: grouped });
	const user = userEvent.setup();
	await user.click(within(groupFrame()).getByLabelText("Collapse Backend"));
	await waitFor(() =>
		expect(screen.getByLabelText("Expand Backend")).toBeInTheDocument(),
	);
	await waitFor(() => expect(puts).toHaveLength(1), { timeout: 3000 });
	expect(puts[0]?.layout.groups[0]).toMatchObject({
		id: "g1",
		collapsed: true,
	});
	await user.click(screen.getByLabelText("Expand Backend"));
	await waitFor(() =>
		expect(screen.getByLabelText("Collapse Backend")).toBeInTheDocument(),
	);
});

test("Ungroup on the frame removes the frame and keeps the services", async () => {
	const { puts } = canvasApi();
	await mountCanvas({ document: grouped });
	await userEvent
		.setup()
		.click(within(groupFrame()).getByLabelText("Ungroup Backend"));
	await waitFor(() =>
		expect(
			document.querySelector('.react-flow__node[data-id="group:g1"]'),
		).toBeNull(),
	);
	expect(screen.getByText("Alpha")).toBeInTheDocument();
	await waitFor(() => expect(puts).toHaveLength(1), { timeout: 3000 });
	expect(puts[0]?.layout.groups).toEqual([]);
	expect(Object.keys(puts[0]?.layout.nodes ?? {}).sort()).toEqual([
		"a",
		"b",
		"c",
	]);
});

test("double-clicking a frame's name renames the group and saves it", async () => {
	const { puts } = canvasApi();
	await mountCanvas({ document: grouped });
	const user = userEvent.setup();
	fireEvent.doubleClick(within(groupFrame()).getByText("Backend"));
	const field = await screen.findByLabelText("Group name");
	await user.clear(field);
	await user.type(field, "Core{Enter}");
	await waitFor(() =>
		expect(within(groupFrame()).getByText("Core")).toBeInTheDocument(),
	);
	await waitFor(() => expect(puts).toHaveLength(1), { timeout: 3000 });
	expect(puts[0]?.layout.groups[0]?.name).toBe("Core");
});

test("a group's menu: Rename opens the name field", async () => {
	canvasApi();
	await mountCanvas({ document: grouped });
	await rightClick(groupFrame());
	expect(await menu()).toHaveAccessibleName("Actions for group Backend");
	await pick("Rename");
	expect(await screen.findByLabelText("Group name")).toBeInTheDocument();
});

test("a group's menu: Collapse folds it and Ungroup removes the frame", async () => {
	const { puts } = canvasApi();
	await mountCanvas({ document: grouped });
	await rightClick(groupFrame());
	await pick("Collapse");
	await waitFor(() =>
		expect(screen.getByLabelText("Expand Backend")).toBeInTheDocument(),
	);
	await rightClick(groupFrame());
	await pick("Ungroup");
	await waitFor(() =>
		expect(
			document.querySelector('.react-flow__node[data-id="group:g1"]'),
		).toBeNull(),
	);
	await waitFor(() => expect(puts.length).toBeGreaterThan(0), {
		timeout: 3000,
	});
	expect(puts.at(-1)?.layout.groups).toEqual([]);
});

test("a template stack's group menu offers Delete stack… and asks the templates flow to delete it", async () => {
	canvasApi({
		"GET /environments/:e/template-instances": {
			body: [
				make("TemplateInstance", {
					id: "inst-1",
					groupId: "g1",
					name: "Docmost",
					environmentId: "env-1",
					services: [],
					templateId: "docmost",
				}),
			],
		},
	});
	await mountCanvas({ document: grouped });
	await rightClick(groupFrame());
	await pick("Delete stack…");
	expect(seenStores.deleting).toBe("inst-1");
});

test("a group that is not a template stack has no Delete stack…", async () => {
	canvasApi({
		"GET /environments/:e/template-instances": { body: [] },
	});
	await mountCanvas({ document: grouped });
	await rightClick(groupFrame());
	const open = await menu();
	expect(
		within(open).queryByRole("menuitem", { name: "Delete stack…" }),
	).toBeNull();
});

// --- the minimap, zoom column, search dimming ------------------------------------

test("the minimap toggles from the zoom column", async () => {
	canvasApi();
	await mountCanvas();
	expect(document.querySelector(".react-flow__minimap")).toBeNull();
	const user = userEvent.setup();
	await user.click(screen.getByRole("button", { name: "Show minimap" }));
	expect(document.querySelector(".react-flow__minimap")).not.toBeNull();
	await user.click(screen.getByRole("button", { name: "Hide minimap" }));
	expect(document.querySelector(".react-flow__minimap")).toBeNull();
});

test("the zoom column's Keyboard shortcuts button opens the dialog", async () => {
	canvasApi();
	await mountCanvas();
	await userEvent
		.setup()
		.click(screen.getByRole("button", { name: "Keyboard shortcuts" }));
	expect(
		await screen.findByRole("dialog", { name: "Keyboard shortcuts" }),
	).toBeInTheDocument();
});

test("typing in the search dims the cards that do not match and keeps the matches clear", async () => {
	canvasApi();
	await mountCanvas();
	await key("f", { metaKey: true });
	await userEvent.setup().type(await screen.findByRole("searchbox"), "Beta");
	expect(await screen.findByText("1 of 3")).toBeInTheDocument();
	const opacityOf = (id: string) =>
		(node(id).firstElementChild as HTMLElement | null)?.style.opacity;
	await waitFor(() => expect(opacityOf("a")).toBe("0.25"));
	expect(opacityOf("c")).toBe("0.25");
	expect(opacityOf("b")).toBe("1");
});

// --- keyboard menu ----------------------------------------------------------------

test("the ContextMenu key with focus on the page opens the menu of the canvas", async () => {
	canvasApi();
	await mountCanvas();
	await key("ContextMenu");
	expect(await menu()).toHaveAccessibleName("Canvas");
});

test("Shift+F10 opens the selected card's menu", async () => {
	canvasApi();
	await mountCanvas();
	await key("ArrowRight");
	await key("F10", { shiftKey: true });
	expect(await menu()).toHaveAccessibleName("Actions for Alpha");
});

test("Enter on the focused Internet node opens its menu", async () => {
	canvasApi();
	await mountCanvas(
		{
			services: [web, db],
			document: {
				version: 1,
				layout: layout({ web: { x: 0, y: 0 }, db: { x: 400, y: 0 } }),
			},
		},
		"Web",
	);
	const internet = document.querySelector<HTMLElement>(
		'.react-flow__node[data-id="internet:public"]',
	);
	if (!internet) throw new Error("no internet node");
	await act(async () => {
		fireEvent.keyDown(internet, { key: "Enter", bubbles: true });
	});
	expect(await menu()).toHaveAccessibleName(/Actions for the Internet/);
});

// --- placing and landing from outside ---------------------------------------------

test("a card placed from outside moves to the rounded point and the layout is saved", async () => {
	const { puts } = canvasApi();
	await mountCanvas();
	await act(async () => {
		await placeNode("b", { x: 123.4, y: 55.6 });
	});
	expect(position("b")).toBe("translate(123px,56px)");
	await waitFor(() => expect(puts).toHaveLength(1), { timeout: 3000 });
	expect(puts[0]?.layout.nodes.b).toEqual({ x: 123, y: 56 });
	expect(puts[0]?.ifMatch).toBe('"2"');
});

test("a place chosen for a service that is not listed yet waits for it", async () => {
	const { puts } = canvasApi();
	const view = await mountCanvas({
		services: [a, b],
		document: {
			version: 2,
			layout: layout({ a: { x: 0, y: 0 }, b: { x: 400, y: 0 } }),
		},
	});
	await act(async () => {
		await placeNode("c", { x: 640, y: 320 });
	});
	expect(document.querySelector('.react-flow__node[data-id="c"]')).toBeNull();
	await view.rerenderCanvas({ services: [a, b, c] });
	await screen.findByText("Gamma");
	expect(position("c")).toBe("translate(640px,320px)");
	await waitFor(() => expect(puts).toHaveLength(1), { timeout: 3000 });
	expect(puts[0]?.layout.nodes.c).toEqual({ x: 640, y: 320 });
});

test("a template stack that landed takes the server's layout once every new card is listed", async () => {
	const { puts } = canvasApi();
	const view = await mountCanvas({
		services: [a],
		document: { version: 2, layout: layout({ a: { x: 0, y: 0 } }) },
	});
	const landed: CanvasDocument = {
		version: 9,
		layout: layout(
			{ a: { x: 0, y: 0 }, b: { x: 900, y: 100 }, c: { x: 900, y: 400 } },
			{
				groups: [
					{
						id: "stack",
						name: "Docmost",
						x: 860,
						y: 40,
						w: 340,
						h: 500,
						collapsed: false,
						serviceIds: ["b", "c"],
					},
				],
			},
		),
	};
	let taken = false;
	act(() => {
		taken = landOnCanvas({
			document: landed,
			groupId: "stack",
			serviceIds: ["b", "c"],
		});
	});
	expect(taken).toBe(true);
	// Not every new service is listed: the layout is not taken yet.
	await view.rerenderCanvas({ services: [a, b] });
	expect(
		document.querySelector('.react-flow__node[data-id="group:stack"]'),
	).toBeNull();
	await view.rerenderCanvas({ services: [a, b, c] });
	await waitFor(() =>
		expect(
			document.querySelector('.react-flow__node[data-id="group:stack"]'),
		).not.toBeNull(),
	);
	expect(position("b")).toBe("translate(900px,100px)");
	// The server wrote it: the canvas writes nothing over it, and its next save uses version 9.
	await act(async () => {
		await placeNode("a", { x: 5, y: 5 });
	});
	await waitFor(() => expect(puts).toHaveLength(1), { timeout: 3000 });
	expect(puts[0]?.ifMatch).toBe('"9"');
	expect(puts[0]?.layout.groups.map((group) => group.id)).toEqual(["stack"]);
});

test("without a canvas on screen a landing is refused", async () => {
	expect(
		landOnCanvas({
			document: { version: 1, layout: layout({}) },
			groupId: "g",
			serviceIds: [],
		}),
	).toBe(false);
});

test("Escape with the search open and focus elsewhere clears the query and closes it", async () => {
	canvasApi();
	await mountCanvas();
	await key("f", { metaKey: true });
	await userEvent.setup().type(await screen.findByRole("searchbox"), "Beta");
	await screen.findByText("1 of 3");
	(document.activeElement as HTMLElement | null)?.blur();
	await key("Escape");
	await waitFor(() => expect(screen.queryByRole("searchbox")).toBeNull());
	// Reopened, the field starts empty and nothing is dimmed.
	await key("f", { metaKey: true });
	expect(await screen.findByRole("searchbox")).toHaveValue("");
	expect((node("a").firstElementChild as HTMLElement).style.opacity).toBe("1");
});

test("arrow keys treat a collapsed group as one stop and skip the cards inside it", async () => {
	canvasApi();
	const doc: CanvasDocument = {
		version: 5,
		layout: layout(
			{ a: { x: 40, y: 80 }, b: { x: 380, y: 80 }, c: { x: 1200, y: 0 } },
			{
				groups: [
					{
						id: "g1",
						name: "Backend",
						x: 0,
						y: 0,
						w: 700,
						h: 260,
						collapsed: true,
						serviceIds: ["a", "b"],
					},
				],
			},
		),
	};
	await mountCanvas({ document: doc }, "Gamma");
	await key("ArrowRight");
	expect(selected()).toEqual(["group:g1"]);
	await key("ArrowRight");
	expect(selected()).toEqual(["c"]);
	await key("ArrowLeft");
	expect(selected()).toEqual(["group:g1"]);
});

test("an arrow key on an empty canvas selects nothing", async () => {
	canvasApi();
	await renderWithProviders(
		ui(
			props({ services: [], document: { version: 0, layout: emptyLayout() } }),
		),
	);
	await screen.findByText("Create your first service");
	await key("ArrowRight");
	expect(selected()).toEqual([]);
});

test("shortcuts typed with a modifier held are left to the browser", async () => {
	canvasApi();
	await mountCanvas();
	await key("ArrowRight", { altKey: true });
	await key("?", { ctrlKey: true });
	expect(selected()).toEqual([]);
	expect(screen.queryByText("Keyboard shortcuts")).toBeNull();
});

test("Enter on a dock button is left to the dock: the canvas does not also open the inspector", async () => {
	canvasApi();
	const onInspect = mock(
		(_id?: string, _tab?: string, _section?: string) => {},
	);
	await mountCanvas({
		onInspect: onInspect as ProjectCanvasProps["onInspect"],
	});
	await key("ArrowRight");
	const dock = await screen.findByRole("toolbar", {
		name: "Actions for Alpha",
	});
	const button = within(dock).getByRole("button", { name: /^Open/ });
	fireEvent.keyDown(button, { key: "Enter" });
	// The dock is chrome: the canvas leaves its keys alone.
	expect(onInspect).not.toHaveBeenCalled();
});
