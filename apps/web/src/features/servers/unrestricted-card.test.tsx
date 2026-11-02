import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { UnrestrictedSettings } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { toServerView } from "./model";
import { UnrestrictedCard } from "./unrestricted-card";

const server = toServerView(
	make("Server", {
		id: "srv_1",
		name: "edge-1",
		status: "online",
		controlPlane: false,
		facts: make("Facts", { os: "linux" }),
	}),
);

const settings = (
	over: Partial<UnrestrictedSettings> = {},
): UnrestrictedSettings => ({
	available: true,
	composeVersion: "2.29.0",
	dockerServices: 0,
	stacks: 0,
	unrestricted: false,
	...over,
});

const PATH = "GET /servers/:serverId/unrestricted";

describe("UnrestrictedCard", () => {
	test("shows a loading placeholder until the setting arrives", async () => {
		stubApi({ [PATH]: () => new Promise<Response>(() => {}) });
		await renderWithProviders(<UnrestrictedCard server={server} />);
		expect(screen.getByRole("status")).toBeInTheDocument();
		expect(screen.queryByRole("switch")).not.toBeInTheDocument();
	});

	test("says the setting did not load, with the server's reason", async () => {
		stubApi({
			[PATH]: problem(500, { title: "Internal", detail: "db is down" }),
		});
		await renderWithProviders(<UnrestrictedCard server={server} />);
		expect(
			await screen.findByText(/The Unrestricted settings did not load/),
		).toBeInTheDocument();
		expect(screen.queryByRole("switch")).not.toBeInTheDocument();
	});

	test("reflects the current state and counts what depends on the switch", async () => {
		stubApi({
			[PATH]: { body: settings({ stacks: 1, dockerServices: 2 }) },
		});
		await renderWithProviders(<UnrestrictedCard server={server} />);
		const toggle = await screen.findByRole("switch", { name: "Unrestricted" });
		expect(toggle).not.toBeChecked();
		expect(screen.getByText("Off")).toBeInTheDocument();
		// Singular for one, plural for several.
		expect(
			screen.getByText(/Live here: 1 Compose stack and 2 services/),
		).toBeInTheDocument();
	});

	test("shows an enabled server as on", async () => {
		stubApi({ [PATH]: { body: settings({ unrestricted: true }) } });
		await renderWithProviders(<UnrestrictedCard server={server} />);
		const toggle = await screen.findByRole("switch", { name: "Unrestricted" });
		expect(toggle).toBeChecked();
		expect(screen.getByText("On")).toBeInTheDocument();
	});

	test("the allow-list is collapsed until asked for", async () => {
		stubApi({ [PATH]: { body: settings() } });
		const user = userEvent.setup();
		await renderWithProviders(<UnrestrictedCard server={server} />);
		const disclosure = await screen.findByRole("button", {
			name: "What the allow-list blocks",
		});
		expect(disclosure).toHaveAttribute("aria-expanded", "false");
		await user.click(disclosure);
		expect(disclosure).toHaveAttribute("aria-expanded", "true");
		expect(
			screen.getByText("Privileged mode and the host PID namespace"),
		).toBeVisible();
		await user.click(disclosure);
		expect(disclosure).toHaveAttribute("aria-expanded", "false");
	});

	test("turning it on asks for the server's name, then sends it with the request", async () => {
		const api = stubApi({
			[PATH]: { body: settings({ stacks: 2, dockerServices: 1 }) },
			"PUT /servers/:serverId/unrestricted": {
				body: settings({ unrestricted: true, stacks: 2, dockerServices: 1 }),
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<UnrestrictedCard server={server} />);
		await user.click(await screen.findByRole("switch"));

		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("Turn on Unrestricted for edge-1?"),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText(/2 Compose stacks and 1 service whose Docker/),
		).toBeInTheDocument();
		const confirm = within(dialog).getByRole("button", {
			name: "Turn on Unrestricted",
		});
		expect(confirm).toBeDisabled();
		// Nothing was sent yet.
		expect(api.calls.filter((c) => c.method === "PUT")).toEqual([]);

		await user.type(within(dialog).getByRole("textbox"), "edge-1");
		expect(confirm).toBeEnabled();
		await user.click(confirm);

		expect(await screen.findByText("Unrestricted turned on")).toBeVisible();
		const put = api.calls.find((c) => c.method === "PUT");
		expect(put?.path).toBe("/servers/srv_1/unrestricted");
		expect(put?.body).toEqual({ unrestricted: true, confirm: "edge-1" });
		expect(api.unmatched).toEqual([]);
	});

	test("a wrong name keeps the confirm key locked", async () => {
		const api = stubApi({ [PATH]: { body: settings() } });
		const user = userEvent.setup();
		await renderWithProviders(<UnrestrictedCard server={server} />);
		await user.click(await screen.findByRole("switch"));
		const dialog = await screen.findByRole("alertdialog");
		await user.type(within(dialog).getByRole("textbox"), "edge-");
		expect(
			within(dialog).getByRole("button", { name: "Turn on Unrestricted" }),
		).toBeDisabled();
		expect(api.calls.filter((c) => c.method === "PUT")).toEqual([]);
	});

	test("cancelling the dialog changes nothing", async () => {
		const api = stubApi({ [PATH]: { body: settings() } });
		const user = userEvent.setup();
		await renderWithProviders(<UnrestrictedCard server={server} />);
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

	test("turning it off needs no confirmation and sends no name", async () => {
		const api = stubApi({
			[PATH]: { body: settings({ unrestricted: true }) },
			"PUT /servers/:serverId/unrestricted": {
				body: settings({ unrestricted: false }),
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<UnrestrictedCard server={server} />);
		await user.click(await screen.findByRole("switch"));

		expect(await screen.findByText("Unrestricted turned off")).toBeVisible();
		expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
		const put = api.calls.find((c) => c.method === "PUT");
		expect(put?.body).toEqual({ unrestricted: false });
	});

	test("reloads the setting after a change", async () => {
		let unrestricted = true;
		const api = stubApi({
			[PATH]: () => Response.json(settings({ unrestricted })),
			"PUT /servers/:serverId/unrestricted": () => {
				unrestricted = false;
				return Response.json({});
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<UnrestrictedCard server={server} />);
		const toggle = await screen.findByRole("switch");
		expect(toggle).toBeChecked();
		await user.click(toggle);
		await waitFor(() => expect(toggle).not.toBeChecked());
		expect(api.calls.filter((c) => c.method === "GET")).toHaveLength(2);
	});

	test("a 403 says only an instance admin can change it", async () => {
		stubApi({
			[PATH]: { body: settings({ unrestricted: true }) },
			"PUT /servers/:serverId/unrestricted": problem(403, {
				title: "Forbidden",
				detail: "nope",
			}),
		});
		const user = userEvent.setup();
		await renderWithProviders(<UnrestrictedCard server={server} />);
		await user.click(await screen.findByRole("switch"));
		expect(
			await screen.findByText(
				"Only an instance admin can change this setting.",
			),
		).toBeInTheDocument();
		expect(
			screen.queryByText("Unrestricted turned off"),
		).not.toBeInTheDocument();
	});

	test("another failure shows the server's message", async () => {
		stubApi({
			[PATH]: { body: settings({ unrestricted: true }) },
			"PUT /servers/:serverId/unrestricted": problem(500, {
				title: "Internal",
				detail: "agent unreachable",
			}),
		});
		const user = userEvent.setup();
		await renderWithProviders(<UnrestrictedCard server={server} />);
		await user.click(await screen.findByRole("switch"));
		expect(await screen.findByText(/agent unreachable/)).toBeInTheDocument();
		expect(
			screen.queryByText("Only an instance admin can change this setting."),
		).not.toBeInTheDocument();
	});
});
