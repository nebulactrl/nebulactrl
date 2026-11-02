import { describe, expect, it } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { EnvironmentService, Team } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import {
	incidentView,
	pageView,
	TEAM_SERVICES_ROUTES,
	teamRoute,
	uptimeReply,
	uptimeService,
	windowView,
} from "./sp-pages-fixtures";
import { StatusPagesPage } from "./status-pages-page";

const NO_PROJECTS = { "GET /projects": { body: { items: [] } } };

interface Setup {
	role?: Team["role"];
	pages?: ReturnType<typeof pageView>[];
	open?: ReturnType<typeof incidentView>[];
	resolved?: ReturnType<typeof incidentView>[];
	windows?: ReturnType<typeof windowView>[];
	uptime?: {
		serviceId: string;
		state: EnvironmentService["state"];
		monitored: boolean;
	}[];
	team?: Record<string, unknown>;
	extra?: Record<string, unknown>;
}

function routes({
	role = "admin",
	pages = [pageView()],
	open = [],
	resolved = [],
	windows = [],
	uptime,
	team = NO_PROJECTS,
	extra = {},
}: Setup = {}) {
	const base = uptime
		? {
				...TEAM_SERVICES_ROUTES,
				"GET /environments/e-web/uptime": uptimeReply(
					...uptime.map((u) =>
						uptimeService(u.serviceId, u.state, u.monitored),
					),
				),
			}
		: team;
	return {
		"GET /status-pages": { body: { items: pages } },
		"GET /status-incidents": (request: Request) => {
			const status = new URL(request.url).searchParams.get("status");
			return Response.json({ items: status === "resolved" ? resolved : open });
		},
		"GET /maintenance-windows": { body: { items: windows } },
		...teamRoute(role),
		...base,
		...extra,
	} as Parameters<typeof stubApi>[0];
}

const render = () =>
	renderWithProviders(<StatusPagesPage />, { path: "/status-pages" });

