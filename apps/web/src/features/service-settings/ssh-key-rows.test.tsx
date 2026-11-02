import { describe, expect, test } from "bun:test";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
	HostView,
	KeyView,
	StatusView,
	TestResult,
} from "#/api/gen/types.gen";
import { makeService } from "#/features/canvas/lib/fixtures";
import { type ApiStub, stubApi } from "#/test/api";
import { make, type Overrides, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { SshKeyRows } from "./ssh-key-rows";

const service = makeService({ id: "svc-git" });

const KEY: KeyView = {
	type: "ssh-ed25519",
	publicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPUBLICKEY nebula",
	fingerprint: "SHA256:abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG",
	source: "generated",
	createdAt: "2026-01-01T00:00:00Z",
};

const status = (overrides: Overrides<StatusView> = {}): StatusView =>
	make("StatusView", { hostKeys: null, usesSshUrl: true, ...overrides });

const host = (overrides: Overrides<HostView> = {}): HostView =>
	make("HostView", {
		fingerprint: "SHA256:pinnedpinnedpinnedpinnedpinnedpinnedpinned",
		...overrides,
	});

/** Runs a hold-to-confirm button's 1.3 s timer on the next tick instead. */
function holdNow<T>(run: () => Promise<T>): Promise<T> {
	const real = window.setTimeout;
	window.setTimeout = ((fn: () => void, ms?: number, ...args: unknown[]) =>
		real(fn, ms === 1300 ? 0 : ms, ...args)) as typeof window.setTimeout;
	return run().finally(() => {
		window.setTimeout = real;
	});
}

const posts = (api: ApiStub, path: string) =>
	api.calls.filter((c) => c.method === "POST" && c.path === path);

describe("SshKeyRows loading", () => {
	test("shows the row with a placeholder until the key status arrives", async () => {
		stubApi({ "GET /services/:serviceId/ssh": { body: status() } });
		await renderWithProviders(<SshKeyRows service={service} />);
		expect(screen.getByText("Deploy key")).toBeInTheDocument();
		expect(screen.queryByRole("button")).toBeNull();
		expect(
			await screen.findByRole("button", { name: "Use SSH: generate a key" }),
		).toBeInTheDocument();
	});

	test("shows why the status did not load", async () => {
		stubApi({
			"GET /services/:serviceId/ssh": problem(500, {
				code: "internal",
				detail: "keystore unavailable",
			}),
		});
		await renderWithProviders(<SshKeyRows service={service} />);
		expect(
			await screen.findByText(
				"Could not load the deploy key: keystore unavailable",
			),
		).toBeInTheDocument();
		expect(screen.queryByRole("button")).toBeNull();
	});
});

describe("SshKeyRows without a key", () => {
	test("generating a key posts an empty body, refetches and shows the new key", async () => {
		let generated = false;
		const api = stubApi({
			"GET /services/:serviceId/ssh": () =>
				Response.json(status(generated ? { key: KEY } : {})),
			"POST /services/:serviceId/ssh/key": () => {
				generated = true;
				return Response.json(status({ key: KEY }));
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<SshKeyRows service={service} />);
		await user.click(
			await screen.findByRole("button", { name: "Use SSH: generate a key" }),
		);

		expect(await screen.findByText("Deploy key added")).toBeInTheDocument();
		expect(
			screen.getByText(
				"Add the public key to the repository, then test the connection.",
			),
		).toBeInTheDocument();
		expect(posts(api, "/services/svc-git/ssh/key")).toHaveLength(1);
		expect(posts(api, "/services/svc-git/ssh/key")[0]?.body).toEqual({});
		expect(await screen.findByText(KEY.publicKey)).toBeInTheDocument();
		expect(api.unmatched).toEqual([]);
	});

	test("reports a failed generation and keeps the button", async () => {
		stubApi({
			"GET /services/:serviceId/ssh": { body: status() },
			"POST /services/:serviceId/ssh/key": problem(409, {
				code: "ssh_key_exists",
				detail: "a key already exists",
			}),
		});
		const user = userEvent.setup();
		await renderWithProviders(<SshKeyRows service={service} />);
		await user.click(
			await screen.findByRole("button", { name: "Use SSH: generate a key" }),
		);
		expect(
			await screen.findByText("Could not set the deploy key"),
		).toBeInTheDocument();
		expect(screen.getByText("a key already exists")).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Use SSH: generate a key" }),
		).toBeEnabled();
	});

	test("uploading your own key posts it as typed, without rotating", async () => {
		const api = stubApi({
			"GET /services/:serviceId/ssh": { body: status() },
			"POST /services/:serviceId/ssh/key": { body: status({ key: KEY }) },
		});
		const user = userEvent.setup();
		await renderWithProviders(<SshKeyRows service={service} />);
		await user.click(
			await screen.findByRole("button", { name: "Upload your own key" }),
		);
		const save = screen.getByRole("button", { name: "Save key" });
		expect(save).toBeDisabled();
		const box = screen.getByLabelText("Private key");
		await user.type(box, "   ");
		expect(save).toBeDisabled();
		await user.type(box, "PRIVATE-KEY-BODY");
		expect(save).toBeEnabled();
		await user.click(save);

		await screen.findByText("Deploy key added");
		expect(posts(api, "/services/svc-git/ssh/key")[0]?.body).toEqual({
			privateKey: "   PRIVATE-KEY-BODY",
			rotate: false,
		});
		// The form closes and the typed secret is dropped.
		expect(screen.queryByLabelText("Private key")).toBeNull();
	});

	test("Cancel closes the upload form and discards what was typed", async () => {
		const api = stubApi({ "GET /services/:serviceId/ssh": { body: status() } });
		const user = userEvent.setup();
		await renderWithProviders(<SshKeyRows service={service} />);
		await user.click(
			await screen.findByRole("button", { name: "Upload your own key" }),
		);
		await user.type(screen.getByLabelText("Private key"), "secret");
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		expect(screen.queryByLabelText("Private key")).toBeNull();
		await user.click(
			screen.getByRole("button", { name: "Upload your own key" }),
		);
		expect(screen.getByLabelText("Private key")).toHaveValue("");
		expect(posts(api, "/services/svc-git/ssh/key")).toEqual([]);
	});

	test("an upload the server rejects is reported and the form stays", async () => {
		stubApi({
			"GET /services/:serviceId/ssh": { body: status() },
			"POST /services/:serviceId/ssh/key": problem(422, {
				code: "ssh_key_invalid",
				detail: "the key is encrypted",
			}),
		});
		const user = userEvent.setup();
		await renderWithProviders(<SshKeyRows service={service} />);
		await user.click(
			await screen.findByRole("button", { name: "Upload your own key" }),
		);
		await user.type(screen.getByLabelText("Private key"), "ENCRYPTED");
		await user.click(screen.getByRole("button", { name: "Save key" }));
		expect(await screen.findByText("the key is encrypted")).toBeInTheDocument();
		expect(screen.getByLabelText("Private key")).toHaveValue("ENCRYPTED");
	});
});

describe("SshKeyRows with a key", () => {
	const withKey = (overrides: Partial<StatusView> = {}) =>
		status({ key: KEY, ...overrides });

	test("shows the public half, its type, short fingerprint and origin", async () => {
		stubApi({ "GET /services/:serviceId/ssh": { body: withKey() } });
		await renderWithProviders(<SshKeyRows service={service} />);
		expect(await screen.findByText(KEY.publicKey)).toBeInTheDocument();
		expect(
			screen.getByText(
				(_, el) =>
					el?.tagName === "SPAN" &&
					(el.textContent ?? "").startsWith(
						"Ed25519 · SHA256:abcdefghij…9ABCDEFG · generated",
					),
			),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Use SSH: generate a key" }),
		).toBeNull();
	});

	test("an uploaded key says so", async () => {
		stubApi({
			"GET /services/:serviceId/ssh": {
				body: withKey({ key: { ...KEY, source: "uploaded" } }),
			},
		});
		await renderWithProviders(<SshKeyRows service={service} />);
		expect(
			await screen.findByText(
				(_, el) =>
					el?.tagName === "SPAN" && /· uploaded/.test(el.textContent ?? ""),
			),
		).toBeInTheDocument();
	});

	test("links to the provider's deploy key page in a new tab", async () => {
		stubApi({
			"GET /services/:serviceId/ssh": {
				body: withKey({
					deployKeyLink: {
						label: "Add deploy key on GitHub",
						provider: "github",
						url: "https://github.com/org/repo/settings/keys/new",
					},
				}),
			},
		});
		await renderWithProviders(<SshKeyRows service={service} />);
		const link = (await screen.findByText("Add deploy key on GitHub")).closest(
			"a",
		);
		if (!link) throw new Error("no deploy key link");
		expect(link).toHaveAttribute(
			"href",
			"https://github.com/org/repo/settings/keys/new",
		);
		expect(link).toHaveAttribute("target", "_blank");
		expect(link).toHaveAttribute("rel", "noreferrer");
	});

	test("without an SSH URL saved, testing is disabled and the row says why", async () => {
		stubApi({
			"GET /services/:serviceId/ssh": { body: withKey({ usesSshUrl: false }) },
		});
		await renderWithProviders(<SshKeyRows service={service} />);
		expect(
			await screen.findByRole("button", { name: "Test connection" }),
		).toBeDisabled();
		expect(
			screen.getByText(/Save the repository URL to test the connection/),
		).toBeInTheDocument();
	});

	test("a successful connection test shows the branch head's short sha", async () => {
		const result: TestResult = {
			ok: true,
			commitSha: "0123456789abcdef",
			status: withKey(),
		};
		const api = stubApi({
			"GET /services/:serviceId/ssh": { body: withKey() },
			"POST /services/:serviceId/ssh/test": { body: result },
		});
		const user = userEvent.setup();
		await renderWithProviders(<SshKeyRows service={service} />);
		await user.click(
			await screen.findByRole("button", { name: "Test connection" }),
		);
		expect(await screen.findByText("Connected")).toBeInTheDocument();
		expect(
			screen.getByText(
				"The deploy key can read the repository (branch head 0123456).",
			),
		).toBeInTheDocument();
		expect(posts(api, "/services/svc-git/ssh/test")).toHaveLength(1);
	});

	test("a connection test without a sha still reports success", async () => {
		stubApi({
			"GET /services/:serviceId/ssh": { body: withKey() },
			"POST /services/:serviceId/ssh/test": {
				body: { ok: true, status: withKey() },
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<SshKeyRows service={service} />);
		await user.click(
			await screen.findByRole("button", { name: "Test connection" }),
		);
		expect(
			await screen.findByText("The deploy key can read the repository."),
		).toBeInTheDocument();
	});

	test("a failed connection test shows the server's message", async () => {
		stubApi({
			"GET /services/:serviceId/ssh": { body: withKey() },
			"POST /services/:serviceId/ssh/test": {
				body: {
					ok: false,
					code: "auth_failed",
					message: "Permission denied (publickey)",
					status: withKey(),
				},
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<SshKeyRows service={service} />);
		await user.click(
			await screen.findByRole("button", { name: "Test connection" }),
		);
		expect(await screen.findByText("Could not connect")).toBeInTheDocument();
		expect(
			screen.getByText("Permission denied (publickey)"),
		).toBeInTheDocument();
	});

	test("a changed host key is named as such in a failed test", async () => {
		stubApi({
			"GET /services/:serviceId/ssh": { body: withKey() },
			"POST /services/:serviceId/ssh/test": {
				body: {
					ok: false,
					code: "host_key_changed",
					message: "the host key does not match",
					status: withKey(),
				},
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<SshKeyRows service={service} />);
		await user.click(
			await screen.findByRole("button", { name: "Test connection" }),
		);
		expect(await screen.findByText("Host key changed")).toBeInTheDocument();
		expect(screen.getByText("the host key does not match")).toBeInTheDocument();
		expect(screen.queryByText("Could not connect")).toBeNull();
	});

	test("a test request that fails toasts the error", async () => {
		stubApi({
			"GET /services/:serviceId/ssh": { body: withKey() },
			"POST /services/:serviceId/ssh/test": {
				status: 502,
				body: {
					status: 502,
					code: "agent_offline",
					detail: "the agent is offline",
				},
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<SshKeyRows service={service} />);
		await user.click(
			await screen.findByRole("button", { name: "Test connection" }),
		);
		expect(
			await screen.findByText("Could not test the connection"),
		).toBeInTheDocument();
		expect(screen.getByText("the agent is offline")).toBeInTheDocument();
		expect(screen.queryByText("Connected")).toBeNull();
	});

	test("generating a new key asks first, then rotates", async () => {
		const api = stubApi({
			"GET /services/:serviceId/ssh": { body: withKey() },
			"POST /services/:serviceId/ssh/key": { body: status({ key: KEY }) },
		});
		const user = userEvent.setup();
		await renderWithProviders(<SshKeyRows service={service} />);
		await user.click(
			await screen.findByRole("button", { name: "Generate new key" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(dialog).toHaveTextContent("Generate a new deploy key?");
		expect(posts(api, "/services/svc-git/ssh/key")).toEqual([]);
		await user.click(
			within(dialog).getByRole("button", { name: "Generate new key" }),
		);
		expect(await screen.findByText("Deploy key replaced")).toBeInTheDocument();
		expect(posts(api, "/services/svc-git/ssh/key")[0]?.body).toEqual({
			rotate: true,
		});
	});

	test("declining the confirmation leaves the key alone", async () => {
		const api = stubApi({
			"GET /services/:serviceId/ssh": { body: withKey() },
		});
		const user = userEvent.setup();
		await renderWithProviders(<SshKeyRows service={service} />);
		await user.click(
			await screen.findByRole("button", { name: "Generate new key" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
		expect(posts(api, "/services/svc-git/ssh/key")).toEqual([]);
	});

	test("uploading a replacement key rotates with the typed key", async () => {
		const api = stubApi({
			"GET /services/:serviceId/ssh": { body: withKey() },
			"POST /services/:serviceId/ssh/key": { body: status({ key: KEY }) },
		});
		const user = userEvent.setup();
		await renderWithProviders(<SshKeyRows service={service} />);
		await user.click(
			await screen.findByRole("button", { name: "Upload a key" }),
		);
		await user.type(screen.getByLabelText("Private key"), "NEW-PRIVATE");
		await user.click(screen.getByRole("button", { name: "Save key" }));
		expect(await screen.findByText("Deploy key replaced")).toBeInTheDocument();
		expect(posts(api, "/services/svc-git/ssh/key")[0]?.body).toEqual({
			privateKey: "NEW-PRIVATE",
			rotate: true,
		});
	});

	test("removing the key needs the hold, then deletes it and refetches", async () => {
		let removed = false;
		const api = stubApi({
			"GET /services/:serviceId/ssh": () =>
				Response.json(removed ? status() : withKey()),
			"DELETE /services/:serviceId/ssh/key": () => {
				removed = true;
				return new Response(null, { status: 204 });
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<SshKeyRows service={service} />);
		await user.click(await screen.findByRole("button", { name: "Remove key" }));
		const dialog = await screen.findByRole("alertdialog");
		expect(dialog).toHaveTextContent("Remove the deploy key?");
		// A plain click is not enough.
		await user.click(
			within(dialog).getByRole("button", { name: "Hold to remove key" }),
		);
		expect(api.calls.filter((c) => c.method === "DELETE")).toEqual([]);

		await holdNow(async () => {
			fireEvent.keyDown(
				within(dialog).getByRole("button", { name: "Hold to remove key" }),
				{
					key: "Enter",
				},
			);
			expect(await screen.findByText("Deploy key removed")).toBeInTheDocument();
		});
		expect(api.calls.filter((c) => c.method === "DELETE")).toHaveLength(1);
		expect(
			await screen.findByRole("button", { name: "Use SSH: generate a key" }),
		).toBeInTheDocument();
	});

	test("a removal the server refuses toasts the error and keeps the key", async () => {
		stubApi({
			"GET /services/:serviceId/ssh": { body: withKey() },
			"DELETE /services/:serviceId/ssh/key": {
				status: 409,
				body: {
					status: 409,
					code: "ssh_key_in_use",
					detail: "a deploy is running",
				},
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<SshKeyRows service={service} />);
		await user.click(await screen.findByRole("button", { name: "Remove key" }));
		const dialog = await screen.findByRole("alertdialog");
		await holdNow(async () => {
			fireEvent.keyDown(
				within(dialog).getByRole("button", { name: "Hold to remove key" }),
				{
					key: "Enter",
				},
			);
			expect(
				await screen.findByText("Could not remove the deploy key"),
			).toBeInTheDocument();
		});
		expect(screen.getByText("a deploy is running")).toBeInTheDocument();
		expect(screen.getByText(KEY.publicKey)).toBeInTheDocument();
	});
});

describe("SshKeyRows host keys", () => {
	const mismatched = host({
		mismatch: {
			fingerprint: "SHA256:offeredofferedofferedofferedofferedoffered",
			seenAt: "2026-02-01T00:00:00Z",
		},
	});

	test("lists a pinned host key without an accept action", async () => {
		stubApi({
			"GET /services/:serviceId/ssh": {
				body: status({ key: KEY, hostKeys: [host()] }),
			},
		});
		await renderWithProviders(<SshKeyRows service={service} />);
		expect(await screen.findByText("github.com")).toBeInTheDocument();
		expect(
			screen.getByText(
				/Ed25519 · SHA256:pinnedpinnedpinnedpinnedpinnedpinnedpinned · pinned/,
			),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Accept new host key" }),
		).toBeNull();
	});

	test("shows no host key row when none was recorded", async () => {
		stubApi({
			"GET /services/:serviceId/ssh": {
				body: status({ key: KEY, hostKeys: [] }),
			},
		});
		await renderWithProviders(<SshKeyRows service={service} />);
		await screen.findByText(KEY.publicKey);
		expect(screen.queryByText("Host key")).toBeNull();
	});

	test("a changed host key shows the offered fingerprint", async () => {
		stubApi({
			"GET /services/:serviceId/ssh": {
				body: status({ key: KEY, hostKeys: [mismatched] }),
			},
		});
		await renderWithProviders(<SshKeyRows service={service} />);
		const alert = await screen.findByRole("alert");
		expect(alert).toHaveTextContent("Host key changed");
		expect(alert).toHaveTextContent(
			"The server presented SHA256:offeredofferedofferedofferedofferedoffered",
		);
		expect(alert).toHaveTextContent(
			"Deploys of this service fail until you accept the new key.",
		);
	});

	test("accepting shows both fingerprints, then posts the offered one", async () => {
		const api = stubApi({
			"GET /services/:serviceId/ssh": {
				body: status({ key: KEY, hostKeys: [mismatched] }),
			},
			"POST /services/:serviceId/ssh/host-keys/accept": {
				body: status({ key: KEY, hostKeys: [host()] }),
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<SshKeyRows service={service} />);
		await user.click(
			await screen.findByRole("button", { name: "Accept new host key" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(dialog).toHaveTextContent("Accept the new host key of github.com?");
		expect(dialog).toHaveTextContent(
			"Pinned: SHA256:pinnedpinnedpinnedpinnedpinnedpinnedpinned",
		);
		expect(dialog).toHaveTextContent(
			"Offered: SHA256:offeredofferedofferedofferedofferedoffered",
		);
		expect(posts(api, "/services/svc-git/ssh/host-keys/accept")).toEqual([]);
		await user.click(
			within(dialog).getByRole("button", { name: "Accept new host key" }),
		);
		expect(await screen.findByText("Host key accepted")).toBeInTheDocument();
		expect(
			posts(api, "/services/svc-git/ssh/host-keys/accept")[0]?.body,
		).toEqual({
			fingerprint: "SHA256:offeredofferedofferedofferedofferedoffered",
		});
	});

	test("a refused accept toasts the error", async () => {
		stubApi({
			"GET /services/:serviceId/ssh": {
				body: status({ key: KEY, hostKeys: [mismatched] }),
			},
			"POST /services/:serviceId/ssh/host-keys/accept": problem(409, {
				code: "host_key_stale",
				detail: "the offered key changed again",
			}),
		});
		const user = userEvent.setup();
		await renderWithProviders(<SshKeyRows service={service} />);
		await user.click(
			await screen.findByRole("button", { name: "Accept new host key" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Accept new host key" }),
		);
		expect(
			await screen.findByText("Could not accept the host key"),
		).toBeInTheDocument();
		expect(
			screen.getByText("the offered key changed again"),
		).toBeInTheDocument();
	});
});
