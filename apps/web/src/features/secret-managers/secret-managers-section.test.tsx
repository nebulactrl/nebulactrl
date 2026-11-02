import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { SecretManager } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { SecretManagersSection } from "./secret-managers-section";

// Literal `${NAME}` placeholder: the escaped `$` keeps it from being a template placeholder.
const ref = (name: string) => `\${${name}}`;

/** Types a value in one step: the field's content, not its keystrokes, is under test. */
async function fill(
	user: ReturnType<typeof userEvent.setup>,
	field: HTMLElement,
	text: string,
) {
	await user.click(field);
	await user.paste(text);
}

const problem = (status: number, detail: string) => ({
	status,
	body: { status, title: "Error", detail, code: "bad_request" },
});

function manager(overrides: Partial<SecretManager> = {}): SecretManager {
	return {
		id: "sm1",
		name: "bao",
		kind: "vault",
		config: { address: "https://bao.example.com:8200", authMethod: "token" },
		createdAt: "2026-01-01T00:00:00Z",
		updatedAt: "2026-01-01T00:00:00Z",
		example: ref("{vault:bao/apps/shop#password}"),
		grants: null,
		lastTest: { at: null, ok: null },
		scope: "team",
		secretSet: true,
		...overrides,
	};
}

const infisical = manager({
	id: "sm2",
	name: "inf",
	kind: "infisical",
	config: { clientId: "c", projectId: "p" },
	example: ref("{infisical:inf/prod#KEY}"),
	scope: "selected",
	grants: [{ projectId: "p1" }, { projectId: "p2", environmentId: "e1" }],
});

const rowOf = (name: string) =>
	screen
		.getByText(name, { selector: "span.font-medium" })
		.closest("tr") as HTMLElement;