describe("StatusPagesPage page list", () => {
	it("shows skeletons while the pages load", async () => {
		stubApi(routes());
		await render();
		expect(
			screen.getByRole("status", { name: "Loading status pages" }),
		).toBeVisible();
		await screen.findByRole("article", { name: "Acme status" });
		expect(
			screen.queryByRole("status", { name: "Loading status pages" }),
		).toBeNull();
	});

	it("shows a card per page with hostname, publish state and counts", async () => {
		stubApi(
			routes({
				pages: [
					pageView({
						id: "page-1",
						title: "Acme status",
						components: [
							{
								serviceId: "svc-api",
								displayName: "API",
								groupName: "",
								url: "",
								serviceName: "api",
								projectName: "Web",
								environmentName: "Production",
							},
						],
					}),
					pageView({
						id: "page-2",
						title: "Beta status",
						hostname: null,
						published: false,
					}),
				],
			}),
		);
		await render();
		const acme = await screen.findByRole("article", { name: "Acme status" });
		expect(within(acme).getByText("status.acme.test")).toBeVisible();
		expect(within(acme).getByText("Published")).toBeVisible();
		expect(within(acme).getByText("1 component")).toBeVisible();
		expect(within(acme).getByText("No open incidents")).toBeVisible();
		expect(
			within(acme).getByRole("link", { name: "Acme status" }),
		).toHaveAttribute("href", "/status-pages/page-1");
		expect(
			within(acme).getByRole("button", { name: /Open page/ }),
		).toHaveAttribute("href", "https://status.acme.test/");
		const beta = screen.getByRole("article", { name: "Beta status" });
		expect(within(beta).getByText("No hostname yet")).toBeVisible();
		expect(within(beta).getByText("Draft")).toBeVisible();
		expect(within(beta).getByText("0 components")).toBeVisible();
		expect(screen.getByText("2 pages")).toBeVisible();
	});

	it("explains why a draft cannot be previewed, and hides it again", async () => {
		const user = userEvent.setup();
		stubApi(
			routes({ pages: [pageView({ title: "Draft page", published: false })] }),
		);
		await render();
		const card = await screen.findByRole("article", { name: "Draft page" });
		expect(within(card).queryByRole("status")).toBeNull();
		await user.click(within(card).getByRole("button", { name: "Preview" }));
		expect(
			within(card).getByText(
				/A draft is not served yet\. Publish the page and Preview opens it on status\.acme\.test\./,
			),
		).toBeVisible();
		await user.click(within(card).getByRole("button", { name: "Preview" }));
		expect(within(card).queryByText(/A draft is not served yet/)).toBeNull();
	});

	it("derives each page's overall state from its services, open incidents and maintenance", async () => {
		const component = (serviceId: string) => ({
			serviceId,
			displayName: serviceId,
			groupName: "",
			url: "",
			serviceName: serviceId,
			projectName: "Web",
			environmentName: "Production",
		});
		stubApi(
			routes({
				pages: [
					pageView({
						id: "page-1",
						title: "Healthy",
						components: [component("svc-api")],
					}),
					pageView({
						id: "page-2",
						title: "Hit by incident",
						components: [component("svc-web")],
					}),
					pageView({
						id: "page-3",
						title: "Nothing monitored",
						components: [component("svc-other")],
					}),
				],
				uptime: [
					{ serviceId: "svc-api", state: "up", monitored: true },
					{ serviceId: "svc-web", state: "up", monitored: true },
				],
				open: [
					incidentView({
						id: "inc-1",
						impact: "critical",
						pageIds: ["page-2"],
					}),
				],
			}),
		);
		await render();
		const healthy = await screen.findByRole("article", { name: "Healthy" });
		await waitFor(() =>
			expect(within(healthy).getByText("Operational")).toBeVisible(),
		);
		expect(
			within(
				screen.getByRole("article", { name: "Hit by incident" }),
			).getByText("Major outage"),
		).toBeVisible();
		expect(
			within(
				screen.getByRole("article", { name: "Hit by incident" }),
			).getByText("1 open incident"),
		).toBeVisible();
		expect(
			within(
				screen.getByRole("article", { name: "Nothing monitored" }),
			).getByText("No data yet"),
		).toBeVisible();
		expect(within(healthy).getByText("No open incidents")).toBeVisible();
	});

	it("shows an active maintenance window as the page state and counts upcoming ones", async () => {
		stubApi(
			routes({
				pages: [pageView({ id: "page-1", title: "Acme status" })],
				windows: [
					windowView({
						id: "w-active",
						title: "Now",
						phase: "active",
						pageIds: ["page-1"],
					}),
					windowView({
						id: "w-up1",
						title: "Later",
						phase: "upcoming",
						pageIds: ["page-1"],
					}),
					windowView({
						id: "w-up2",
						title: "Elsewhere",
						phase: "upcoming",
						pageIds: ["page-9"],
					}),
				],
			}),
		);
		await render();
		const card = await screen.findByRole("article", { name: "Acme status" });
		await waitFor(() =>
			expect(within(card).getByText("Under maintenance")).toBeVisible(),
		);
		expect(within(card).getByText("1 maintenance window ahead")).toBeVisible();
	});

	it("explains the empty state and offers the first page to a manager", async () => {
		const user = userEvent.setup();
		stubApi(routes({ pages: [], team: TEAM_SERVICES_ROUTES }));
		await render();
		expect(await screen.findByText("No status pages yet")).toBeVisible();
		expect(
			await screen.findByText("Pick services, choose a hostname, publish."),
		).toBeVisible();
		await user.click(screen.getByRole("button", { name: "New status page" }));
		expect(
			await screen.findByText(
				"A public page that shows how your services are doing, on your own domain. Only the page is public; your dashboard stays private.",
			),
		).toBeVisible();
	});

	it("tells a team without services to deploy one first", async () => {
		stubApi(routes({ pages: [], team: NO_PROJECTS }));
		await render();
		expect(
			await screen.findByText(/this team has none yet\. Deploy one first/),
		).toBeVisible();
	});

	it("shows no create buttons to a viewer", async () => {
		stubApi(routes({ role: "viewer", pages: [], team: NO_PROJECTS }));
		await render();
		await screen.findByText("No status pages yet");
		await waitFor(() =>
			expect(
				screen.queryByRole("button", { name: "New status page" }),
			).toBeNull(),
		);
		expect(screen.queryByRole("button", { name: "Post incident" })).toBeNull();
		expect(
			screen.queryByRole("button", { name: "Schedule maintenance" }),
		).toBeNull();
	});

	it("shows an error with a retry when the pages do not load", async () => {
		const user = userEvent.setup();
		let fail = true;
		const api = stubApi({
			...routes(),
			"GET /status-pages": () =>
				fail
					? Response.json(
							{ status: 500, code: "boom", detail: "down" },
							{ status: 500 },
						)
					: Response.json({ items: [pageView()] }),
		});
		await render();
		expect(await screen.findByText("Status pages did not load")).toBeVisible();
		fail = false;
		await user.click(screen.getByRole("button", { name: "Try again" }));
		expect(
			await screen.findByRole("article", { name: "Acme status" }),
		).toBeVisible();
		expect(api.unmatched).toEqual([]);
	});

	it("sets the document title", async () => {
		stubApi(routes());
		await render();
		await screen.findByRole("article", { name: "Acme status" });
		expect(document.title).toContain("Status pages");
	});
});

