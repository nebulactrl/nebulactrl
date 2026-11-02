import { afterEach, beforeEach, describe, expect, spyOn, test } from "bun:test";
import {
	act,
	renderHook,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import { Terminal } from "@xterm/xterm";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { HostShellSheet } from "./host-shell-sheet";
import { closeHostShell, openHostShell, useHostShellTarget } from "./store";
import {
	type FakeWebSocket,
	holdToConfirm,
	hostShellInfo,
	installFakeWebSocket,
} from "./test-support";

let sockets: FakeWebSocket[];
let restoreSocket: () => void;

beforeEach(() => {
	({ sockets, restore: restoreSocket } = installFakeWebSocket());
});

afterEach(() => {
	act(() => closeHostShell());
	restoreSocket();
});

async function openPanel(info = hostShellInfo()) {
	const api = stubApi({ "GET /servers/:serverId/host-shell": { body: info } });
	const user = userEvent.setup();
	await renderWithProviders(<HostShellSheet />);
	act(() => openHostShell("srv-1"));
	return { api, user };
}

async function startSession(user: UserEvent) {
	await user.click(
		await screen.findByRole("button", { name: "Open host shell" }),
	);
	await waitFor(() => expect(sockets).toHaveLength(1));
	const socket = sockets[0] as FakeWebSocket;
	act(() => socket.open());
	return socket;
}

const opened = {
	type: "opened",
	replica: 0,
	container: "host",
	command: ["/bin/bash", "-l"],
};

describe("HostShellSheet setup", () => {
	test("renders nothing until a host shell is opened", async () => {
		stubApi({});
		await renderWithProviders(<HostShellSheet />);
		expect(screen.queryByRole("dialog")).toBeNull();
	});

	test("titles the panel with the server and warns what a host shell is", async () => {
		const { api } = await openPanel();
		const dialog = await screen.findByRole("dialog");
		expect(
			await within(dialog).findByText("Host shell · edge-1"),
		).toBeInTheDocument();
		expect(within(dialog).getByText("Not connected")).toBeInTheDocument();
		expect(
			within(dialog).getByText("This is a shell on the server itself"),
		).toBeInTheDocument();
		expect(dialog).toHaveTextContent("You can have 2 open at once.");
		expect(
			within(dialog).getByRole("button", { name: "Open host shell" }),
		).toBeEnabled();
		expect(api.calls[0]?.path).toBe("/servers/srv-1/host-shell");
	});

	test("the title is just 'Host shell' while the server's name is unknown", async () => {
		stubApi({
			"GET /servers/:serverId/host-shell": () =>
				new Promise<Response>(() => {}),
		});
		await renderWithProviders(<HostShellSheet />);
		act(() => openHostShell("srv-1"));
		const dialog = await screen.findByRole("dialog");
		expect(within(dialog).getByText("Host shell")).toBeInTheDocument();
		expect(within(dialog).getByRole("status")).toBeInTheDocument();
		expect(
			within(dialog).queryByRole("button", { name: "Open host shell" }),
		).toBeNull();
	});

	test("a 403 shows the host shell as unavailable with the server's reason", async () => {
		stubApi({
			"GET /servers/:serverId/host-shell": problem(403, {
				title: "Forbidden",
				detail: "Only owners and admins.",
			}),
		});
		await renderWithProviders(<HostShellSheet />);
		act(() => openHostShell("srv-1"));
		expect(
			await screen.findByText("The host shell is not available"),
		).toBeInTheDocument();
		expect(screen.getByText("Only owners and admins.")).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Open host shell" }),
		).toBeNull();
	});

	test("an unavailable host shell gives the reason and cannot be opened", async () => {
		await openPanel(
			hostShellInfo({ available: false, reason: "Turn on two-factor first." }),
		);
		expect(
			await screen.findByText("Turn on two-factor first."),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Open host shell" }),
		).toBeDisabled();
	});

	test("without a reason it still says it cannot be opened", async () => {
		await openPanel(hostShellInfo({ available: false }));
		expect(
			await screen.findByText("A host shell cannot be opened right now."),
		).toBeInTheDocument();
	});

	test("at the session limit, says so and cannot be opened", async () => {
		await openPanel(hostShellInfo({ activeSessions: 2, maxSessions: 2 }));
		expect(
			await screen.findByText(/You already have 2 host shells open/),
		).toBeInTheDocument();
		expect(screen.getByText(/\(2 open now\)/)).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Open host shell" }),
		).toBeDisabled();
	});
});

