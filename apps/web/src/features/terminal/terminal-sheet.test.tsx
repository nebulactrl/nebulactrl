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
import { type ApiRoutes, stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import {
	closeFiles,
	closeShell,
	openShell,
	useFilesProcess,
	useFilesReplica,
	useFilesTarget,
	useShellTarget,
} from "./store";
import { TerminalSheet } from "./terminal-sheet";
import {
	type FakeWebSocket,
	holdToConfirm,
	installFakeWebSocket,
	serviceView,
	terminalInfo,
} from "./test-support";

let sockets: FakeWebSocket[];
let restoreSocket: () => void;

beforeEach(() => {
	({ sockets, restore: restoreSocket } = installFakeWebSocket());
});

afterEach(() => {
	act(() => {
		closeShell();
		closeFiles();
	});
	restoreSocket();
});

const replica = (
	index: number,
	over: { process?: string; running?: boolean; state?: string } = {},
) => ({
	index,
	process: over.process ?? "",
	running: over.running ?? true,
	state: over.state ?? "running",
});

/** Opens the panel for svc-1 with the given info and returns the stub. */
async function openPanel(
	info = terminalInfo(),
	extra: ApiRoutes = {},
	options?: Parameters<typeof openShell>[1],
	service = serviceView(),
) {
	const api = stubApi({
		"GET /services/:serviceId": { body: service },
		"GET /services/:serviceId/terminal": { body: info },
		...extra,
	});
	const user = userEvent.setup();
	await renderWithProviders(<TerminalSheet />);
	act(() => openShell("svc-1", options));
	return { api, user };
}

/** Starts a session from the setup form and waits for the socket. */
async function startSession(user: UserEvent) {
	await user.click(
		await screen.findByRole("button", { name: "Open terminal" }),
	);
	await waitFor(() => expect(sockets).toHaveLength(1));
	const socket = sockets[0] as FakeWebSocket;
	act(() => socket.open());
	return socket;
}

const opened = {
	type: "opened",
	replica: 0,
	container: "nebula-api-1",
	command: ["bash"],
};

describe("TerminalSheet setup", () => {
	test("renders nothing until a shell is opened", async () => {
		stubApi({});
		await renderWithProviders(<TerminalSheet />);
		expect(screen.queryByRole("dialog")).toBeNull();
	});

	test("opens with the service's name, a Not connected pill and the setup form", async () => {
		await openPanel();
		const dialog = await screen.findByRole("dialog");
		expect(await within(dialog).findByText("Shell · api")).toBeInTheDocument();
		expect(within(dialog).getByText("Not connected")).toBeInTheDocument();
		expect(
			await within(dialog).findByRole("button", { name: "Open terminal" }),
		).toBeEnabled();
		expect(within(dialog).getByLabelText("Command")).toHaveValue("");
		expect(within(dialog).getByText(/Only running replicas/)).toHaveTextContent(
			"On edge-1.",
		);
		expect(dialog).toHaveTextContent("You can have 4 open at once.");
	});

	test("shows a skeleton while the terminal info loads", async () => {
		stubApi({
			"GET /services/:serviceId": { body: serviceView() },
			"GET /services/:serviceId/terminal": () =>
				new Promise<Response>(() => {}),
		});
		await renderWithProviders(<TerminalSheet />);
		act(() => openShell("svc-1"));
		const dialog = await screen.findByRole("dialog");
		expect(within(dialog).getByRole("status")).toBeInTheDocument();
		expect(
			within(dialog).queryByRole("button", { name: "Open terminal" }),
		).toBeNull();
	});

	test("a failed info call shows the reason and Retry asks again", async () => {
		let calls = 0;
		const user = userEvent.setup();
		const api = stubApi({
			"GET /services/:serviceId": { body: serviceView() },
			"GET /services/:serviceId/terminal": () => {
				calls += 1;
				return calls === 1
					? Response.json(
							{ title: "Bad Gateway", detail: "The agent is unreachable." },
							{ status: 502 },
						)
					: Response.json(terminalInfo());
			},
		});
		await renderWithProviders(<TerminalSheet />);
		act(() => openShell("svc-1"));
		expect(
			await screen.findByText("The terminal is not available"),
		).toBeInTheDocument();
		expect(screen.getByText("The agent is unreachable.")).toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: /Retry|Try again/i }));
		expect(
			await screen.findByRole("button", { name: "Open terminal" }),
		).toBeEnabled();
		expect(
			api.calls.filter((c) => c.path === "/services/svc-1/terminal"),
		).toHaveLength(2);
	});

	test("an unavailable terminal gives the reason and cannot be opened", async () => {
		await openPanel(
			terminalInfo({ available: false, reason: "The server is offline." }),
		);
		expect(
			await screen.findByText("The server is offline."),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Open terminal" }),
		).toBeDisabled();
	});

	test("an unavailable terminal without a reason still explains itself", async () => {
		await openPanel(terminalInfo({ available: false }));
		expect(
			await screen.findByText("A terminal cannot be opened right now."),
		).toBeInTheDocument();
	});

	test("at the session limit, says so and cannot be opened", async () => {
		await openPanel(terminalInfo({ activeSessions: 4, maxSessions: 4 }));
		expect(
			await screen.findByText(/You already have 4 terminals open/),
		).toBeInTheDocument();
		expect(screen.getByText(/\(4 open now\)/)).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Open terminal" }),
		).toBeDisabled();
	});

	test("with no running replica there is nothing to open", async () => {
		await openPanel(
			terminalInfo({
				replicas: [replica(0, { running: false, state: "exited" })],
			}),
		);
		const button = await screen.findByRole("button", { name: "Open terminal" });
		expect(button).toBeDisabled();
	});

	test("a service with processes picks the process first and the replica inside it", async () => {
		const { user } = await openPanel(
			terminalInfo({
				replicas: [
					replica(0, { process: "web" }),
					replica(0, { process: "worker" }),
					replica(1, { process: "worker", running: false, state: "exited" }),
				],
			}),
			{},
			{ process: "worker" },
		);
		const processSelect = await screen.findByRole("combobox", {
			name: "Process",
		});
		expect(processSelect).toHaveTextContent("worker");
		const replicaSelect = screen.getByRole("combobox", { name: "Replica" });
		expect(replicaSelect).toHaveTextContent("Replica 0 · running");

		await user.click(processSelect);
		await user.click(await screen.findByRole("option", { name: "web" }));
		await waitFor(() => expect(processSelect).toHaveTextContent("web"));
		expect(screen.getByRole("combobox", { name: "Replica" })).toHaveTextContent(
			"Replica 0 · running",
		);
	});

	test("a stack lists its components by name", async () => {
		await openPanel(
			terminalInfo({
				replicas: [replica(0), replica(1, { running: false, state: "exited" })],
			}),
			{},
			{ replica: 0 },
			serviceView({
				kind: "stack",
				config: {
					healthcheck: { type: "http" },
					stack: {
						components: [
							{ name: "web", index: 0 },
							{ name: "db", index: 1 },
						],
					},
				},
			}),
		);
		const select = await screen.findByRole("combobox", { name: "Component" });
		await waitFor(() => expect(select).toHaveTextContent("web · running"));
	});
});