describe("SecretManagersSection", () => {
	test("empty team: invites to connect a provider and opens the add dialog", async () => {
		stubApi({ "GET /secret-managers": { body: { items: [] } } });
		const user = userEvent.setup();
		await renderWithProviders(<SecretManagersSection />);
		expect(
			await screen.findByText("Keep secrets where they already live"),
		).toBeInTheDocument();
		expect(screen.queryByRole("table")).not.toBeInTheDocument();
		await user.click(
			screen.getByRole("button", { name: "Add secret manager" }),
		);
		expect(
			await screen.findByRole("heading", { name: "Add a secret manager" }),
		).toBeInTheDocument();
	});

	test("lists each integration with kind, host, reference, scope and test state", async () => {
		stubApi({
			"GET /secret-managers": {
				body: {
					items: [
						manager({
							lastTest: { at: "2026-01-02T00:00:00Z", ok: true },
						}),
						infisical,
						manager({
							id: "sm3",
							name: "dop",
							kind: "doppler",
							config: {},
							example: ref("{doppler:dop#KEY}"),
							lastTest: {
								at: null,
								ok: false,
								error: "401 unauthorized",
							},
						}),
					],
				},
			},
		});
		await renderWithProviders(<SecretManagersSection />);
		await screen.findByText("bao", { selector: "span.font-medium" });

		const bao = within(rowOf("bao"));
		expect(bao.getByText("Vault / OpenBao")).toBeInTheDocument();
		expect(bao.getByText("bao.example.com:8200")).toBeInTheDocument();
		expect(
			bao.getByText(ref("{vault:bao/apps/shop#password}")),
		).toBeInTheDocument();
		expect(bao.getByText("Whole team")).toBeInTheDocument();
		expect(bao.getByText("Works")).toBeInTheDocument();

		const inf = within(rowOf("inf"));
		expect(inf.getByText("Infisical")).toBeInTheDocument();
		// No address configured: the Infisical cloud host is shown.
		expect(inf.getByText("us.infisical.com")).toBeInTheDocument();
		expect(inf.getByText("1 project, 1 environment")).toBeInTheDocument();
		expect(inf.getByText("Not tested")).toBeInTheDocument();

		const dop = within(rowOf("dop"));
		expect(dop.getByText("Failed")).toBeInTheDocument();
		expect(dop.getByText("401 unauthorized")).toBeInTheDocument();
		expect(dop.getByText("api.doppler.com")).toBeInTheDocument();
	});

	test("shows a retry when the list did not load, and recovers", async () => {
		let attempts = 0;
		stubApi({
			"GET /secret-managers": () => {
				attempts += 1;
				return attempts === 1
					? Response.json(
							{ status: 500, title: "x", detail: "boom", code: "internal" },
							{ status: 500 },
						)
					: Response.json({ items: [manager()] });
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<SecretManagersSection />);
		expect(
			await screen.findByText("Secret managers did not load"),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: /try again|retry/i }));
		expect(
			await screen.findByText("bao", { selector: "span.font-medium" }),
		).toBeInTheDocument();
		expect(attempts).toBe(2);
	});

	test("Edit opens the dialog on that integration", async () => {
		stubApi({
			"GET /secret-managers": { body: { items: [manager(), infisical] } },
		});
		const user = userEvent.setup();
		await renderWithProviders(<SecretManagersSection />);
		await screen.findByText("inf", { selector: "span.font-medium" });
		await user.click(
			within(rowOf("inf")).getByRole("button", { name: "Edit" }),
		);
		expect(
			await screen.findByRole("heading", { name: "Edit inf" }),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Client id")).toHaveValue("c");
	});

	test("Remove asks first, then DELETEs and confirms", async () => {
		let removed = false;
		const api = stubApi({
			"GET /secret-managers": () =>
				Response.json({ items: removed ? [] : [manager()] }),
			"DELETE /secret-managers/:id": () => {
				removed = true;
				return new Response(null, { status: 204 });
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<SecretManagersSection />);
		await screen.findByText("bao", { selector: "span.font-medium" });
		await user.click(
			within(rowOf("bao")).getByRole("button", { name: "Remove" }),
		);
		expect(await screen.findByText("Remove bao?")).toBeInTheDocument();
		expect(api.calls.filter((c) => c.method === "DELETE")).toEqual([]);

		await user.click(
			screen.getByRole("button", { name: "Remove secret manager" }),
		);
		expect(await screen.findByText("Removed bao")).toBeInTheDocument();
		expect(api.calls.filter((c) => c.method === "DELETE")).toMatchObject([
			{ path: "/secret-managers/sm1" },
		]);
		// The list refetches and the empty state returns.
		expect(
			await screen.findByText("Keep secrets where they already live"),
		).toBeInTheDocument();
	});

	test("cancelling the removal sends nothing", async () => {
		const api = stubApi({
			"GET /secret-managers": { body: { items: [manager()] } },
		});
		const user = userEvent.setup();
		await renderWithProviders(<SecretManagersSection />);
		await screen.findByText("bao", { selector: "span.font-medium" });
		await user.click(
			within(rowOf("bao")).getByRole("button", { name: "Remove" }),
		);
		await user.click(await screen.findByRole("button", { name: "Cancel" }));
		expect(api.calls.filter((c) => c.method === "DELETE")).toEqual([]);
	});

	test("a refused removal shows the reason and keeps the row", async () => {
		const api = stubApi({
			"GET /secret-managers": { body: { items: [manager()] } },
			"DELETE /secret-managers/:id": problem(
				409,
				"A variable still references bao",
			),
		});
		const user = userEvent.setup();
		await renderWithProviders(<SecretManagersSection />);
		await screen.findByText("bao", { selector: "span.font-medium" });
		await user.click(
			within(rowOf("bao")).getByRole("button", { name: "Remove" }),
		);
		await user.click(
			await screen.findByRole("button", { name: "Remove secret manager" }),
		);
		expect(
			await screen.findAllByText("A variable still references bao"),
		).not.toHaveLength(0);
		expect(screen.queryByText("Removed bao")).not.toBeInTheDocument();
		expect(rowOf("bao")).toBeInTheDocument();
		expect(api.calls.filter((c) => c.method === "DELETE")).toHaveLength(1);
	});
});

describe("SecretManagersSection: test dialog", () => {
	const listing = (items: SecretManager[]) => ({
		"GET /secret-managers": { body: { items } },
	});

	async function openTest(name: string) {
		const user = userEvent.setup();
		await screen.findByText(name, { selector: "span.font-medium" });
		await user.click(within(rowOf(name)).getByRole("button", { name: "Test" }));
		return user;
	}

	test("a sign-in-only test posts empty path and key and shows success", async () => {
		const api = stubApi({
			...listing([manager()]),
			"POST /secret-managers/:id/test": {
				body: { ok: true, read: false, message: "Signed in as bao-role" },
			},
		});
		await renderWithProviders(<SecretManagersSection />);
		const user = await openTest("bao");
		expect(
			await screen.findByRole("heading", { name: "Test bao" }),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Run test" }));
		expect(await screen.findByText("It works")).toBeInTheDocument();
		expect(screen.getByText("Signed in as bao-role")).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "POST")).toMatchObject({
			path: "/secret-managers/sm1/test",
			body: { path: "", key: "" },
		});
	});

	test("Vault asks for a path and a field, sends them trimmed, and reports failure", async () => {
		const api = stubApi({
			...listing([manager()]),
			"POST /secret-managers/:id/test": {
				body: { ok: false, read: false, message: "permission denied" },
			},
		});
		await renderWithProviders(<SecretManagersSection />);
		const user = await openTest("bao");
		await fill(
			user,
			await screen.findByLabelText("Secret path"),
			" apps/shop/db ",
		);
		// A path without a field cannot run.
		expect(screen.getByRole("button", { name: "Run test" })).toBeDisabled();
		await fill(user, screen.getByLabelText("Field"), "password");
		expect(screen.getByRole("button", { name: "Run test" })).toBeEnabled();
		await user.click(screen.getByRole("button", { name: "Run test" }));
		expect(await screen.findByText("It failed")).toBeInTheDocument();
		expect(screen.getByText("permission denied")).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
			path: "apps/shop/db",
			key: "password",
		});
	});

	test("Infisical labels the key as a secret name", async () => {
		stubApi(listing([infisical]));
		await renderWithProviders(<SecretManagersSection />);
		await openTest("inf");
		expect(await screen.findByLabelText("Secret name")).toHaveAttribute(
			"placeholder",
			"DATABASE_URL",
		);
		expect(screen.getByLabelText("Secret path")).toHaveAttribute(
			"placeholder",
			"prod/backend",
		);
	});

	test("Doppler's path is optional, but a path still needs a name", async () => {
		const api = stubApi({
			...listing([
				manager({
					id: "sm3",
					name: "dop",
					kind: "doppler",
					config: {},
				}),
			]),
			"POST /secret-managers/:id/test": {
				body: { ok: true, read: true, message: "read ok" },
			},
		});
		await renderWithProviders(<SecretManagersSection />);
		const user = await openTest("dop");
		expect(
			await screen.findByLabelText("Project and config (optional)"),
		).toBeInTheDocument();
		// A name alone (defaults or a service token supply the path) is fine.
		await fill(user, screen.getByLabelText("Secret name"), "API_KEY");
		expect(screen.getByRole("button", { name: "Run test" })).toBeEnabled();
		await fill(
			user,
			screen.getByLabelText("Project and config (optional)"),
			"backend/prd",
		);
		await user.clear(screen.getByLabelText("Secret name"));
		expect(screen.getByRole("button", { name: "Run test" })).toBeDisabled();
		await fill(user, screen.getByLabelText("Secret name"), "API_KEY");
		await user.click(screen.getByRole("button", { name: "Run test" }));
		expect(await screen.findByText("read ok")).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
			path: "backend/prd",
			key: "API_KEY",
		});
	});

	test("a request failure raises an error toast and no result", async () => {
		stubApi({
			...listing([manager()]),
			"POST /secret-managers/:id/test": problem(502, "Vault unreachable"),
		});
		await renderWithProviders(<SecretManagersSection />);
		const user = await openTest("bao");
		await user.click(await screen.findByRole("button", { name: "Run test" }));
		expect(
			await screen.findByText("Could not test the secret manager"),
		).toBeInTheDocument();
		expect(screen.getByText("Vault unreachable")).toBeInTheDocument();
		expect(screen.queryByText("It works")).not.toBeInTheDocument();
		expect(screen.queryByText("It failed")).not.toBeInTheDocument();
	});

	test("a finished test refreshes the list so the row shows the new state", async () => {
		let tested = false;
		stubApi({
			"GET /secret-managers": () =>
				Response.json({
					items: [
						manager({
							lastTest: tested
								? { at: "2026-01-02T00:00:00Z", ok: true }
								: { at: null, ok: null },
						}),
					],
				}),
			"POST /secret-managers/:id/test": () => {
				tested = true;
				return Response.json({ ok: true, read: false, message: "fine" });
			},
		});
		await renderWithProviders(<SecretManagersSection />);
		const user = await openTest("bao");
		expect(within(rowOf("bao")).getByText("Not tested")).toBeInTheDocument();
		await user.click(await screen.findByRole("button", { name: "Run test" }));
		await waitFor(() =>
			expect(within(rowOf("bao")).getByText("Works")).toBeInTheDocument(),
		);
	});

	test("Close dismisses the dialog", async () => {
		stubApi(listing([manager()]));
		await renderWithProviders(<SecretManagersSection />);
		const user = await openTest("bao");
		await screen.findByRole("heading", { name: "Test bao" });
		await user.click(screen.getByRole("button", { name: "Close" }));
		await waitFor(() =>
			expect(
				screen.queryByRole("heading", { name: "Test bao" }),
			).not.toBeInTheDocument(),
		);
	});
});
