import { describe, expect, it } from "bun:test";
import { useLocation } from "@tanstack/react-router";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
	MaintenanceWindowView,
	StatusIncidentView,
	StatusPageComponentView,
	StatusPageView,
	Team,
} from "#/api/gen/types.gen";
import { teamsQuery } from "#/features/teams/queries";
import { type ApiReply, type ApiRoutes, stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders, testQueryClient } from "#/test/render";
import { PageEditor } from "./page-editor";
import {
	dnsCheck,
	team,
	uptimeReply,
	uptimeService,
} from "./sp-pages-fixtures";

/** One project "Acme" with one environment "env-1": what the page's service states are read from. */
const acmeProjects = make("PageProject", {
	items: [make("Project", { id: "proj-1", name: "Acme" })],
});
const env1 = make("PageEnvironment", {
	items: [make("Environment", { id: "env-1", projectId: "proj-1" })],
});

const WEB: StatusPageComponentView = {
	serviceId: "web",
	serviceName: "web",
	displayName: "Website",
	groupName: "",
	url: "",
	projectName: "Acme",
	environmentName: "production",
};
const API: StatusPageComponentView = {
	serviceId: "api",
	serviceName: "api",
	displayName: "API",
	groupName: "",
	url: "",
	projectName: "Acme",
	environmentName: "production",
};

function makePage(over: Partial<StatusPageView> = {}): StatusPageView {
	return {
		id: "page-1",
		title: "Acme status",
		slug: "acme-status",
		description: "",
		footer: "",
		theme: "auto",
		showUptime: true,
		hostname: "status.acme.test",
		published: false,
		components: [WEB, API],
		createdAt: "2026-10-01T00:00:00Z",
		updatedAt: "2026-10-01T00:00:00Z",
		...over,
	};
}

function incident(over: Partial<StatusIncidentView> = {}): StatusIncidentView {
	return {
		id: "inc-1",
		title: "API is slow",
		impact: "major",
		status: "investigating",
		pageIds: ["page-1"],
		serviceIds: [],
		createdAt: "2026-10-30T10:00:00Z",
		updatedAt: "2026-10-30T10:05:00Z",
		resolvedAt: null,
		updates: [],
		...over,
	};
}

function maintenance(
	over: Partial<MaintenanceWindowView> = {},
): MaintenanceWindowView {
	return {
		id: "win-1",
		title: "Database upgrade",
		description: "",
		phase: "upcoming",
		startsAt: "2026-11-02T01:00:00Z",
		endsAt: "2026-11-02T02:30:00Z",
		pageIds: ["page-1"],
		serviceIds: [],
		canceledAt: null,
		createdAt: "2026-10-30T10:00:00Z",
		updatedAt: "2026-10-30T10:00:00Z",
		...over,
	};
}

function problem(status: number, code: string, detail: string): ApiReply {
	return { status, body: { code, title: code, detail, status } };
}

interface Setup {
	page?: StatusPageView;
	role?: "owner" | "admin" | "developer" | "viewer";
	pageIncidents?: StatusIncidentView[];
	activeIncidents?: StatusIncidentView[];
	pageWindows?: MaintenanceWindowView[];
	activeWindows?: MaintenanceWindowView[];
	routes?: ApiRoutes;
}

/** The API a status page editor talks to; `state.page` is what GET returns next. */
function setup(options: Setup = {}) {
	seededRole = options.role ?? "owner";
	const state = { page: options.page ?? makePage() };
	const api = stubApi({
		"GET /status-pages": () => Response.json({ items: [state.page] }),
		"GET /status-pages/:id": () => Response.json(state.page),
		"PATCH /status-pages/:id": () => Response.json(state.page),
		"PUT /status-pages/:id/components": () => Response.json(state.page),
		"DELETE /status-pages/:id": { status: 204 },
		"GET /status-incidents": (request) => {
			const query = new URL(request.url).searchParams;
			const items = query.has("pageId")
				? (options.pageIncidents ?? [])
				: (options.activeIncidents ?? []);
			return Response.json({ items });
		},
		"GET /maintenance-windows": (request) => {
			const query = new URL(request.url).searchParams;
			const items = query.has("pageId")
				? (options.pageWindows ?? [])
				: (options.activeWindows ?? []);
			return Response.json({ items });
		},
		"GET /projects": { body: make("PageProject", { items: [] }) },
		"GET /status-pages/:id/cloudflare": problem(404, "not_found", "no zone"),
		"GET /status-pages/:id/dns": { body: dnsCheck() },
		...options.routes,
	});
	return { api, state };
}

