import { describe, expect, mock, test } from "bun:test";
import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { ConnectDialog } from "./connect-dialog";
import {
	connection,
	restoreFromBfcache,
	stubLocationAssign,
} from "./test-fixtures";

async function open() {
	const onOpenChange = mock();
	await renderWithProviders(<ConnectDialog open onOpenChange={onOpenChange} />);
	return { onOpenChange, user: userEvent.setup() };
}

async function pick(user: ReturnType<typeof userEvent.setup>, label: string) {
	await user.click(
		await screen.findByRole("button", { name: new RegExp(label) }),
	);
}

describe("ConnectDialog provider picker", () => {
	test("offers every provider with its blurb", async () => {
		stubApi({});
		await open();
		expect(
			await screen.findByRole("dialog", { name: "Connect a git provider" }),
		).toBeInTheDocument();
		for (const label of ["GitHub", "GitLab", "Gitea", "Forgejo", "Bitbucket"]) {
			expect(
				screen.getByRole("button", { name: new RegExp(label) }),
			).toBeVisible();
		}
		expect(
			screen.getByText(/No repository picker and no previews/),
		).toBeInTheDocument();
	});

	test("picking a provider opens its form and Back returns to the list", async () => {
		stubApi({});
		const { user } = await open();
		await pick(user, "GitLab");
		expect(
			await screen.findByRole("dialog", { name: "Connect GitLab" }),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Back to providers" }));
		expect(
			await screen.findByRole("dialog", { name: "Connect a git provider" }),
		).toBeInTheDocument();
	});

	test("closing the dialog reports it", async () => {
		stubApi({});
		const { user, onOpenChange } = await open();
		await pick(user, "Gitea");
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		expect(onOpenChange).toHaveBeenCalledWith(false);
	});
});

describe("GitHub", () => {
	const FORM_URL = "/api/v1/git/github/manifest/form?state=s1";
	const started = {
		body: {
			connectionId: "c1",
			formUrl: FORM_URL,
			manifest: '{"name":"nebula"}',
			postUrl: "https://github.com/settings/apps/new?state=s1",
			state: "s1",
		},
	};

	test("posts owner and base URL, then navigates to the returned form page", async () => {
		const loc = stubLocationAssign();
		try {
			const api = stubApi({ "POST /git/github/manifest": started });
			const { user } = await open();
			await pick(user, "GitHub");
			await user.click(screen.getByLabelText(/Organization/));
			await user.paste(" acme ");
			await user.click(screen.getByLabelText(/Enterprise Server URL/));
			await user.paste("https://ghe.example.com");
			await user.click(
				screen.getByRole("button", { name: /Create the GitHub App/ }),
			);
			await waitFor(() => expect(loc.urls).toEqual([FORM_URL]));
			expect(api.calls[0]?.body).toEqual({
				owner: "acme",
				baseUrl: "https://ghe.example.com",
			});
		} finally {
			loc.restore();
		}
	});

	test("leaves empty optional fields out of the request", async () => {
		const loc = stubLocationAssign();
		try {
			const api = stubApi({ "POST /git/github/manifest": started });
			const { user } = await open();
			await pick(user, "GitHub");
			await user.click(
				screen.getByRole("button", { name: /Create the GitHub App/ }),
			);
			await waitFor(() => expect(api.calls).toHaveLength(1));
			expect(api.calls[0]?.body).toEqual({});
		} finally {
			loc.restore();
		}
	});

	test("after success the button stays disabled, so no second connection is created", async () => {
		const loc = stubLocationAssign();
		try {
			const api = stubApi({ "POST /git/github/manifest": started });
			const { user } = await open();
			await pick(user, "GitHub");
			const create = screen.getByRole("button", {
				name: /Create the GitHub App/,
			});
			await user.click(create);
			await waitFor(() => expect(loc.urls).toEqual([FORM_URL]));
			expect(create).toBeDisabled();
			await user.click(create);
			expect(api.calls).toHaveLength(1);
			expect(loc.urls).toEqual([FORM_URL]);
		} finally {
			loc.restore();
		}
	});

	test("restoring the page from the back/forward cache makes the button usable again", async () => {
		const loc = stubLocationAssign();
		try {
			const api = stubApi({ "POST /git/github/manifest": started });
			const { user } = await open();
			await pick(user, "GitHub");
			const create = screen.getByRole("button", {
				name: /Create the GitHub App/,
			});
			await user.click(create);
			await waitFor(() => expect(loc.urls).toEqual([FORM_URL]));
			await waitFor(() => expect(create).toBeDisabled());
			act(() => restoreFromBfcache());
			await waitFor(() => expect(create).toBeEnabled());
			await user.click(create);
			await waitFor(() => expect(api.calls).toHaveLength(2));
		} finally {
			loc.restore();
		}
	});

	test("a rejected start shows why and does not leave the page", async () => {
		const loc = stubLocationAssign();
		try {
			stubApi({
				"POST /git/github/manifest": {
					status: 400,
					body: {
						title: "Bad",
						detail: "NEBULACTRL_PUBLIC_URL is not set",
						status: 400,
						code: "bad_request",
					},
				},
			});
			const { user } = await open();
			await pick(user, "GitHub");
			await user.click(
				screen.getByRole("button", { name: /Create the GitHub App/ }),
			);
			expect(await screen.findByText("Could not start")).toBeInTheDocument();
			expect(
				screen.getByText("NEBULACTRL_PUBLIC_URL is not set"),
			).toBeInTheDocument();
			expect(loc.urls).toEqual([]);
			expect(
				screen.getByRole("button", { name: /Create the GitHub App/ }),
			).toBeEnabled();
		} finally {
			loc.restore();
		}
	});
});

describe("token connections", () => {
	const created = (over = {}) => ({
		status: 201,
		body: {
			authorizeUrl: "",
			webhookSecret: "",
			connection: connection({
				id: "n1",
				name: "GitLab",
				provider: "gitlab",
				authType: "token",
				...over,
			}),
		},
	});

	test("Connect stays disabled until a token is entered, then creates the connection", async () => {
		const api = stubApi({
			"POST /git/connections": created({ accountLogin: "robin" }),
			"GET /git/connections": { body: { items: [] } },
			"GET /system": { body: make("SystemInfo", { publicUrl: "" }) },
		});
		const { user } = await open();
		await pick(user, "GitLab");
		const connect = screen.getByRole("button", { name: "Connect" });
		expect(connect).toBeDisabled();
		await user.type(screen.getByLabelText("Name"), "  Work  ");
		await user.type(screen.getByLabelText("Access token"), " glpat-1 ");
		expect(connect).toBeEnabled();
		await user.click(connect);
		expect(
			await screen.findByRole("dialog", { name: "Connected GitLab" }),
		).toBeInTheDocument();
		const post = api.calls.find((c) => c.method === "POST");
		expect(post?.path).toBe("/git/connections");
		expect(post?.body).toEqual({
			provider: "gitlab",
			name: "Work",
			authType: "token",
			token: "glpat-1",
		});
		// The dialog moved on to the done step; non-manual connections need no webhook.
		expect(
			screen.getByText(/registers the repository webhook itself/),
		).toBeInTheDocument();
		expect(screen.getByText("Signed in as robin.")).toBeInTheDocument();
	});

	test("the success toast without an account login says the connection is ready", async () => {
		stubApi({
			"POST /git/connections": created({ accountLogin: "" }),
			"GET /git/connections": { body: { items: [] } },
		});
		const { user } = await open();
		await pick(user, "GitLab");
		await user.type(screen.getByLabelText("Access token"), "t");
		await user.click(screen.getByRole("button", { name: "Connect" }));
		expect(
			await screen.findByText("The connection is ready."),
		).toBeInTheDocument();
	});

	test("Done closes the dialog", async () => {
		stubApi({
			"POST /git/connections": created(),
			"GET /git/connections": { body: { items: [] } },
		});
		const { user, onOpenChange } = await open();
		await pick(user, "GitLab");
		await user.type(screen.getByLabelText("Access token"), "t");
		await user.click(screen.getByRole("button", { name: "Connect" }));
		await user.click(await screen.findByRole("button", { name: "Done" }));
		expect(onOpenChange).toHaveBeenCalledWith(false);
	});

	test("an instance URL is required for Gitea and sent with the request", async () => {
		const api = stubApi({
			"POST /git/connections": created({ provider: "gitea", name: "Home" }),
			"GET /git/connections": { body: { items: [] } },
		});
		const { user } = await open();
		await pick(user, "Gitea");
		await user.type(screen.getByLabelText("Access token"), "tok");
		expect(screen.getByLabelText("Instance URL")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Connect" })).toBeDisabled();
		await user.type(
			screen.getByLabelText("Instance URL"),
			"https://git.example.com",
		);
		await user.click(screen.getByRole("button", { name: "Connect" }));
		await screen.findByRole("dialog", { name: "Connected Home" });
		expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
			provider: "gitea",
			baseUrl: "https://git.example.com",
			authType: "token",
			token: "tok",
		});
	});

	test("GitLab's instance URL is optional", async () => {
		stubApi({});
		const { user } = await open();
		await pick(user, "GitLab");
		expect(
			screen.getByLabelText("Instance URL (self-hosted only)"),
		).toBeInTheDocument();
		await user.type(screen.getByLabelText("Access token"), "tok");
		expect(screen.getByRole("button", { name: "Connect" })).toBeEnabled();
	});

	test("a server error is shown in the form and keeps it editable", async () => {
		stubApi({
			"POST /git/connections": problem(422, {
				title: "Invalid",
				detail: "the provider rejected the token",
			}),
		});
		const { user } = await open();
		await pick(user, "GitLab");
		await user.type(screen.getByLabelText("Access token"), "bad");
		await user.click(screen.getByRole("button", { name: "Connect" }));
		expect(
			await screen.findByText("the provider rejected the token"),
		).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Connect" })).toBeEnabled();
	});

	test("the credential guide follows the typed instance URL", async () => {
		stubApi({});
		const { user } = await open();
		await pick(user, "GitLab");
		expect(
			screen.getByRole("region", { name: /GitLab credential setup/ }),
		).toBeInTheDocument();
	});
});

