import { describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type {
	EnrollmentToken,
	Server as ServerWire,
} from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, type Overrides, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { EnrollDialog } from "./enroll-dialog";
import { enrollmentKey } from "./enrollment";

const GiB = 1024 ** 3;

/** The server a fresh enrollment creates: pending, no agent connected yet. */
const wireServer = (overrides: Overrides<ServerWire> = {}) =>
	make("Server", {
		id: "srv-9",
		name: "hetzner-01",
		status: "pending",
		controlPlane: false,
		serviceCount: 0,
		...overrides,
	});

const onlineServer = () =>
	wireServer({
		status: "online",
		agentVersion: "v0.4.2",
		facts: {
			arch: "arm64",
			cgroupVersion: "2",
			cpus: 4,
			diskTotal: 100 * GiB,
			dockerVersion: "27.3",
			kernel: "6.8",
			memTotal: 16 * GiB,
			os: "Ubuntu 24.04",
			port443Free: true,
			port80Free: false,
		},
	});

const COMMAND =
	"curl -fsSL https://ctrl.example.com/install-agent.sh | sudo sh -s -- --url https://ctrl.example.com --token nct_enroll_abc";

const enrollment = (server: ServerWire = wireServer()): EnrollmentToken => ({
	expiresAt: new Date(Date.now() + 60 * 60_000).toISOString(),
	installCommand: COMMAND,
	server,
	token: "nct_enroll_abc",
});

/** Hosts the dialog the way the route does: the open flag lives outside it. */
function Host({ onClose }: { onClose?: () => void }) {
	const [open, setOpen] = useState(true);
	return (
		<>
			<button type="button" onClick={() => setOpen(true)}>
				reopen
			</button>
			<EnrollDialog
				open={open}
				onOpenChange={(next) => {
					setOpen(next);
					if (!next) onClose?.();
				}}
			/>
		</>
	);
}

describe("EnrollDialog: the form", () => {
	test("stays closed until it is opened", async () => {
		stubApi({});
		await renderWithProviders(
			<EnrollDialog open={false} onOpenChange={() => {}} />,
		);
		expect(screen.queryByText("Add a server")).not.toBeInTheDocument();
	});

	test("cannot create a command without a name, and says why", async () => {
		const api = stubApi({});
		const user = userEvent.setup();
		await renderWithProviders(<Host />);
		const create = await screen.findByRole("button", {
			name: "Create install command",
		});
		expect(create).toBeDisabled();
		expect(
			screen.getByText("Give the server a name to create its install command."),
		).toBeInTheDocument();

		await user.type(screen.getByLabelText("Name"), "   ");
		expect(create).toBeDisabled();

		await user.type(screen.getByLabelText("Name"), "hetzner-01");
		expect(create).toBeEnabled();
		expect(
			screen.queryByText(
				"Give the server a name to create its install command.",
			),
		).not.toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});

	test("Cancel closes the dialog without a request", async () => {
		const api = stubApi({});
		const user = userEvent.setup();
		let closed = 0;
		await renderWithProviders(<Host onClose={() => (closed += 1)} />);
		await user.click(await screen.findByRole("button", { name: "Cancel" }));
		expect(closed).toBe(1);
		await waitFor(() =>
			expect(screen.queryByText("Add a server")).not.toBeInTheDocument(),
		);
		expect(api.calls).toEqual([]);
	});

	test("sends only the name when the optional fields are empty", async () => {
		const api = stubApi({
			"POST /servers/enrollment-tokens": { status: 201, body: enrollment() },
			"GET /servers/:serverId": { body: wireServer() },
		});
		const user = userEvent.setup();
		await renderWithProviders(<Host />);
		await user.type(await screen.findByLabelText("Name"), "  hetzner-01  ");
		await user.click(
			screen.getByRole("button", { name: "Create install command" }),
		);
		await screen.findByLabelText("Install command");
		const post = api.calls.find((call) => call.method === "POST");
		expect(post?.path).toBe("/servers/enrollment-tokens");
		expect(post?.body).toEqual({ name: "hetzner-01" });
	});

	test("sends the trimmed optional address details and an idempotency key", async () => {
		let key: string | null = null;
		const api = stubApi({
			"POST /servers/enrollment-tokens": (request) => {
				key = request.headers.get("Idempotency-Key");
				return Response.json(enrollment());
			},
			"GET /servers/:serverId": { body: wireServer() },
		});
		const user = userEvent.setup();
		await renderWithProviders(<Host />);
		await user.type(await screen.findByLabelText("Name"), "hetzner-01");
		await user.click(screen.getByLabelText("Public IP (optional)"));
		await user.paste(" 203.0.113.10 ");
		await user.click(screen.getByLabelText("Base domain (optional)"));
		await user.paste("apps.example.com");
		await user.click(screen.getByLabelText("ACME email (optional)"));
		await user.paste("ops@example.com");
		await user.click(
			screen.getByRole("button", { name: "Create install command" }),
		);
		await screen.findByLabelText("Install command");
		const post = api.calls.find((call) => call.method === "POST");
		expect(post?.body).toEqual({
			name: "hetzner-01",
			publicIp: "203.0.113.10",
			baseDomain: "apps.example.com",
			acmeEmail: "ops@example.com",
		});
		expect(key).toMatch(/^[0-9a-f-]{36}$/);
	});

	test("Enter in the name field submits the form", async () => {
		const api = stubApi({
			"POST /servers/enrollment-tokens": { status: 201, body: enrollment() },
			"GET /servers/:serverId": { body: wireServer() },
		});
		const user = userEvent.setup();
		await renderWithProviders(<Host />);
		await user.type(await screen.findByLabelText("Name"), "edge-1{Enter}");
		await screen.findByLabelText("Install command");
		expect(api.calls.filter((c) => c.method === "POST")).toHaveLength(1);
	});

	test("shows the control plane's reason when creating fails, and keeps the form", async () => {
		stubApi({
			"POST /servers/enrollment-tokens": problem(409, {
				title: "Conflict",
				detail: "a server with that name already exists",
				code: "conflict",
			}),
		});
		const user = userEvent.setup();
		await renderWithProviders(<Host />);
		await user.type(await screen.findByLabelText("Name"), "hetzner-01");
		await user.click(
			screen.getByRole("button", { name: "Create install command" }),
		);
		expect(
			await screen.findByText(/a server with that name already exists/),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Name")).toHaveValue("hetzner-01");
		expect(
			screen.getByRole("button", { name: "Create install command" }),
		).toBeEnabled();
	});
});

describe("EnrollDialog: waiting for the agent", () => {
	test("shows the install command and waits while the server has not connected", async () => {
		const api = stubApi({
			"POST /servers/enrollment-tokens": { status: 201, body: enrollment() },
			"GET /servers/:serverId": { body: wireServer() },
		});
		const user = userEvent.setup();
		const { queryClient } = await renderWithProviders(<Host />);
		await user.type(await screen.findByLabelText("Name"), "hetzner-01");
		await user.click(
			screen.getByRole("button", { name: "Create install command" }),
		);
		const command = await screen.findByLabelText("Install command");
		expect(command).toHaveValue(COMMAND.replace(/ (?=\||--[a-z])/g, "\n"));
		expect(
			await screen.findByText("Waiting for the agent to connect…"),
		).toBeInTheDocument();
		expect(screen.queryByText("Agent connected")).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Close" })).toBeInTheDocument();
		expect(screen.queryByText("Open server")).not.toBeInTheDocument();
		expect(
			screen.getByText(
				/Closing it is fine: the server's page keeps the command/,
			),
		).toBeInTheDocument();
		// The command is remembered for the server's own page.
		expect(
			(queryClient.getQueryData(enrollmentKey("srv-9")) as EnrollmentToken)
				.installCommand,
		).toBe(COMMAND);
		const poll = api.calls.find((call) => call.method === "GET");
		expect(poll?.path).toBe("/servers/srv-9");
	});

	test("turns into 'Agent connected' with the host's facts once the agent is online", async () => {
		const api = stubApi({
			"POST /servers/enrollment-tokens": { status: 201, body: enrollment() },
			"GET /servers/:serverId": { body: onlineServer() },
		});
		const user = userEvent.setup();
		await renderWithProviders(<Host />);
		await user.type(await screen.findByLabelText("Name"), "hetzner-01");
		await user.click(
			screen.getByRole("button", { name: "Create install command" }),
		);
		expect(await screen.findByText("Agent connected")).toBeInTheDocument();
		expect(
			screen.getByText("hetzner-01 is online and ready for services."),
		).toBeInTheDocument();
		expect(
			screen.queryByText("Waiting for the agent to connect…"),
		).not.toBeInTheDocument();

		for (const [label, value] of [
			["Agent", "v0.4.2"],
			["OS", "Ubuntu 24.04 · arm64"],
			["CPUs", "4"],
			["Memory", "16 GiB"],
			["Docker", "27.3"],
			["Ports 80 / 443", "in use / free"],
		]) {
			expect(screen.getByText(label as string)).toBeInTheDocument();
			expect(screen.getByText(value as string)).toBeInTheDocument();
		}

		expect(screen.getByText("Open server").closest("a")).toHaveAttribute(
			"href",
			"/servers/srv-9",
		);
		expect(screen.getByRole("button", { name: "Done" })).toBeInTheDocument();
		expect(api.unmatched).toEqual([]);
	});

	test("Done closes the dialog", async () => {
		stubApi({
			"POST /servers/enrollment-tokens": { status: 201, body: enrollment() },
			"GET /servers/:serverId": { body: onlineServer() },
		});
		const user = userEvent.setup();
		let closed = 0;
		await renderWithProviders(<Host onClose={() => (closed += 1)} />);
		await user.type(await screen.findByLabelText("Name"), "hetzner-01");
		await user.click(
			screen.getByRole("button", { name: "Create install command" }),
		);
		await user.click(await screen.findByRole("button", { name: "Done" }));
		expect(closed).toBe(1);
	});

	test("reopening starts from an empty form again", async () => {
		stubApi({
			"POST /servers/enrollment-tokens": { status: 201, body: enrollment() },
			"GET /servers/:serverId": { body: wireServer() },
		});
		const user = userEvent.setup();
		await renderWithProviders(<Host />);
		await user.type(await screen.findByLabelText("Name"), "hetzner-01");
		await user.click(
			screen.getByRole("button", { name: "Create install command" }),
		);
		await user.click(await screen.findByRole("button", { name: "Close" }));
		await waitFor(() =>
			expect(screen.queryByText("Add a server")).not.toBeInTheDocument(),
		);
		await user.click(screen.getByRole("button", { name: "reopen" }));
		expect(await screen.findByLabelText("Name")).toHaveValue("");
		expect(screen.queryByLabelText("Install command")).not.toBeInTheDocument();
	});
});