describe("HostShellSheet sessions", () => {
	test("opens the server's shell socket with a command-less open message", async () => {
		const { user } = await openPanel();
		const socket = await startSession(user);
		expect(socket.url).toBe("ws://localhost/api/v1/servers/srv-1/shell/ws");
		expect(socket.messages[0]).toMatchObject({
			type: "open",
			replica: 0,
			confirmProduction: false,
		});
		expect(socket.messages[0]).not.toHaveProperty("command");
		expect(screen.getByText("Connecting…")).toBeInTheDocument();
		expect(screen.getByText("Connecting")).toBeInTheDocument();
	});

	test("once open, says it is connected to the server", async () => {
		const { user } = await openPanel();
		const socket = await startSession(user);
		act(() => socket.serve(opened));
		expect(await screen.findByText("Connected to edge-1")).toBeInTheDocument();
		expect(screen.getByText("Connected")).toBeInTheDocument();
	});

	test("an idle warning shows the countdown", async () => {
		const { user } = await openPanel();
		const socket = await startSession(user);
		act(() => socket.serve(opened));
		act(() => socket.serve({ type: "idle_warning", secondsLeft: 120 }));
		expect(
			await screen.findByText(/this session closes in 2 minutes/),
		).toBeInTheDocument();
	});

	test("a shell that exits ends the session; New session returns to setup", async () => {
		const { user } = await openPanel();
		const socket = await startSession(user);
		act(() => socket.serve(opened));
		act(() => socket.serve({ type: "exit", exitCode: 0 }));
		expect(await screen.findByText("The shell exited.")).toBeInTheDocument();
		expect(screen.getByText("Ended")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "New session" }));
		expect(
			await screen.findByRole("button", { name: "Open host shell" }),
		).toBeInTheDocument();
	});

	test("a server refusal is shown with its title", async () => {
		const { user } = await openPanel();
		const socket = await startSession(user);
		act(() =>
			socket.serve({
				type: "error",
				code: "two_factor_required",
				message: "Enable 2FA.",
			}),
		);
		expect(
			await screen.findByText(
				"Two-factor authentication is required: Enable 2FA.",
			),
		).toBeInTheDocument();
		expect(screen.getByText("Failed")).toBeInTheDocument();
	});

	test("an admin switching the host shell off ends the session with an explanation", async () => {
		const { user } = await openPanel();
		const socket = await startSession(user);
		act(() => socket.serve(opened));
		act(() => socket.serve({ type: "closed", reason: "host_shell_disabled" }));
		expect(
			await screen.findByText(
				"An admin turned the host shell off, so the session ended.",
			),
		).toBeInTheDocument();
	});

	test("a dropped connection is reported as lost", async () => {
		const { user } = await openPanel();
		const socket = await startSession(user);
		act(() => socket.serve(opened));
		act(() => socket.drop());
		expect(
			await screen.findByText(
				"Connection lost: The connection to the control plane was lost.",
			),
		).toBeInTheDocument();
	});

	test("End session closes the socket and returns to setup", async () => {
		const { user } = await openPanel();
		const socket = await startSession(user);
		act(() => socket.serve(opened));
		await user.click(screen.getByRole("button", { name: "End session" }));
		expect(
			await screen.findByRole("button", { name: "Open host shell" }),
		).toBeInTheDocument();
		expect(socket.messages.at(-1)).toEqual({ type: "close" });
		expect(socket.closedWith).toBe(1000);
	});

	test("a different server starts from setup again", async () => {
		const { user } = await openPanel();
		const socket = await startSession(user);
		act(() => socket.serve(opened));
		act(() => openHostShell("srv-2"));
		expect(
			await screen.findByRole("button", { name: "Open host shell" }),
		).toBeInTheDocument();
		expect(socket.closedWith).toBe(1000);
	});
});