describe("OAuth connections", () => {
	async function toOAuth(publicUrl: string) {
		const api = stubApi({
			"GET /system": { body: make("SystemInfo", { publicUrl }) },
			"POST /git/connections": {
				status: 201,
				body: make("CreateConnectionResult", {
					authorizeUrl: "https://gitlab.com/oauth/authorize?client_id=x",
					webhookSecret: "",
					connection: connection({
						provider: "gitlab",
						authType: "oauth",
						status: "pending",
					}),
				}),
			},
		});
		const { user } = await open();
		await pick(user, "GitLab");
		await user.click(screen.getByRole("button", { name: "OAuth application" }));
		return { api, user };
	}

	test("shows the callback URL built from the public address", async () => {
		await toOAuth("https://ctrl.example.com/");
		expect(
			await screen.findByText(
				"https://ctrl.example.com/api/v1/git/oauth/callback",
			),
		).toBeInTheDocument();
		expect(
			screen.getByText(/Scopes: api and read_repository/),
		).toBeInTheDocument();
	});

	test("falls back to this page's origin when the public address is unknown", async () => {
		await toOAuth("");
		expect(
			await screen.findByText(
				`${window.location.origin}/api/v1/git/oauth/callback`,
			),
		).toBeInTheDocument();
	});

	test("needs both client id and secret, then hands over to the provider", async () => {
		const loc = stubLocationAssign();
		try {
			const { api, user } = await toOAuth("https://ctrl.example.com");
			const save = screen.getByRole("button", { name: "Save and authorize" });
			expect(save).toBeDisabled();
			await user.type(screen.getByLabelText("Client ID"), " id-1 ");
			expect(save).toBeDisabled();
			await user.type(screen.getByLabelText("Client secret"), "s3cret");
			expect(save).toBeEnabled();
			await user.click(save);
			await waitFor(() =>
				expect(loc.urls).toEqual([
					"https://gitlab.com/oauth/authorize?client_id=x",
				]),
			);
			expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
				provider: "gitlab",
				authType: "oauth",
				clientId: "id-1",
				clientSecret: "s3cret",
			});
			// Leaving for the provider is not a "connected" outcome.
			expect(screen.queryByText(/^Connected /)).not.toBeInTheDocument();
		} finally {
			loc.restore();
		}
	});

	test("Gitea has no scope hint", async () => {
		stubApi({ "GET /system": { body: make("SystemInfo", { publicUrl: "" }) } });
		const { user } = await open();
		await pick(user, "Gitea");
		await user.click(screen.getByRole("button", { name: "OAuth application" }));
		expect(screen.queryByText(/Scopes: api/)).not.toBeInTheDocument();
		expect(
			screen.getByText(/Create an OAuth application at your Gitea instance/),
		).toBeInTheDocument();
	});
});

