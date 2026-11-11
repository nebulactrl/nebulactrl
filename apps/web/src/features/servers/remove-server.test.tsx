import { describe, expect, spyOn, test } from "bun:test";
import { useRouterState } from "@tanstack/react-router";
import {
	act,
	fireEvent,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import { listServersQueryKey } from "#/api/gen/@tanstack/react-query.gen";
import type { Server as ServerWire, ServiceView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, type Overrides, problem } from "#/test/factories";
import { renderWithProviders, testQueryClient } from "#/test/render";
import { toServerView } from "./model";
import { RemoveServerButton } from "./remove-server";

const serverWith = (over: Overrides<ServerWire> = {}) =>
	toServerView(
		make("Server", {
			id: "srv_1",
			name: "edge-1",
			status: "online",
			canDecommission: true,
			controlPlane: false,
			facts: make("Facts", { os: "linux" }),
			...over,
		}),
	);

const project = (id: string, name: string) =>
	make("Project", { id, name, slug: name });
const environment = (id: string, projectId: string, slug: string) =>
	make("Environment", { id, projectId, slug, name: slug });
const service = (id: string, name: string, serverId: string) =>
	make("ServiceView", { id, name, serverId, server: { id: serverId } });

/** Routes for an account with no services at all. */
const NO_SERVICES = {
	"GET /projects": { body: make("PageProject", { items: [] }) },
};

/** Routes for one project / environment holding the given services. */
const withServices = (services: ServiceView[]) => ({
	"GET /projects": {
		body: make("PageProject", { items: [project("prj_1", "shop")] }),
	},
	"GET /projects/:projectId/environments": {
		body: make("PageEnvironment", {
			items: [environment("env_1", "prj_1", "production")],
		}),
	},
	"GET /environments/:environmentId/services": {
		body: make("PageServiceView", { items: services }),
	},
});

function Where() {
	const pathname = useRouterState({ select: (s) => s.location.pathname });
	return <p data-testid="pathname">{pathname}</p>;
}

function renderButton(server = serverWith(), queryClient = testQueryClient()) {
	return renderWithProviders(
		<>
			<RemoveServerButton server={server} />
			<Where />
		</>,
		{ path: "/servers/srv_1", queryClient },
	);
}

/** The hold key's duration in HoldActionButton, as the confirm dialog sets it. */
const HOLD_MS = 1300;

/**
 * Presses the hold key with its 1.3 s timer shortened to the next tick: the
 * fake clock would also strand react-query's own timers, so only the hold's
 * timer is rushed. Returns after the key is down; the caller releases or waits.
 */
function pressHold(button: HTMLElement) {
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
}

/** Lets every timer already queued run. */
async function nextTick() {
	const tick = Promise.withResolvers<void>();
	window.setTimeout(tick.resolve, 0);
	await act(async () => {
		await tick.promise;
	});
}

/** Holds the key to the end. */
async function hold(button: HTMLElement) {
	pressHold(button);
	await nextTick();
}

async function openAndName(user: UserEvent, opener: string, name = "edge-1") {
	await user.click(await screen.findByRole("button", { name: opener }));
	const dialog = await screen.findByRole("alertdialog");
	await user.type(within(dialog).getByRole("textbox"), name);
	return dialog;
}

describe("RemoveServerButton: which keys are offered", () => {
	test("an online server that can clean itself offers both ways", async () => {
		stubApi(NO_SERVICES);
		await renderButton();
		expect(
			screen.getByRole("button", { name: "Remove server…" }),
		).toBeEnabled();
		expect(
			screen.getByRole("button", { name: "Remove without cleaning…" }),
		).toBeEnabled();
	});

	test("an offline server can only be forgotten, with the one key", async () => {
		stubApi(NO_SERVICES);
		await renderButton(serverWith({ status: "offline" }));
		expect(
			screen.getByRole("button", { name: "Remove server…" }),
		).toBeEnabled();
		expect(
			screen.queryByRole("button", { name: "Remove without cleaning…" }),
		).not.toBeInTheDocument();
	});

	test("an online agent too old to clean can only be forgotten", async () => {
		stubApi(NO_SERVICES);
		await renderButton(serverWith({ canDecommission: false }));
		expect(
			screen.queryByRole("button", { name: "Remove without cleaning…" }),
		).not.toBeInTheDocument();
	});

	test("a removal in progress disables the key and hides the other", async () => {
		stubApi(NO_SERVICES);
		await renderButton(serverWith({ removal: { state: "removing" } }));
		expect(screen.getByRole("button", { name: "Removing…" })).toBeDisabled();
		expect(
			screen.queryByRole("button", { name: "Remove server…" }),
		).not.toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Remove without cleaning…" }),
		).not.toBeInTheDocument();
	});

	test("a failed earlier removal can be tried again", async () => {
		stubApi(NO_SERVICES);
		await renderButton(serverWith({ removal: { state: "failed" } }));
		expect(
			screen.getByRole("button", { name: "Remove server…" }),
		).toBeEnabled();
	});

	test("the control-plane server cannot be removed from here", async () => {
		stubApi(NO_SERVICES);
		await renderButton(serverWith({ controlPlane: true }));
		expect(screen.queryByRole("button")).not.toBeInTheDocument();
	});
});

