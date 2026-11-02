import { describe, expect, setDefaultTimeout, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ProviderBody } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { ProvidersCard } from "./providers-card";

setDefaultTimeout(30_000);

const problem = (status: number, detail: string, code = "bad_request") => ({
	status,
	body: { status, title: "Error", detail, code },
});

const okta: ProviderBody = {
	id: "p-okta",
	name: "Okta",
	kind: "oidc",
	issuerUrl: "https://dev-1.okta.com/oauth2/default",
	clientId: "cid",
	allowedDomains: ["corp.example", "other.io"],
	scopes: null,
	trustMfa: true,
	enabled: true,
	createdAt: "2026-01-01T00:00:00Z",
	redirectUri: "https://nebula.example/api/v1/auth/oidc/p-okta/callback",
};

const github: ProviderBody = {
	id: "p-gh",
	name: "GitHub",
	kind: "github",
	issuerUrl: "",
	clientId: "gh",
	allowedDomains: null,
	scopes: null,
	trustMfa: false,
	enabled: false,
	createdAt: "2026-01-01T00:00:00Z",
	redirectUri: "https://nebula.example/api/v1/auth/oidc/p-gh/callback",
};

const system = {
	"GET /system": {
		body: make("SystemInfo", { publicUrl: "https://nebula.example" }),
	},
};

describe("ProvidersCard list", () => {
	test("says passwords are the only way in when no provider exists", async () => {
		stubApi({
			"GET /auth-providers": { body: { items: [] } },
			"GET /auth-settings": { body: { allowOpenSignup: false } },
		});
		await renderWithProviders(<ProvidersCard />);
		expect(
			await screen.findByText(
				"No provider yet. Passwords are the only way to sign in.",
			),
		).toBeInTheDocument();
		expect(screen.queryByRole("list")).not.toBeInTheDocument();
	});

	test("shows each provider's state, kind, issuer and allowed domains", async () => {
		stubApi({
			"GET /auth-providers": { body: { items: [okta, github] } },
			"GET /auth-settings": { body: { allowOpenSignup: false } },
		});
		await renderWithProviders(<ProvidersCard />);
		const [first, second] = await screen.findAllByRole("listitem");
		expect(first).toHaveTextContent("Okta");
		expect(first).toHaveTextContent("Enabled");
		expect(first).toHaveTextContent("Trusted MFA");
		expect(first).toHaveTextContent(
			"OpenID Connect · https://dev-1.okta.com/oauth2/default · corp.example, other.io",
		);
		expect(second).toHaveTextContent("GitHub");
		expect(second).toHaveTextContent("Disabled");
		expect(second).not.toHaveTextContent("Trusted MFA");
		expect(second).toHaveTextContent("GitHub · any verified email");
	});

	test("shows the reason when the providers cannot be loaded", async () => {
		stubApi({
			"GET /auth-providers": problem(
				403,
				"Only the instance administrator may read this.",
			),
			"GET /auth-settings": { body: { allowOpenSignup: false } },
		});
		await renderWithProviders(<ProvidersCard />);
		expect(
			await screen.findByText("Only the instance administrator may read this."),
		).toBeInTheDocument();
		expect(screen.queryByRole("list")).not.toBeInTheDocument();
	});
});