describe("StatusPagesPage header actions", () => {
	it("offers a manager the three actions once a page exists", async () => {
		stubApi(routes());
		await render();
		await screen.findByRole("article", { name: "Acme status" });
		const actions = screen.getAllByRole("button");
		const names = actions.map((b) => b.textContent?.trim());
		expect(names).toContain("Schedule maintenance");
		expect(names).toContain("Post incident");
		expect(names).toContain("New status page");
	});

	it("hides the header actions from a developer", async () => {
		stubApi(routes({ role: "developer" }));
		await render();
		await screen.findByRole("article", { name: "Acme status" });
		await waitFor(() =>
			expect(
				screen.queryByRole("button", { name: "Post incident" }),
			).toBeNull(),
		);
		expect(
			screen.queryByRole("button", { name: "New status page" }),
		).toBeNull();
	});

	it("opens the incident dialog with the only page already ticked", async () => {
		const user = userEvent.setup();
		stubApi(routes({ team: TEAM_SERVICES_ROUTES }));
		await render();
		await screen.findByRole("article", { name: "Acme status" });
		const header = (
			await screen.findAllByRole("button", { name: "Post incident" })
		)[0] as HTMLElement;
		await user.click(header);
		expect(await screen.findByText("Post an incident")).toBeVisible();
		expect(
			await screen.findByRole("checkbox", { name: /Acme status/ }),
		).toBeChecked();
	});

	it("leaves the pages unticked when there are several", async () => {
		const user = userEvent.setup();
		stubApi(
			routes({
				pages: [
					pageView({ id: "page-1", title: "Acme status" }),
					pageView({ id: "page-2", title: "Beta status" }),
				],
				team: TEAM_SERVICES_ROUTES,
			}),
		);
		await render();
		await screen.findByRole("article", { name: "Beta status" });
		await user.click(
			(
				await screen.findAllByRole("button", { name: "Schedule maintenance" })
			)[0] as HTMLElement,
		);
		expect(
			await screen.findByText("Schedule maintenance", { selector: "h2" }),
		).toBeVisible();
		expect(
			await screen.findByRole("checkbox", { name: /Acme status/ }),
		).not.toBeChecked();
		expect(
			screen.getByRole("checkbox", { name: /Beta status/ }),
		).not.toBeChecked();
	});
});

