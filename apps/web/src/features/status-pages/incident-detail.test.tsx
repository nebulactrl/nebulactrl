import { describe, expect, it } from "bun:test";
import { useRouterState } from "@tanstack/react-router";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Team } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { IncidentDetail } from "./incident-detail";
import {
	incidentView,
	pageView,
	TEAM_SERVICES_ROUTES,
	teamRoute,
} from "./sp-pages-fixtures";

const PATH = "/status-pages/incidents/inc-1";
const INCIDENT = "GET /status-incidents/:id";

const UPDATES = [
	{
		id: "u1",
		status: "investigating" as const,
		body: "We are looking into it.",
		createdAt: "2026-11-01T09:00:00Z",
	},
	{
		id: "u2",
		status: "identified" as const,
		body: "Found the cause.\nRolling out a fix.",
		createdAt: "2026-11-01T09:45:00Z",
	},
];

function routes(
	incident = incidentView({ status: "identified", updates: UPDATES }),
	role: Team["role"] = "admin",
	extra: Record<string, unknown> = {},
) {
	return {
		[INCIDENT]: { body: incident },
		"GET /status-pages": {
			body: {
				items: [
					pageView({ id: "page-1", title: "Acme status" }),
					pageView({
						id: "page-2",
						title: "Beta status",
						hostname: "beta.acme.test",
						published: false,
					}),
				],
			},
		},
		...TEAM_SERVICES_ROUTES,
		...teamRoute(role),
		...extra,
	} as Parameters<typeof stubApi>[0];
}

function Detail() {
	const pathname = useRouterState({ select: (s) => s.location.pathname });
	return (
		<>
			<output aria-label="location">{pathname}</output>
			<IncidentDetail incidentId="inc-1" />
		</>
	);
}

const render = () => renderWithProviders(<Detail />, { path: PATH });

const posts = (api: ReturnType<typeof stubApi>) =>
	api.calls.filter((c) => c.method === "POST");

async function fill(
	user: ReturnType<typeof userEvent.setup>,
	field: HTMLElement,
	text: string,
) {
	await user.click(field);
	await user.paste(text);
}

describe("IncidentDetail loading and failure", () => {
	it("shows a skeleton while the incident loads", async () => {
		stubApi(routes());
		await render();
		expect(
			screen.getByRole("status", { name: "Loading incident" }),
		).toBeVisible();
		await screen.findByRole("heading", { name: "Elevated API errors" });
		expect(
			screen.queryByRole("status", { name: "Loading incident" }),
		).toBeNull();
	});

	it("says a missing incident does not exist and offers no retry", async () => {
		stubApi(
			routes(undefined, "admin", {
				[INCIDENT]: {
					status: 404,
					body: { status: 404, code: "not_found", detail: "no such incident" },
				},
			}),
		);
		await render();
		expect(
			await screen.findByText("This incident does not exist"),
		).toBeVisible();
		expect(
			screen.getByText("It may have been deleted. Nothing was changed."),
		).toBeVisible();
		expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
		expect(
			screen.getByRole("button", { name: /All status pages/ }),
		).toHaveAttribute("href", "/status-pages");
	});

	it("offers a retry when the incident failed to load", async () => {
		const user = userEvent.setup();
		let fail = true;
		stubApi(
			routes(undefined, "admin", {
				[INCIDENT]: () =>
					fail
						? Response.json(
								{ status: 500, code: "boom", detail: "down" },
								{ status: 500 },
							)
						: Response.json(incidentView({ updates: UPDATES })),
			}),
		);
		await render();
		expect(await screen.findByText("The incident did not load")).toBeVisible();
		fail = false;
		await user.click(screen.getByRole("button", { name: "Try again" }));
		expect(
			await screen.findByRole("heading", { name: "Elevated API errors" }),
		).toBeVisible();
	});
});

