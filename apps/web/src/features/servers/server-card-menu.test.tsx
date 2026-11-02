import { describe, expect, spyOn, test } from "bun:test";
import { useRouterState } from "@tanstack/react-router";
import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Server as ServerWire, ServiceView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, type Overrides } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { toServerView } from "./model";
import { ServerCardMenu } from "./server-card-menu";

/** An online server the menu may remove: decommissionable and not the control-plane host. */
const wireServer = (overrides: Overrides<ServerWire> = {}) =>
	make("Server", {
		id: "srv-1",
		name: "hetzner-01",
		agentVersion: "v0.4.2",
		canDecommission: true,
		controlPlane: false,
		serviceCount: 0,
		...overrides,
	});

/** Shows where the router is, so a test can see a navigation happen. */
function Where({ children }: { children: React.ReactNode }) {
	const pathname = useRouterState({ select: (s) => s.location.pathname });
	return (
		<>
			<output aria-label="location">{pathname}</output>
			{children}
		</>
	);
}

/** The projects tree of the team: one project, one environment, the given services. */
function placementRoutes(services: ServiceView[]) {
	return {
		"GET /projects": {
			body: make("PageProject", {
				items: [make("Project", { id: "p1", name: "Shop" })],
			}),
		},
		"GET /projects/:projectId/environments": {
			body: make("PageEnvironment", {
				items: [
					make("Environment", {
						id: "e1",
						slug: "production",
						name: "Production",
					}),
				],
			}),
		},
		"GET /environments/:environmentId/services": {
			body: make("PageServiceView", { items: services }),
		},
	};
}

async function openMenu(
	server: ServerWire,
	routes: Parameters<typeof stubApi>[0] = {},
) {
	const api = stubApi({ ...placementRoutes([]), ...routes });
	const user = userEvent.setup();
	await renderWithProviders(
		<Where>
			<ServerCardMenu server={toServerView(server)} />
		</Where>,
		{ path: "/servers" },
	);
	await user.click(
		screen.getByRole("button", { name: "Actions for hetzner-01" }),
	);
	return { api, user };
}

/**
 * Holds the confirm button the way a person does. The hold is a real 1.3 s
 * timer, so its callback is captured instead of waited for (fake timers would
 * freeze the animation frame loop the dialog runs on).
 */
function holdToConfirm(name: RegExp) {
	const button = screen.getByRole("button", { name });
	const realSetTimeout = window.setTimeout.bind(window);
	let finishHold: (() => void) | undefined;
	const spy = spyOn(window, "setTimeout").mockImplementation(((
		handler: () => void,
		ms?: number,
	) => {
		if (ms === 1300) {
			finishHold = handler;
			return 0;
		}
		return realSetTimeout(handler, ms);
	}) as typeof window.setTimeout);
	try {
		fireEvent.keyDown(button, { key: "Enter" });
	} finally {
		spy.mockRestore();
	}
	if (!finishHold) throw new Error("pressing the button did not start a hold");
	act(() => finishHold?.());
}