describe("RemoveServerButton: the dialogs", () => {
	test("clean removal explains what the agent does and warns it cannot be undone", async () => {
		stubApi(NO_SERVICES);
		const user = userEvent.setup();
		await renderButton();
		await user.click(screen.getByRole("button", { name: "Remove server…" }));
		const dialog = await screen.findByRole("alertdialog");
		expect(within(dialog).getByText("Remove edge-1?")).toBeInTheDocument();
		expect(
			within(dialog).getByText(/The agent stops and removes every container/),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText(/Backups stored on this server stay at/),
		).toHaveTextContent("/var/lib/nebulactrl-backups");
		expect(
			within(dialog).getByText("This cannot be undone."),
		).toBeInTheDocument();
		expect(
			within(dialog).getByRole("textbox", { name: /Type edge-1 to confirm/ }),
		).toBeInTheDocument();
		expect(
			within(dialog).getByRole("button", { name: /Hold to remove server/ }),
		).toBeInTheDocument();
	});

	test("removing without cleaning names itself and gives the by-hand commands", async () => {
		stubApi(NO_SERVICES);
		const user = userEvent.setup();
		await renderButton();
		await user.click(
			screen.getByRole("button", { name: "Remove without cleaning…" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("Remove edge-1 without cleaning?"),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText(/host keeps its containers, volumes/),
		).toBeInTheDocument();
		// The uninstall one-liner points at this control plane.
		const commands = within(dialog).getByText(/--uninstall --purge/);
		expect(commands).toHaveTextContent(
			`curl -fsSL ${window.location.origin}/install-agent.sh | sudo sh -s -- --uninstall --purge`,
		);
		expect(commands).toHaveTextContent("docker rm -f nebulactrl-traefik");
		expect(
			within(dialog).queryByText("This cannot be undone."),
		).not.toBeInTheDocument();
	});

	test("an offline server's only key is a forget, titled as a plain removal", async () => {
		stubApi(NO_SERVICES);
		const user = userEvent.setup();
		await renderButton(serverWith({ status: "offline" }));
		await user.click(screen.getByRole("button", { name: "Remove server…" }));
		const dialog = await screen.findByRole("alertdialog");
		expect(within(dialog).getByText("Remove edge-1?")).toBeInTheDocument();
		expect(
			within(dialog).getByText(/host keeps its containers, volumes/),
		).toBeInTheDocument();
	});

	test("a server that never connected has nothing to clean and no commands", async () => {
		stubApi(NO_SERVICES);
		const user = userEvent.setup();
		await renderButton(
			serverWith({ status: "pending", canDecommission: false }),
		);
		await user.click(screen.getByRole("button", { name: "Remove server…" }));
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText(/edge-1 never connected, so there is nothing/),
		).toBeInTheDocument();
		expect(within(dialog).queryByText(/--uninstall/)).not.toBeInTheDocument();
		expect(
			within(dialog).queryByText(/The agent stops and removes/),
		).not.toBeInTheDocument();
	});

	test("names the one service that blocks the removal", async () => {
		stubApi(
			withServices([
				service("svc_1", "api", "srv_1"),
				service("svc_2", "elsewhere", "srv_2"),
			]),
		);
		const user = userEvent.setup();
		await renderButton();
		await user.click(screen.getByRole("button", { name: "Remove server…" }));
		const dialog = await screen.findByRole("alertdialog");
		expect(
			await within(dialog).findByText(/1 service is still placed here:/),
		).toHaveTextContent("api (shop)");
		expect(within(dialog).queryByText(/elsewhere/)).not.toBeInTheDocument();
		expect(
			within(dialog).getByText(/Move or delete it first/),
		).toBeInTheDocument();
	});

	test("lists five blocking services and counts the rest", async () => {
		stubApi(
			withServices(
				["g", "a", "f", "c", "b", "e", "d"].map((name, i) =>
					service(`svc_${i}`, name, "srv_1"),
				),
			),
		);
		const user = userEvent.setup();
		await renderButton();
		await user.click(screen.getByRole("button", { name: "Remove server…" }));
		const dialog = await screen.findByRole("alertdialog");
		const note = await within(dialog).findByText(
			/7 services are still placed here:/,
		);
		expect(note).toHaveTextContent(
			"a (shop), b (shop), c (shop), d (shop), e (shop) and 2 more",
		);
		expect(
			within(dialog).getByText(/Move or delete them first/),
		).toBeInTheDocument();
	});
});

describe("RemoveServerButton: removing", () => {
	test("holding the key after typing the name removes the server, says so and leaves for the list", async () => {
		const queryClient = testQueryClient();
		queryClient.setQueryData(listServersQueryKey(), { items: [] });
		const api = stubApi({
			...NO_SERVICES,
			"DELETE /servers/:serverId": {
				body: make("ServerRemoval", {
					removed: true,
					mode: "clean",
					hostUntouched: false,
				}),
			},
			"GET /servers": { body: make("PageServer", { items: [] }) },
		});
		const user = userEvent.setup();
		await renderButton(serverWith(), queryClient);
		const dialog = await openAndName(user, "Remove server…");
		await hold(
			within(dialog).getByRole("button", { name: /Hold to remove server/ }),
		);

		expect(await screen.findByText("Removed edge-1")).toBeVisible();
		await waitFor(() =>
			expect(screen.getByTestId("pathname")).toHaveTextContent("/servers"),
		);
		expect(screen.getByTestId("pathname").textContent).toBe("/servers");
		const del = api.calls.find((c) => c.method === "DELETE");
		expect(del?.path).toBe("/servers/srv_1");
		expect(del?.query.get("confirm")).toBe("edge-1");
		expect(del?.query.get("mode")).toBe("clean");
		expect(api.unmatched).toEqual([]);
	});

	test("removing without cleaning sends mode=forget", async () => {
		const api = stubApi({
			...NO_SERVICES,
			"DELETE /servers/:serverId": {
				body: make("ServerRemoval", {
					removed: true,
					mode: "forget",
					hostUntouched: true,
				}),
			},
			"GET /servers": { body: make("PageServer", { items: [] }) },
		});
		const user = userEvent.setup();
		await renderButton();
		const dialog = await openAndName(user, "Remove without cleaning…");
		await hold(
			within(dialog).getByRole("button", { name: /Hold to remove server/ }),
		);
		expect(await screen.findByText("Removed edge-1")).toBeVisible();
		expect(
			api.calls.find((c) => c.method === "DELETE")?.query.get("mode"),
		).toBe("forget");
	});

	test("a clean removal the agent is still carrying out stays on the page and refreshes the list", async () => {
		const queryClient = testQueryClient();
		queryClient.setQueryData(listServersQueryKey(), { items: [] });
		stubApi({
			...NO_SERVICES,
			"DELETE /servers/:serverId": {
				body: make("ServerRemoval", {
					removed: false,
					mode: "clean",
					hostUntouched: false,
				}),
			},
			"GET /servers": { body: make("PageServer", { items: [] }) },
		});
		const user = userEvent.setup();
		await renderButton(serverWith(), queryClient);
		const dialog = await openAndName(user, "Remove server…");
		await hold(
			within(dialog).getByRole("button", { name: /Hold to remove server/ }),
		);

		expect(
			await screen.findByText(
				"Removing edge-1: the agent is emptying the host",
			),
		).toBeVisible();
		expect(screen.queryByText("Removed edge-1")).not.toBeInTheDocument();
		expect(screen.getByTestId("pathname")).toHaveTextContent("/servers/srv_1");
		await waitFor(() =>
			expect(
				queryClient.getQueryState(listServersQueryKey())?.isInvalidated,
			).toBe(true),
		);
	});

	test("a wrong name refuses the hold: nothing is sent", async () => {
		const api = stubApi(NO_SERVICES);
		const user = userEvent.setup();
		await renderButton();
		const dialog = await openAndName(user, "Remove server…", "edge-2");
		await hold(
			within(dialog).getByRole("button", { name: /Hold to remove server/ }),
		);
		expect(api.calls.filter((c) => c.method === "DELETE")).toEqual([]);
		expect(screen.getByRole("alertdialog")).toBeInTheDocument();
	});

	test("a release before the hold completes sends nothing", async () => {
		const api = stubApi(NO_SERVICES);
		const user = userEvent.setup();
		await renderButton();
		const dialog = await openAndName(user, "Remove server…");
		const key = within(dialog).getByRole("button", {
			name: /Hold to remove server/,
		});
		pressHold(key);
		// Let go before the (rushed) timer can fire, then let it run if it still would.
		fireEvent.keyUp(key, { key: "Enter" });
		await nextTick();
		expect(api.calls.filter((c) => c.method === "DELETE")).toEqual([]);
	});

	test("a refusal from the control plane stays in the dialog with its reason", async () => {
		stubApi({
			...NO_SERVICES,
			"DELETE /servers/:serverId": problem(409, {
				title: "Conflict",
				detail: "2 services are still placed on this server",
			}),
		});
		const user = userEvent.setup();
		await renderButton();
		const dialog = await openAndName(user, "Remove server…");
		await hold(
			within(dialog).getByRole("button", { name: /Hold to remove server/ }),
		);
		expect(
			await within(dialog).findByText(
				"2 services are still placed on this server",
			),
		).toBeInTheDocument();
		expect(screen.queryByText("Removed edge-1")).not.toBeInTheDocument();
		expect(screen.getByTestId("pathname")).toHaveTextContent("/servers/srv_1");
	});

	test("cancelling closes the dialog without a request", async () => {
		const api = stubApi(NO_SERVICES);
		const user = userEvent.setup();
		await renderButton();
		const dialog = await openAndName(user, "Remove server…");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
		);
		expect(api.calls.filter((c) => c.method === "DELETE")).toEqual([]);
	});
});