describe("TerminalSheet sessions", () => {
	test("starting opens the socket on the service's terminal URL with the chosen options", async () => {
		const { user } = await openPanel(terminalInfo(), {}, { replica: 0 });
		await user.type(
			await screen.findByLabelText("Command"),
			"psql -U postgres",
		);
		const socket = await startSession(user);

		expect(socket.url).toBe("ws://localhost/api/v1/services/svc-1/terminal/ws");
		expect(socket.messages[0]).toMatchObject({
			type: "open",
			replica: 0,
			command: ["psql", "-U", "postgres"],
			confirmProduction: false,
		});
		expect(socket.messages[0]).not.toHaveProperty("process");
		expect(screen.getByText("Connecting…")).toBeInTheDocument();
		expect(screen.getByText("Connecting")).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "End session" }),
		).toBeInTheDocument();
	});

	test("a service with processes puts the process in the socket URL and open message", async () => {
		const { user } = await openPanel(
			terminalInfo({
				replicas: [
					replica(0, { process: "worker" }),
					replica(1, { process: "worker" }),
				],
			}),
			{},
			{ process: "worker", replica: 1 },
		);
		const socket = await startSession(user);
		expect(socket.url).toBe(
			"ws://localhost/api/v1/services/svc-1/terminal/ws?process=worker",
		);
		expect(socket.messages[0]).toMatchObject({
			process: "worker",
			replica: 1,
		});
		act(() => socket.serve({ ...opened, replica: 1 }));
		expect(
			await screen.findByText("Connected to worker · replica 1 · nebula-api-1"),
		).toBeInTheDocument();
	});

	test("once the server opens the session, the panel says where it is connected", async () => {
		const { user } = await openPanel();
		const socket = await startSession(user);
		act(() => socket.serve(opened));
		expect(
			await screen.findByText("Connected to replica 0 · nebula-api-1"),
		).toBeInTheDocument();
		expect(screen.getByText("Connected")).toBeInTheDocument();
		expect(
			screen.getByRole("application", { name: "Terminal" }),
		).toHaveAttribute("data-phase", "live");
	});

	test("an idle warning shows the countdown", async () => {
		const { user } = await openPanel();
		const socket = await startSession(user);
		act(() => socket.serve(opened));
		act(() => socket.serve({ type: "idle_warning", secondsLeft: 45 }));
		expect(
			await screen.findByText(/this session closes in 45 seconds/),
		).toBeInTheDocument();
	});

	test("a shell that exits ends the session and offers a new one", async () => {
		const { user } = await openPanel();
		const socket = await startSession(user);
		act(() => socket.serve(opened));
		act(() => socket.serve({ type: "exit", exitCode: 2 }));
		expect(
			await screen.findByText("The shell exited with code 2."),
		).toBeInTheDocument();
		expect(screen.getByText("Ended")).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "End session" })).toBeNull();

		await user.click(screen.getByRole("button", { name: "New session" }));
		expect(
			await screen.findByRole("button", { name: "Open terminal" }),
		).toBeInTheDocument();
		expect(screen.getByText("Not connected")).toBeInTheDocument();
		expect(screen.queryByRole("application", { name: "Terminal" })).toBeNull();
	});

	test("a refusal from the server is shown with its title and message", async () => {
		const { user } = await openPanel();
		const socket = await startSession(user);
		act(() =>
			socket.serve({
				type: "error",
				code: "replica_not_running",
				message: "Replica 0 exited.",
			}),
		);
		expect(
			await screen.findByText("That replica is not running: Replica 0 exited."),
		).toBeInTheDocument();
		expect(screen.getByText("Failed")).toBeInTheDocument();
	});

	test("a connection that drops is reported as lost", async () => {
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

	test("a session that was closed by the server says why", async () => {
		const { user } = await openPanel();
		const socket = await startSession(user);
		act(() => socket.serve(opened));
		act(() => socket.serve({ type: "closed", reason: "idle" }));
		expect(
			await screen.findByText("Closed after 15 minutes without input."),
		).toBeInTheDocument();
	});

	test("End session closes the socket and returns to the setup form", async () => {
		const { user } = await openPanel();
		const socket = await startSession(user);
		act(() => socket.serve(opened));
		await user.click(screen.getByRole("button", { name: "End session" }));
		expect(
			await screen.findByRole("button", { name: "Open terminal" }),
		).toBeInTheDocument();
		expect(socket.messages.at(-1)).toEqual({ type: "close" });
		expect(socket.closedWith).toBe(1000);
	});

	test("a new session after an ended one opens a fresh socket", async () => {
		const { user } = await openPanel();
		const first = await startSession(user);
		act(() => first.serve({ type: "exit", exitCode: 0 }));
		await user.click(
			await screen.findByRole("button", { name: "New session" }),
		);
		await user.click(
			await screen.findByRole("button", { name: "Open terminal" }),
		);
		await waitFor(() => expect(sockets).toHaveLength(2));
		expect(first.closedWith).toBe(1000);
	});

	test("Files opens the file browser on the same process and replica", async () => {
		const { user } = await openPanel(
			terminalInfo({
				replicas: [
					replica(0, { process: "worker" }),
					replica(1, { process: "worker" }),
				],
			}),
			{},
			{ process: "worker", replica: 1 },
		);
		await startSession(user);
		const files = renderHook(() => ({
			target: useFilesTarget(),
			process: useFilesProcess(),
			replica: useFilesReplica(),
		}));
		await user.click(screen.getByRole("button", { name: "Files" }));
		expect(files.result.current).toEqual({
			target: "svc-1",
			process: "worker",
			replica: 1,
		});
	});
});