/** The role `setup` chose: `open` seeds it, so the editor starts with the right permissions. */
let seededRole: Team["role"] = "owner";

function Location() {
	const pathname = useLocation({ select: (l) => l.pathname });
	return <output aria-label="location">{pathname}</output>;
}

/** Renders the editor at its own URL with the team already loaded, and waits for the page. */
async function open() {
	const queryClient = testQueryClient();
	queryClient.setQueryData(teamsQuery().queryKey, {
		items: [team(seededRole)],
	});
	const view = await renderWithProviders(
		<>
			<PageEditor pageId="page-1" />
			<Location />
		</>,
		{ queryClient, path: "/status-pages/page-1" },
	);
	await screen.findByRole("heading", { level: 1 });
	return view;
}

const calls = (api: ReturnType<typeof stubApi>, method: string) =>
	api.calls.filter((c) => c.method === method && !c.path.endsWith("/dns"));

describe("PageEditor: loading and failing", () => {
	it("shows a loading state while the page is read", async () => {
		stubApi({
			"GET /status-pages/:id": () => new Promise<Response>(() => {}),
		});
		await renderWithProviders(<PageEditor pageId="page-1" />);
		expect(
			screen.getByRole("status", { name: "Loading status page" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("heading", { name: "Status page" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: /All status pages/ }),
		).toHaveAttribute("href", "/status-pages");
	});

	it("says the page does not exist on a 404, with no retry", async () => {
		stubApi({
			"GET /status-pages/:id": problem(404, "not_found", "gone"),
		});
		await renderWithProviders(<PageEditor pageId="page-1" />);
		const alert = await screen.findByRole("alert");
		expect(alert).toHaveTextContent("This status page does not exist");
		expect(alert).toHaveTextContent(
			"It may have been deleted. Nothing was changed.",
		);
		expect(
			screen.queryByRole("button", { name: "Try again" }),
		).not.toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: /All status pages/ }),
		).toHaveAttribute("href", "/status-pages");
	});

	it("offers to try again when the control plane fails, and loads the page then", async () => {
		const user = userEvent.setup();
		let failing = true;
		const { api } = setup({
			routes: {
				"GET /status-pages/:id": () =>
					failing
						? Response.json(
								{
									code: "internal",
									title: "internal",
									detail: "boom",
									status: 500,
								},
								{ status: 500 },
							)
						: Response.json(makePage()),
			},
		});
		await open();
		const alert = await screen.findByRole("alert");
		expect(alert).toHaveTextContent("The page did not load");
		expect(alert).toHaveTextContent(
			"The control plane did not answer. Nothing was changed; try again.",
		);

		failing = false;
		await user.click(screen.getByRole("button", { name: "Try again" }));
		expect(
			await screen.findByRole("heading", { level: 1, name: "Acme status" }),
		).toBeInTheDocument();
		expect(api.unmatched).toEqual([]);
	});
});