describe("IncidentDetail content", () => {
	it("shows the incident with its status, impact and a timeline, newest first", async () => {
		stubApi(
			routes(
				incidentView({
					impact: "major",
					status: "identified",
					updates: UPDATES,
				}),
			),
		);
		await render();
		expect(
			await screen.findByRole("heading", { name: "Elevated API errors" }),
		).toBeVisible();
		expect(screen.getByText("Major")).toBeVisible();
		const timeline = screen.getByRole("list", {
			name: "Timeline, newest first",
		});
		const entries = within(timeline).getAllByRole("listitem");
		expect(entries).toHaveLength(2);
		expect(entries[0]).toHaveTextContent("Found the cause.");
		expect(entries[0]).toHaveTextContent("Latest");
		expect(entries[1]).toHaveTextContent("We are looking into it.");
		expect(entries[1]).not.toHaveTextContent("Latest");
		expect(screen.getByText("2 updates")).toBeVisible();
	});

	it("says when the timeline is empty", async () => {
		stubApi(routes(incidentView({ updates: null })));
		await render();
		expect(await screen.findByText("No updates yet.")).toBeVisible();
		expect(screen.getByText("0 updates")).toBeVisible();
	});

	it("shows when a resolved incident was resolved", async () => {
		stubApi(
			routes(
				incidentView({
					status: "resolved",
					resolvedAt: "2026-11-01T10:00:00Z",
					updates: UPDATES,
				}),
			),
		);
		await render();
		await screen.findByRole("heading", { name: "Elevated API errors" });
		expect(screen.getByText(/, resolved/)).toBeVisible();
	});

	it("lists the pages it is posted to, marking drafts and pages the reader cannot see", async () => {
		stubApi(
			routes(
				incidentView({
					pageIds: ["page-1", "page-2", "page-x"],
					updates: UPDATES,
				}),
			),
		);
		await render();
		const posted = await screen.findByRole("region", { name: "Posted to" });
		expect(
			within(posted).getByRole("link", { name: "Acme status" }),
		).toHaveAttribute("href", "/status-pages/page-1");
		expect(within(posted).getByText("status.acme.test")).toBeVisible();
		expect(within(posted).getByText("beta.acme.test · draft")).toBeVisible();
		expect(within(posted).getByText("A page you cannot see")).toBeVisible();
	});

	it("lists affected services under their project and environment", async () => {
		stubApi(
			routes(
				incidentView({ serviceIds: ["svc-api", "svc-gone"], updates: UPDATES }),
			),
		);
		await render();
		const affected = await screen.findByRole("region", {
			name: "Affected services",
		});
		await waitFor(() =>
			expect(within(affected).getByText("api")).toBeVisible(),
		);
		expect(within(affected).getByText("Web · Production")).toBeVisible();
		expect(
			within(affected).getByText("A service you cannot see"),
		).toBeVisible();
	});

	it("says no service is marked when none is", async () => {
		stubApi(routes(incidentView({ serviceIds: null, updates: UPDATES })));
		await render();
		expect(await screen.findByText("None marked.")).toBeVisible();
	});

	it("warns that an incident on no page is invisible, and tells a manager how to fix it", async () => {
		stubApi(routes(incidentView({ pageIds: [], updates: UPDATES })));
		await render();
		expect(await screen.findByText("Not posted to any page")).toBeVisible();
		expect(
			screen.getByText(
				/Nobody outside the team can see this incident\. Edit it to choose the pages/,
			),
		).toBeVisible();
		expect(screen.getByText("No pages.")).toBeVisible();
	});

	it("does not tell a viewer to edit and hides every management control", async () => {
		stubApi(routes(incidentView({ pageIds: [], updates: UPDATES }), "viewer"));
		await render();
		await screen.findByText("Not posted to any page");
		expect(screen.queryByText(/Edit it to choose the pages/)).toBeNull();
		expect(
			await screen.findByText(/Only team owners and admins post updates/),
		).toBeVisible();
		expect(screen.queryByRole("button", { name: "Edit" })).toBeNull();
		expect(screen.queryByRole("button", { name: "Delete" })).toBeNull();
		expect(screen.queryByLabelText("What is new")).toBeNull();
	});
});

