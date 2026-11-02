import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Environment } from "#/api/gen/types.gen";
import type { Deployment, Service } from "#/api/models";
import { type ApiStub, stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import {
	deployment,
	deploymentView,
	rollbackView,
	service,
} from "./test-fixtures";
import { useDeploymentActions } from "./use-deployment-actions";

function Harness({
	svc,
	target = deployment(),
	label = "v1.2.3",
}: {
	svc: Service;
	target?: Deployment;
	label?: string;
}) {
	const actions = useDeploymentActions(svc);
	return (
		<div>
			<button type="button" onClick={() => actions.deploy.mutate(undefined)}>
				deploy
			</button>
			<button type="button" onClick={() => actions.deploy.mutate("deadbeef")}>
				deploy commit
			</button>
			<button type="button" onClick={() => void actions.confirmDeployNoCache()}>
				no cache
			</button>
			<button type="button" onClick={() => actions.restart.mutate()}>
				restart
			</button>
			<button type="button" onClick={() => actions.stop.mutate()}>
				stop
			</button>
			<button type="button" onClick={() => void actions.confirmCancel(target)}>
				cancel
			</button>
			<button
				type="button"
				onClick={() => void actions.rollback(target, label)}
			>
				rollback
			</button>
		</div>
	);
}

const environment = (overrides: Partial<Environment> = {}): Environment => ({
	id: "env-prod",
	projectId: "proj-1",
	name: "Production",
	slug: "production",
	isProduction: true,
	kind: "standard",
	parentEnvironmentId: null,
	prNumber: null,
	prTitle: "",
	createdAt: "2026-01-01T00:00:00Z",
	...overrides,
});

const PROJECT_ROUTE = "/p/$projectId/$envSlug";
const PROJECT_PATH = "/p/proj-1/staging";

/** Routes the hook needs besides the action under test: refetches after a change. */
function baseRoutes(
	env = environment({
		id: "env-1",
		name: "Staging",
		slug: "staging",
		isProduction: false,
	}),
) {
	return {
		"GET /projects": { body: { items: [] } },
		"GET /projects/:id/environments": { body: { items: [env] } },
	};
}

async function renderHarness(
	svc: Service,
	extra: Parameters<typeof Harness>[0] extends infer P
		? Partial<P>
		: never = {},
) {
	return renderWithProviders(<Harness svc={svc} {...extra} />, {
		routePath: PROJECT_ROUTE,
		path: PROJECT_PATH,
	});
}

function noUnexpected(api: ApiStub) {
	expect(api.unmatched).toEqual([]);
}

const stagingService = () => service({ environmentId: "env-1" });

describe("deploy", () => {
	test("posts a deployment of the current configuration and toasts it", async () => {
		const api = stubApi({
			...baseRoutes(),
			"POST /services/:id/deployments": {
				status: 202,
				body: deploymentView({ status: "queued" }),
			},
			"GET /services/:id/deployments": { body: { items: [] } },
		});
		await renderHarness(stagingService());

		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "deploy" }));

		expect(await screen.findByText("Deploying api")).toBeInTheDocument();
		expect(
			screen.getByText("Follow it under Deployments."),
		).toBeInTheDocument();
		const post = api.calls.find((call) => call.method === "POST");
		expect(post).toMatchObject({
			path: "/services/svc-1/deployments",
			body: undefined,
		});
		noUnexpected(api);
	});

	test("rebuilds a given commit", async () => {
		const api = stubApi({
			...baseRoutes(),
			"POST /services/:id/deployments": {
				status: 202,
				body: deploymentView(),
			},
		});
		await renderHarness(stagingService());

		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "deploy commit" }));

		await screen.findByText("Deploying api");
		expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
			commitSha: "deadbeef",
		});
	});

	test("a refused deploy toasts the server's reason", async () => {
		stubApi({
			...baseRoutes(),
			"POST /services/:id/deployments": problem(409, {
				code: "deployment_in_progress",
				detail: "Another deployment of api is running.",
			}),
		});
		await renderHarness(stagingService());

		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "deploy" }));

		expect(await screen.findByText("Could not deploy api")).toBeInTheDocument();
		expect(
			screen.getByText("Another deployment of api is running."),
		).toBeInTheDocument();
		expect(screen.queryByText("Deploying api")).not.toBeInTheDocument();
	});
});