describe("PageEditor: the page", () => {
	it("shows the title, hostname, draft state and component count", async () => {
		setup();
		await open();
		const heading = screen.getByRole("heading", { level: 1 });
		expect(heading).toHaveTextContent("Acme status");
		const summary = heading.parentElement as HTMLElement;
		expect(within(summary).getByText("status.acme.test")).toBeInTheDocument();
		expect(within(summary).getByText("Draft")).toBeInTheDocument();
		expect(within(summary).getByText("2 components")).toBeInTheDocument();
		expect(within(summary).getByText("No data yet")).toBeInTheDocument();
		expect(document.title).toStartWith("Acme status · Status pages");
	});

	it("says there is no hostname yet, and a singular component", async () => {
		setup({ page: makePage({ hostname: null, components: [WEB] }) });
		await open();
		const summary = screen.getByRole("heading", { level: 1 })
			.parentElement as HTMLElement;
		expect(within(summary).getByText("No hostname yet")).toBeInTheDocument();
		expect(within(summary).getByText("1 component")).toBeInTheDocument();
	});

	it("fills the form from the saved page", async () => {
		setup({
			page: makePage({
				description: "Live status",
				footer: "ops@acme.test",
				theme: "dark",
			}),
		});
		await open();
		expect(screen.getByLabelText("Title")).toHaveValue("Acme status");
		expect(screen.getByLabelText("Short name")).toHaveValue("acme-status");
		expect(screen.getByLabelText("Description")).toHaveValue("Live status");
		expect(screen.getByLabelText("Footer")).toHaveValue("ops@acme.test");
		expect(screen.getByLabelText("Public hostname")).toHaveValue(
			"status.acme.test",
		);
		expect(
			within(
				screen.getByRole("group", { name: "Theme of the public page" }),
			).getByRole("button", { name: "Dark" }),
		).toHaveAttribute("aria-pressed", "true");
		expect(
			screen
				.getAllByRole("textbox", { name: "Public name" })
				.map((i) => (i as HTMLInputElement).value),
		).toEqual(["Website", "API"]);
	});

	it("derives the overall state from the services' checks", async () => {
		setup({
			routes: {
				"GET /projects": { body: acmeProjects },
				"GET /projects/:id/environments": { body: env1 },
				"GET /environments/:id/uptime": uptimeReply(
					uptimeService("web", "up", true),
					uptimeService("api", "down", true),
				),
			},
		});
		await open();
		expect(await screen.findByText("Partial outage")).toBeInTheDocument();
	});

	it("raises the state for an open incident posted to the page", async () => {
		setup({
			activeIncidents: [incident({ impact: "critical" })],
			routes: {
				"GET /projects": { body: acmeProjects },
				"GET /projects/:id/environments": { body: env1 },
				"GET /environments/:id/uptime": uptimeReply(
					uptimeService("web", "up", true),
				),
			},
		});
		await open();
		expect(await screen.findByText("Major outage")).toBeInTheDocument();
	});

	it("shows an active maintenance window as the state", async () => {
		setup({
			activeWindows: [maintenance({ phase: "active" })],
			routes: {
				"GET /projects": { body: acmeProjects },
				"GET /projects/:id/environments": { body: env1 },
				"GET /environments/:id/uptime": uptimeReply(
					uptimeService("web", "up", true),
				),
			},
		});
		await open();
		expect(await screen.findByText("Under maintenance")).toBeInTheDocument();
	});

	it("shows the DNS check for the saved hostname", async () => {
		const { api } = setup();
		await open();
		expect(await screen.findByText(/Resolves to now/)).toBeInTheDocument();
		expect(api.calls.map((c) => c.path)).toContain("/status-pages/page-1/dns");
		expect(api.unmatched).toEqual([]);
	});
});