describe("TerminalSheet copy selection", () => {
	function stubClipboard(writeText: (text: string) => Promise<void>) {
		Object.defineProperty(navigator, "clipboard", {
			configurable: true,
			value: { writeText },
		});
	}

	test("without a selection it asks for one", async () => {
		const { user } = await openPanel();
		await startSession(user);
		await user.click(screen.getByRole("button", { name: "Copy selection" }));
		expect(await screen.findByText("Nothing selected")).toBeInTheDocument();
		expect(
			screen.getByText("Select text in the terminal first."),
		).toBeInTheDocument();
	});

	test("copies the selected text and reports its length", async () => {
		const copied: string[] = [];
		const selection = spyOn(Terminal.prototype, "getSelection").mockReturnValue(
			"SELECT 1;",
		);
		try {
			const { user } = await openPanel();
			// userEvent.setup() installs its own clipboard: replace it after.
			stubClipboard(async (text) => {
				copied.push(text);
			});
			await startSession(user);
			await user.click(screen.getByRole("button", { name: "Copy selection" }));
			expect(await screen.findByText("9 characters")).toBeInTheDocument();
			expect(copied).toEqual(["SELECT 1;"]);
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
			stubClipboard(() => Promise.reject(new Error("denied")));
			await startSession(user);
			await user.click(screen.getByRole("button", { name: "Copy selection" }));
			expect(await screen.findByText("Could not copy")).toBeInTheDocument();
			expect(
				screen.getByText("The browser blocked clipboard access."),
			).toBeInTheDocument();
		} finally {
			selection.mockRestore();
		}
	});
});