describe("deploy without cache", () => {
	test("asks first and deploys with noCache once confirmed", async () => {
		const api = stubApi({
			...baseRoutes(),
			"POST /services/:id/deployments": {
				status: 202,
				body: deploymentView(),
			},
		});
		await renderHarness(stagingService());
		const user = userEvent.setup();

		await user.click(screen.getByRole("button", { name: "no cache" }));
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("Redeploy api without cache?"),
		).toBeInTheDocument();
		expect(api.calls.some((c) => c.method === "POST")).toBe(false);

		await user.click(
			within(dialog).getByRole("button", { name: "Redeploy without cache" }),
		);

		expect(
			await screen.findByText("Deploying api without cache"),
		).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
			noCache: true,
		});
	});

	test("backing out sends nothing", async () => {
		const api = stubApi(baseRoutes());
		await renderHarness(stagingService());
		const user = userEvent.setup();

		await user.click(screen.getByRole("button", { name: "no cache" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));

		await waitFor(() =>
			expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
		);
		expect(api.calls.some((c) => c.method === "POST")).toBe(false);
	});

	test("a failure toasts the reason and still closes the dialog", async () => {
		stubApi({
			...baseRoutes(),
			"POST /services/:id/deployments": problem(422, {
				code: "no_server",
				detail: "The service has no server.",
			}),
		});
		await renderHarness(stagingService());
		const user = userEvent.setup();

		await user.click(screen.getByRole("button", { name: "no cache" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Redeploy without cache" }),
		);

		expect(
			await screen.findByText("Could not deploy api without cache"),
		).toBeInTheDocument();
		expect(screen.getByText("The service has no server.")).toBeInTheDocument();
	});
});

describe("restart and stop", () => {
	test("restart posts the command and toasts", async () => {
		const api = stubApi({
			...baseRoutes(),
			"POST /services/:id/restart": {
				status: 202,
				body: make("ActionResult", { commandId: "cmd-1" }),
			},
		});
		await renderHarness(stagingService());

		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "restart" }));

		expect(await screen.findByText("Restarting api")).toBeInTheDocument();
		expect(
			screen.getByText("The command was sent to its server."),
		).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "POST")?.path).toBe(
			"/services/svc-1/restart",
		);
	});

	test("a failed restart toasts the reason", async () => {
		stubApi({
			...baseRoutes(),
			"POST /services/:id/restart": problem(409, {
				code: "server_offline",
				detail: "edge-1 is offline.",
			}),
		});
		await renderHarness(stagingService());

		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "restart" }));

		expect(
			await screen.findByText("Could not restart api"),
		).toBeInTheDocument();
		expect(screen.getByText("edge-1 is offline.")).toBeInTheDocument();
	});

	test("stop posts to the stop endpoint and toasts", async () => {
		const api = stubApi({
			...baseRoutes(),
			"POST /services/:id/stop": {
				status: 202,
				body: make("ActionResult", { commandId: "cmd-1" }),
			},
		});
		await renderHarness(stagingService());

		await userEvent.setup().click(screen.getByRole("button", { name: "stop" }));

		expect(await screen.findByText("Stopping api")).toBeInTheDocument();
		expect(
			screen.getByText("The container stops; its data stays."),
		).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "POST")?.path).toBe(
			"/services/svc-1/stop",
		);
	});

	test("a failed stop toasts the reason", async () => {
		stubApi({
			...baseRoutes(),
			"POST /services/:id/stop": problem(500, {
				code: "internal",
				detail: "Agent unreachable.",
			}),
		});
		await renderHarness(stagingService());

		await userEvent.setup().click(screen.getByRole("button", { name: "stop" }));

		expect(await screen.findByText("Could not stop api")).toBeInTheDocument();
		expect(screen.getByText("Agent unreachable.")).toBeInTheDocument();
	});
});

