import { afterEach, describe, expect, test } from "bun:test";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ConnectionView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { GitConnectionsSection } from "./connections-section";
import {
	connection,
	restoreFromBfcache,
	stubLocationAssign,
} from "./test-fixtures";

afterEach(() => window.history.replaceState(null, "", "/"));

const LIST = "GET /git/connections";
const list = (...items: ConnectionView[]) => ({ [LIST]: { body: { items } } });

function card(name: string): HTMLElement {
	return screen.getByRole("article", { name });
}

describe("GitConnectionsSection states", () => {
	test("shows the empty state with a way to connect", async () => {
		stubApi(list());
		await renderWithProviders(<GitConnectionsSection />);
		expect(await screen.findByText("Deploy on every push")).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Connect a provider" }),
		).toBeInTheDocument();
	});

	test("a failed load says so and Retry asks again", async () => {
		let n = 0;
		const api = stubApi({
			[LIST]: () => {
				n++;
				return n === 1
					? Response.json({ title: "down", status: 500 }, { status: 500 })
					: Response.json({ items: [connection()] });
			},
		});
		await renderWithProviders(<GitConnectionsSection />);
		expect(
			await screen.findByText("Git connections did not load"),
		).toBeInTheDocument();
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: /retry|try again/i }));
		expect(
			await screen.findByRole("article", { name: "Acme GitHub" }),
		).toBeInTheDocument();
		expect(api.calls.filter((c) => c.path === "/git/connections")).toHaveLength(
			2,
		);
	});

	test("counts connections and lists a card for each", async () => {
		stubApi(
			list(
				connection(),
				connection({
					id: "c2",
					name: "Home Gitea",
					provider: "gitea",
					authType: "token",
				}),
			),
		);
		await renderWithProviders(<GitConnectionsSection />);
		expect(await screen.findByText(/2 connections/)).toBeInTheDocument();
		expect(card("Acme GitHub")).toBeInTheDocument();
		expect(card("Home Gitea")).toBeInTheDocument();
	});

	test("singular count for one connection", async () => {
		stubApi(list(connection()));
		await renderWithProviders(<GitConnectionsSection />);
		expect(await screen.findByText(/1 connection\./)).toBeInTheDocument();
	});

	test("Connect a provider opens the provider picker", async () => {
		stubApi(list(connection()));
		await renderWithProviders(<GitConnectionsSection />);
		await screen.findByRole("article", { name: "Acme GitHub" });
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Connect a provider" }));
		expect(
			await screen.findByRole("dialog", { name: "Connect a git provider" }),
		).toBeInTheDocument();
	});
});

describe("flow result from the provider redirect", () => {
	test("a finished connection toasts success and strips the parameter", async () => {
		window.history.replaceState(
			null,
			"",
			"/settings/integrations?git_connected=c1&tab=git",
		);
		const api = stubApi(list(connection()));
		await renderWithProviders(<GitConnectionsSection />);
		expect(
			await screen.findByText("The provider is ready to use."),
		).toBeInTheDocument();
		expect(window.location.search).toBe("?tab=git");
		expect(window.location.pathname).toBe("/settings/integrations");
		await waitFor(() => expect(api.unmatched).toEqual([]));
	});

	test("an error toasts the reason", async () => {
		window.history.replaceState(null, "", "/?git_error=access_denied");
		stubApi(list());
		await renderWithProviders(<GitConnectionsSection />);
		expect(
			await screen.findByText("The connection did not finish"),
		).toBeInTheDocument();
		expect(screen.getByText("access_denied")).toBeInTheDocument();
		expect(window.location.search).toBe("");
	});

	test("a notice toasts as a warning", async () => {
		window.history.replaceState(null, "", "/?git_notice=Install+the+App+next");
		stubApi(list());
		await renderWithProviders(<GitConnectionsSection />);
		expect(await screen.findByText("Almost there")).toBeInTheDocument();
		expect(screen.getByText("Install the App next")).toBeInTheDocument();
	});

	test("an error wins over a connected parameter", async () => {
		window.history.replaceState(null, "", "/?git_connected=c1&git_error=boom");
		stubApi(list());
		await renderWithProviders(<GitConnectionsSection />);
		expect(
			await screen.findByText("The connection did not finish"),
		).toBeInTheDocument();
		expect(
			screen.queryByText("The provider is ready to use."),
		).not.toBeInTheDocument();
	});

	test("no parameters, no toast and the URL is left alone", async () => {
		window.history.replaceState(null, "", "/?tab=git");
		stubApi(list());
		await renderWithProviders(<GitConnectionsSection />);
		await screen.findByText("Deploy on every push");
		expect(window.location.search).toBe("?tab=git");
	});
});

