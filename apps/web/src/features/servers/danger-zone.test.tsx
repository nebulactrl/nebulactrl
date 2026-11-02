import { describe, expect, spyOn, test } from "bun:test";
import {
	act,
	fireEvent,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
	Server as ServerWire,
	UnrestrictedSettings,
} from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, type Overrides, problem } from "#/test/factories";
import { renderWithProviders, testQueryClient } from "#/test/render";
import { DangerZone } from "./danger-zone";
import { type ServerView, toServerView } from "./model";
import { serverViewQuery, serverViewsQuery } from "./queries";
import { ServerProvider } from "./server-context";

const serverWith = (over: Overrides<ServerWire> = {}) =>
	toServerView(
		make("Server", {
			id: "srv_1",
			name: "edge-1",
			status: "online",
			canDecommission: true,
			controlPlane: false,
			drainState: "none",
			drainMessage: "",
			facts: make("Facts", { os: "linux" }),
			...over,
		}),
	);

const unrestricted: UnrestrictedSettings = {
	available: true,
	composeVersion: "2.29.1",
	dockerServices: 0,
	stacks: 0,
	unrestricted: false,
};

const BASE = {
	"GET /servers/:serverId/unrestricted": { body: unrestricted },
	"GET /projects": { body: make("PageProject", { items: [] }) },
};

function renderZone(
	server: ServerView,
	openDrain: () => void = () => {},
	queryClient = testQueryClient(),
) {
	return renderWithProviders(
		<ServerProvider value={{ server, openDrain }}>
			<DangerZone server={server} />
		</ServerProvider>,
		{ queryClient },
	);
}

const HOLD_MS = 1300;

/** Presses a hold key with its 1.3 s timer shortened to the next tick (fake clocks would strand react-query's timers) and lets it run. */
async function hold(button: HTMLElement) {
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
	const tick = Promise.withResolvers<void>();
	window.setTimeout(tick.resolve, 0);
	await act(async () => {
		await tick.promise;
	});
}

describe("DangerZone: the cards", () => {
	test("offers Unrestricted, drain, revoke and remove, in that order", async () => {
		stubApi(BASE);
		await renderZone(serverWith());
		await screen.findByRole("switch");
		const titles = [
			// The card title comes before the row label of the same name.
			screen.getAllByText("Unrestricted")[0],
			screen.getByText("Drain this server"),
			screen.getByText("Revoke the agent"),
			screen.getByText("Remove this server"),
		] as HTMLElement[];
		titles.slice(1).forEach((title, i) => {
			const before = titles[i] as HTMLElement;
			expect(
				before.compareDocumentPosition(title) &
					Node.DOCUMENT_POSITION_FOLLOWING,
			).toBeTruthy();
		});
	});

	test("the control-plane server has no remove card", async () => {
		stubApi(BASE);
		await renderZone(serverWith({ controlPlane: true }));
		await screen.findByText("Revoke the agent");
		expect(screen.queryByText("Remove this server")).not.toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Remove server…" }),
		).not.toBeInTheDocument();
	});

	test("the remove card carries the remove key", async () => {
		stubApi(BASE);
		await renderZone(serverWith());
		expect(
			await screen.findByRole("button", { name: "Remove server…" }),
		).toBeEnabled();
	});
});

describe("DangerZone: drain", () => {
	test("an idle server can start a drain, which opens the drain sheet", async () => {
		stubApi(BASE);
		let opened = 0;
		const user = userEvent.setup();
		await renderZone(serverWith(), () => {
			opened += 1;
		});
		expect(
			screen.getByText(/Evacuate this host before maintenance/),
		).toBeVisible();
		await user.click(screen.getByRole("button", { name: "Drain server…" }));
		expect(opened).toBe(1);
	});

	test("a server that never connected cannot be drained", async () => {
		stubApi(BASE);
		await renderZone(serverWith({ status: "pending" }));
		expect(
			screen.getByRole("button", { name: "Drain server…" }),
		).toBeDisabled();
	});

	test("a revoked server cannot be drained", async () => {
		stubApi(BASE);
		await renderZone(serverWith({ status: "revoked" }));
		expect(
			screen.getByRole("button", { name: "Drain server…" }),
		).toBeDisabled();
	});

	test("while draining the key follows the progress", async () => {
		stubApi(BASE);
		let opened = 0;
		const user = userEvent.setup();
		await renderZone(serverWith({ drainState: "draining" }), () => {
			opened += 1;
		});
		expect(screen.getByText(/This server is being drained/)).toBeVisible();
		const key = screen.getByRole("button", { name: "View drain progress" });
		expect(key).toBeEnabled();
		await user.click(key);
		expect(opened).toBe(1);
		expect(screen.getByText("Draining")).toBeInTheDocument();
	});

	test("a drained server says nothing runs there and reopens the drain", async () => {
		stubApi(BASE);
		await renderZone(serverWith({ drainState: "drained" }));
		expect(
			screen.getByText(/nothing runs here and it accepts no new services/),
		).toBeVisible();
		expect(screen.getByRole("button", { name: "Open drain" })).toBeEnabled();
		expect(screen.getByText("Drained")).toBeInTheDocument();
	});

	test("a failed drain quotes why it stopped", async () => {
		stubApi(BASE);
		await renderZone(
			serverWith({ drainState: "failed", drainMessage: "api would not stop" }),
		);
		expect(
			screen.getByText(/The last drain stopped early: api would not stop/),
		).toBeVisible();
		expect(screen.getByRole("button", { name: "Open drain" })).toBeEnabled();
	});

	test("a failed drain without a message still reads as a sentence", async () => {
		stubApi(BASE);
		await renderZone(serverWith({ drainState: "failed", drainMessage: "" }));
		expect(
			screen.getByText(/The last drain stopped early\. Retry it, or undrain/),
		).toBeVisible();
	});
});