describe("HostShellSheet copy selection", () => {
	test("without a selection it asks for one", async () => {
		const { user } = await openPanel();
		await startSession(user);
		await user.click(screen.getByRole("button", { name: "Copy selection" }));
		expect(await screen.findByText("Nothing selected")).toBeInTheDocument();
	});

	test("copies the selected text and reports its length", async () => {
		const selection = spyOn(Terminal.prototype, "getSelection").mockReturnValue(
			"uptime",
		);
		try {
			const { user } = await openPanel();
			const copied: string[] = [];
			Object.defineProperty(navigator, "clipboard", {
				configurable: true,
				value: {
					writeText: async (text: string) => {
						copied.push(text);
					},
				},
			});
			await startSession(user);
			await user.click(screen.getByRole("button", { name: "Copy selection" }));
			expect(await screen.findByText("6 characters")).toBeInTheDocument();
			expect(copied).toEqual(["uptime"]);
		} finally {
			selection.mockRestore();
		}
	});

	test("a blocked clipboard is reported", async () => {
		const selection = spyOn(Terminal.prototype, "getSelection").mockReturnValue(
			"x",
		);
		try {
			const { user } = await openPanel();
			Object.defineProperty(navigator, "clipboard", {
				configurable: true,
				value: { writeText: () => Promise.reject(new Error("denied")) },
			});
			await startSession(user);
			await user.click(screen.getByRole("button", { name: "Copy selection" }));
			expect(await screen.findByText("Could not copy")).toBeInTheDocument();
		} finally {
			selection.mockRestore();
		}
	});
});

describe("HostShellSheet production", () => {
	const production = () => hostShellInfo({ production: true });

	test("cancelling the production confirmation opens nothing", async () => {
		const { user } = await openPanel(production());
		await user.click(
			await screen.findByRole("button", { name: "Open in production…" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(dialog).toHaveTextContent("Open a shell on edge-1 (production)?");
		expect(dialog).toHaveTextContent("root shell on the machine itself");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
		expect(sockets).toEqual([]);
	});

	test("confirming sends confirmProduction with the open message", async () => {
		const { user } = await openPanel(production());
		await user.click(
			await screen.findByRole("button", { name: "Open in production…" }),
		);
		await screen.findByRole("alertdialog");
		holdToConfirm(/Hold to open host shell/i);
		await waitFor(() => expect(sockets).toHaveLength(1));
		const socket = sockets[0] as FakeWebSocket;
		act(() => socket.open());
		expect(socket.messages[0]).toMatchObject({
			type: "open",
			confirmProduction: true,
		});
	});
});

describe("HostShellSheet closing", () => {
	test("closing the setup panel needs no confirmation", async () => {
		const { user } = await openPanel();
		await screen.findByRole("button", { name: "Open host shell" });
		const target = renderHook(() => useHostShellTarget());
		await user.click(screen.getByRole("button", { name: "Close" }));
		await waitFor(() => expect(target.result.current).toBeNull());
		expect(screen.queryByRole("alertdialog")).toBeNull();
	});

	test("closing a live session asks first; Keep open leaves it running", async () => {
		const { user } = await openPanel();
		const socket = await startSession(user);
		act(() => socket.serve(opened));
		const target = renderHook(() => useHostShellTarget());
		await user.click(screen.getByRole("button", { name: "Close" }));
		const dialog = await screen.findByRole("alertdialog");
		expect(dialog).toHaveTextContent("End this host shell session?");
		await user.click(within(dialog).getByRole("button", { name: "Keep open" }));
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
		expect(target.result.current).toBe("srv-1");
		expect(socket.closedWith).toBeUndefined();
	});

	test("ending the session from the confirmation closes the panel and the socket", async () => {
		const { user } = await openPanel();
		const socket = await startSession(user);
		act(() => socket.serve(opened));
		const target = renderHook(() => useHostShellTarget());
		await user.click(screen.getByRole("button", { name: "Close" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "End session" }),
		);
		await waitFor(() => expect(target.result.current).toBeNull());
		await waitFor(() => expect(socket.closedWith).toBe(1000));
	});
});
