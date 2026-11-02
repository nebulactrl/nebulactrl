import { afterEach, describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { type ClipboardStub, stubClipboard } from "./database.fixture";
import { NO_PUBLIC_ADDRESS } from "./public-port";
import { usePublicReveal } from "./use-public-reveal";

const CONNECTION = "POST /services/:id/database/public-port/connection";
const URL = "postgres://app:pw@203.0.113.7:15432/app";

const connection = (expiresInMs = 120_000) => ({
	body: {
		key: "DATABASE_URL",
		host: "203.0.113.7",
		port: 15432,
		value: URL,
		revealedAt: new Date().toISOString(),
		expiresAt: new Date(Date.now() + expiresInMs).toISOString(),
	},
});

let clipboard: ClipboardStub | undefined;
afterEach(() => clipboard?.restore());

/** Exposes what the hook returns as text and buttons. */
function Probe() {
	const { shown, mode, busy, problem, run, hide } = usePublicReveal("svc-db");
	return (
		<div>
			<button type="button" onClick={() => void run("copy")}>
				copy
			</button>
			<button type="button" onClick={() => void run("show")}>
				show
			</button>
			<button type="button" onClick={hide}>
				hide
			</button>
			<output aria-label="shown">{shown?.value ?? "nothing"}</output>
			<output aria-label="shown key">{shown?.key ?? "no key"}</output>
			<output aria-label="mode">{mode ?? "idle"}</output>
			<output aria-label="busy">{String(busy)}</output>
			<output aria-label="problem">{problem ?? "none"}</output>
		</div>
	);
}

describe("usePublicReveal", () => {
	test("show keeps the value in state with its key, asks with an empty body, and hide drops it", async () => {
		const api = stubApi({ [CONNECTION]: connection() });
		const user = userEvent.setup();
		const { queryClient } = await renderWithProviders(<Probe />);
		expect(screen.getByLabelText("shown")).toHaveTextContent("nothing");

		await user.click(screen.getByRole("button", { name: "show" }));
		await waitFor(() =>
			expect(screen.getByLabelText("shown")).toHaveTextContent(URL),
		);
		expect(screen.getByLabelText("shown key")).toHaveTextContent(
			"DATABASE_URL",
		);
		expect(screen.getByLabelText("mode")).toHaveTextContent("idle");
		expect(screen.getByLabelText("problem")).toHaveTextContent("none");
		const call = api.calls[0];
		expect(call?.method).toBe("POST");
		expect(call?.path).toBe("/services/svc-db/database/public-port/connection");
		expect(call?.body).toEqual({});
		// Not kept in the mutation cache.
		expect(queryClient.getMutationCache().getAll()).toHaveLength(0);

		await user.click(screen.getByRole("button", { name: "hide" }));
		expect(screen.getByLabelText("shown")).toHaveTextContent("nothing");
	});

	test("copy writes to the clipboard and does not keep the value", async () => {
		const user = userEvent.setup();
		clipboard = stubClipboard("ok");
		stubApi({ [CONNECTION]: connection() });
		await renderWithProviders(<Probe />);
		await user.click(screen.getByRole("button", { name: "copy" }));
		expect(await screen.findByText("Copied")).toBeVisible();
		expect(clipboard.writes).toEqual([URL]);
		expect(screen.getByLabelText("shown")).toHaveTextContent("nothing");
		expect(screen.getByLabelText("busy")).toHaveTextContent("false");
	});

	test("copy says so when the clipboard is blocked", async () => {
		const user = userEvent.setup();
		clipboard = stubClipboard("absent");
		stubApi({ [CONNECTION]: connection() });
		await renderWithProviders(<Probe />);
		await user.click(screen.getByRole("button", { name: "copy" }));
		expect(await screen.findByText("Nothing was copied")).toBeVisible();
		expect(screen.getByLabelText("problem")).toHaveTextContent("none");
	});

	test("is busy, in the requested mode, while the request is in flight", async () => {
		const gate = Promise.withResolvers<Response>();
		const api = stubApi({ [CONNECTION]: () => gate.promise });
		const user = userEvent.setup();
		await renderWithProviders(<Probe />);
		await user.click(screen.getByRole("button", { name: "show" }));
		await waitFor(() => expect(api.calls).toHaveLength(1));
		expect(screen.getByLabelText("busy")).toHaveTextContent("true");
		expect(screen.getByLabelText("mode")).toHaveTextContent("show");

		gate.resolve(Response.json(connection().body));
		await waitFor(() =>
			expect(screen.getByLabelText("busy")).toHaveTextContent("false"),
		);
		expect(screen.getByLabelText("mode")).toHaveTextContent("idle");
	});

	test("a server without a public address leaves guidance in problem, not a toast", async () => {
		stubApi({
			[CONNECTION]: {
				status: 409,
				body: {
					status: 409,
					title: "x",
					detail: "raw",
					code: "no_public_address",
				},
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<Probe />);
		await user.click(screen.getByRole("button", { name: "show" }));
		await waitFor(() =>
			expect(screen.getByLabelText("problem")).toHaveTextContent(
				NO_PUBLIC_ADDRESS,
			),
		);
		expect(screen.getByLabelText("shown")).toHaveTextContent("nothing");
		expect(screen.getByLabelText("busy")).toHaveTextContent("false");
		expect(screen.queryByText("Nothing was copied")).not.toBeInTheDocument();
	});

	test("a closed public port says to expose it first", async () => {
		stubApi({
			[CONNECTION]: {
				status: 409,
				body: {
					status: 409,
					title: "x",
					detail: "raw",
					code: "public_port_disabled",
				},
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<Probe />);
		await user.click(screen.getByRole("button", { name: "copy" }));
		await waitFor(() =>
			expect(screen.getByLabelText("problem")).toHaveTextContent(
				"The public port is closed. Expose it first.",
			),
		);
	});

	test("any other failure keeps the server's message, and the next try clears it", async () => {
		let attempts = 0;
		stubApi({
			[CONNECTION]: () => {
				attempts += 1;
				return attempts === 1
					? Response.json(
							{
								status: 500,
								title: "x",
								detail: "agent unreachable",
								code: "internal",
							},
							{ status: 500 },
						)
					: Response.json(connection().body);
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<Probe />);
		await user.click(screen.getByRole("button", { name: "show" }));
		await waitFor(() =>
			expect(screen.getByLabelText("problem")).toHaveTextContent(
				"agent unreachable",
			),
		);

		await user.click(screen.getByRole("button", { name: "show" }));
		await waitFor(() =>
			expect(screen.getByLabelText("shown")).toHaveTextContent(URL),
		);
		expect(screen.getByLabelText("problem")).toHaveTextContent("none");
	});

	test("an already expired value is removed straight after it is shown", async () => {
		const user = userEvent.setup();
		const api = stubApi({ [CONNECTION]: connection(-1_000) });
		await renderWithProviders(<Probe />);
		await user.click(screen.getByRole("button", { name: "show" }));
		await waitFor(() => expect(api.calls).toHaveLength(1));
		await waitFor(() =>
			expect(screen.getByLabelText("busy")).toHaveTextContent("false"),
		);
		await waitFor(() =>
			expect(screen.getByLabelText("shown")).toHaveTextContent("nothing"),
		);
	});
});