describe("ProvidersCard open sign-up", () => {
	test("reflects the saved setting", async () => {
		stubApi({
			"GET /auth-providers": { body: { items: [] } },
			"GET /auth-settings": { body: { allowOpenSignup: true } },
		});
		await renderWithProviders(<ProvidersCard />);
		await screen.findByText(/No provider yet/);
		await waitFor(() =>
			expect(screen.getByRole("switch")).toHaveAttribute(
				"aria-checked",
				"true",
			),
		);
	});

	test("turning it on PUTs the new value and shows it on", async () => {
		let allow = false;
		const api = stubApi({
			"GET /auth-providers": { body: { items: [] } },
			"GET /auth-settings": () => Response.json({ allowOpenSignup: allow }),
			"PUT /auth-settings": async (request) => {
				const body: unknown = await request.json();
				allow =
					typeof body === "object" &&
					body !== null &&
					"allowOpenSignup" in body &&
					body.allowOpenSignup === true;
				return Response.json({ allowOpenSignup: allow });
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<ProvidersCard />);
		await screen.findByText(/No provider yet/);
		const toggle = screen.getByRole("switch");
		await waitFor(() => expect(toggle).toBeEnabled());
		expect(toggle).toHaveAttribute("aria-checked", "false");
		await user.click(toggle);
		await waitFor(() =>
			expect(screen.getByRole("switch")).toHaveAttribute(
				"aria-checked",
				"true",
			),
		);
		const put = api.calls.find((c) => c.method === "PUT");
		expect(put?.path).toBe("/auth-settings");
		expect(put?.body).toEqual({ allowOpenSignup: true });
	});

	test("a rejected change raises an error toast and the switch stays off", async () => {
		stubApi({
			"GET /auth-providers": { body: { items: [] } },
			"GET /auth-settings": { body: { allowOpenSignup: false } },
			"PUT /auth-settings": problem(500, "The setting could not be saved."),
		});
		const user = userEvent.setup();
		await renderWithProviders(<ProvidersCard />);
		await screen.findByText(/No provider yet/);
		const toggle = screen.getByRole("switch");
		await waitFor(() => expect(toggle).toBeEnabled());
		await user.click(toggle);
		expect(
			await screen.findByText("Could not change the setting"),
		).toBeInTheDocument();
		expect(
			screen.getByText("The setting could not be saved."),
		).toBeInTheDocument();
		expect(screen.getByRole("switch")).toHaveAttribute("aria-checked", "false");
	});
});

describe("ProvidersCard removing", () => {
	test("confirms, DELETEs that provider and refreshes the list", async () => {
		let items = [okta, github];
		const api = stubApi({
			"GET /auth-providers": () => Response.json({ items }),
			"GET /auth-settings": { body: { allowOpenSignup: false } },
			"DELETE /auth-providers/:id": () => {
				items = [okta];
				return new Response(null, { status: 204 });
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<ProvidersCard />);
		const row = (await screen.findAllByRole("listitem"))[1] as HTMLElement;
		await user.click(within(row).getByRole("button", { name: "Remove" }));
		const dialog = await screen.findByRole("alertdialog");
		expect(within(dialog).getByText("Remove GitHub?")).toBeInTheDocument();
		expect(api.calls.some((c) => c.method === "DELETE")).toBe(false);
		await user.click(
			within(dialog).getByRole("button", { name: "Remove provider" }),
		);

		expect(await screen.findByText("Removed GitHub")).toBeInTheDocument();
		const del = api.calls.find((c) => c.method === "DELETE");
		expect(del?.path).toBe("/auth-providers/p-gh");
		await waitFor(() =>
			expect(screen.getAllByRole("listitem")).toHaveLength(1),
		);
		expect(screen.queryByText("GitHub")).not.toBeInTheDocument();
	});

	test("declining leaves the provider and sends nothing", async () => {
		const api = stubApi({
			"GET /auth-providers": { body: { items: [okta] } },
			"GET /auth-settings": { body: { allowOpenSignup: false } },
		});
		const user = userEvent.setup();
		await renderWithProviders(<ProvidersCard />);
		await user.click(await screen.findByRole("button", { name: "Remove" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
		);
		expect(api.calls.some((c) => c.method === "DELETE")).toBe(false);
		expect(screen.getByText("Okta")).toBeInTheDocument();
	});

	test("a failed removal reports the reason and keeps the provider", async () => {
		stubApi({
			"GET /auth-providers": { body: { items: [okta] } },
			"GET /auth-settings": { body: { allowOpenSignup: false } },
			"DELETE /auth-providers/:id": problem(
				409,
				"It is the last way anyone can sign in.",
			),
		});
		const user = userEvent.setup();
		await renderWithProviders(<ProvidersCard />);
		await user.click(await screen.findByRole("button", { name: "Remove" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Remove provider" }),
		);
		expect(
			await screen.findByText("It is the last way anyone can sign in."),
		).toBeInTheDocument();
		expect(screen.queryByText("Removed Okta")).not.toBeInTheDocument();
		// The confirm dialog is still open, so the page behind it is inert.
		expect(screen.getAllByRole("listitem", { hidden: true })).toHaveLength(1);
	});
});

describe("ProvidersCard dialogs", () => {
	test("Connect your identity provider opens an empty add dialog", async () => {
		stubApi({
			"GET /auth-providers": { body: { items: [okta] } },
			"GET /auth-settings": { body: { allowOpenSignup: false } },
			...system,
		});
		const user = userEvent.setup();
		await renderWithProviders(<ProvidersCard />);
		await screen.findByText("Okta");
		await user.click(
			screen.getByRole("button", { name: "Connect your identity provider" }),
		);
		const dialog = await screen.findByRole("dialog");
		expect(
			within(dialog).getByRole("heading", { name: "Add a sign-in provider" }),
		).toBeInTheDocument();
		expect(within(dialog).getByLabelText("Name")).toHaveValue("");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
		);
	});

	test("Edit opens the dialog for that provider, and saving PUTs it", async () => {
		const api = stubApi({
			"GET /auth-providers": { body: { items: [okta, github] } },
			"GET /auth-settings": { body: { allowOpenSignup: false } },
			"PUT /auth-providers/:id": { body: github },
			...system,
		});
		const user = userEvent.setup();
		await renderWithProviders(<ProvidersCard />);
		const row = (await screen.findAllByRole("listitem"))[1] as HTMLElement;
		await user.click(within(row).getByRole("button", { name: "Edit" }));
		const dialog = await screen.findByRole("dialog");
		expect(
			within(dialog).getByRole("heading", { name: "Edit GitHub" }),
		).toBeInTheDocument();
		await user.click(within(dialog).getByRole("button", { name: "Save" }));
		expect(await screen.findByText("Saved GitHub")).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "PUT")?.path).toBe(
			"/auth-providers/p-gh",
		);
		await waitFor(() =>
			expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
		);
	});
});