describe("TerminalSheet production", () => {
	const production = () => terminalInfo({ production: true });

	test("the button warns that this is production", async () => {
		await openPanel(production());
		expect(
			await screen.findByRole("button", { name: "Open in production…" }),
		).toBeEnabled();
	});

	test("cancelling the production confirmation opens nothing", async () => {
		const { user } = await openPanel(production());
		await user.click(
			await screen.findByRole("button", { name: "Open in production…" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(dialog).toHaveTextContent("Open a terminal in production: api?");
		expect(dialog).toHaveTextContent("can see the service's secret variables");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
		expect(sockets).toEqual([]);
		expect(
			screen.getByRole("button", { name: "Open in production…" }),
		).toBeInTheDocument();
	});

	test("confirming sends confirmProduction with the open message", async () => {
		const { user } = await openPanel(production());
		await user.click(
			await screen.findByRole("button", { name: "Open in production…" }),
		);
		await screen.findByRole("alertdialog");
		holdToConfirm(/Hold to open terminal/i);
		await waitFor(() => expect(sockets).toHaveLength(1));
		const socket = sockets[0] as FakeWebSocket;
		act(() => socket.open());
		expect(socket.messages[0]).toMatchObject({
			type: "open",
			confirmProduction: true,
		});
	});
});

describe("TerminalSheet closing", () => {
	test("closing the setup form needs no confirmation", async () => {
		const { user } = await openPanel();
		await screen.findByRole("button", { name: "Open terminal" });
		const target = renderHook(() => useShellTarget());
		await user.click(screen.getByRole("button", { name: "Close" }));
		await waitFor(() => expect(target.result.current).toBeNull());
		expect(screen.queryByRole("alertdialog")).toBeNull();
	});

	test("closing a live session asks first; Keep open leaves it running", async () => {
		const { user } = await openPanel();
		const socket = await startSession(user);
		act(() => socket.serve(opened));
		const target = renderHook(() => useShellTarget());
		await user.click(screen.getByRole("button", { name: "Close" }));
		const dialog = await screen.findByRole("alertdialog");
		expect(dialog).toHaveTextContent("End this terminal session?");
		await user.click(within(dialog).getByRole("button", { name: "Keep open" }));
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
		expect(target.result.current).toBe("svc-1");
		expect(socket.closedWith).toBeUndefined();
	});

	test("ending the session from the confirmation closes the panel and the socket", async () => {
		const { user } = await openPanel();
		const socket = await startSession(user);
		act(() => socket.serve(opened));
		const target = renderHook(() => useShellTarget());
		await user.click(screen.getByRole("button", { name: "Close" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "End session" }),
		);
		await waitFor(() => expect(target.result.current).toBeNull());
		await waitFor(() => expect(socket.closedWith).toBe(1000));
		expect(socket.messages.at(-1)).toEqual({ type: "close" });
	});

	test("a different service starts from the setup form again", async () => {
		const { user } = await openPanel();
		const socket = await startSession(user);
		act(() => socket.serve(opened));
		act(() => openShell("svc-2"));
		expect(
			await screen.findByRole("button", { name: "Open terminal" }),
		).toBeInTheDocument();
		expect(screen.getByText("Not connected")).toBeInTheDocument();
		expect(socket.closedWith).toBe(1000);
	});
});