describe("testing a connection", () => {
	test("a working connection toasts success and refetches the list", async () => {
		const api = stubApi({
			...list(connection()),
			"POST /git/connections/:id/test": {
				body: connection({ status: "active" }),
			},
		});
		await renderWithProviders(<GitConnectionsSection />);
		await screen.findByRole("article", { name: "Acme GitHub" });
		await userEvent
			.setup()
			.click(within(card("Acme GitHub")).getByRole("button", { name: "Test" }));
		expect(await screen.findByText("Acme GitHub works")).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "POST")?.path).toBe(
			"/git/connections/c1/test",
		);
		await waitFor(() =>
			expect(
				api.calls.filter(
					(c) => c.method === "GET" && c.path === "/git/connections",
				),
			).toHaveLength(2),
		);
	});

	test("a failing connection toasts its status message", async () => {
		stubApi({
			...list(connection()),
			"POST /git/connections/:id/test": {
				body: connection({ status: "error", statusMessage: "token expired" }),
			},
		});
		await renderWithProviders(<GitConnectionsSection />);
		await screen.findByRole("article", { name: "Acme GitHub" });
		await userEvent
			.setup()
			.click(within(card("Acme GitHub")).getByRole("button", { name: "Test" }));
		expect(
			await screen.findByText("Acme GitHub needs attention"),
		).toBeInTheDocument();
		expect(screen.getByText("token expired")).toBeInTheDocument();
	});

	test("a request error toasts it and re-enables the button", async () => {
		stubApi({
			...list(connection()),
			"POST /git/connections/:id/test": problem(500, {
				title: "x",
				detail: "provider unreachable",
			}),
		});
		await renderWithProviders(<GitConnectionsSection />);
		await screen.findByRole("article", { name: "Acme GitHub" });
		await userEvent
			.setup()
			.click(within(card("Acme GitHub")).getByRole("button", { name: "Test" }));
		expect(
			await screen.findByText("Could not test the connection"),
		).toBeInTheDocument();
		expect(screen.getByText("provider unreachable")).toBeInTheDocument();
		await waitFor(() =>
			expect(
				within(card("Acme GitHub")).getByRole("button", { name: "Test" }),
			).toBeEnabled(),
		);
	});
});