describe("ServerCardMenu", () => {
	test("the control-plane host has no menu", async () => {
		stubApi({});
		await renderWithProviders(
			<ServerCardMenu
				server={toServerView(wireServer({ controlPlane: true }))}
			/>,
		);
		expect(
			screen.queryByRole("button", { name: "Actions for hetzner-01" }),
		).not.toBeInTheDocument();
	});

	test("Open goes to the server's page", async () => {
		const { user } = await openMenu(wireServer());
		expect(screen.getByLabelText("location")).toHaveTextContent("/servers");
		await user.click(screen.getByRole("menuitem", { name: "Open" }));
		await waitFor(() =>
			expect(screen.getByLabelText("location")).toHaveTextContent(
				"/servers/srv-1",
			),
		);
	});

	test("an online server whose agent can clean offers both ways to remove it", async () => {
		await openMenu(wireServer());
		expect(
			screen.getByRole("menuitem", { name: "Remove server…" }),
		).toBeEnabled();
		expect(
			screen.getByRole("menuitem", { name: "Remove without cleaning…" }),
		).toBeInTheDocument();
	});

	test("an offline server can only be removed without cleaning", async () => {
		await openMenu(wireServer({ status: "offline" }));
		expect(
			screen.getByRole("menuitem", { name: "Remove server…" }),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("menuitem", { name: "Remove without cleaning…" }),
		).not.toBeInTheDocument();
	});

	test("an agent too old to clean can only be forgotten", async () => {
		await openMenu(wireServer({ canDecommission: false }));
		expect(
			screen.queryByRole("menuitem", { name: "Remove without cleaning…" }),
		).not.toBeInTheDocument();
	});

	test("a server being removed shows Removing… and cannot be removed again", async () => {
		await openMenu(
			wireServer({
				removal: { state: "removing", startedAt: "2026-01-01T00:00:00Z" },
			}),
		);
		const item = screen.getByRole("menuitem", { name: "Removing…" });
		expect(item).toHaveAttribute("aria-disabled", "true");
		expect(
			screen.queryByRole("menuitem", { name: "Remove without cleaning…" }),
		).not.toBeInTheDocument();
	});

	test("Remove server asks for the server's name and removes it with mode=clean", async () => {
		const { api, user } = await openMenu(wireServer(), {
			"DELETE /servers/:serverId": {
				body: make("ServerRemoval", {
					mode: "clean",
					removed: true,
					hostUntouched: false,
				}),
			},
			"GET /servers": { body: make("PageServer", { items: [] }) },
		});
		await user.click(screen.getByRole("menuitem", { name: "Remove server…" }));
		expect(await screen.findByText("Remove hetzner-01?")).toBeInTheDocument();
		expect(
			screen.getByText(/The agent stops and removes every container/),
		).toBeInTheDocument();

		// The hold stays refused until the name is typed.
		await user.type(
			screen.getByLabelText(/Type hetzner-01 to confirm/),
			"hetzner-01",
		);
		holdToConfirm(/Hold to remove server/i);

		expect(await screen.findByText("Removed hetzner-01")).toBeInTheDocument();
		const del = api.calls.find((call) => call.method === "DELETE");
		expect(del?.path).toBe("/servers/srv-1");
		expect(del?.query.get("confirm")).toBe("hetzner-01");
		expect(del?.query.get("mode")).toBe("clean");
		await waitFor(() =>
			expect(screen.getByLabelText("location")).toHaveTextContent("/servers"),
		);
		expect(api.unmatched).toEqual([]);
	});

	test("Remove without cleaning forgets the server with mode=forget and says the host keeps its files", async () => {
		const { api, user } = await openMenu(wireServer(), {
			"DELETE /servers/:serverId": {
				body: make("ServerRemoval", {
					mode: "forget",
					removed: true,
					hostUntouched: true,
				}),
			},
			"GET /servers": { body: make("PageServer", { items: [] }) },
		});
		await user.click(
			screen.getByRole("menuitem", { name: "Remove without cleaning…" }),
		);
		expect(
			await screen.findByText("Remove hetzner-01 without cleaning?"),
		).toBeInTheDocument();
		expect(
			screen.getByText(/host keeps its containers, volumes/),
		).toBeInTheDocument();
		await user.type(
			screen.getByLabelText(/Type hetzner-01 to confirm/),
			"hetzner-01",
		);
		holdToConfirm(/Hold to remove server/i);
		await screen.findByText("Removed hetzner-01");
		const del = api.calls.find((call) => call.method === "DELETE");
		expect(del?.query.get("mode")).toBe("forget");
	});

	test("a removal the agent still has to finish says the host is being emptied", async () => {
		const { user } = await openMenu(wireServer(), {
			"DELETE /servers/:serverId": {
				status: 202,
				body: make("ServerRemoval", {
					mode: "clean",
					removed: false,
					hostUntouched: false,
				}),
			},
			"GET /servers": { body: make("PageServer", { items: [] }) },
		});
		await user.click(screen.getByRole("menuitem", { name: "Remove server…" }));
		await user.type(
			await screen.findByLabelText(/Type hetzner-01 to confirm/),
			"hetzner-01",
		);
		holdToConfirm(/Hold to remove server/i);
		expect(
			await screen.findByText(
				"Removing hetzner-01: the agent is emptying the host",
			),
		).toBeInTheDocument();
		expect(screen.getByLabelText("location")).toHaveTextContent("/servers");
	});
});