describe("cancel", () => {
	const running = deployment({ id: "dep-run", status: "building" });

	test("asks first, then cancels that deployment", async () => {
		const api = stubApi({
			...baseRoutes(),
			"POST /deployments/:id/cancel": {
				status: 200,
				body: deploymentView({
					id: "dep-run",
					status: "building",
					finishedAt: null,
					cancelRequestedAt: "2026-10-31T10:00:30Z",
				}),
			},
		});
		await renderHarness(stagingService(), { target: running });
		const user = userEvent.setup();

		await user.click(screen.getByRole("button", { name: "cancel" }));
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("Cancel this deployment of api?"),
		).toBeInTheDocument();
		// The dismiss button names what is kept.
		expect(
			within(dialog).getByRole("button", { name: "Keep running" }),
		).toBeInTheDocument();
		expect(api.calls.some((c) => c.method === "POST")).toBe(false);

		await user.click(
			within(dialog).getByRole("button", { name: "Cancel deployment" }),
		);

		expect(
			await screen.findByText("Canceling the deployment of api"),
		).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "POST")?.path).toBe(
			"/deployments/dep-run/cancel",
		);
	});

	test("keeping it running sends nothing", async () => {
		const api = stubApi(baseRoutes());
		await renderHarness(stagingService(), { target: running });
		const user = userEvent.setup();

		await user.click(screen.getByRole("button", { name: "cancel" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Keep running" }),
		);

		await waitFor(() =>
			expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
		);
		expect(api.calls.some((c) => c.method === "POST")).toBe(false);
	});

	test("cannot_cancel says it is too late and refetches where the deployment is now", async () => {
		const api = stubApi({
			...baseRoutes(),
			"POST /deployments/:id/cancel": problem(409, {
				code: "cannot_cancel",
				detail: "The deployment already finished.",
			}),
		});
		await renderHarness(stagingService(), { target: running });
		const user = userEvent.setup();

		await user.click(screen.getByRole("button", { name: "cancel" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Cancel deployment" }),
		);

		expect(await screen.findByText("Too late to cancel")).toBeInTheDocument();
		expect(
			screen.getByText("The deployment already finished."),
		).toBeInTheDocument();
		expect(
			screen.queryByText("Could not cancel the deployment of api"),
		).not.toBeInTheDocument();
		noUnexpected(api);
	});

	test("any other failure names the service", async () => {
		stubApi({
			...baseRoutes(),
			"POST /deployments/:id/cancel": problem(500, {
				code: "internal",
				detail: "Agent unreachable.",
			}),
		});
		await renderHarness(stagingService(), { target: running });
		const user = userEvent.setup();

		await user.click(screen.getByRole("button", { name: "cancel" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Cancel deployment" }),
		);

		expect(
			await screen.findByText("Could not cancel the deployment of api"),
		).toBeInTheDocument();
		expect(screen.getByText("Agent unreachable.")).toBeInTheDocument();
	});
});

describe("rollback", () => {
	const target = deployment({
		id: "dep-old",
		releaseId: "rel-old",
		message: "before the migration",
	});

	test("names the environment and the moment, then rolls back", async () => {
		const api = stubApi({
			...baseRoutes(),
			"POST /deployments/:id/rollback": {
				status: 202,
				body: rollbackView({ id: "dep-new", status: "queued" }),
			},
		});
		await renderHarness(stagingService(), { target });
		const user = userEvent.setup();

		await user.click(screen.getByRole("button", { name: "rollback" }));
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText(/^Roll back api in Staging to the release from/),
		).toBeInTheDocument();
		expect(within(dialog).getByText(/v1\.2\.3/)).toBeInTheDocument();
		expect(
			within(dialog).getByText(/before the migration/),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText(
				/Databases, volumes and external services are not rolled back/,
			),
		).toBeInTheDocument();

		await user.click(within(dialog).getByRole("button", { name: "Roll back" }));

		expect(await screen.findByText("Rolling back api")).toBeInTheDocument();
		expect(screen.getByText("Deploying v1.2.3 again.")).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "POST")?.path).toBe(
			"/deployments/dep-old/rollback",
		);
	});

	test("production rolls back only after the guard is lifted", async () => {
		const api = stubApi({
			...baseRoutes(
				environment({
					id: "env-1",
					name: "Production",
					slug: "staging",
					isProduction: true,
				}),
			),
			"POST /deployments/:id/rollback": {
				status: 202,
				body: rollbackView({ id: "dep-new", status: "queued" }),
			},
		});
		await renderHarness(stagingService(), { target });
		const user = userEvent.setup();

		await user.click(screen.getByRole("button", { name: "rollback" }));
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText(
				/^Roll back api in production to the release from/,
			),
		).toBeInTheDocument();
		const cover = within(dialog).getByRole("button", {
			name: "Lift production guard",
		});
		expect(cover).toHaveAttribute("aria-expanded", "false");

		// A press on the key behind the closed guard does nothing.
		await user.click(within(dialog).getByRole("button", { name: "Roll back" }));
		expect(api.calls.some((c) => c.method === "POST")).toBe(false);

		await user.click(cover);
		await user.click(within(dialog).getByRole("button", { name: "Roll back" }));

		expect(await screen.findByText("Rolling back api")).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "POST")?.path).toBe(
			"/deployments/dep-old/rollback",
		);
	});

	test("production: backing out sends nothing", async () => {
		const api = stubApi(
			baseRoutes(
				environment({
					id: "env-1",
					name: "Production",
					slug: "staging",
					isProduction: true,
				}),
			),
		);
		await renderHarness(stagingService(), { target });
		const user = userEvent.setup();

		await user.click(screen.getByRole("button", { name: "rollback" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));

		await waitFor(() =>
			expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
		);
		expect(api.calls.some((c) => c.method === "POST")).toBe(false);
	});

	test("an unknown environment is treated as production: guarded, and named generically", async () => {
		stubApi({
			"GET /projects": { body: { items: [] } },
			"GET /projects/:id/environments": { body: { items: [] } },
		});
		await renderHarness(stagingService(), { target });
		const user = userEvent.setup();

		await user.click(screen.getByRole("button", { name: "rollback" }));
		const dialog = await screen.findByRole("alertdialog");

		expect(
			within(dialog).getByText(
				/^Roll back api in this environment to the release from/,
			),
		).toBeInTheDocument();
		expect(
			within(dialog).getByRole("button", { name: "Lift production guard" }),
		).toBeInTheDocument();
	});

	test("backing out of the rollback dialog sends nothing and toasts nothing", async () => {
		const api = stubApi(baseRoutes());
		await renderHarness(stagingService(), { target });
		const user = userEvent.setup();

		await user.click(screen.getByRole("button", { name: "rollback" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));

		await waitFor(() =>
			expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
		);
		expect(api.calls.some((c) => c.method === "POST")).toBe(false);
		expect(screen.queryByText("Rolling back api")).not.toBeInTheDocument();
	});

	test("a failure stays in the dialog with the reason and does not toast success", async () => {
		stubApi({
			...baseRoutes(),
			"POST /deployments/:id/rollback": problem(409, {
				code: "release_gone",
				detail: "The release was deleted.",
			}),
		});
		await renderHarness(stagingService(), { target });
		const user = userEvent.setup();

		await user.click(screen.getByRole("button", { name: "rollback" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Roll back" }));

		expect(
			await within(dialog).findByText("The release was deleted."),
		).toBeInTheDocument();
		expect(screen.queryByText("Rolling back api")).not.toBeInTheDocument();
	});

	test("a pruned image offers to rebuild the commit, then deploys it", async () => {
		const api = stubApi({
			...baseRoutes(),
			"POST /deployments/:id/rollback": problem(409, {
				code: "image_pruned",
				detail: "The image was pruned.",
				details: { commitSha: "0123456789abcdef" },
			}),
			"POST /services/:id/deployments": {
				status: 202,
				body: deploymentView(),
			},
		});
		await renderHarness(
			service({
				environmentId: "env-1",
				config: { gitUrl: "https://git.example/acme/api.git" },
			} as Partial<Service>),
			{ target },
		);
		const user = userEvent.setup();

		await user.click(screen.getByRole("button", { name: "rollback" }));
		await user.click(
			within(await screen.findByRole("alertdialog")).getByRole("button", {
				name: "Roll back",
			}),
		);

		const rebuild = await screen.findByText("The image for 0123456 was pruned");
		expect(rebuild).toBeInTheDocument();
		const dialog = screen.getByRole("alertdialog");
		expect(
			within(dialog).getByText(/from https:\/\/git\.example\/acme\/api\.git/),
		).toBeInTheDocument();
		// The rollback itself was reported as an answer, not as a success.
		expect(screen.queryByText("Rolling back api")).not.toBeInTheDocument();

		await user.click(
			within(dialog).getByRole("button", { name: "Rebuild commit 0123456" }),
		);

		expect(await screen.findByText("Deploying api")).toBeInTheDocument();
		const posts = api.calls.filter((c) => c.method === "POST");
		expect(posts.map((c) => c.path)).toEqual([
			"/deployments/dep-old/rollback",
			"/services/svc-1/deployments",
		]);
		expect(posts[1]?.body).toEqual({ commitSha: "0123456789abcdef" });
	});

	test("a pruned image without a commit is reported as a failed rollback", async () => {
		stubApi({
			...baseRoutes(),
			"POST /deployments/:id/rollback": problem(409, {
				code: "image_pruned",
				detail: "The image was pruned.",
			}),
		});
		await renderHarness(stagingService(), { target });
		const user = userEvent.setup();

		await user.click(screen.getByRole("button", { name: "rollback" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Roll back" }));

		expect(
			await within(dialog).findByText("The image was pruned."),
		).toBeInTheDocument();
		expect(screen.queryByText(/was pruned$/)).not.toBeInTheDocument();
	});

	test("a git service with no repository URL names 'its repository' in the rebuild prompt", async () => {
		stubApi({
			...baseRoutes(),
			"POST /deployments/:id/rollback": problem(409, {
				code: "image_pruned",
				detail: "pruned",
				details: { commitSha: "abcdef0123" },
			}),
		});
		await renderHarness(stagingService(), { target });
		const user = userEvent.setup();

		await user.click(screen.getByRole("button", { name: "rollback" }));
		await user.click(
			within(await screen.findByRole("alertdialog")).getByRole("button", {
				name: "Roll back",
			}),
		);

		expect(await screen.findByText(/from its repository/)).toBeInTheDocument();
	});
});