describe("authorize and install", () => {
	const oauth = connection({
		id: "o1",
		name: "Lab",
		provider: "gitlab",
		authType: "oauth",
		status: "pending",
	});

	test("Authorize starts the flow and sends the browser to the provider", async () => {
		const loc = stubLocationAssign();
		try {
			const api = stubApi({
				...list(oauth),
				"POST /git/connections/:id/oauth/start": {
					body: { authorizeUrl: "https://gitlab.com/oauth/authorize?x=1" },
				},
			});
			await renderWithProviders(<GitConnectionsSection />);
			await screen.findByRole("article", { name: "Lab" });
			await userEvent
				.setup()
				.click(within(card("Lab")).getByRole("button", { name: "Authorize" }));
			await waitFor(() =>
				expect(loc.urls).toEqual(["https://gitlab.com/oauth/authorize?x=1"]),
			);
			expect(api.calls.find((c) => c.method === "POST")?.path).toBe(
				"/git/connections/o1/oauth/start",
			);
		} finally {
			loc.restore();
		}
	});

	test("a failed authorization start toasts and stays", async () => {
		const loc = stubLocationAssign();
		try {
			stubApi({
				...list(oauth),
				"POST /git/connections/:id/oauth/start": {
					status: 400,
					body: {
						title: "x",
						detail: "no client id",
						status: 400,
						code: "bad_request",
					},
				},
			});
			await renderWithProviders(<GitConnectionsSection />);
			await screen.findByRole("article", { name: "Lab" });
			await userEvent
				.setup()
				.click(within(card("Lab")).getByRole("button", { name: "Authorize" }));
			expect(
				await screen.findByText("Could not start the authorization"),
			).toBeInTheDocument();
			expect(loc.urls).toEqual([]);
		} finally {
			loc.restore();
		}
	});

	const pending = connection({ status: "pending" });

	test("'I installed it' refreshes the installation and toasts", async () => {
		const api = stubApi({
			...list(pending),
			"POST /git/connections/:id/refresh": {
				body: connection({ status: "active" }),
			},
		});
		await renderWithProviders(<GitConnectionsSection />);
		await screen.findByRole("article", { name: "Acme GitHub" });
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "I installed it" }));
		expect(await screen.findByText("GitHub App installed")).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "POST")?.path).toBe(
			"/git/connections/c1/refresh",
		);
	});

	test("refresh before the App is installed explains why", async () => {
		stubApi({
			...list(pending),
			"POST /git/connections/:id/refresh": problem(409, {
				title: "x",
				detail: "no installation found",
			}),
		});
		await renderWithProviders(<GitConnectionsSection />);
		await screen.findByRole("article", { name: "Acme GitHub" });
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "I installed it" }));
		expect(
			await screen.findByText("The App is not installed yet"),
		).toBeInTheDocument();
		expect(screen.getByText("no installation found")).toBeInTheDocument();
	});

	const unfinished = connection({
		status: "pending",
		github: {
			appId: 0,
			appSlug: "",
			appUrl: "",
			installUrl: "",
			installationId: 0,
		},
	});

	test("Finish setup restarts the manifest flow and sends the browser to the returned page", async () => {
		const loc = stubLocationAssign();
		try {
			const api = stubApi({
				...list(unfinished),
				"POST /git/connections/:id/github/manifest": {
					body: {
						connectionId: "c1",
						formUrl: "/api/v1/git/github/manifest/form?state=s1",
						manifest: "{}",
						postUrl: "https://github.com/settings/apps/new?state=s1",
						state: "s1",
					},
				},
			});
			await renderWithProviders(<GitConnectionsSection />);
			await screen.findByRole("article", { name: "Acme GitHub" });
			await userEvent
				.setup()
				.click(screen.getByRole("button", { name: "Finish setup" }));
			await waitFor(() =>
				expect(loc.urls).toEqual(["/api/v1/git/github/manifest/form?state=s1"]),
			);
			expect(api.calls.find((c) => c.method === "POST")?.path).toBe(
				"/git/connections/c1/github/manifest",
			);
		} finally {
			loc.restore();
		}
	});

	test("restoring the page from the back/forward cache makes Finish setup usable again", async () => {
		const loc = stubLocationAssign();
		try {
			stubApi({
				...list(unfinished),
				"POST /git/connections/:id/github/manifest": {
					body: {
						connectionId: "c1",
						formUrl: "/api/v1/git/github/manifest/form?state=s1",
						manifest: "{}",
						postUrl: "https://github.com/settings/apps/new?state=s1",
						state: "s1",
					},
				},
			});
			await renderWithProviders(<GitConnectionsSection />);
			await screen.findByRole("article", { name: "Acme GitHub" });
			await userEvent
				.setup()
				.click(screen.getByRole("button", { name: "Finish setup" }));
			await waitFor(() => expect(loc.urls).toHaveLength(1));
			await waitFor(() =>
				expect(
					screen.getByRole("button", { name: /Finish setup/ }),
				).toBeDisabled(),
			);
			act(() => restoreFromBfcache());
			await waitFor(() =>
				expect(
					screen.getByRole("button", { name: "Finish setup" }),
				).toBeEnabled(),
			);
		} finally {
			loc.restore();
		}
	});

	test("a failed Finish setup toasts why and stays", async () => {
		const loc = stubLocationAssign();
		try {
			stubApi({
				...list(unfinished),
				"POST /git/connections/:id/github/manifest": problem(422, {
					title: "x",
					detail: "the GitHub App already exists",
					code: "app_exists",
				}),
			});
			await renderWithProviders(<GitConnectionsSection />);
			await screen.findByRole("article", { name: "Acme GitHub" });
			await userEvent
				.setup()
				.click(screen.getByRole("button", { name: "Finish setup" }));
			expect(
				await screen.findByText("Could not resume the GitHub App setup"),
			).toBeInTheDocument();
			expect(
				screen.getByText("the GitHub App already exists"),
			).toBeInTheDocument();
			expect(loc.urls).toEqual([]);
		} finally {
			loc.restore();
		}
	});
});