describe("IncidentDetail posting updates", () => {
	it("suggests the next status and explains it", async () => {
		stubApi(
			routes(incidentView({ status: "investigating", updates: UPDATES })),
		);
		await render();
		const identified = await screen.findByRole("button", {
			name: "Identified",
		});
		expect(identified).toHaveAttribute("aria-pressed", "true");
		expect(
			screen.getByText("We found the cause and are working on a fix."),
		).toBeVisible();
		expect(screen.getByRole("button", { name: "Post update" })).toBeVisible();
	});

	it("asks for text before posting an update", async () => {
		const user = userEvent.setup();
		const api = stubApi(routes());
		await render();
		await screen.findByLabelText("What is new");
		await user.click(screen.getByRole("button", { name: "Post update" }));
		expect(
			await screen.findByText("Write what people should know."),
		).toBeVisible();
		expect(screen.getByLabelText("What is new")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
		expect(posts(api)).toEqual([]);
	});

	it("rejects an update over 4000 characters", async () => {
		const user = userEvent.setup();
		const api = stubApi(routes());
		await render();
		await fill(
			user,
			await screen.findByLabelText("What is new"),
			"x".repeat(4001),
		);
		await user.click(screen.getByRole("button", { name: "Post update" }));
		expect(
			await screen.findByText("An update is at most 4000 characters."),
		).toBeVisible();
		expect(posts(api)).toEqual([]);
	});

	it("posts the update with the chosen status, toasts and clears the form", async () => {
		const user = userEvent.setup();
		const api = stubApi(
			routes(undefined, "admin", {
				"POST /status-incidents/:id/updates": {
					status: 201,
					body: incidentView(),
				},
			}),
		);
		await render();
		await user.click(await screen.findByRole("button", { name: "Monitoring" }));
		expect(
			screen.getByText("A fix is in place; we are watching to be sure."),
		).toBeVisible();
		await fill(user, screen.getByLabelText("What is new"), "  Fix is live.  ");
		await user.click(screen.getByRole("button", { name: "Post update" }));

		expect(await screen.findByText("Update posted")).toBeVisible();
		expect(screen.getByText("It is on the pages now.")).toBeVisible();
		expect(posts(api)).toHaveLength(1);
		expect(posts(api)[0]?.path).toBe("/status-incidents/inc-1/updates");
		expect(posts(api)[0]?.body).toEqual({
			status: "monitoring",
			body: "Fix is live.",
		});
		await waitFor(() =>
			expect(screen.getByLabelText("What is new")).toHaveValue(""),
		);
	});

	it("resolves with a ready-made text that keeps what was already typed", async () => {
		const user = userEvent.setup();
		const api = stubApi(
			routes(undefined, "admin", {
				"POST /status-incidents/:id/updates": {
					status: 201,
					body: incidentView(),
				},
			}),
		);
		await render();
		await user.click(await screen.findByRole("button", { name: "Resolve" }));
		expect(screen.getByLabelText("What is new")).toHaveValue(
			"This incident has been resolved.",
		);
		expect(
			screen.getByText(
				/It is over\. The page returns to its live state\. Pick a status above to keep it open\./,
			),
		).toBeVisible();
		await user.click(screen.getByRole("button", { name: "Post resolution" }));

		expect(await screen.findByText("Incident resolved")).toBeVisible();
		expect(
			screen.getByText("The pages return to their live state."),
		).toBeVisible();
		expect(posts(api)[0]?.body).toEqual({
			status: "resolved",
			body: "This incident has been resolved.",
		});
	});

	it("keeps the text typed before Resolve", async () => {
		const user = userEvent.setup();
		const api = stubApi(
			routes(undefined, "admin", {
				"POST /status-incidents/:id/updates": {
					status: 201,
					body: incidentView(),
				},
			}),
		);
		await render();
		await fill(
			user,
			await screen.findByLabelText("What is new"),
			"All clear since 10:00.",
		);
		await user.click(screen.getByRole("button", { name: "Resolve" }));
		expect(screen.getByLabelText("What is new")).toHaveValue(
			"All clear since 10:00.",
		);
		await user.click(screen.getByRole("button", { name: "Post resolution" }));
		await screen.findByText("Incident resolved");
		expect(posts(api)[0]?.body).toEqual({
			status: "resolved",
			body: "All clear since 10:00.",
		});
	});

	it("goes back to an open status after Resolve when one is picked", async () => {
		const user = userEvent.setup();
		stubApi(routes());
		await render();
		await user.click(await screen.findByRole("button", { name: "Resolve" }));
		expect(
			screen.getByRole("button", { name: "Post resolution" }),
		).toBeVisible();
		await user.click(screen.getByRole("button", { name: "Monitoring" }));
		expect(screen.getByRole("button", { name: "Post update" })).toBeVisible();
		expect(screen.getByRole("button", { name: "Resolve" })).toBeVisible();
	});

	it("explains that an update reopens a resolved incident, with no Resolve button", async () => {
		const user = userEvent.setup();
		const api = stubApi(
			routes(
				incidentView({
					status: "resolved",
					resolvedAt: "2026-11-01T10:00:00Z",
					updates: UPDATES,
				}),
				"admin",
				{
					"POST /status-incidents/:id/updates": {
						status: 201,
						body: incidentView(),
					},
				},
			),
		);
		await render();
		expect(
			await screen.findByText(
				"This incident is resolved. An update with any other status reopens it.",
			),
		).toBeVisible();
		expect(
			screen.getByRole("button", { name: "Investigating" }),
		).toHaveAttribute("aria-pressed", "true");
		expect(screen.queryByRole("button", { name: "Resolve" })).toBeNull();
		await fill(user, screen.getByLabelText("What is new"), "It is back.");
		await user.click(screen.getByRole("button", { name: "Post update" }));
		await screen.findByText("Update posted");
		expect(posts(api)[0]?.body).toEqual({
			status: "investigating",
			body: "It is back.",
		});
	});

	it("shows the server's message and keeps the text when posting fails", async () => {
		const user = userEvent.setup();
		stubApi(
			routes(undefined, "admin", {
				"POST /status-incidents/:id/updates": {
					status: 500,
					body: { status: 500, code: "boom", detail: "Could not post." },
				},
			}),
		);
		await render();
		await fill(
			user,
			await screen.findByLabelText("What is new"),
			"Fix is live.",
		);
		await user.click(screen.getByRole("button", { name: "Post update" }));
		expect(await screen.findByText("Could not post.")).toBeVisible();
		expect(screen.getByLabelText("What is new")).toHaveValue("Fix is live.");
		expect(screen.queryByText("Update posted")).toBeNull();
	});
});

describe("IncidentDetail edit and delete", () => {
	it("opens the edit dialog on the incident", async () => {
		const user = userEvent.setup();
		stubApi(routes());
		await render();
		await user.click(await screen.findByRole("button", { name: "Edit" }));
		expect(await screen.findByText("Edit incident")).toBeVisible();
		expect(screen.getByLabelText("Title")).toHaveValue("Elevated API errors");
	});

	it("deletes the incident only after confirming, then returns to the list", async () => {
		const user = userEvent.setup();
		const api = stubApi(
			routes(undefined, "admin", {
				"DELETE /status-incidents/:id": { status: 204 },
			}),
		);
		await render();
		await user.click(await screen.findByRole("button", { name: "Delete" }));
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("Delete “Elevated API errors”?"),
		).toBeVisible();
		expect(api.calls.filter((c) => c.method === "DELETE")).toEqual([]);
		await user.click(
			within(dialog).getByRole("button", { name: "Delete incident" }),
		);

		expect(await screen.findByText("Incident deleted")).toBeVisible();
		const del = api.calls.find((c) => c.method === "DELETE");
		expect(del?.path).toBe("/status-incidents/inc-1");
		await waitFor(() =>
			expect(screen.getByLabelText("location")).toHaveTextContent(
				/^\/status-pages$/,
			),
		);
	});

	it("keeps the incident when the delete is dismissed", async () => {
		const user = userEvent.setup();
		const api = stubApi(routes());
		await render();
		await user.click(await screen.findByRole("button", { name: "Delete" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
		expect(api.calls.filter((c) => c.method === "DELETE")).toEqual([]);
		expect(screen.getByLabelText("location")).toHaveTextContent(PATH);
	});

	it("shows why the delete failed inside the dialog and stays on the incident", async () => {
		const user = userEvent.setup();
		stubApi(
			routes(undefined, "admin", {
				"DELETE /status-incidents/:id": {
					status: 500,
					body: { status: 500, code: "boom", detail: "Could not delete." },
				},
			}),
		);
		await render();
		await user.click(await screen.findByRole("button", { name: "Delete" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Delete incident" }),
		);
		expect(await within(dialog).findByText("Could not delete.")).toBeVisible();
		expect(screen.queryByText("Incident deleted")).toBeNull();
		expect(screen.getByLabelText("location")).toHaveTextContent(PATH);
	});
});
