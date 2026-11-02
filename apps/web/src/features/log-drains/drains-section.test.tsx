import { test as bunTest, describe, expect } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { LogDrainView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { LogDrainsSection } from "./drains-section";
import { drainView, serverStatus, TEAM_ROUTES } from "./test-fixtures";

/** Form tests type into a large dialog; allow for a loaded CI machine. */
const test = (
	name: string,
	fn: (() => void) | (() => Promise<unknown>),
): void => {
	bunTest(name, fn, 30_000);
};

const CENTRAL = drainView({
	id: "central",
	name: "internal",
	builtin: true,
	enabled: false,
	kind: "http",
	loki: undefined,
});
const GRAFANA = drainView({ id: "d-1", name: "Grafana" });
const SYSLOG = drainView({
	id: "d-2",
	name: "Papertrail",
	kind: "syslog",
	loki: undefined,
	syslog: { address: "logs.example.com:6514", protocol: "tcp+tls" },
	enabled: false,
});

/** A drain list the test can change; every GET reads the current items. */
function drainStore(items: LogDrainView[]) {
	const state = { items };
	return {
		state,
		reply: () => Response.json({ items: state.items }),
	};
}

const articles = () => screen.getAllByTestId("log-drain");

describe("LogDrainsSection: list", () => {
	test("shows the central logs first, then the team's drains in order, with the count", async () => {
		const store = drainStore([GRAFANA, CENTRAL, SYSLOG]);
		stubApi({ ...TEAM_ROUTES, "GET /log-drains": store.reply });
		await renderWithProviders(<LogDrainsSection />);
		await screen.findByRole("heading", { name: "Central logs" });
		expect(
			articles().map((a) => within(a).getByRole("heading").textContent),
		).toEqual(["Central logs", "Grafana", "Papertrail"]);
		expect(screen.getByText(/2 drains of your own\./)).toBeVisible();
	});

	test("with only the central logs, says there is no drain of your own", async () => {
		const store = drainStore([CENTRAL]);
		stubApi({ ...TEAM_ROUTES, "GET /log-drains": store.reply });
		await renderWithProviders(<LogDrainsSection />);
		expect(await screen.findByText(/No drain of your own yet\./)).toBeVisible();
		expect(screen.getByRole("button", { name: "Add drain" })).toBeVisible();
	});

	test("one drain reads singular", async () => {
		const store = drainStore([GRAFANA]);
		stubApi({ ...TEAM_ROUTES, "GET /log-drains": store.reply });
		await renderWithProviders(<LogDrainsSection />);
		expect(await screen.findByText(/1 drain of your own\./)).toBeVisible();
	});

	test("shows each drain's project by name once projects load", async () => {
		const scoped = drainView({
			allProjects: false,
			projectIds: ["p-web"],
		});
		stubApi({ ...TEAM_ROUTES, "GET /log-drains": drainStore([scoped]).reply });
		await renderWithProviders(<LogDrainsSection />);
		expect(await screen.findByText("Web")).toBeVisible();
	});

	test("shows a skeleton while loading", async () => {
		stubApi({
			...TEAM_ROUTES,
			"GET /log-drains": () => new Promise<Response>(() => {}),
		});
		await renderWithProviders(<LogDrainsSection />);
		expect(screen.queryByTestId("log-drain")).toBeNull();
		expect(screen.queryByRole("button", { name: "Add drain" })).toBeNull();
		expect(screen.queryByText("Log drains did not load")).toBeNull();
	});

	test("empty team: explains drains and offers to add one", async () => {
		stubApi({ ...TEAM_ROUTES, "GET /log-drains": drainStore([]).reply });
		await renderWithProviders(<LogDrainsSection />);
		expect(
			await screen.findByText("Ship your logs to the platform you already use"),
		).toBeVisible();
		expect(screen.queryByTestId("log-drain")).toBeNull();
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Add drain" }));
		expect(
			await screen.findByRole("dialog", { name: "Add log drain" }),
		).toBeVisible();
	});

	test("a null item list is the empty state too", async () => {
		stubApi({
			...TEAM_ROUTES,
			"GET /log-drains": { body: { items: null } },
		});
		await renderWithProviders(<LogDrainsSection />);
		expect(
			await screen.findByText("Ship your logs to the platform you already use"),
		).toBeVisible();
	});

	test("a non-admin (403) sees why the list did not load and can retry", async () => {
		const user = userEvent.setup();
		let allowed = false;
		const api = stubApi({
			...TEAM_ROUTES,
			"GET /log-drains": () =>
				allowed
					? Response.json({ items: [GRAFANA] })
					: Response.json(
							{ title: "Forbidden", code: "forbidden", detail: "admins only" },
							{
								status: 403,
								headers: { "Content-Type": "application/problem+json" },
							},
						),
		});
		await renderWithProviders(<LogDrainsSection />);
		expect(await screen.findByText("Log drains did not load")).toBeVisible();
		expect(screen.getByText(/Log drains are for team admins/)).toBeVisible();
		expect(screen.queryByRole("button", { name: "Add drain" })).toBeNull();
		allowed = true;
		await user.click(screen.getByRole("button", { name: "Try again" }));
		expect(
			await screen.findByRole("heading", { name: "Grafana" }),
		).toBeVisible();
		expect(api.unmatched).toEqual([]);
	});
});

describe("LogDrainsSection: switch", () => {
	test("pausing a drain PATCHes enabled:false, toasts and re-reads the list", async () => {
		const user = userEvent.setup();
		const store = drainStore([GRAFANA]);
		const api = stubApi({
			...TEAM_ROUTES,
			"GET /log-drains": store.reply,
			"PATCH /log-drains/:id": () => {
				store.state.items = [{ ...GRAFANA, enabled: false }];
				return Response.json(store.state.items[0]);
			},
		});
		await renderWithProviders(<LogDrainsSection />);
		await user.click(
			await screen.findByRole("switch", { name: "Pause Grafana" }),
		);
		expect(await screen.findByText("Paused Grafana")).toBeVisible();
		expect(
			screen.getByText("Nothing is shipped until you resume it."),
		).toBeVisible();
		const patch = api.calls.find((c) => c.method === "PATCH");
		expect([patch?.path, patch?.body]).toEqual([
			"/log-drains/d-1",
			{ enabled: false },
		]);
		expect(
			await screen.findByRole("switch", { name: "Resume Grafana" }),
		).not.toBeChecked();
		expect(screen.getByText("paused")).toBeVisible();
	});

	test("turning on the central logs says so", async () => {
		const user = userEvent.setup();
		const store = drainStore([CENTRAL]);
		const api = stubApi({
			...TEAM_ROUTES,
			"GET /log-drains": store.reply,
			"PATCH /log-drains/central": () => {
				store.state.items = [{ ...CENTRAL, enabled: true }];
				return Response.json(store.state.items[0]);
			},
		});
		await renderWithProviders(<LogDrainsSection />);
		await user.click(
			await screen.findByRole("switch", { name: "Turn on central logs" }),
		);
		expect(await screen.findByText("Central logs are on")).toBeVisible();
		expect(api.calls.find((c) => c.method === "PATCH")?.body).toEqual({
			enabled: true,
		});
	});

	test("a rejected switch toasts the server's message and leaves the drain as it was", async () => {
		const user = userEvent.setup();
		stubApi({
			...TEAM_ROUTES,
			"GET /log-drains": drainStore([GRAFANA]).reply,
			"PATCH /log-drains/d-1": problem(403, {
				title: "Forbidden",
				code: "forbidden",
				detail: "Only admins can pause drains",
			}),
		});
		await renderWithProviders(<LogDrainsSection />);
		await user.click(
			await screen.findByRole("switch", { name: "Pause Grafana" }),
		);
		expect(await screen.findByText("Could not change the drain")).toBeVisible();
		expect(screen.getByText("Only admins can pause drains")).toBeVisible();
		expect(screen.queryByText("Paused Grafana")).toBeNull();
		expect(screen.getByRole("switch", { name: "Pause Grafana" })).toBeChecked();
	});
});

describe("LogDrainsSection: test a drain", () => {
	test("POSTs an empty body and shows the delivered line, then Dismiss removes it", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			...TEAM_ROUTES,
			"GET /log-drains": drainStore([GRAFANA]).reply,
			"POST /log-drains/d-1/test": {
				body: {
					ok: true,
					serverId: "srv-1",
					serverName: "fra-1",
					statusCode: 204,
					message: "",
				},
			},
		});
		await renderWithProviders(<LogDrainsSection />);
		await user.click(await screen.findByRole("button", { name: "Test" }));
		const status = await screen.findByRole("status");
		expect(status).toHaveTextContent("Test line delivered");
		expect(status).toHaveTextContent("From fra-1 · HTTP 204");
		const post = api.calls.find((c) => c.method === "POST");
		expect([post?.path, post?.body]).toEqual(["/log-drains/d-1/test", {}]);
		// The list is read again so the card's saved test result updates.
		await waitFor(() =>
			expect(api.calls.filter((c) => c.path === "/log-drains")).toHaveLength(2),
		);
		await user.click(within(status).getByRole("button", { name: "Dismiss" }));
		expect(screen.queryByRole("status")).toBeNull();
	});

	test("a receiver that rejects the line shows a failure with its message", async () => {
		const user = userEvent.setup();
		stubApi({
			...TEAM_ROUTES,
			"GET /log-drains": drainStore([GRAFANA]).reply,
			"POST /log-drains/d-1/test": {
				body: {
					ok: false,
					serverId: "s",
					serverName: "fra-1",
					statusCode: 401,
					message: "bad token",
				},
			},
		});
		await renderWithProviders(<LogDrainsSection />);
		await user.click(await screen.findByRole("button", { name: "Test" }));
		const status = await screen.findByRole("status");
		expect(status).toHaveTextContent("Test line failed");
		expect(status).toHaveTextContent("From fra-1 · HTTP 401 · bad token");
	});

	test("with no server online, says to connect one", async () => {
		const user = userEvent.setup();
		stubApi({
			...TEAM_ROUTES,
			"GET /log-drains": drainStore([GRAFANA]).reply,
			"POST /log-drains/d-1/test": problem(409, {
				title: "Conflict",
				code: "no_online_server",
				detail: "no server",
			}),
		});
		await renderWithProviders(<LogDrainsSection />);
		await user.click(await screen.findByRole("button", { name: "Test" }));
		const status = await screen.findByRole("status");
		expect(status).toHaveTextContent("No server is connected");
		expect(status).toHaveTextContent("Connect a server and try again.");
	});

	test("any other API error shows its message", async () => {
		const user = userEvent.setup();
		stubApi({
			...TEAM_ROUTES,
			"GET /log-drains": drainStore([GRAFANA]).reply,
			"POST /log-drains/d-1/test": problem(500, {
				title: "Internal",
				code: "internal",
				detail: "agent timed out",
			}),
		});
		await renderWithProviders(<LogDrainsSection />);
		await user.click(await screen.findByRole("button", { name: "Test" }));
		const status = await screen.findByRole("status");
		expect(status).toHaveTextContent("Could not send the test");
		expect(status).toHaveTextContent("agent timed out");
	});

	test("the result belongs to the drain tested, not its neighbours", async () => {
		const user = userEvent.setup();
		stubApi({
			...TEAM_ROUTES,
			"GET /log-drains": drainStore([GRAFANA, SYSLOG]).reply,
			"POST /log-drains/d-2/test": {
				body: { ok: true, serverId: "s", serverName: "fra-1", message: "" },
			},
		});
		await renderWithProviders(<LogDrainsSection />);
		await screen.findByRole("heading", { name: "Papertrail" });
		const syslogCard = screen.getByRole("article", { name: "Papertrail" });
		await user.click(within(syslogCard).getByRole("button", { name: "Test" }));
		expect(await within(syslogCard).findByRole("status")).toBeVisible();
		expect(
			within(screen.getByRole("article", { name: "Grafana" })).queryByRole(
				"status",
			),
		).toBeNull();
	});
});