describe("removing a connection", () => {
	async function ask(c: ConnectionView) {
		const api = stubApi({
			...list(c),
			"DELETE /git/connections/:id": { status: 204 },
		});
		await renderWithProviders(<GitConnectionsSection />);
		await screen.findByRole("article", { name: c.name });
		const user = userEvent.setup();
		await user.click(
			within(card(c.name)).getByRole("button", { name: "Remove" }),
		);
		return { api, user };
	}

	test("an unused connection asks plainly, then deletes with force", async () => {
		const { api, user } = await ask(connection({ serviceCount: 0 }));
		const dialog = await screen.findByRole("alertdialog", {
			name: /Remove Acme GitHub\?/,
		});
		expect(
			within(dialog).getByText("Webhooks NebulaCtrl created are deleted."),
		).toBeInTheDocument();
		await user.click(
			within(dialog).getByRole("button", { name: "Remove connection" }),
		);
		expect(await screen.findByText("Removed Acme GitHub")).toBeInTheDocument();
		const del = api.calls.find((c) => c.method === "DELETE");
		expect(del?.path).toBe("/git/connections/c1");
		expect(del?.query.get("force")).toBe("true");
	});

	test("warns how many services lose push deploys and that the GitHub App stays", async () => {
		await ask(connection({ serviceCount: 2 }));
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText(/2 services stop deploying on push/),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText(/The GitHub App itself stays on GitHub/),
		).toBeInTheDocument();
	});

	test("one service, non-GitHub-App: singular wording and no App note", async () => {
		await ask(
			connection({ serviceCount: 1, authType: "token", provider: "gitea" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText(/1 service stop deploying on push/),
		).toBeInTheDocument();
		expect(
			within(dialog).queryByText(/GitHub App itself/),
		).not.toBeInTheDocument();
	});

	test("cancelling deletes nothing", async () => {
		const { api, user } = await ask(connection());
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
		);
		expect(api.calls.some((c) => c.method === "DELETE")).toBe(false);
	});

	test("a failed delete toasts the reason", async () => {
		stubApi({
			...list(connection()),
			"DELETE /git/connections/:id": problem(409, {
				title: "x",
				detail: "still referenced",
			}),
		});
		await renderWithProviders(<GitConnectionsSection />);
		await screen.findByRole("article", { name: "Acme GitHub" });
		const user = userEvent.setup();
		await user.click(
			within(card("Acme GitHub")).getByRole("button", { name: "Remove" }),
		);
		await user.click(
			await screen.findByRole("button", { name: "Remove connection" }),
		);
		expect(
			await screen.findByText("Could not remove the connection"),
		).toBeInTheDocument();
	});
});

describe("replacing a token", () => {
	const token = connection({
		id: "t1",
		name: "Home Gitea",
		provider: "gitea",
		authType: "token",
		baseUrl: "https://git.example.com",
	});

	async function openDialog() {
		const api = stubApi({
			...list(token),
			"PATCH /git/connections/:id": { body: token },
		});
		await renderWithProviders(<GitConnectionsSection />);
		await screen.findByRole("article", { name: "Home Gitea" });
		const user = userEvent.setup();
		await user.click(
			within(card("Home Gitea")).getByRole("button", { name: "Replace token" }),
		);
		const dialog = await screen.findByRole("dialog", {
			name: /Replace the token of Home Gitea/,
		});
		return { api, user, dialog };
	}

	test("the submit button waits for a token, then PATCHes the trimmed one", async () => {
		const { api, user, dialog } = await openDialog();
		const submit = within(dialog).getByRole("button", {
			name: "Replace token",
		});
		expect(submit).toBeDisabled();
		await user.type(
			within(dialog).getByLabelText("New access token"),
			"  new-tok ",
		);
		expect(submit).toBeEnabled();
		await user.click(submit);
		expect(await screen.findByText("Token replaced")).toBeInTheDocument();
		const patch = api.calls.find((c) => c.method === "PATCH");
		expect(patch?.path).toBe("/git/connections/t1");
		expect(patch?.body).toEqual({ token: "new-tok" });
		await waitFor(() =>
			expect(
				screen.queryByRole("dialog", { name: /Replace the token/ }),
			).not.toBeInTheDocument(),
		);
	});

	test("a rejected token is shown in the dialog, which stays open", async () => {
		stubApi({
			...list(token),
			"PATCH /git/connections/:id": problem(422, {
				title: "x",
				detail: "401 from git.example.com",
			}),
		});
		await renderWithProviders(<GitConnectionsSection />);
		await screen.findByRole("article", { name: "Home Gitea" });
		const user = userEvent.setup();
		await user.click(
			within(card("Home Gitea")).getByRole("button", { name: "Replace token" }),
		);
		const dialog = await screen.findByRole("dialog", {
			name: /Replace the token/,
		});
		await user.type(within(dialog).getByLabelText("New access token"), "bad");
		await user.click(
			within(dialog).getByRole("button", { name: "Replace token" }),
		);
		expect(
			await within(dialog).findByText("401 from git.example.com"),
		).toBeInTheDocument();
		expect(dialog).toBeInTheDocument();
	});

	test("Cancel closes without a request", async () => {
		const { api, user, dialog } = await openDialog();
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(
				screen.queryByRole("dialog", { name: /Replace the token/ }),
			).not.toBeInTheDocument(),
		);
		expect(api.calls.some((c) => c.method === "PATCH")).toBe(false);
	});
});