describe("DangerZone: revoke", () => {
	test("a revoked server shows the revoked text and its key is off", async () => {
		stubApi(BASE);
		await renderZone(serverWith({ status: "revoked" }));
		expect(
			screen.getByText(/agent identity is revoked\. Enrol a new server/),
		).toBeVisible();
		expect(screen.getByRole("button", { name: "Revoke agent" })).toBeDisabled();
	});

	test("asks first, then revokes by holding, toasts and refreshes the server", async () => {
		const queryClient = testQueryClient();
		const server = serverWith();
		queryClient.setQueryData(serverViewQuery(server.id).queryKey, server);
		queryClient.setQueryData(serverViewsQuery().queryKey, [server]);
		const revoked = make("Server", {
			id: "srv_1",
			name: "edge-1",
			status: "revoked",
		});
		const api = stubApi({
			...BASE,
			"POST /servers/:serverId/revoke": { body: revoked },
			"GET /servers/:serverId": { body: revoked },
			"GET /servers": { body: make("PageServer", { items: [] }) },
		});
		const user = userEvent.setup();
		await renderZone(server, () => {}, queryClient);
		await user.click(screen.getByRole("button", { name: "Revoke agent" }));

		const dialog = await screen.findByRole("alertdialog");
		expect(within(dialog).getByText("Revoke edge-1?")).toBeInTheDocument();
		expect(
			within(dialog).getByText(/its identity can never connect again/),
		).toBeInTheDocument();
		expect(api.calls.filter((c) => c.method === "POST")).toEqual([]);

		await hold(
			within(dialog).getByRole("button", { name: /Hold to revoke agent/ }),
		);

		expect(await screen.findByText("Revoked edge-1")).toBeVisible();
		expect(
			screen.getByText(
				"The agent was disconnected. Containers on the host keep running.",
			),
		).toBeVisible();
		const post = api.calls.find((c) => c.method === "POST");
		expect(post?.path).toBe("/servers/srv_1/revoke");
		await waitFor(() => {
			expect(
				queryClient.getQueryState(serverViewQuery(server.id).queryKey)
					?.isInvalidated,
			).toBe(true);
			expect(
				queryClient.getQueryState(serverViewsQuery().queryKey)?.isInvalidated,
			).toBe(true);
		});
	});

	test("cancelling revokes nothing", async () => {
		const api = stubApi(BASE);
		const user = userEvent.setup();
		await renderZone(serverWith());
		await user.click(screen.getByRole("button", { name: "Revoke agent" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
		);
		expect(api.calls.filter((c) => c.method === "POST")).toEqual([]);
	});

	test("a failed revoke keeps the dialog open and shows the reason", async () => {
		stubApi({
			...BASE,
			"POST /servers/:serverId/revoke": problem(500, {
				title: "Internal",
				detail: "store unavailable",
			}),
		});
		const user = userEvent.setup();
		await renderZone(serverWith());
		await user.click(screen.getByRole("button", { name: "Revoke agent" }));
		const dialog = await screen.findByRole("alertdialog");
		await hold(
			within(dialog).getByRole("button", { name: /Hold to revoke agent/ }),
		);
		expect(
			await within(dialog).findByText("store unavailable"),
		).toBeInTheDocument();
		expect(screen.queryByText("Revoked edge-1")).not.toBeInTheDocument();
		// The card shows it too, so it is still there once the dialog is closed.
		expect(screen.getAllByText("store unavailable").length).toBeGreaterThan(1);
	});
});
