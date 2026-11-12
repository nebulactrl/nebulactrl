import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { GitLinkView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { GitLinkRows } from "./git-link-section";
import {
	connection,
	link,
	linkWithoutProvider,
	repo,
	service,
} from "./test-fixtures";

const GET_LINK = "GET /services/:id/git";
const PUT_LINK = "PUT /services/:id/git";
const DEL_LINK = "DELETE /services/:id/git";
const CONNS = "GET /git/connections";

const unlinked = (): GitLinkView =>
	linkWithoutProvider({
		linked: false,
		connectionId: "",
		connectionName: "",
		repo: "",
		repoUrl: "",
		autoDeploy: false,
		waitForCi: false,
	});

async function render(dirty = false) {
	return renderWithProviders(<GitLinkRows service={service()} dirty={dirty} />);
}

const putCall = (api: ReturnType<typeof stubApi>) =>
	api.calls.find((c) => c.method === "PUT");

/** The body of the PUT the test expects to have been sent. */
const putBody = <T,>(api: ReturnType<typeof stubApi>): T => {
	const call = putCall(api);
	if (!call) throw new Error("no PUT request was sent");
	return call.body as T;
};

describe("GitLinkRows without a link", () => {
	test("points to Integrations when no provider is usable", async () => {
		stubApi({
			[GET_LINK]: { body: unlinked() },
			[CONNS]: { body: { items: [connection({ status: "pending" })] } },
		});
		await render();
		expect(
			await screen.findByText(/No provider is connected\./),
		).toBeInTheDocument();
		expect(
			screen.getByRole("link", { name: /Connect GitHub, GitLab, Gitea/ }),
		).toHaveAttribute("href", "/settings/integrations");
		expect(
			screen.queryByRole("button", { name: "Pick a repository" }),
		).not.toBeInTheDocument();
	});

	test("offers Pick a repository when a connection is usable", async () => {
		stubApi({
			[GET_LINK]: { body: unlinked() },
			[CONNS]: { body: { items: [connection()] } },
		});
		await render();
		expect(
			await screen.findByRole("button", { name: "Pick a repository" }),
		).toBeEnabled();
		expect(
			screen.queryByText("Save or discard your edits first."),
		).not.toBeInTheDocument();
	});

	test("unsaved edits block picking and say why", async () => {
		stubApi({
			[GET_LINK]: { body: unlinked() },
			[CONNS]: { body: { items: [connection()] } },
		});
		await render(true);
		expect(
			await screen.findByRole("button", { name: "Pick a repository" }),
		).toBeDisabled();
		expect(
			screen.getByText("Save or discard your edits first."),
		).toBeInTheDocument();
	});

	describe("the link dialog", () => {
		const routes = () => ({
			[GET_LINK]: { body: unlinked() },
			[CONNS]: { body: { items: [connection()] } },
			"GET /git/connections/:id/repos": { body: { items: [repo()] } },
			"GET /git/connections/:id/branches": { body: { items: ["main", "dev"] } },
			[PUT_LINK]: { body: link() },
		});

		async function openAndPick() {
			const api = stubApi(routes());
			await render();
			const user = userEvent.setup();
			await user.click(
				await screen.findByRole("button", { name: "Pick a repository" }),
			);
			const dialog = await screen.findByRole("dialog", {
				name: "Pick a repository",
			});
			await user.click(await within(dialog).findByText("acme/api"));
			return { api, user, dialog };
		}

		test("Link repository stays disabled until a repository is picked", async () => {
			stubApi(routes());
			await render();
			const user = userEvent.setup();
			await user.click(
				await screen.findByRole("button", { name: "Pick a repository" }),
			);
			const dialog = await screen.findByRole("dialog", {
				name: "Pick a repository",
			});
			await within(dialog).findByText("acme/api");
			expect(
				within(dialog).getByRole("button", { name: "Link repository" }),
			).toBeDisabled();
		});

		test("links with branch, auto-deploy on and empty watch paths", async () => {
			const { api, user, dialog } = await openAndPick();
			await user.click(
				within(dialog).getByRole("button", { name: "Link repository" }),
			);
			expect(
				await screen.findByText("api deploys from acme/api"),
			).toBeInTheDocument();
			expect(
				screen.getByText("Every push to main deploys it."),
			).toBeInTheDocument();
			expect(putCall(api)?.path).toBe("/services/svc-1/git");
			expect(putCall(api)?.body).toEqual({
				connectionId: "c1",
				repo: "acme/api",
				branch: "main",
				autoDeploy: true,
				watchPaths: [],
			});
			await waitFor(() =>
				expect(
					screen.queryByRole("dialog", { name: "Pick a repository" }),
				).not.toBeInTheDocument(),
			);
		});

		test("watch paths are parsed into the request", async () => {
			const { api, user, dialog } = await openAndPick();
			await user.click(within(dialog).getByLabelText("Watch paths (monorepo)"));
			await user.paste("apps/api/**\n!**/*.md\napps/api/**");
			await user.click(
				within(dialog).getByRole("button", { name: "Link repository" }),
			);
			await screen.findByText("api deploys from acme/api");
			expect(putBody<{ watchPaths: string[] }>(api).watchPaths).toEqual([
				"apps/api/**",
				"!**/*.md",
			]);
		});

		test("an invalid watch path shows the problem and blocks linking", async () => {
			const { user, dialog } = await openAndPick();
			await user.click(within(dialog).getByLabelText("Watch paths (monorepo)"));
			await user.paste("../secrets");
			expect(
				within(dialog).getByText('"../secrets" must not contain ..'),
			).toBeInTheDocument();
			expect(
				within(dialog).getByLabelText("Watch paths (monorepo)"),
			).toHaveAttribute("aria-invalid", "true");
			expect(
				within(dialog).getByRole("button", { name: "Link repository" }),
			).toBeDisabled();
		});

		test("turning auto-deploy off hides watch paths and says so in the toast", async () => {
			const { api, user, dialog } = await openAndPick();
			await user.click(
				within(dialog).getByRole("switch", { name: "Deploy on every push" }),
			);
			expect(
				within(dialog).queryByLabelText("Watch paths (monorepo)"),
			).not.toBeInTheDocument();
			await user.click(
				within(dialog).getByRole("button", { name: "Link repository" }),
			);
			expect(
				await screen.findByText("Auto-deploy is off: deploy it by hand."),
			).toBeInTheDocument();
			expect(putBody<{ autoDeploy: boolean }>(api).autoDeploy).toBe(false);
		});

		test("a rejected link toasts the reason and keeps the dialog", async () => {
			stubApi({
				...routes(),
				[PUT_LINK]: problem(409, {
					title: "x",
					detail: "webhook could not be created",
				}),
			});
			await render();
			const user = userEvent.setup();
			await user.click(
				await screen.findByRole("button", { name: "Pick a repository" }),
			);
			const dialog = await screen.findByRole("dialog", {
				name: "Pick a repository",
			});
			await user.click(await within(dialog).findByText("acme/api"));
			await user.click(
				within(dialog).getByRole("button", { name: "Link repository" }),
			);
			expect(
				await screen.findByText("Could not link the repository"),
			).toBeInTheDocument();
			expect(
				screen.getByText("webhook could not be created"),
			).toBeInTheDocument();
			expect(dialog).toBeInTheDocument();
		});

		test("Cancel closes without linking", async () => {
			const { api, user, dialog } = await openAndPick();
			await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
			await waitFor(() =>
				expect(
					screen.queryByRole("dialog", { name: "Pick a repository" }),
				).not.toBeInTheDocument(),
			);
			expect(putCall(api)).toBeUndefined();
		});
	});
});

describe("GitLinkRows with a link", () => {
	function setup(
		over: Partial<GitLinkView> = {},
		dirty = false,
		body = link(over),
	) {
		const api = stubApi({
			[GET_LINK]: { body },
			[CONNS]: { body: { items: [connection()] } },
			"GET /git/connections/:id/repos": { body: { items: [repo()] } },
			"GET /git/connections/:id/branches": { body: { items: ["main", "dev"] } },
			[PUT_LINK]: { body: link() },
			[DEL_LINK]: { status: 204 },
		});
		return { api, ready: render(dirty) };
	}

	test("shows the repository, its connection and provider", async () => {
		await setup().ready;
		const a = await screen.findByRole("link", { name: /acme\/api/ });
		expect(a).toHaveAttribute("href", "https://github.com/acme/api");
		expect(a).toHaveAttribute("target", "_blank");
		expect(screen.getByText("Acme GitHub · GitHub")).toBeInTheDocument();
	});

	test("a link without a provider falls back to Gitea's mark and label", async () => {
		await setup({}, false, linkWithoutProvider({ connectionName: "Mine" }))
			.ready;
		expect(await screen.findByText("Mine · Gitea")).toBeInTheDocument();
	});

	test.each([
		[
			{ mode: "app", status: "active", message: "", url: "" },
			"GitHub App webhook",
		],
		[
			{ mode: "registered", status: "active", message: "", url: "" },
			"Webhook registered",
		],
	] as const)("webhook %j reads %s", async (webhook, label) => {
		await setup({ webhook: { ...webhook } }).ready;
		expect(await screen.findByText(label)).toBeInTheDocument();
		expect(screen.queryByText("Webhook problem")).not.toBeInTheDocument();
	});

	test("a webhook in error shows the problem and its message", async () => {
		await setup({
			webhook: {
				mode: "registered",
				status: "error",
				message: "hook deleted upstream",
				url: "",
			},
		}).ready;
		expect(await screen.findByText("Webhook problem")).toBeInTheDocument();
		expect(screen.getByText("hook deleted upstream")).toBeInTheDocument();
	});

	test("a manual webhook explains it must be created by hand", async () => {
		await setup({
			provider: "bitbucket",
			webhook: { mode: "manual", status: "active", message: "", url: "" },
		}).ready;
		expect(
			await screen.findByText(/create it by hand at Bitbucket/),
		).toBeInTheDocument();
		expect(screen.queryByText("Webhook registered")).not.toBeInTheDocument();
	});

	test("unsaved edits disable Change repository", async () => {
		await setup({}, true).ready;
		expect(
			await screen.findByRole("button", { name: "Change repository" }),
		).toBeDisabled();
		expect(
			screen.getByText(/Save or discard your edits before changing/),
		).toBeInTheDocument();
	});

	test("Change repository opens the dialog seeded from the link and saves", async () => {
		const { api, ready } = setup({
			autoDeploy: false,
			watchPaths: ["apps/**"],
		});
		await ready;
		const user = userEvent.setup();
		await user.click(
			await screen.findByRole("button", { name: "Change repository" }),
		);
		const dialog = await screen.findByRole("dialog", {
			name: "Change repository",
		});
		expect(
			within(dialog).getByRole("switch", { name: "Deploy on every push" }),
		).not.toBeChecked();
		await user.click(await within(dialog).findByText("acme/api"));
		await user.click(
			within(dialog).getByRole("switch", { name: "Deploy on every push" }),
		);
		expect(within(dialog).getByLabelText("Watch paths (monorepo)")).toHaveValue(
			"apps/**",
		);
		await user.click(within(dialog).getByRole("button", { name: "Save" }));
		await screen.findByText("api deploys from acme/api");
		expect(putCall(api)?.body).toEqual({
			connectionId: "c1",
			repo: "acme/api",
			branch: "main",
			autoDeploy: true,
			watchPaths: ["apps/**"],
		});
	});

	test("the deploy-on-push switch reflects the link and updates it at once", async () => {
		const { api, ready } = setup({ autoDeploy: true });
		await ready;
		const sw = await screen.findByRole("switch", {
			name: "Deploy on every push",
		});
		expect(sw).toBeChecked();
		await userEvent.setup().click(sw);
		await waitFor(() => expect(putCall(api)).toBeDefined());
		expect(putCall(api)?.body).toEqual({
			connectionId: "c1",
			repo: "acme/api",
			autoDeploy: false,
		});
	});

	test("a failed toggle toasts the reason", async () => {
		stubApi({
			[GET_LINK]: { body: link() },
			[CONNS]: { body: { items: [connection()] } },
			[PUT_LINK]: problem(500, { title: "x", detail: "db down" }),
		});
		await render();
		await userEvent
			.setup()
			.click(
				await screen.findByRole("switch", { name: "Deploy on every push" }),
			);
		expect(await screen.findByText("Could not save")).toBeInTheDocument();
		expect(screen.getByText("db down")).toBeInTheDocument();
	});

	test("a queued push is announced with its short sha", async () => {
		await setup({ queuedCommit: "bffeb74224043ba2feb48e12f2ac4a0a9de3e04c" })
			.ready;
		expect(
			await screen.findByText(/A push \(bffeb74\) is waiting/),
		).toBeInTheDocument();
	});

	test("the wait-for-CI switch reflects the link and updates it at once", async () => {
		const { api, ready } = setup({ waitForCi: false });
		await ready;
		const sw = await screen.findByRole("switch", {
			name: "Wait for CI to pass",
		});
		expect(sw).not.toBeChecked();
		expect(sw).not.toHaveAttribute("aria-disabled", "true");
		expect(
			screen.getByText(/Deploys a push only after its checks pass\./),
		).toBeInTheDocument();
		await userEvent.setup().click(sw);
		await waitFor(() => expect(putCall(api)).toBeDefined());
		expect(putCall(api)?.body).toEqual({
			connectionId: "c1",
			repo: "acme/api",
			waitForCi: true,
		});
	});

	test("a push waiting for CI is announced with its short sha", async () => {
		await setup({
			waitForCi: true,
			waitingForCi: {
				sha: "bffeb74224043ba2feb48e12f2ac4a0a9de3e04c",
				message: "Add services folder",
				author: "Ada",
				branch: "main",
				since: new Date(Date.now() - 5 * 60_000).toISOString(),
			},
		}).ready;
		expect(
			await screen.findByText(/Waiting for CI on bffeb74 since 5m ago/),
		).toBeInTheDocument();
		expect(
			screen.getByRole("switch", { name: "Wait for CI to pass" }),
		).toBeChecked();
	});

	test("a commit wait-for-CI refused is shown with why", async () => {
		await setup({
			waitForCi: true,
			ciBlocked: {
				sha: "e73651fd1f8dc509a08dad413f52bfb4490260f7",
				branch: "main",
				reason: "CI failed: ci/test",
				at: new Date(Date.now() - 5 * 60_000).toISOString(),
			},
		}).ready;
		expect(
			await screen.findByText(
				/e73651f was not deployed \(5m ago\): CI failed: ci\/test/,
			),
		).toBeInTheDocument();
	});

	test("a GitHub App that cannot read checks is called out", async () => {
		const warning =
			"The GitHub App may not read this repository's CI results, so Wait for CI refuses every push: grant it 'Checks: read'.";
		await setup({
			waitForCi: true,
			ciWarning: warning,
			ciBlocked: {
				sha: "e73651fd1f8dc509a08dad413f52bfb4490260f7",
				branch: "main",
				reason: "NebulaCtrl cannot read check runs",
				at: new Date().toISOString(),
			},
		}).ready;
		expect(await screen.findByText(warning)).toBeInTheDocument();
		expect(
			screen.queryByText(/NebulaCtrl cannot read check runs/),
		).not.toBeInTheDocument();
	});

	test("a Bitbucket link cannot wait for CI", async () => {
		await setup({
			provider: "bitbucket",
			webhook: { mode: "manual", status: "active", message: "", url: "" },
		}).ready;
		expect(
			await screen.findByRole("switch", { name: "Wait for CI to pass" }),
		).toHaveAttribute("aria-disabled", "true");
		expect(
			screen.getByText("This connection cannot read CI results."),
		).toBeInTheDocument();
	});

	test("a webhook-only connection cannot wait for CI", async () => {
		stubApi({
			[GET_LINK]: { body: link() },
			[CONNS]: { body: { items: [connection({ authType: "webhook_only" })] } },
		});
		await render();
		expect(
			await screen.findByRole("switch", { name: "Wait for CI to pass" }),
		).toHaveAttribute("aria-disabled", "true");
	});

	test("a connection that can read CI shows no note", async () => {
		await setup().ready;
		await screen.findByRole("switch", { name: "Wait for CI to pass" });
		expect(
			screen.queryByText("This connection cannot read CI results."),
		).not.toBeInTheDocument();
	});

	test("no queued push, no notice", async () => {
		await setup().ready;
		await screen.findByRole("switch", { name: "Deploy on every push" });
		expect(
			screen.queryByText(/is waiting for the running deployment/),
		).not.toBeInTheDocument();
	});

	describe("watch paths", () => {
		test("start from the saved list with no save or reset offered", async () => {
			await setup({ watchPaths: ["apps/a/**", "docs/**"] }).ready;
			const field = await screen.findByLabelText("Watch paths");
			expect(field).toHaveValue("apps/a/**\ndocs/**");
			expect(
				screen.queryByRole("button", { name: "Save watch paths" }),
			).not.toBeInTheDocument();
		});

		test("editing offers Save, which sends only the paths", async () => {
			const { api, ready } = setup({ watchPaths: ["apps/a/**"] });
			await ready;
			const user = userEvent.setup();
			const field = await screen.findByLabelText("Watch paths");
			await user.click(field);
			await user.paste("\n!**/*.md");
			await user.click(
				screen.getByRole("button", { name: "Save watch paths" }),
			);
			await waitFor(() => expect(putCall(api)).toBeDefined());
			expect(putCall(api)?.body).toEqual({
				connectionId: "c1",
				repo: "acme/api",
				watchPaths: ["apps/a/**", "!**/*.md"],
			});
		});

		test("Reset puts the saved paths back", async () => {
			const { ready } = setup({ watchPaths: ["apps/a/**"] });
			await ready;
			const user = userEvent.setup();
			const field = await screen.findByLabelText("Watch paths");
			await user.click(field);
			await user.paste("x");
			expect(field).toHaveValue("apps/a/**x");
			await user.click(screen.getByRole("button", { name: "Reset" }));
			expect(field).toHaveValue("apps/a/**");
			expect(
				screen.queryByRole("button", { name: "Reset" }),
			).not.toBeInTheDocument();
		});

		test("an invalid path disables Save and names the problem", async () => {
			await setup().ready;
			const user = userEvent.setup();
			await user.click(await screen.findByLabelText("Watch paths"));
			await user.paste("a/../b");
			expect(
				screen.getByText('"a/../b" must not contain ..'),
			).toBeInTheDocument();
			expect(
				screen.getByRole("button", { name: "Save watch paths" }),
			).toBeDisabled();
		});

		test("clearing the saved paths is a change that can be saved", async () => {
			const { api, ready } = setup({ watchPaths: ["apps/a/**"] });
			await ready;
			const user = userEvent.setup();
			await user.clear(await screen.findByLabelText("Watch paths"));
			await user.click(
				screen.getByRole("button", { name: "Save watch paths" }),
			);
			await waitFor(() => expect(putCall(api)).toBeDefined());
			expect(putBody<{ watchPaths: string[] }>(api).watchPaths).toEqual([]);
		});
	});

	describe("unlinking", () => {
		test("asks first, then deletes the link and says what stays", async () => {
			const { api, ready } = setup();
			await ready;
			const user = userEvent.setup();
			await user.click(await screen.findByRole("button", { name: "Unlink" }));
			const dialog = await screen.findByRole("alertdialog", {
				name: /Unlink api from acme\/api\?/,
			});
			await user.click(within(dialog).getByRole("button", { name: "Unlink" }));
			expect(await screen.findByText("Unlinked")).toBeInTheDocument();
			expect(
				screen.getByText(/api keeps its repository URL/),
			).toBeInTheDocument();
			const del = api.calls.find((c) => c.method === "DELETE");
			expect(del?.path).toBe("/services/svc-1/git");
		});

		test("cancelling keeps the link", async () => {
			const { api, ready } = setup();
			await ready;
			const user = userEvent.setup();
			await user.click(await screen.findByRole("button", { name: "Unlink" }));
			const dialog = await screen.findByRole("alertdialog");
			await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
			await waitFor(() =>
				expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
			);
			expect(api.calls.some((c) => c.method === "DELETE")).toBe(false);
		});

		test("a failed unlink toasts the reason", async () => {
			stubApi({
				[GET_LINK]: { body: link() },
				[CONNS]: { body: { items: [connection()] } },
				[DEL_LINK]: problem(500, {
					title: "x",
					detail: "cannot reach git host",
				}),
			});
			await render();
			const user = userEvent.setup();
			await user.click(await screen.findByRole("button", { name: "Unlink" }));
			const dialog = await screen.findByRole("alertdialog");
			await user.click(within(dialog).getByRole("button", { name: "Unlink" }));
			expect(await screen.findByText("Could not unlink")).toBeInTheDocument();
		});
	});
});