describe("RemoveServerButton: releasing to another NebulaCtrl", () => {
	test("a server that has connected offers the release, online or not", async () => {
		stubApi(NO_SERVICES);
		const { unmount } = await renderButton();
		expect(
			screen.getByRole("button", { name: "Release to another NebulaCtrl…" }),
		).toBeEnabled();
		unmount();
		await renderButton(serverWith({ status: "offline" }));
		expect(
			screen.getByRole("button", { name: "Release to another NebulaCtrl…" }),
		).toBeEnabled();
	});

	test("a server that never connected, or is being removed, has no release", async () => {
		stubApi(NO_SERVICES);
		const { unmount } = await renderButton(
			serverWith({ status: "pending", canDecommission: false }),
		);
		expect(
			screen.queryByRole("button", { name: "Release to another NebulaCtrl…" }),
		).not.toBeInTheDocument();
		unmount();
		await renderButton(serverWith({ removal: { state: "removing" } }));
		expect(
			screen.queryByRole("button", { name: "Release to another NebulaCtrl…" }),
		).not.toBeInTheDocument();
	});

	test("the dialog says the host keeps running its containers", async () => {
		stubApi(NO_SERVICES);
		const user = userEvent.setup();
		await renderButton();
		await user.click(
			screen.getByRole("button", { name: "Release to another NebulaCtrl…" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("Release edge-1 to another NebulaCtrl?"),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText(
				"Deletes this server and its services here without touching the host; the containers keep running for the instance that adopts it.",
			),
		).toBeInTheDocument();
		expect(within(dialog).queryByText(/--uninstall/)).not.toBeInTheDocument();
		expect(
			within(dialog).getByRole("button", { name: /Hold to release server/ }),
		).toBeInTheDocument();
	});

	test("names the services the release deletes, marking the ones in production", async () => {
		stubApi({
			"GET /projects": {
				body: make("PageProject", { items: [project("prj_1", "shop")] }),
			},
			"GET /projects/:projectId/environments": {
				body: make("PageEnvironment", {
					items: [
						make("Environment", {
							id: "env_1",
							projectId: "prj_1",
							slug: "production",
							name: "production",
							isProduction: true,
						}),
					],
				}),
			},
			"GET /environments/:environmentId/services": {
				body: make("PageServiceView", {
					items: [
						service("svc_1", "web", "srv_1"),
						service("svc_2", "db", "srv_1"),
						service("svc_3", "elsewhere", "srv_2"),
					],
				}),
			},
		});
		const user = userEvent.setup();
		await renderButton();
		await user.click(
			screen.getByRole("button", { name: "Release to another NebulaCtrl…" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		const note = await within(dialog).findByText(
			/2 services are deleted from this instance/,
		);
		expect(note).toHaveTextContent(
			"db (shop, production), web (shop, production)",
		);
		expect(note).not.toHaveTextContent("elsewhere");
		expect(note).toHaveTextContent("2 are in production");
	});

	test("a release with no service placed here says nothing about services", async () => {
		stubApi(NO_SERVICES);
		const user = userEvent.setup();
		await renderButton();
		await user.click(
			screen.getByRole("button", { name: "Release to another NebulaCtrl…" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).queryByText(/deleted from this instance/),
		).not.toBeInTheDocument();
	});

	test("holding the key sends mode=release", async () => {
		const api = stubApi({
			...NO_SERVICES,
			"DELETE /servers/:serverId": {
				body: make("ServerRemoval", {
					removed: true,
					mode: "release",
					hostUntouched: true,
				}),
			},
			"GET /servers": { body: make("PageServer", { items: [] }) },
		});
		const user = userEvent.setup();
		await renderButton();
		const dialog = await openAndName(user, "Release to another NebulaCtrl…");
		await hold(
			within(dialog).getByRole("button", { name: /Hold to release server/ }),
		);
		expect(await screen.findByText("Removed edge-1")).toBeVisible();
		const del = api.calls.find((c) => c.method === "DELETE");
		expect(del?.query.get("mode")).toBe("release");
		expect(del?.query.get("confirm")).toBe("edge-1");
	});
});