describe("Bitbucket webhook setup", () => {
	const bb = connection({
		id: "b1",
		name: "Team BB",
		provider: "bitbucket",
		authType: "webhook_only",
		manualWebhook: true,
		webhookUrl: "https://ctrl.example.com/hooks/b1",
		canListRepos: false,
	});

	async function openDialog(extra: Parameters<typeof stubApi>[0] = {}) {
		const api = stubApi({ ...list(bb), ...extra });
		await renderWithProviders(<GitConnectionsSection />);
		await screen.findByRole("article", { name: "Team BB" });
		const user = userEvent.setup();
		await user.click(
			within(card("Team BB")).getByRole("button", { name: "Webhook setup" }),
		);
		const dialog = await screen.findByRole("dialog", {
			name: /Webhook setup · Team BB/,
		});
		return { api, user, dialog };
	}

	test("shows the URL without a secret until it is rotated", async () => {
		const { dialog } = await openDialog();
		expect(
			within(dialog).getByText("https://ctrl.example.com/hooks/b1"),
		).toBeInTheDocument();
		expect(within(dialog).queryByText("Shown once")).not.toBeInTheDocument();
	});

	test("Rotate the secret shows the new secret once", async () => {
		const { api, user, dialog } = await openDialog({
			"POST /git/connections/:id/webhook-secret/rotate": {
				body: make("SecretOutputBody", { webhookSecret: "whsec_new" }),
			},
		});
		await user.click(
			within(dialog).getByRole("button", { name: /Rotate the secret/ }),
		);
		expect(await within(dialog).findByText("whsec_new")).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "POST")?.path).toBe(
			"/git/connections/b1/webhook-secret/rotate",
		);
	});

	test("the secret is forgotten when the dialog is closed and reopened", async () => {
		const { user, dialog } = await openDialog({
			"POST /git/connections/:id/webhook-secret/rotate": {
				body: make("SecretOutputBody", { webhookSecret: "whsec_new" }),
			},
		});
		await user.click(
			within(dialog).getByRole("button", { name: /Rotate the secret/ }),
		);
		await within(dialog).findByText("whsec_new");
		await user.click(within(dialog).getByRole("button", { name: "Done" }));
		await waitFor(() =>
			expect(
				screen.queryByRole("dialog", { name: /Webhook setup/ }),
			).not.toBeInTheDocument(),
		);
		await user.click(
			within(card("Team BB")).getByRole("button", { name: "Webhook setup" }),
		);
		const again = await screen.findByRole("dialog", { name: /Webhook setup/ });
		expect(within(again).queryByText("whsec_new")).not.toBeInTheDocument();
	});

	test("a failed rotation toasts", async () => {
		const { user, dialog } = await openDialog({
			"POST /git/connections/:id/webhook-secret/rotate": problem(500, {
				title: "x",
				detail: "vault sealed",
			}),
		});
		await user.click(
			within(dialog).getByRole("button", { name: /Rotate the secret/ }),
		);
		expect(
			await screen.findByText("Could not rotate the secret"),
		).toBeInTheDocument();
		expect(screen.getByText("vault sealed")).toBeInTheDocument();
	});
});

describe("deliveries", () => {
	test("Deliveries opens the log of that connection", async () => {
		const api = stubApi({
			...list(connection({ id: "c5", name: "Five" })),
			"GET /git/connections/:id/deliveries": { body: { items: [] } },
		});
		await renderWithProviders(<GitConnectionsSection />);
		await screen.findByRole("article", { name: "Five" });
		await userEvent
			.setup()
			.click(within(card("Five")).getByRole("button", { name: "Deliveries" }));
		expect(
			await screen.findByRole("dialog", { name: /Webhook deliveries · Five/ }),
		).toBeInTheDocument();
		await screen.findByText("No deliveries yet");
		expect(
			api.calls.some((c) => c.path === "/git/connections/c5/deliveries"),
		).toBe(true);
	});
});
