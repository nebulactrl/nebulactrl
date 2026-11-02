import { describe, expect, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { DeploymentView } from "#/api/gen/types.gen";
import type { Service } from "#/api/models";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { DeploymentsTab } from "./deployments-tab";
import {
	deploymentView,
	logStreamResponse,
	rollbackView,
	service,
} from "./test-fixtures";

const release = (id: string, source: Record<string, string>, imageRef = "") =>
	({
		id,
		imageRef,
		createdAt: "2026-10-31T10:00:00Z",
		source: { type: "image", ...source },
	}) as DeploymentView["release"];

/** What a row's detail (when opened) and the header chips read. */
const detailRoutes = {
	"GET /deployments/:id/events": { body: { items: [] } },
	"GET /deployments/:id/logs": () => logStreamResponse([]),
	"GET /servers": { body: { items: [] } },
};

function listRoutes(items: DeploymentView[]) {
	return {
		...detailRoutes,
		"GET /services/svc-1/deployments": {
			body: make("PageDeploymentView", { items }),
		},
	};
}

async function rowOf(headline: string): Promise<HTMLElement> {
	const li = (await screen.findByText(headline)).closest("li");
	if (!li) throw new Error(`no row for ${headline}`);
	return li;
}

async function renderTab(svc: Service = service()) {
	return renderWithProviders(<DeploymentsTab service={svc} />, {
		path: "/p/proj-1/staging",
		routePath: "/p/$projectId/$envSlug",
	});
}

describe("DeploymentsTab list states", () => {
	test("shows a loading placeholder until the deployments arrive", async () => {
		const gate = Promise.withResolvers<void>();
		stubApi({
			...detailRoutes,
			"GET /projects": { body: { items: [] } },
			"GET /projects/:id/environments": { body: { items: [] } },
			"GET /services/svc-1/deployments": async () => {
				await gate.promise;
				return Response.json({ items: [] });
			},
		});
		await renderTab();

		expect(
			screen.getByRole("status", { name: "Loading deployments" }),
		).toBeInTheDocument();
		gate.resolve();
		expect(await screen.findByText("Not deployed yet")).toBeInTheDocument();
	});

	test("a failed load shows the reason and Retry loads again", async () => {
		let attempt = 0;
		const api = stubApi({
			...detailRoutes,
			"GET /services/svc-1/deployments": () => {
				attempt += 1;
				return attempt === 1
					? Response.json(
							{ code: "internal", detail: "database is down" },
							{ status: 500 },
						)
					: Response.json({ items: [] });
			},
		});
		await renderTab();

		expect(
			await screen.findByText("Deployments did not load"),
		).toBeInTheDocument();
		expect(screen.getByText("database is down")).toBeInTheDocument();

		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Try again" }));

		expect(await screen.findByText("Not deployed yet")).toBeInTheDocument();
		expect(
			api.calls.filter((c) => c.path === "/services/svc-1/deployments"),
		).toHaveLength(2);
	});

	test("an empty history says the service was never deployed", async () => {
		stubApi(listRoutes([]));
		await renderTab();

		expect(await screen.findByText("Not deployed yet")).toBeInTheDocument();
		expect(screen.getByText("0 deployments")).toBeInTheDocument();
		expect(
			screen.queryByRole("list", { name: "Deployments, newest first" }),
		).not.toBeInTheDocument();
	});

	test("requests the newest 50 deployments of the service", async () => {
		const api = stubApi(listRoutes([]));
		await renderTab();
		await screen.findByText("Not deployed yet");
		const call = api.calls.find(
			(c) => c.path === "/services/svc-1/deployments",
		);
		expect(call?.query.get("limit")).toBe("50");
	});

	test("counts the deployments in the heading", async () => {
		stubApi(
			listRoutes([
				deploymentView({ id: "d-2", message: "second" }),
				deploymentView({ id: "d-1", message: "first" }),
			]),
		);
		await renderTab();

		expect(await screen.findByText("2 deployments")).toBeInTheDocument();
		const rows = within(
			screen.getByRole("list", { name: "Deployments, newest first" }),
		).getAllByRole("listitem");
		expect(rows).toHaveLength(2);
		expect(
			within(rows[0] as HTMLElement).getByText("second"),
		).toBeInTheDocument();
	});
});

describe("DeploymentsTab rows", () => {
	test("a note is the headline; the release shows as the detail and the trigger, actor and duration follow", async () => {
		stubApi(
			listRoutes([
				deploymentView({
					id: "d-1",
					message: "ship the fix",
					trigger: "redeploy",
					actorLabel: "Ada",
					startedAt: "2026-10-31T10:00:00Z",
					finishedAt: "2026-10-31T10:01:05Z",
					release: release("rel-1", {
						commitSha: "a91c3f2deadbeef",
						branch: "main",
					}),
				}),
			]),
		);
		await renderTab(service({ currentDeploymentId: "other" }));

		const row = await rowOf("ship the fix");
		// Superseded and succeeded: the word stands alone, no Live badge.
		expect(within(row).queryByText("Live")).not.toBeInTheDocument();
		expect(
			within(row).getByText(
				(_, el) =>
					el?.tagName === "SPAN" &&
					(el.textContent ?? "") ===
						"Succeeded · Redeploy · a91c3f2 · Ada · 1m 05s",
			),
		).toBeInTheDocument();
	});

	test("the serving deployment carries the Live badge", async () => {
		stubApi(
			listRoutes([deploymentView({ id: "d-live", message: "current one" })]),
		);
		await renderTab(service({ currentDeploymentId: "d-live" }));

		expect(
			within(await rowOf("current one")).getByText("Live"),
		).toBeInTheDocument();
	});

	test("a failure reads 'Failed at <step>' for the latest deployment, with the reason as headline", async () => {
		stubApi(
			listRoutes([
				deploymentView({
					id: "d-bad",
					status: "failed",
					statusMessage: "health check timed out",
					message: null,
				}),
			]),
		);
		await renderTab(
			service({
				currentDeploymentId: "d-old",
				latestDeployment: { id: "d-bad", failedStep: "verify" },
			} as Partial<Service>),
		);

		// The status word names the step; the headline carries only the reason.
		const row = await rowOf("health check timed out");
		expect(within(row).getByText("Failed at Verify")).toBeInTheDocument();
	});

	test("an older failed deployment does not borrow the latest one's failed step", async () => {
		stubApi(
			listRoutes([
				deploymentView({ id: "d-new", status: "succeeded", message: "new" }),
				deploymentView({
					id: "d-old-bad",
					status: "failed",
					statusMessage: "build error",
				}),
			]),
		);
		await renderTab(
			service({
				latestDeployment: { id: "d-new", failedStep: "build" },
			} as Partial<Service>),
		);

		const row = await rowOf("build error");
		expect(within(row).getByText("Failed")).toBeInTheDocument();
		expect(within(row).queryByText(/Failed at/)).not.toBeInTheDocument();
	});

	test("a row without a note or release is known by its trigger", async () => {
		stubApi(listRoutes([deploymentView({ id: "d-1", trigger: "git_push" })]));
		await renderTab();

		expect(
			await screen.findByText("Git push", { selector: "span[title]" }),
		).toBeInTheDocument();
	});

	test("a running deployment shows no duration in its meta line", async () => {
		stubApi(
			listRoutes([
				deploymentView({
					id: "d-run",
					status: "building",
					message: "building now",
					finishedAt: null,
				}),
			]),
		);
		await renderTab();

		const row = await rowOf("building now");
		expect(
			within(row).getByText(
				(_, el) =>
					el?.tagName === "SPAN" &&
					el.textContent === "Building · Deploy · Ada",
			),
		).toBeInTheDocument();
	});
});

describe("DeploymentsTab expanding", () => {
	test("a row opens on click to its steps and log, and closes again", async () => {
		stubApi(listRoutes([deploymentView({ id: "d-1", message: "one" })]));
		await renderTab();
		const user = userEvent.setup();
		const toggle = within(
			await screen.findByRole("list", { name: "Deployments, newest first" }),
		).getByRole("button", { expanded: false });
		expect(screen.queryByText("Steps")).not.toBeInTheDocument();

		await user.click(toggle);

		expect(toggle).toHaveAttribute("aria-expanded", "true");
		expect(await screen.findByText("Build and deploy log")).toBeInTheDocument();
		expect(
			screen.getByRole("log", { name: "Deployment log" }),
		).toBeInTheDocument();

		await user.click(toggle);

		expect(toggle).toHaveAttribute("aria-expanded", "false");
		expect(document.getElementById("deployment-d-1")).toHaveAttribute(
			"aria-hidden",
			"true",
		);
	});

	test("the running deployment opens by itself", async () => {
		stubApi(
			listRoutes([
				deploymentView({
					id: "d-run",
					status: "building",
					message: "live build",
					finishedAt: null,
				}),
				deploymentView({ id: "d-old", message: "older" }),
			]),
		);
		await renderTab();

		expect(await screen.findByText("Build and deploy log")).toBeInTheDocument();
		const buttons = screen.getAllByRole("button", { expanded: true });
		expect(buttons).toHaveLength(1);
		const row = await rowOf("live build");
		expect(within(row).getByRole("button", { expanded: true })).toBe(
			buttons[0] as HTMLElement,
		);
	});
});

describe("DeploymentsTab Without cache", () => {
	test("is offered for git services only", async () => {
		stubApi(listRoutes([]));
		await renderTab(service({ sourceType: "image" }));
		await screen.findByText("Not deployed yet");
		expect(
			screen.queryByRole("button", { name: "Without cache" }),
		).not.toBeInTheDocument();
	});

	test("a git service confirms, then posts a noCache deployment", async () => {
		const api = stubApi({
			...listRoutes([deploymentView({ id: "d-1", message: "one" })]),
			"POST /services/svc-1/deployments": {
				status: 202,
				body: deploymentView({ id: "d-2", status: "queued" }),
			},
			"GET /projects": { body: { items: [] } },
			"GET /projects/:id/environments": { body: { items: [] } },
		});
		await renderTab(service({ sourceType: "git" }));
		const user = userEvent.setup();

		await user.click(
			await screen.findByRole("button", { name: "Without cache" }),
		);
		const dialog = await screen.findByRole("alertdialog");
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

	test("is disabled while a deployment is running", async () => {
		stubApi(
			listRoutes([
				deploymentView({
					id: "d-run",
					status: "queued",
					message: "queued one",
					finishedAt: null,
				}),
			]),
		);
		await renderTab(service({ sourceType: "git" }));

		const button = await screen.findByRole("button", { name: "Without cache" });
		expect(button).toBeDisabled();
		expect(button).toHaveAttribute("title", "A deployment is already running");
	});
});

describe("DeploymentsTab row actions", () => {
	test("Cancel deployment confirms, then cancels that deployment", async () => {
		const api = stubApi({
			...listRoutes([
				deploymentView({
					id: "d-run",
					status: "building",
					message: "busy",
					finishedAt: null,
				}),
			]),
			"POST /deployments/d-run/cancel": {
				status: 200,
				body: deploymentView({
					id: "d-run",
					status: "building",
					finishedAt: null,
					cancelRequestedAt: "2026-10-31T10:00:30Z",
				}),
			},
			"GET /projects": { body: { items: [] } },
			"GET /projects/:id/environments": { body: { items: [] } },
		});
		await renderTab();
		const user = userEvent.setup();

		await user.click(
			await screen.findByRole("button", { name: "Cancel deployment" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Cancel deployment" }),
		);

		expect(
			await screen.findByText("Canceling the deployment of api"),
		).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "POST")?.path).toBe(
			"/deployments/d-run/cancel",
		);
	});

	test("a cancel the agent is already acting on shows 'Canceling…' and cannot be repeated", async () => {
		stubApi(
			listRoutes([
				deploymentView({
					id: "d-run",
					status: "building",
					message: "busy",
					finishedAt: null,
					cancelRequestedAt: "2026-10-31T10:00:30Z",
				}),
			]),
		);
		await renderTab();

		const button = await screen.findByRole("button", { name: /Canceling…/ });
		expect(button).toBeDisabled();
		expect(
			screen.queryByRole("button", { name: "Cancel deployment" }),
		).not.toBeInTheDocument();
	});

	test("a finished deployment offers no cancel", async () => {
		stubApi(listRoutes([deploymentView({ id: "d-1", message: "done" })]));
		await renderTab(service({ currentDeploymentId: "d-1" }));
		await screen.findByText("done");
		expect(
			screen.queryByRole("button", { name: /cancel/i }),
		).not.toBeInTheDocument();
	});

	test("an older succeeded release can be rolled back to; the serving one cannot", async () => {
		const api = stubApi({
			...listRoutes([
				deploymentView({
					id: "d-new",
					releaseId: "rel-new",
					message: "serving",
					release: release("rel-new", { commitSha: "bbbbbbb1" }),
				}),
				deploymentView({
					id: "d-old",
					releaseId: "rel-old",
					message: "previous",
					release: release("rel-old", { commitSha: "aaaaaaa1" }),
				}),
			]),
			"POST /deployments/d-old/rollback": {
				status: 202,
				body: rollbackView({ id: "d-3", status: "queued" }),
			},
			"GET /projects": { body: { items: [] } },
			"GET /projects/:id/environments": { body: { items: [] } },
		});
		await renderTab(
			service({
				currentDeploymentId: "d-new",
				currentRelease: { id: "rel-new" },
			} as Partial<Service>),
		);
		const user = userEvent.setup();

		await screen.findByText("serving");
		expect(
			within(await rowOf("serving")).queryByRole("button", {
				name: "Rollback to this release",
			}),
		).not.toBeInTheDocument();
		await user.click(
			within(await rowOf("previous")).getByRole("button", {
				name: "Rollback to this release",
			}),
		);

		const dialog = await screen.findByRole("alertdialog");
		// Environments are unknown here: production wording behind the guard.
		expect(within(dialog).getByText(/aaaaaaa/)).toBeInTheDocument();
		await user.click(
			within(dialog).getByRole("button", { name: "Lift production guard" }),
		);
		await user.click(within(dialog).getByRole("button", { name: "Roll back" }));

		expect(await screen.findByText("Rolling back api")).toBeInTheDocument();
		expect(screen.getByText("Deploying aaaaaaa again.")).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "POST")?.path).toBe(
			"/deployments/d-old/rollback",
		);
	});

	test("a deployment with the release the service already runs has no rollback", async () => {
		stubApi(
			listRoutes([
				deploymentView({
					id: "d-a",
					releaseId: "rel-1",
					message: "same release",
				}),
			]),
		);
		await renderTab(
			service({
				currentDeploymentId: "d-other",
				currentRelease: { id: "rel-1" },
			} as Partial<Service>),
		);
		await screen.findByText("same release");
		expect(
			screen.queryByRole("button", { name: "Rollback to this release" }),
		).not.toBeInTheDocument();
	});

	test("a failed deployment has no rollback", async () => {
		stubApi(
			listRoutes([
				deploymentView({
					id: "d-f",
					status: "failed",
					statusMessage: "bad",
					releaseId: "rel-9",
				}),
			]),
		);
		await renderTab();
		await screen.findByText("bad");
		expect(
			screen.queryByRole("button", { name: "Rollback to this release" }),
		).not.toBeInTheDocument();
	});
});

describe("DeploymentsTab recovery banner", () => {
	test("the latest deployment's recovery notice shows above the list", async () => {
		stubApi(
			listRoutes([
				deploymentView({
					id: "d-bad",
					status: "rolled_back",
					statusMessage: "probe failed",
				}),
			]),
		);
		await renderTab();

		expect(
			await screen.findByText(
				"The new release failed verification and was rolled back",
			),
		).toBeInTheDocument();
	});
});
