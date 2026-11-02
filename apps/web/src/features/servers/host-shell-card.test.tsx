import { afterEach, describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { HostShellInfoView } from "#/api/gen/types.gen";
import { closeHostShell, useHostShellTarget } from "#/features/terminal/store";
import { stubApi } from "#/test/api";
import { make, type Overrides, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { HostShellCard } from "./host-shell-card";
import { toServerView } from "./model";

const server = toServerView(
	make("Server", {
		id: "srv_1",
		name: "edge-1",
		status: "online",
		controlPlane: false,
		facts: make("Facts", { os: "linux" }),
	}),
);

const info = (over: Overrides<HostShellInfoView> = {}) =>
	make("HostShellInfoView", {
		activeSessions: 0,
		available: false,
		controlPlane: false,
		enabled: false,
		idleTimeoutSeconds: 900,
		maxSessions: 3,
		production: false,
		reason: "The host shell is off for this server.",
		restricted: false,
		serverId: "srv_1",
		serverName: "edge-1",
		twoFactorEnabled: true,
		...over,
	});

const GET = "GET /servers/:serverId/host-shell";
const PUT = "PUT /servers/:serverId/host-shell";

/** Shows which server's host shell panel the store was asked to open. */
function OpenedProbe() {
	const target = useHostShellTarget();
	return <p data-testid="opened-shell">{target ?? "none"}</p>;
}

function renderCard() {
	return renderWithProviders(
		<>
			<HostShellCard server={server} />
			<OpenedProbe />
		</>,
	);
}

afterEach(() => {
	closeHostShell();
});

describe("HostShellCard", () => {
	test("shows a placeholder while the setting loads", async () => {
		stubApi({ [GET]: () => new Promise<Response>(() => {}) });
		await renderCard();
		expect(screen.getByRole("status")).toBeInTheDocument();
		expect(screen.queryByText("Host shell")).not.toBeInTheDocument();
	});

	test("renders nothing for a caller who may not read it (403)", async () => {
		const api = stubApi({
			[GET]: problem(403, { title: "Forbidden" }),
		});
		await renderCard();
		await waitFor(() => expect(api.calls).toHaveLength(1));
		await waitFor(() =>
			expect(screen.queryByRole("status")).not.toBeInTheDocument(),
		);
		expect(screen.queryByText("Host shell")).not.toBeInTheDocument();
		expect(screen.queryByText(/did not load/)).not.toBeInTheDocument();
	});

	test("another load failure is reported with its reason", async () => {
		stubApi({
			[GET]: problem(500, { title: "Internal", detail: "db is down" }),
		});
		await renderCard();
		expect(
			await screen.findByText(
				/The host shell setting did not load: db is down/,
			),
		).toBeInTheDocument();
		expect(screen.queryByRole("switch")).not.toBeInTheDocument();
	});

	test("off: the switch is off and opening is disabled with the reason why", async () => {
		stubApi({ [GET]: { body: info() } });
		await renderCard();
		const toggle = await screen.findByRole("switch", {
			name: "Allow a shell on this server",
		});
		expect(toggle).not.toBeChecked();
		expect(toggle).toBeEnabled();
		expect(screen.getByText("Off")).toBeInTheDocument();
		expect(
			screen.getByText("The host shell is off for this server."),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Open host shell" }),
		).toBeDisabled();
		expect(screen.queryByRole("alert")).not.toBeInTheDocument();
	});

	test("falls back to a generic reason when none is given", async () => {
		stubApi({ [GET]: { body: info({ reason: undefined }) } });
		await renderCard();
		expect(await screen.findByText("Not available right now.")).toBeVisible();
	});

	test("available: opening a shell asks the terminal panel to open this server", async () => {
		stubApi({
			[GET]: { body: info({ enabled: true, available: true }) },
		});
		const user = userEvent.setup();
		await renderCard();
		expect(await screen.findByRole("switch")).toBeChecked();
		expect(screen.getByText("On")).toBeInTheDocument();
		expect(screen.getByTestId("opened-shell")).toHaveTextContent("none");
		const open = screen.getByRole("button", { name: "Open host shell" });
		expect(open).toBeEnabled();
		expect(screen.getByText(/Sessions are audited/)).toBeInTheDocument();
		await user.click(open);
		expect(screen.getByTestId("opened-shell")).toHaveTextContent("srv_1");
	});

	test("enabled without two-factor links to setting it up", async () => {
		stubApi({
			[GET]: {
				body: info({
					enabled: true,
					twoFactorEnabled: false,
					reason: "no 2fa",
				}),
			},
		});
		await renderCard();
		const link = await screen.findByRole("link", {
			name: "Set up two-factor authentication",
		});
		expect(link).toHaveAttribute("href", "/account/security");
	});

	test("no two-factor prompt while the shell is off", async () => {
		stubApi({ [GET]: { body: info({ twoFactorEnabled: false }) } });
		await renderCard();
		await screen.findByRole("switch");
		expect(
			screen.queryByRole("link", { name: "Set up two-factor authentication" }),
		).not.toBeInTheDocument();
	});

	test("no two-factor prompt once the caller has two-factor", async () => {
		stubApi({
			[GET]: { body: info({ enabled: true, twoFactorEnabled: true }) },
		});
		await renderCard();
		await screen.findByRole("switch");
		expect(
			screen.queryByRole("link", { name: "Set up two-factor authentication" }),
		).not.toBeInTheDocument();
	});

	test("the control-plane server warns that it is root on the whole instance", async () => {
		stubApi({ [GET]: { body: info({ controlPlane: true }) } });
		await renderCard();
		expect(
			await screen.findByText(/This server runs NebulaCtrl itself/),
		).toBeInTheDocument();
	});

	test("a restricted caller cannot flip the switch and is told who can", async () => {
		const api = stubApi({
			[GET]: { body: info({ controlPlane: true, restricted: true }) },
		});
		const user = userEvent.setup();
		await renderCard();
		const toggle = await screen.findByRole("switch");
		expect(toggle).toHaveAttribute("aria-disabled", "true");
		expect(
			screen.getByText(
				"Only the instance admin can change this on the server NebulaCtrl runs on.",
			),
		).toBeInTheDocument();
		await user.click(toggle);
		expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
		expect(api.calls.filter((c) => c.method === "PUT")).toEqual([]);
	});

	test("turning it on needs confirmation, then sends enabled: true and says so", async () => {
		let enabled = false;
		const api = stubApi({
			[GET]: () => Response.json(info({ enabled })),
			[PUT]: () => {
				enabled = true;
				return Response.json({ enabled: true });
			},
		});
		const user = userEvent.setup();
		await renderCard();
		const toggle = await screen.findByRole("switch");
		await user.click(toggle);

		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("Turn on the host shell for edge-1?"),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText(/root shell on this machine/),
		).toBeVisible();
		expect(api.calls.filter((c) => c.method === "PUT")).toEqual([]);
		await user.click(within(dialog).getByRole("button", { name: "Turn on" }));

		expect(await screen.findByText("Host shell turned on")).toBeVisible();
		const put = api.calls.find((c) => c.method === "PUT");
		expect(put?.path).toBe("/servers/srv_1/host-shell");
		expect(put?.body).toEqual({ enabled: true });
		// The info is reloaded, so the switch now reads on.
		await waitFor(() => expect(toggle).toBeChecked());
		expect(api.unmatched).toEqual([]);
	});

	test("declining the confirmation sends nothing", async () => {
		const api = stubApi({ [GET]: { body: info() } });
		const user = userEvent.setup();
		await renderCard();
		const toggle = await screen.findByRole("switch");
		await user.click(toggle);
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
		);
		expect(toggle).not.toBeChecked();
		expect(api.calls.filter((c) => c.method === "PUT")).toEqual([]);
	});

	test("turning it off is immediate and says open sessions ended", async () => {
		const api = stubApi({
			[GET]: { body: info({ enabled: true, available: true }) },
			[PUT]: { body: make("HostShellSettingsView", { enabled: false }) },
		});
		const user = userEvent.setup();
		await renderCard();
		await user.click(await screen.findByRole("switch"));

		expect(await screen.findByText("Host shell turned off")).toBeVisible();
		expect(
			screen.getByText("New sessions are refused and open ones were ended."),
		).toBeVisible();
		expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
			enabled: false,
		});
	});

	test("a failed change shows the server's message and leaves the switch alone", async () => {
		stubApi({
			[GET]: { body: info({ enabled: true, available: true }) },
			[PUT]: problem(500, { title: "Internal", detail: "agent unreachable" }),
		});
		const user = userEvent.setup();
		await renderCard();
		const toggle = await screen.findByRole("switch");
		await user.click(toggle);
		expect(await screen.findByText(/agent unreachable/)).toBeInTheDocument();
		expect(screen.queryByText("Host shell turned off")).not.toBeInTheDocument();
		expect(toggle).toBeChecked();
	});
});