describe("StatusPagesPage incidents", () => {
	it("lists open incidents worst first with their pages, and counts them", async () => {
		stubApi(
			routes({
				open: [
					incidentView({
						id: "inc-minor",
						title: "Slow dashboard",
						impact: "minor",
						pageIds: ["page-1"],
					}),
					incidentView({
						id: "inc-crit",
						title: "Total outage",
						impact: "critical",
						pageIds: ["page-1"],
						serviceIds: ["svc-a", "svc-b"],
					}),
					incidentView({
						id: "inc-old",
						title: "Already fixed",
						status: "resolved",
						impact: "critical",
					}),
				],
			}),
		);
		await render();
		const list = await screen.findByRole("list", { name: "Open incidents" });
		const rows = within(list).getAllByRole("listitem");
		expect(rows).toHaveLength(2);
		expect(rows[0]).toHaveTextContent("Total outage");
		expect(rows[0]).toHaveTextContent(
			"Posted to Acme status · 2 affected services",
		);
		expect(rows[1]).toHaveTextContent("Slow dashboard");
		expect(
			within(list).getByRole("link", { name: /Total outage/ }),
		).toHaveAttribute("href", "/status-pages/incidents/inc-crit");
		expect(screen.getByRole("button", { name: "Open (2)" })).toBeVisible();
		expect(screen.queryByText("Already fixed")).toBeNull();
	});

	it("says there are no open incidents and offers to post one", async () => {
		stubApi(routes());
		await render();
		expect(await screen.findByText("No open incidents.")).toBeVisible();
		const section = screen.getByRole("region", { name: "Incidents" });
		expect(
			within(section).getByRole("button", { name: "Post incident" }),
		).toBeVisible();
	});

	it("asks a manager to create a page first when there is none", async () => {
		stubApi(routes({ pages: [], team: NO_PROJECTS }));
		await render();
		expect(
			await screen.findByText(
				/No open incidents\. Create a status page first; incidents are posted to a page\./,
			),
		).toBeVisible();
		const section = screen.getByRole("region", { name: "Incidents" });
		expect(
			within(section).queryByRole("button", { name: "Post incident" }),
		).toBeNull();
	});

	it("keeps the viewer's empty note free of hints and buttons", async () => {
		stubApi(routes({ role: "viewer" }));
		await render();
		const note = await screen.findByText("No open incidents.");
		await waitFor(() =>
			expect(note.parentElement?.querySelector("button")).toBeNull(),
		);
		expect(note).not.toHaveTextContent("Create a status page first");
	});

	it("loads resolved incidents only when asked for them", async () => {
		const user = userEvent.setup();
		const api = stubApi(
			routes({
				resolved: [
					incidentView({
						id: "inc-r",
						title: "Old problem",
						status: "resolved",
					}),
				],
			}),
		);
		await render();
		await screen.findByText("No open incidents.");
		const statusOf = () =>
			api.calls
				.filter((c) => c.path === "/status-incidents")
				.map((c) => c.query.get("status"));
		expect(statusOf()).toEqual(["active"]);

		await user.click(screen.getByRole("button", { name: "Resolved" }));
		const list = await screen.findByRole("list", {
			name: "Resolved incidents",
		});
		expect(within(list).getByText("Old problem")).toBeVisible();
		expect(statusOf()).toContain("resolved");
		const resolvedCall = api.calls.find(
			(c) => c.query.get("status") === "resolved",
		);
		expect(resolvedCall?.query.get("limit")).toBe("20");
	});

	it("says nothing is resolved yet without a post button", async () => {
		const user = userEvent.setup();
		stubApi(routes());
		await render();
		await user.click(await screen.findByRole("button", { name: "Resolved" }));
		expect(await screen.findByText("No resolved incidents yet.")).toBeVisible();
		const section = screen.getByRole("region", { name: "Incidents" });
		expect(
			within(section).queryByRole("button", { name: "Post incident" }),
		).toBeNull();
	});
});