describe("Bitbucket", () => {
	test("has no instance URL or auth choice, and the token is optional", async () => {
		stubApi({});
		const { user } = await open();
		await pick(user, "Bitbucket");
		expect(screen.queryByLabelText(/Instance URL/)).not.toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "OAuth application" }),
		).not.toBeInTheDocument();
		expect(
			screen.getByLabelText("Access token (optional)"),
		).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Connect" })).toBeEnabled();
	});

	test("creates the connection and shows the webhook URL and the one-time secret", async () => {
		const api = stubApi({
			"POST /git/connections": {
				status: 201,
				body: make("CreateConnectionResult", {
					authorizeUrl: "",
					webhookSecret: "whsec_abc",
					connection: connection({
						provider: "bitbucket",
						authType: "webhook_only",
						name: "Team BB",
						manualWebhook: true,
						webhookUrl: "https://ctrl.example.com/hooks/bb1",
					}),
				}),
			},
			"GET /git/connections": { body: { items: [] } },
		});
		const { user, onOpenChange } = await open();
		await pick(user, "Bitbucket");
		await user.click(screen.getByRole("button", { name: "Connect" }));
		expect(await screen.findByText("whsec_abc")).toBeInTheDocument();
		expect(
			screen.getByText("https://ctrl.example.com/hooks/bb1"),
		).toBeInTheDocument();
		expect(screen.getByText("Shown once")).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
			provider: "bitbucket",
		});
		await user.click(
			screen.getByRole("button", { name: "I have created the webhook" }),
		);
		expect(onOpenChange).toHaveBeenCalledWith(false);
	});

	test("sends the optional token when given", async () => {
		const api = stubApi({
			"POST /git/connections": {
				status: 201,
				body: make("CreateConnectionResult", {
					authorizeUrl: "",
					webhookSecret: "x",
					connection: connection({
						provider: "bitbucket",
						manualWebhook: true,
					}),
				}),
			},
			"GET /git/connections": { body: { items: [] } },
		});
		const { user } = await open();
		await pick(user, "Bitbucket");
		await user.type(screen.getByLabelText("Access token (optional)"), "bbtok");
		await user.click(screen.getByRole("button", { name: "Connect" }));
		await screen.findByText("x");
		expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
			provider: "bitbucket",
			token: "bbtok",
		});
	});
});