describe("LogDrainsSection: delete", () => {
	test("asks first, then DELETEs and toasts", async () => {
		const user = userEvent.setup();
		const store = drainStore([GRAFANA, SYSLOG]);
		const api = stubApi({
			...TEAM_ROUTES,
			"GET /log-drains": store.reply,
			"DELETE /log-drains/d-1": () => {
				store.state.items = [SYSLOG];
				return new Response(null, { status: 204 });
			},
		});
		await renderWithProviders(<LogDrainsSection />);
		const card = await screen.findByRole("article", { name: "Grafana" });
		await user.click(within(card).getByRole("button", { name: "Delete" }));
		const dialog = await screen.findByRole("alertdialog");
		expect(dialog).toHaveTextContent("Delete Grafana?");
		expect(dialog).toHaveTextContent("Every server stops shipping logs to it.");
		expect(api.calls.some((c) => c.method === "DELETE")).toBe(false);
		await user.click(
			within(dialog).getByRole("button", { name: "Delete drain" }),
		);
		expect(await screen.findByText("Deleted Grafana")).toBeVisible();
		expect(screen.getByText("Servers stop shipping to it.")).toBeVisible();
		expect(
			api.calls.filter((c) => c.method === "DELETE").map((c) => c.path),
		).toEqual(["/log-drains/d-1"]);
		await waitFor(() =>
			expect(screen.queryByRole("article", { name: "Grafana" })).toBeNull(),
		);
		expect(screen.getByRole("article", { name: "Papertrail" })).toBeVisible();
	});

	test("Cancel keeps the drain and sends nothing", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			...TEAM_ROUTES,
			"GET /log-drains": drainStore([GRAFANA]).reply,
		});
		await renderWithProviders(<LogDrainsSection />);
		await user.click(await screen.findByRole("button", { name: "Delete" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
		expect(screen.getByRole("article", { name: "Grafana" })).toBeVisible();
		expect(api.calls.some((c) => c.method === "DELETE")).toBe(false);
	});

	test("a failed delete explains why and keeps the dialog and the drain", async () => {
		const user = userEvent.setup();
		stubApi({
			...TEAM_ROUTES,
			"GET /log-drains": drainStore([GRAFANA]).reply,
			"DELETE /log-drains/d-1": problem(403, {
				title: "Forbidden",
				code: "forbidden",
				detail: "Only admins can delete drains",
			}),
		});
		await renderWithProviders(<LogDrainsSection />);
		await user.click(await screen.findByRole("button", { name: "Delete" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Delete drain" }),
		);
		expect(await screen.findByText("Could not delete the drain")).toBeVisible();
		expect(
			within(dialog).getByText("Only admins can delete drains"),
		).toBeVisible();
		expect(screen.queryByText("Deleted Grafana")).toBeNull();
		// The open dialog hides the page from assistive tech, so look past that.
		expect(
			screen.getByRole("article", { name: "Grafana", hidden: true }),
		).toBeInTheDocument();
	});

	test("the central logs have nothing to delete", async () => {
		stubApi({ ...TEAM_ROUTES, "GET /log-drains": drainStore([CENTRAL]).reply });
		await renderWithProviders(<LogDrainsSection />);
		await screen.findByRole("heading", { name: "Central logs" });
		expect(screen.queryByRole("button", { name: "Delete" })).toBeNull();
		expect(screen.queryByRole("button", { name: "Edit" })).toBeNull();
	});
});

describe("LogDrainsSection: add and edit", () => {
	test("Edit opens the dialog on that drain and saving PATCHes it", async () => {
		const user = userEvent.setup();
		const store = drainStore([GRAFANA, SYSLOG]);
		const api = stubApi({
			...TEAM_ROUTES,
			"GET /log-drains": store.reply,
			"PATCH /log-drains/d-2": () => {
				store.state.items = [GRAFANA, { ...SYSLOG, name: "Papertrail EU" }];
				return Response.json(store.state.items[1]);
			},
		});
		await renderWithProviders(<LogDrainsSection />);
		const card = await screen.findByRole("article", { name: "Papertrail" });
		await user.click(within(card).getByRole("button", { name: "Edit" }));
		const dialog = await screen.findByRole("dialog", {
			name: "Edit Papertrail",
		});
		expect(within(dialog).getByLabelText("Address")).toHaveValue(
			"logs.example.com:6514",
		);
		await user.type(within(dialog).getByLabelText("Name"), " EU");
		await user.click(within(dialog).getByRole("button", { name: "Save" }));
		expect(await screen.findByText("Log drain saved")).toBeVisible();
		const patch = api.calls.find((c) => c.method === "PATCH");
		expect(patch?.path).toBe("/log-drains/d-2");
		expect(patch?.body).toMatchObject({
			name: "Papertrail EU",
			config: {
				syslog: { address: "logs.example.com:6514", protocol: "tcp+tls" },
			},
		});
		expect(
			await screen.findByRole("article", { name: "Papertrail EU" }),
		).toBeVisible();
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
	});

	test("Add drain from the list creates one and it appears", async () => {
		const user = userEvent.setup();
		const store = drainStore([GRAFANA]);
		const api = stubApi({
			...TEAM_ROUTES,
			"GET /log-drains": store.reply,
			"POST /log-drains": () => {
				store.state.items = [GRAFANA, drainView({ id: "d-3", name: "Loki 2" })];
				return Response.json(store.state.items[1], { status: 201 });
			},
		});
		await renderWithProviders(<LogDrainsSection />);
		await user.click(await screen.findByRole("button", { name: "Add drain" }));
		const dialog = await screen.findByRole("dialog", { name: "Add log drain" });
		await user.type(within(dialog).getByLabelText("Name"), "Loki 2");
		await user.click(within(dialog).getByLabelText("Push URL"));
		await user.paste("https://loki.example/push");
		await user.click(within(dialog).getByRole("button", { name: "Add drain" }));
		expect(await screen.findByText("Log drain added")).toBeVisible();
		expect(api.calls.find((c) => c.method === "POST")?.body).toMatchObject({
			name: "Loki 2",
			kind: "loki",
			config: { loki: { url: "https://loki.example/push" } },
		});
		expect(
			await screen.findByRole("article", { name: "Loki 2" }),
		).toBeVisible();
	});

	test("a server's delivery state shows on its card", async () => {
		const withServers = drainView({
			servers: [
				serverStatus({ serverName: "fra-1" }),
				serverStatus({
					serverId: "s2",
					serverName: "ams-1",
					state: "failing",
					lastError: "dial tcp: refused",
				}),
			],
		});
		stubApi({
			...TEAM_ROUTES,
			"GET /log-drains": drainStore([withServers]).reply,
		});
		await renderWithProviders(<LogDrainsSection />);
		const card = await screen.findByRole("article", { name: "Grafana" });
		expect(within(card).getByText("ams-1")).toBeVisible();
		expect(within(card).getByText("Failing")).toBeVisible();
		expect(within(card).getByText("dial tcp: refused")).toBeVisible();
	});
});