describe("StatusPagesPage maintenance", () => {
	const windows = [
		windowView({ id: "w-up", title: "Database upgrade", phase: "upcoming" }),
		windowView({ id: "w-act", title: "Rolling restart", phase: "active" }),
		windowView({
			id: "w-past",
			title: "Old move",
			phase: "ended",
			startsAt: "2026-01-01T01:00:00Z",
			endsAt: "2026-01-01T02:00:00Z",
		}),
	];

	it("lists active then upcoming windows, counting both", async () => {
		stubApi(routes({ windows }));
		await render();
		const list = await screen.findByRole("list", {
			name: "Upcoming and active maintenance",
		});
		const rows = within(list).getAllByRole("listitem");
		expect(rows).toHaveLength(2);
		expect(rows[0]).toHaveTextContent("Rolling restart");
		expect(rows[0]).toHaveTextContent("In progress");
		expect(rows[1]).toHaveTextContent("Database upgrade");
		expect(rows[1]).toHaveTextContent("Upcoming");
		expect(rows[1]).toHaveTextContent("Posted to Acme status");
		expect(
			screen.getByRole("button", { name: "Upcoming and active (2)" }),
		).toBeVisible();
	});

	it("switches to past windows, which can only be deleted", async () => {
		const user = userEvent.setup();
		stubApi(routes({ windows }));
		await render();
		await user.click(await screen.findByRole("button", { name: "Past" }));
		const list = await screen.findByRole("list", { name: "Past maintenance" });
		const rows = within(list).getAllByRole("listitem");
		expect(rows).toHaveLength(1);
		expect(rows[0]).toHaveTextContent("Old move");
		expect(
			within(rows[0] as HTMLElement).getByRole("button", { name: "Delete" }),
		).toBeVisible();
		expect(
			within(rows[0] as HTMLElement).queryByRole("button", { name: "Edit" }),
		).toBeNull();
	});

	it("says nothing is scheduled and offers to schedule", async () => {
		stubApi(routes());
		await render();
		expect(await screen.findByText("Nothing scheduled.")).toBeVisible();
		const section = screen.getByRole("region", { name: "Maintenance" });
		expect(
			within(section).getByRole("button", { name: "Schedule maintenance" }),
		).toBeVisible();
	});

	it("says there is no past maintenance", async () => {
		const user = userEvent.setup();
		stubApi(routes());
		await render();
		await user.click(await screen.findByRole("button", { name: "Past" }));
		expect(await screen.findByText("No past maintenance.")).toBeVisible();
	});

	it("hides edit, cancel and delete from a viewer", async () => {
		stubApi(routes({ role: "viewer", windows }));
		await render();
		const list = await screen.findByRole("list", {
			name: "Upcoming and active maintenance",
		});
		await waitFor(() =>
			expect(within(list).getAllByRole("listitem")).toHaveLength(2),
		);
		expect(within(list).queryByRole("button")).toBeNull();
	});

	it("edits a window in the dialog from its row", async () => {
		const user = userEvent.setup();
		stubApi(routes({ windows, team: TEAM_SERVICES_ROUTES }));
		await render();
		const list = await screen.findByRole("list", {
			name: "Upcoming and active maintenance",
		});
		const row = within(list)
			.getByText("Database upgrade")
			.closest("li") as HTMLElement;
		await waitFor(() =>
			expect(within(row).getByRole("button", { name: "Edit" })).toBeVisible(),
		);
		await user.click(within(row).getByRole("button", { name: "Edit" }));
		expect(await screen.findByText("Edit maintenance")).toBeVisible();
		expect(screen.getByLabelText("Title")).toHaveValue("Database upgrade");
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByText("Edit maintenance")).toBeNull(),
		);
	});

	it("cancels an upcoming window after confirming and deletes a window after confirming", async () => {
		const user = userEvent.setup();
		const api = stubApi(
			routes({
				windows,
				extra: {
					"POST /maintenance-windows/:id/cancel": { body: windows[0] },
					"DELETE /maintenance-windows/:id": { status: 204 },
				},
			}),
		);
		await render();
		const list = await screen.findByRole("list", {
			name: "Upcoming and active maintenance",
		});
		const upcoming = within(list)
			.getByText("Database upgrade")
			.closest("li") as HTMLElement;
		await waitFor(() =>
			expect(
				within(upcoming).getByRole("button", { name: "Cancel" }),
			).toBeVisible(),
		);
		await user.click(within(upcoming).getByRole("button", { name: "Cancel" }));
		let dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Cancel maintenance" }),
		);
		expect(await screen.findByText("Maintenance canceled")).toBeVisible();
		expect(api.calls.find((c) => c.method === "POST")?.path).toBe(
			"/maintenance-windows/w-up/cancel",
		);

		const running = within(list)
			.getByText("Rolling restart")
			.closest("li") as HTMLElement;
		expect(
			within(running).getByRole("button", { name: "End now" }),
		).toBeVisible();
		await user.click(within(running).getByRole("button", { name: "Delete" }));
		dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Delete window" }),
		);
		expect(await screen.findByText("Maintenance deleted")).toBeVisible();
		expect(api.calls.find((c) => c.method === "DELETE")?.path).toBe(
			"/maintenance-windows/w-act",
		);
	});
});
