import { expect, mock, setDefaultTimeout, test } from "bun:test";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { type ApiRoutes, stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { CanvasScreen, type CanvasScreenProps } from "./canvas-screen";
import { makeNoConfigSource, makeService } from "./lib/fixtures";

setDefaultTimeout(30_000);

const env = () =>
	make("Environment", {
		id: "env-1",
		projectId: "p1",
		name: "Staging",
		slug: "staging",
		isProduction: false,
	});

function routes(extra: ApiRoutes = {}): ApiRoutes {
	return {
		"GET /projects/:p/environments": {
			body: make("PageEnvironment", { items: [env()] }),
		},
		"GET /environments/:e/services": {
			body: make("PageServiceView", {
				items: [
					make("ServiceView", {
						id: "a",
						name: "Alpha",
						environmentId: "env-1",
					}),
					make("ServiceView", {
						id: "b",
						name: "Beta",
						environmentId: "env-1",
					}),
				],
			}),
		},
		"GET /environments/:e/links": { body: make("LinkListOutputBody") },
		"GET /servers": { body: make("PageServer", { items: [] }) },
		// Each running service's card samples its memory for the bar.
		"GET /services/:s/metrics": {
			body: make("MetricsOutputBody", { points: [] }),
		},
		"GET /projects/:p/environments/:e/canvas": {
			body: make("Canvas", {
				version: 2,
				layout: {
					nodes: { a: { x: 0, y: 0 }, b: { x: 400, y: 0 } },
					groups: [],
					viewport: { x: 0, y: 0, zoom: 1 },
				},
			}),
		},
		"PUT /projects/:p/environments/:e/canvas": {
			body: make("Canvas", {
				version: 3,
				layout: { nodes: {}, groups: [], viewport: { x: 0, y: 0, zoom: 1 } },
			}),
		},
		"GET /environments/:e/config-source": { body: makeNoConfigSource() },
		...extra,
	};
}

function props(overrides: Partial<CanvasScreenProps> = {}): CanvasScreenProps {
	return {
		projectId: "p1",
		envSlug: "staging",
		serviceId: undefined,
		tab: "overview",
		section: undefined,
		onInspect: () => {},
		onTabChange: () => {},
		onSectionChange: () => {},
		...overrides,
	};
}

const mount = (p: Partial<CanvasScreenProps> = {}) =>
	renderWithProviders(
		<div style={{ width: 1200, height: 800 }}>
			<CanvasScreen {...props(p)} />
		</div>,
	);

test("with everything loaded the environment's services are drawn as cards", async () => {
	const api = stubApi(routes());
	await mount();
	expect(await screen.findByText("Alpha")).toBeInTheDocument();
	expect(screen.getByText("Beta")).toBeInTheDocument();
	expect(api.unmatched).toEqual([]);
	// The canvas asks for the services and the layout of the environment the slug names.
	const paths = api.calls.map((c) => c.path);
	expect(paths).toContain("/environments/env-1/services");
	expect(paths).toContain("/projects/p1/environments/env-1/canvas");
});

test("the tab title names the environment and the page", async () => {
	stubApi(routes());
	await mount();
	await screen.findByText("Alpha");
	expect(document.title).toContain("Staging");
	expect(document.title).toContain("Architecture");
});

test("while the environments load, no error and no canvas are shown", async () => {
	stubApi(
		routes({
			"GET /projects/:p/environments": () => new Promise<Response>(() => {}),
		}),
	);
	await mount();
	expect(screen.queryByText("Project not found")).toBeNull();
	expect(screen.queryByText("Alpha")).toBeNull();
});

test("a project that does not exist says so and offers the way back", async () => {
	stubApi(
		routes({
			"GET /projects/:p/environments": problem(404, { title: "Not Found" }),
		}),
	);
	await mount();
	expect(await screen.findByText("Project not found")).toBeInTheDocument();
	expect(
		screen.getByText("It may have been deleted, or you do not have access."),
	).toBeInTheDocument();
	expect(screen.getByRole("link", { name: "Go to projects" })).toHaveAttribute(
		"href",
		"/",
	);
});

test("another failure to load the project shows its reason and retries on request", async () => {
	let calls = 0;
	const api = stubApi(
		routes({
			"GET /projects/:p/environments": () => {
				calls += 1;
				return calls === 1
					? Response.json(
							{ status: 500, title: "x", detail: "database down" },
							{ status: 500 },
						)
					: Response.json({ items: [env()] });
			},
		}),
	);
	await mount();
	expect(
		await screen.findByText("Couldn't load this project"),
	).toBeInTheDocument();
	expect(screen.getByText("database down")).toBeInTheDocument();
	await userEvent
		.setup()
		.click(screen.getByRole("button", { name: /retry|try again/i }));
	expect(await screen.findByText("Alpha")).toBeInTheDocument();
	expect(
		api.calls.filter((c) => c.path === "/projects/p1/environments"),
	).toHaveLength(2);
});

test("a slug that names no environment of the project says which one is missing", async () => {
	stubApi(routes());
	await mount({ envSlug: "qa" });
	expect(await screen.findByText("Environment not found")).toBeInTheDocument();
	expect(
		screen.getByText("This project has no environment called “qa”."),
	).toBeInTheDocument();
	expect(
		screen.getByRole("link", { name: "Go to projects" }),
	).toBeInTheDocument();
});

test("services that cannot be listed show the reason beside the empty canvas, with a retry", async () => {
	let calls = 0;
	stubApi(
		routes({
			"GET /environments/:e/services": () => {
				calls += 1;
				return calls === 1
					? Response.json(
							{ status: 500, title: "x", detail: "agent bus down" },
							{ status: 500 },
						)
					: Response.json({ items: [makeService({ id: "a", name: "Alpha" })] });
			},
		}),
	);
	await mount();
	expect(await screen.findByText(/agent bus down/)).toBeInTheDocument();
	expect(screen.getByText("Create your first service")).toBeInTheDocument();
	await userEvent
		.setup()
		.click(screen.getByRole("button", { name: /retry|try again/i }));
	expect(await screen.findByText("Alpha")).toBeInTheDocument();
});

test("a 404 on the services list is explained as a missing endpoint", async () => {
	stubApi(
		routes({
			"GET /environments/:e/services": problem(404, { title: "Not Found" }),
		}),
	);
	await mount();
	expect(
		await screen.findByText(
			/no services endpoint for this environment \(404\)/,
		),
	).toBeInTheDocument();
});

test("the canvas waits for the saved layout before drawing", async () => {
	let release: (() => void) | undefined;
	stubApi(
		routes({
			"GET /projects/:p/environments/:e/canvas": () =>
				new Promise<Response>((resolve) => {
					release = () =>
						resolve(
							Response.json({
								version: 1,
								layout: {
									nodes: {},
									groups: [],
									viewport: { x: 0, y: 0, zoom: 1 },
								},
							}),
						);
				}),
		}),
	);
	await mount();
	await waitFor(() => expect(release).toBeDefined());
	expect(screen.queryByText("Alpha")).toBeNull();
	release?.();
	expect(await screen.findByText("Alpha")).toBeInTheDocument();
});

test("a layout that cannot be read starts blank: the services are still drawn", async () => {
	stubApi(
		routes({
			"GET /projects/:p/environments/:e/canvas": problem(500, { title: "x" }),
		}),
	);
	await mount();
	expect(await screen.findByText("Alpha")).toBeInTheDocument();
	expect(screen.getByText("Beta")).toBeInTheDocument();
});

test("an environment with no services shows the invitation to create the first one", async () => {
	stubApi(
		routes({
			"GET /environments/:e/services": {
				body: make("PageServiceView", { items: [] }),
			},
		}),
	);
	await mount();
	expect(
		await screen.findByText("Create your first service"),
	).toBeInTheDocument();
});

test("clicking a card opens its inspector", async () => {
	stubApi(routes());
	const onInspect = mock(
		(_id: string | undefined, _tab?: string, _section?: string) => {},
	);
	await mount({ onInspect: onInspect as CanvasScreenProps["onInspect"] });
	const card = (await screen.findByText("Alpha")).closest(".react-flow__node");
	if (!card) throw new Error("Alpha is not on a node");
	fireEvent.click(card);
	await waitFor(() =>
		expect(onInspect).toHaveBeenCalledWith("a", undefined, undefined),
	);
});