describe("PageEditor: preview", () => {
	it("explains why a draft cannot be previewed", async () => {
		const user = userEvent.setup();
		setup({ page: makePage({ published: false }) });
		await open();
		expect(
			screen.queryByText(/A draft is not served yet/),
		).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Preview" }));
		expect(
			screen.getByText(
				"A draft is not served yet. Publish the page and Preview opens it on status.acme.test.",
			),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Preview" }));
		expect(
			screen.queryByText(/A draft is not served yet/),
		).not.toBeInTheDocument();
	});

	it("opens a published page on its hostname", async () => {
		setup({ page: makePage({ published: true }) });
		await open();
		expect(
			screen.queryByRole("button", { name: "Preview" }),
		).not.toBeInTheDocument();
		const links = screen.getAllByRole("button", { name: /Open page/ });
		expect(links.length).toBeGreaterThan(0);
		for (const link of links) {
			expect(link).toHaveAttribute("href", "https://status.acme.test/");
		}
	});
});

describe("PageEditor: permissions", () => {
	it.each(["developer", "viewer"] as const)(
		"is read-only for a %s",
		async (role) => {
			setup({ role, page: makePage({ published: true }) });
			await open();
			await screen.findByText(
				/Only team owners and admins change status pages/,
			);
			expect(
				screen.queryByRole("button", { name: "Save changes" }),
			).not.toBeInTheDocument();
			expect(
				screen.queryByRole("button", { name: "Delete page" }),
			).not.toBeInTheDocument();
			expect(
				screen.queryByRole("button", { name: "Post incident" }),
			).not.toBeInTheDocument();
			expect(
				screen.queryByRole("button", { name: "Schedule maintenance" }),
			).not.toBeInTheDocument();
			expect(screen.getByLabelText("Title")).toBeDisabled();
			expect(screen.getByRole("switch", { name: "Published" })).toHaveAttribute(
				"aria-disabled",
				"true",
			);
		},
	);

	it.each(["owner", "admin"] as const)("lets an %s edit", async (role) => {
		setup({ role });
		await open();
		await screen.findByRole("button", { name: "Save changes" });
		expect(
			screen.queryByText(/Only team owners and admins change status pages/),
		).not.toBeInTheDocument();
		expect(screen.getByLabelText("Title")).toBeEnabled();
		expect(
			screen.getByRole("button", { name: "Delete page" }),
		).toBeInTheDocument();
	});
});

describe("PageEditor: saving", () => {
	it("keeps Save and Discard off until something changes", async () => {
		setup();
		await open();
		expect(
			await screen.findByRole("button", { name: "Save changes" }),
		).toBeDisabled();
		expect(screen.getByRole("button", { name: "Discard" })).toBeDisabled();
	});

	it("sends only what changed and confirms with a toast", async () => {
		const user = userEvent.setup();
		const { api, state } = setup();
		await open();
		const title = screen.getByLabelText("Title");
		await user.clear(title);
		await user.type(title, "Acme systems");
		expect(screen.getByText("Unsaved changes")).toBeInTheDocument();

		state.page = makePage({ title: "Acme systems" });
		await user.click(screen.getByRole("button", { name: "Save changes" }));

		expect(await screen.findByText("Status page saved")).toBeInTheDocument();
		expect(calls(api, "PATCH")).toHaveLength(1);
		expect(calls(api, "PATCH")[0]).toMatchObject({
			path: "/status-pages/page-1",
			body: { title: "Acme systems" },
		});
		expect(calls(api, "PUT")).toEqual([]);
		await waitFor(() =>
			expect(
				screen.getByRole("button", { name: "Save changes" }),
			).toBeDisabled(),
		);
		expect(screen.getByLabelText("Title")).toHaveValue("Acme systems");
		expect(api.unmatched).toEqual([]);
	});

	it("normalises the hostname and sends the other form fields as patch", async () => {
		const user = userEvent.setup();
		const { api } = setup();
		await open();
		const hostname = screen.getByLabelText("Public hostname");
		await user.clear(hostname);
		await user.type(hostname, "  Status.New.TEST.  ");
		await user.click(screen.getByRole("switch", { name: "Show uptime" }));
		await user.click(
			within(
				screen.getByRole("group", { name: "Theme of the public page" }),
			).getByRole("button", { name: "Light" }),
		);
		await user.click(screen.getByRole("button", { name: "Save changes" }));
		await screen.findByText("Status page saved");
		expect(calls(api, "PATCH")[0]?.body).toEqual({
			hostname: "status.new.test",
			showUptime: false,
			theme: "light",
		});
	});

	it("blocks saving an invalid form and says so", async () => {
		const user = userEvent.setup();
		const { api } = setup();
		await open();
		await user.clear(screen.getByLabelText("Title"));
		expect(screen.getByText("Enter a title.")).toBeInTheDocument();
		expect(
			screen.getByText("Fix the highlighted fields to save."),
		).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled();
		expect(calls(api, "PATCH")).toEqual([]);
	});

	it("blocks saving a component with an invalid link", async () => {
		const user = userEvent.setup();
		setup();
		await open();
		await user.type(
			screen.getAllByLabelText("Link (optional)")[0] as HTMLElement,
			"http://nope",
		);
		expect(
			screen.getByText(
				"Use an absolute https link, like https://example.com/docs.",
			),
		).toBeInTheDocument();
		expect(
			screen.getByText("Fix the highlighted fields to save."),
		).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled();
	});

	it("discards edits to the form and the components", async () => {
		const user = userEvent.setup();
		const { api } = setup();
		await open();
		await user.type(screen.getByLabelText("Title"), " 2");
		await user.click(
			screen.getByRole("button", { name: "Remove API from the page" }),
		);
		expect(
			screen.getAllByRole("textbox", { name: "Public name" }),
		).toHaveLength(1);

		await user.click(screen.getByRole("button", { name: "Discard" }));
		expect(screen.getByLabelText("Title")).toHaveValue("Acme status");
		expect(
			screen.getAllByRole("textbox", { name: "Public name" }),
		).toHaveLength(2);
		expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled();
		expect(calls(api, "PATCH")).toEqual([]);
		expect(calls(api, "PUT")).toEqual([]);
	});

	it("saves reordered components as the full ordered list", async () => {
		const user = userEvent.setup();
		const { api } = setup({
			page: makePage({
				components: [
					{ ...WEB, groupName: "Core" },
					{ ...API, groupName: "Core" },
				],
			}),
		});
		await open();
		await user.click(screen.getByRole("button", { name: "Move Website down" }));
		await user.click(screen.getByRole("button", { name: "Save changes" }));
		await screen.findByText("Status page saved");
		const puts = calls(api, "PUT");
		expect(puts).toHaveLength(1);
		expect(puts[0]?.path).toBe("/status-pages/page-1/components");
		expect(puts[0]?.body).toEqual({
			components: [
				{ serviceId: "api", displayName: "API", groupName: "Core" },
				{ serviceId: "web", displayName: "Website", groupName: "Core" },
			],
		});
		expect(calls(api, "PATCH")).toEqual([]);
		expect(api.unmatched).toEqual([]);
	});

	it("saves a component's new group, listing the ungrouped ones first", async () => {
		const user = userEvent.setup();
		const { api } = setup();
		await open();
		await user.type(
			screen.getAllByLabelText("Group")[0] as HTMLElement,
			"Core{Enter}",
		);
		await user.click(screen.getByRole("button", { name: "Save changes" }));
		await screen.findByText("Status page saved");
		expect(calls(api, "PUT")[0]?.body).toEqual({
			components: [
				{ serviceId: "api", displayName: "API" },
				{ serviceId: "web", displayName: "Website", groupName: "Core" },
			],
		});
		expect(calls(api, "PATCH")).toEqual([]);
	});

	it("saves the components before the form when both changed", async () => {
		const user = userEvent.setup();
		const { api } = setup();
		await open();
		await user.type(screen.getByLabelText("Footer"), "Call us");
		await user.click(
			screen.getByRole("button", { name: "Remove API from the page" }),
		);
		await user.click(screen.getByRole("button", { name: "Save changes" }));
		await screen.findByText("Status page saved");
		const writes = api.calls
			.filter((c) => c.method === "PUT" || c.method === "PATCH")
			.map((c) => `${c.method} ${c.path}`);
		expect(writes).toEqual([
			"PUT /status-pages/page-1/components",
			"PATCH /status-pages/page-1",
		]);
		expect(calls(api, "PUT")[0]?.body).toEqual({
			components: [{ serviceId: "web", displayName: "Website" }],
		});
		expect(calls(api, "PATCH")[0]?.body).toEqual({ footer: "Call us" });
	});

	it("puts the control plane's refusal at the slug field until it is edited", async () => {
		const user = userEvent.setup();
		setup({
			routes: {
				"PATCH /status-pages/:id": problem(409, "slug_conflict", "taken"),
			},
		});
		await open();
		const slug = screen.getByLabelText("Short name");
		await user.clear(slug);
		await user.type(slug, "other");
		await user.click(screen.getByRole("button", { name: "Save changes" }));
		expect(
			await screen.findByText(
				"Another status page already uses this short name.",
			),
		).toBeInTheDocument();
		expect(slug).toBeInvalid();
		expect(screen.queryByText("Status page saved")).not.toBeInTheDocument();

		await user.type(slug, "x");
		expect(
			screen.queryByText("Another status page already uses this short name."),
		).not.toBeInTheDocument();
	});

	it("puts a hostname refusal at the hostname field", async () => {
		const user = userEvent.setup();
		setup({
			routes: {
				"PATCH /status-pages/:id": problem(409, "hostname_conflict", "taken"),
			},
		});
		await open();
		const hostname = screen.getByLabelText("Public hostname");
		await user.clear(hostname);
		await user.type(hostname, "taken.example.com");
		await user.click(screen.getByRole("button", { name: "Save changes" }));
		expect(
			await screen.findByText(
				"Another status page or a service domain already serves this hostname.",
			),
		).toBeInTheDocument();
		expect(hostname).toBeInvalid();
	});

	it("shows an unrecognised refusal in the save bar and keeps the edits", async () => {
		const user = userEvent.setup();
		setup({
			routes: {
				"PATCH /status-pages/:id": problem(
					500,
					"internal",
					"The database is busy.",
				),
			},
		});
		await open();
		await user.type(screen.getByLabelText("Title"), "!");
		await user.click(screen.getByRole("button", { name: "Save changes" }));
		expect(
			await screen.findByText("The database is busy."),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Title")).toHaveValue("Acme status!");

		// Editing again clears the error.
		await user.type(screen.getByLabelText("Title"), "?");
		expect(screen.queryByText("The database is busy.")).not.toBeInTheDocument();
		expect(screen.getByText("Unsaved changes")).toBeInTheDocument();
	});

	it("shows a publish-code refusal in the save bar for a field with no home", async () => {
		const user = userEvent.setup();
		setup({
			page: makePage({ published: true }),
			routes: {
				"PATCH /status-pages/:id": problem(409, "publish_needs_hostname", "x"),
			},
		});
		await open();
		await user.type(screen.getByLabelText("Footer"), "x");
		await user.click(screen.getByRole("button", { name: "Save changes" }));
		expect(
			await screen.findByText(
				"A published page needs a hostname. Unpublish it first, or keep the hostname.",
			),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Public hostname")).toBeInvalid();
	});

	it("shows a refused component save under the components and does not send the form", async () => {
		const user = userEvent.setup();
		const { api } = setup({
			page: makePage({ published: true, components: [WEB] }),
			routes: {
				"PUT /status-pages/:id/components": problem(
					409,
					"publish_needs_components",
					"x",
				),
			},
		});
		await open();
		await user.type(screen.getByLabelText("Footer"), "x");
		await user.click(
			screen.getByRole("button", { name: "Remove Website from the page" }),
		);
		await user.click(screen.getByRole("button", { name: "Save changes" }));
		expect(
			await screen.findByText(
				"A published page needs at least one component. Unpublish it first, or keep one.",
			),
		).toBeInTheDocument();
		expect(calls(api, "PATCH")).toEqual([]);
		// Both edits are still waiting to be saved.
		expect(screen.getByLabelText("Footer")).toHaveValue("x");
		expect(screen.getByRole("button", { name: "Save changes" })).toBeEnabled();

		// Changing the components clears the message.
		await user.click(screen.getByRole("button", { name: "Add services" }));
		await screen.findByRole("dialog");
	});

	it("shows the server's own message when a component save fails with an unknown code", async () => {
		const user = userEvent.setup();
		setup({
			routes: {
				"PUT /status-pages/:id/components": problem(
					500,
					"internal",
					"Could not store the list.",
				),
			},
		});
		await open();
		await user.click(
			screen.getByRole("button", { name: "Remove API from the page" }),
		);
		await user.click(screen.getByRole("button", { name: "Save changes" }));
		expect(
			await screen.findByText("Could not store the list."),
		).toBeInTheDocument();
		await user.click(
			screen.getByRole("button", { name: "Remove Website from the page" }),
		);
		expect(
			screen.queryByText("Could not store the list."),
		).not.toBeInTheDocument();
	});
});

describe("PageEditor: publishing", () => {
	it("publishes the saved page with its own request and says so", async () => {
		const user = userEvent.setup();
		const { api, state } = setup();
		await open();
		state.page = makePage({ published: true });
		await user.click(screen.getByRole("switch", { name: "Published" }));
		expect(await screen.findByText("Page published")).toBeInTheDocument();
		expect(
			screen.getByText("It is served on its hostname within moments."),
		).toBeInTheDocument();
		expect(calls(api, "PATCH")).toHaveLength(1);
		expect(calls(api, "PATCH")[0]).toMatchObject({
			path: "/status-pages/page-1",
			body: { published: true },
		});
		await waitFor(() =>
			expect(screen.getByText("Live on its hostname")).toBeInTheDocument(),
		);
	});

	it("unpublishes a live page", async () => {
		const user = userEvent.setup();
		const { api, state } = setup({ page: makePage({ published: true }) });
		await open();
		state.page = makePage({ published: false });
		await user.click(screen.getByRole("switch", { name: "Published" }));
		expect(await screen.findByText("Page unpublished")).toBeInTheDocument();
		expect(calls(api, "PATCH")[0]?.body).toEqual({ published: false });
		await waitFor(() =>
			expect(
				screen.getByText("Draft, not visible to anyone"),
			).toBeInTheDocument(),
		);
	});

	it("shows the refusal under the switch and keeps the page a draft", async () => {
		const user = userEvent.setup();
		setup({
			page: makePage({ components: [] }),
			routes: {
				"PATCH /status-pages/:id": problem(
					409,
					"publish_needs_components",
					"x",
				),
			},
		});
		await open();
		await user.click(screen.getByRole("switch", { name: "Published" }));
		expect(
			await screen.findByText(
				"Add at least one component before publishing the page.",
			),
		).toBeInTheDocument();
		expect(
			screen.getByText("Draft, not visible to anyone"),
		).toBeInTheDocument();
	});

	it("shows the server's message for a refusal it has no sentence for", async () => {
		const user = userEvent.setup();
		setup({
			routes: {
				"PATCH /status-pages/:id": problem(500, "internal", "Try later."),
			},
		});
		await open();
		await user.click(screen.getByRole("switch", { name: "Published" }));
		expect(await screen.findByText("Try later.")).toBeInTheDocument();
	});

	it("will not publish while there are unsaved edits", async () => {
		const user = userEvent.setup();
		const { api } = setup();
		await open();
		await user.type(screen.getByLabelText("Title"), "!");
		expect(screen.getByRole("switch", { name: "Published" })).toHaveAttribute(
			"aria-disabled",
			"true",
		);
		expect(
			screen.getByText(
				"Save or discard your changes first: publishing uses what is saved.",
			),
		).toBeInTheDocument();
		expect(calls(api, "PATCH")).toEqual([]);
	});
});

describe("PageEditor: deleting", () => {
	it("asks first, deletes, and says what stays", async () => {
		const user = userEvent.setup();
		const { api } = setup();
		await open();
		await user.click(screen.getByRole("button", { name: "Delete page" }));
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("Delete “Acme status”?"),
		).toBeInTheDocument();
		expect(dialog).toHaveTextContent(
			"Its components and settings are removed. This cannot be undone.",
		);
		expect(dialog).not.toHaveTextContent("stops serving it");
		expect(calls(api, "DELETE")).toEqual([]);

		await user.click(
			within(dialog).getByRole("button", { name: "Delete page" }),
		);
		expect(await screen.findByText("Status page deleted")).toBeInTheDocument();
		expect(
			screen.getByText(
				"Its hostname stops serving it. Incidents and maintenance stay.",
			),
		).toBeInTheDocument();
		expect(calls(api, "DELETE")).toHaveLength(1);
		expect(calls(api, "DELETE")[0]?.path).toBe("/status-pages/page-1");
		await waitFor(() => {
			if (screen.getByLabelText("location").textContent !== "/status-pages")
				throw new Error("has not navigated to the list yet");
		});
	});

	it("warns that a published page stops serving on its hostname", async () => {
		const user = userEvent.setup();
		setup({ page: makePage({ published: true }) });
		await open();
		await user.click(screen.getByRole("button", { name: "Delete page" }));
		const dialog = await screen.findByRole("alertdialog");
		expect(dialog).toHaveTextContent(
			"status.acme.test stops serving it within moments.",
		);
	});

	it("keeps the page when the confirmation is cancelled", async () => {
		const user = userEvent.setup();
		const { api } = setup();
		await open();
		await user.click(screen.getByRole("button", { name: "Delete page" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
		);
		expect(calls(api, "DELETE")).toEqual([]);
	});

	it("keeps the dialog open with the reason when the delete fails", async () => {
		const user = userEvent.setup();
		setup({
			routes: {
				"DELETE /status-pages/:id": problem(500, "internal", "Delete refused."),
			},
		});
		await open();
		await user.click(screen.getByRole("button", { name: "Delete page" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Delete page" }),
		);
		expect(
			await within(dialog).findByText("Delete refused."),
		).toBeInTheDocument();
		expect(screen.queryByText("Status page deleted")).not.toBeInTheDocument();
	});
});

describe("PageEditor: incidents and maintenance of the page", () => {
	it("explains an empty incident list and an empty maintenance list", async () => {
		setup();
		await open();
		expect(
			await screen.findByText(/No incidents posted to this page/),
		).toBeInTheDocument();
		expect(
			await screen.findByText(/No maintenance is scheduled for this page/),
		).toBeInTheDocument();
	});

	it("asks for incidents and windows of this page only", async () => {
		const { api } = setup();
		await open();
		await screen.findByText(/No incidents posted to this page/);
		const asked = api.calls.filter(
			(c) =>
				c.path === "/status-incidents" || c.path === "/maintenance-windows",
		);
		expect(
			asked.some(
				(c) =>
					c.path === "/status-incidents" && c.query.get("pageId") === "page-1",
			),
		).toBe(true);
		expect(
			asked.some(
				(c) =>
					c.path === "/maintenance-windows" &&
					c.query.get("pageId") === "page-1",
			),
		).toBe(true);
	});

	it("lists the incidents worst first, as links to their timelines", async () => {
		setup({
			pageIncidents: [
				incident({
					id: "inc-old",
					title: "Old hiccup",
					impact: "minor",
					status: "resolved",
					resolvedAt: "2026-10-01T00:00:00Z",
				}),
				incident({ id: "inc-minor", title: "Slow logins", impact: "minor" }),
				incident({
					id: "inc-crit",
					title: "Everything down",
					impact: "critical",
				}),
			],
		});
		await open();
		const list = await screen.findByRole("list", {
			name: "Incidents on this page",
		});
		const links = within(list).getAllByRole("link");
		expect(
			links.map((l) => l.querySelector("span.font-medium")?.textContent),
		).toEqual(["Everything down", "Slow logins", "Old hiccup"]);
		expect(links[0]).toHaveAttribute(
			"href",
			"/status-pages/incidents/inc-crit",
		);
		expect(within(list).getAllByText("Posted to Acme status")).toHaveLength(3);
	});

	it("separates current maintenance from past windows", async () => {
		setup({
			pageWindows: [
				maintenance({ id: "w-up", title: "Next week", phase: "upcoming" }),
				maintenance({ id: "w-now", title: "Right now", phase: "active" }),
				maintenance({ id: "w-old", title: "Last month", phase: "ended" }),
			],
		});
		await open();
		const current = await screen.findByRole("list", {
			name: "Active and upcoming maintenance",
		});
		expect(
			within(current)
				.getAllByRole("listitem")
				.map((li) => li.querySelector("span.truncate")?.textContent),
		).toEqual(["Right now", "Next week"]);
		expect(screen.getByText("1 past window")).toBeInTheDocument();
		const past = screen.getByRole("list", {
			name: "Past maintenance",
			hidden: true,
		});
		expect(within(past).getByText("Last month")).toBeInTheDocument();
	});

	it("says nothing is coming up when only past windows exist", async () => {
		setup({
			pageWindows: [
				maintenance({ phase: "ended" }),
				maintenance({ id: "w2", phase: "canceled" }),
			],
		});
		await open();
		expect(
			await screen.findByText("Nothing running or coming up."),
		).toBeInTheDocument();
		expect(screen.getByText("2 past windows")).toBeInTheDocument();
	});

	it("lets a manager post an incident to this page from the editor", async () => {
		const user = userEvent.setup();
		setup();
		await open();
		await user.click(
			await screen.findByRole("button", { name: "Post incident" }),
		);
		const dialog = await screen.findByRole("dialog", {
			name: "Post an incident",
		});
		expect(
			within(dialog).getByRole("checkbox", { name: /Acme status/ }),
		).toBeChecked();
	});

	it("lets a manager schedule maintenance for this page from the editor", async () => {
		const user = userEvent.setup();
		setup();
		await open();
		await user.click(
			await screen.findByRole("button", { name: "Schedule maintenance" }),
		);
		const dialog = await screen.findByRole("dialog", {
			name: "Schedule maintenance",
		});
		expect(
			within(dialog).getByRole("checkbox", { name: /Acme status/ }),
		).toBeChecked();
	});

	it("opens a window in the edit dialog from its row", async () => {
		const user = userEvent.setup();
		setup({ pageWindows: [maintenance({ title: "Database upgrade" })] });
		await open();
		await screen.findByText("Database upgrade");
		await user.click(screen.getByRole("button", { name: "Edit" }));
		const dialog = await screen.findByRole("dialog", {
			name: "Edit maintenance",
		});
		expect(within(dialog).getByLabelText("Title")).toHaveValue(
			"Database upgrade",
		);
	});

	it("shows windows to a viewer without any action", async () => {
		setup({
			role: "viewer",
			pageWindows: [maintenance({ title: "Database upgrade" })],
		});
		await open();
		await screen.findByText("Database upgrade");
		expect(
			screen.queryByRole("button", { name: "Edit" }),
		).not.toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Delete" }),
		).not.toBeInTheDocument();
	});
});
