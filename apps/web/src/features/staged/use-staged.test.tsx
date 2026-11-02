import { describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import type { Service } from "#/api/models";
import { makeService } from "#/features/canvas/lib/fixtures";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import {
	type DeployRequest,
	useStagedActions,
	useStagedChanges,
} from "./use-staged";

type Actions = ReturnType<typeof useStagedActions>;

/** Mounts the actions hook and hands its mutations to the test's buttons. */
function Actions({
	services,
	children,
}: {
	services: readonly Service[];
	children: (a: Actions) => ReactNode;
}) {
	return <>{children(useStagedActions("env1", services))}</>;
}

function Changes({ services }: { services: readonly Service[] }) {
	const { entries, loading } = useStagedChanges(services);
	return (
		<div>
			<p data-testid="loading">{String(loading)}</p>
			<ul>
				{entries.map((e) => (
					<li key={e.service.id}>
						{e.service.name}:{" "}
						{e.changes.map((c) => c.field).join(",") || "none"}
					</li>
				))}
			</ul>
		</div>
	);
}

const deployment = (id: string, serviceId: string) =>
	make("DeploymentView", {
		id,
		serviceId,
		status: "queued",
		trigger: "staged",
		actorType: "user",
		actorLabel: "robin",
		strategy: "rolling",
		generation: 2,
		createdAt: "2026-10-31T10:00:00Z",
		releaseId: null,
		serverId: null,
		buildServerId: null,
		statusMessage: "",
		rolledBackFrom: null,
		cancelRequestedAt: null,
		cancelRequestedBy: "",
		canceledAtStep: "",
		startedAt: null,
		finishedAt: null,
		message: null,
	});

const change = (field: string) => ({ field, from: "a", to: "b" });
const calls = (api: { calls: { method: string; path: string }[] }) =>
	api.calls.map((c) => `${c.method} ${c.path}`);

function None() {
	const { entries, loading } = useStagedChanges(undefined);
	return (
		<p data-testid="r">
			{entries.length} {String(loading)}
		</p>
	);
}

function Button() {
	return (
		<Actions services={[]}>
			{(a) => (
				<button
					type="button"
					onClick={() =>
						a.discardChange.mutate({ serviceId: "web", field: "config.port" })
					}
				>
					discard
				</button>
			)}
		</Actions>
	);
}

describe("useStagedChanges", () => {
	test("loads pending changes only for services that have staged changes", async () => {
		const api = stubApi({
			"GET /services/web/pending-changes": {
				body: { items: [change("config.port"), change("variables.A")] },
			},
			"GET /services/db/pending-changes": { body: { items: null } },
		});
		const services = [
			makeService({ id: "web", name: "Web", hasStagedChanges: true }),
			makeService({ id: "idle", name: "Idle", hasStagedChanges: false }),
			makeService({ id: "db", name: "DB", hasStagedChanges: true }),
		];
		await renderWithProviders(<Changes services={services} />);
		expect(
			await screen.findByText("Web: config.port,variables.A"),
		).toBeInTheDocument();
		expect(screen.getByText("DB: none")).toBeInTheDocument();
		expect(screen.queryByText(/Idle/)).toBeNull();
		await waitFor(() =>
			expect(screen.getByTestId("loading")).toHaveTextContent("false"),
		);
		expect(calls(api).sort()).toEqual([
			"GET /services/db/pending-changes",
			"GET /services/web/pending-changes",
		]);
	});

	test("reports loading until every list arrived, and nothing for no services", async () => {
		stubApi({
			"GET /services/web/pending-changes": () =>
				new Promise<Response>(() => {}),
		});
		await renderWithProviders(
			<Changes
				services={[
					makeService({ id: "web", name: "Web", hasStagedChanges: true }),
				]}
			/>,
		);
		expect(screen.getByTestId("loading")).toHaveTextContent("true");
		expect(screen.getByText("Web: none")).toBeInTheDocument();
	});

	test("undefined services yield no entries and are not loading", async () => {
		const api = stubApi({});
		await renderWithProviders(<None />);
		expect(screen.getByTestId("r")).toHaveTextContent("0 false");
		expect(api.calls).toEqual([]);
	});
});

describe("useStagedActions.discardChange", () => {
	test("deletes exactly that field", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"DELETE /services/web/pending-changes/:field": {
				body: make("ServiceView"),
			},
		});
		await renderWithProviders(<Button />);
		await user.click(screen.getByRole("button", { name: "discard" }));
		await waitFor(() => expect(api.calls).toHaveLength(1));
		expect(api.calls[0]).toMatchObject({
			method: "DELETE",
			path: "/services/web/pending-changes/config.port",
		});
	});

	test("a failure is shown as a toast", async () => {
		const user = userEvent.setup();
		stubApi({
			"DELETE /services/web/pending-changes/:field": problem(500, {
				code: "internal_error",
				detail: "disk full",
			}),
		});
		await renderWithProviders(<Button />);
		await user.click(screen.getByRole("button", { name: "discard" }));
		expect(
			await screen.findByText("Could not discard that change"),
		).toBeInTheDocument();
	});
});

describe("useStagedActions.discardAll", () => {
	const staged = [
		makeService({ id: "web", name: "Web", hasStagedChanges: true }),
		makeService({ id: "db", name: "DB", hasStagedChanges: true }),
	];
	const mount = () =>
		renderWithProviders(
			<Actions services={staged}>
				{(a) => (
					<button type="button" onClick={() => a.discardAll.mutate(staged)}>
						discard all
					</button>
				)}
			</Actions>,
		);

	test("reverts every staged field, service by service, and says how many", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"GET /services/web/pending-changes": {
				body: { items: [change("config.port"), change("variables.A")] },
			},
			"GET /services/db/pending-changes": {
				body: { items: [change("config.image")] },
			},
			"DELETE /services/:id/pending-changes/:field": {
				body: make("ServiceView"),
			},
		});
		await mount();
		await user.click(screen.getByRole("button", { name: "discard all" }));
		expect(await screen.findByText("Changes discarded")).toBeInTheDocument();
		expect(
			screen.getByText("Services are back to their deployed configuration."),
		).toBeInTheDocument();
		expect(calls(api)).toEqual([
			"GET /services/web/pending-changes",
			"DELETE /services/web/pending-changes/config.port",
			"DELETE /services/web/pending-changes/variables.A",
			"GET /services/db/pending-changes",
			"DELETE /services/db/pending-changes/config.image",
		]);
	});

	test("one discarded field reads in the singular", async () => {
		const user = userEvent.setup();
		stubApi({
			"GET /services/web/pending-changes": {
				body: { items: [change("config.port")] },
			},
			"GET /services/db/pending-changes": { body: { items: [] } },
			"DELETE /services/:id/pending-changes/:field": {
				body: make("ServiceView"),
			},
		});
		await mount();
		await user.click(screen.getByRole("button", { name: "discard all" }));
		expect(await screen.findByText("Change discarded")).toBeInTheDocument();
	});

	test("a first deployment is left staged and nothing is deleted", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"GET /services/web/pending-changes": {
				body: { items: [change("deployment")] },
			},
			"GET /services/db/pending-changes": { body: { items: [] } },
		});
		await mount();
		await user.click(screen.getByRole("button", { name: "discard all" }));
		expect(await screen.findByText("Nothing to discard")).toBeInTheDocument();
		expect(api.calls.some((c) => c.method === "DELETE")).toBe(false);
	});

	test("stops and reports when a revert fails", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"GET /services/web/pending-changes": {
				body: { items: [change("config.port"), change("variables.A")] },
			},
			"GET /services/db/pending-changes": { body: { items: [change("x")] } },
			"DELETE /services/web/pending-changes/config.port": problem(500, {
				code: "internal_error",
				detail: "nope",
			}),
		});
		await mount();
		await user.click(screen.getByRole("button", { name: "discard all" }));
		expect(
			await screen.findByText("Could not discard the changes"),
		).toBeInTheDocument();
		expect(calls(api)).toEqual([
			"GET /services/web/pending-changes",
			"DELETE /services/web/pending-changes/config.port",
		]);
	});
});

describe("useStagedActions.deploy", () => {
	function mount(request: DeployRequest) {
		return renderWithProviders(
			<Actions services={[]}>
				{(a) => (
					<button type="button" onClick={() => a.deploy.mutate(request)}>
						deploy
					</button>
				)}
			</Actions>,
		);
	}

	test("deploys every staged service when none is named; trims no empty note", async () => {
		const user = userEvent.setup();
		const keys: (string | null)[] = [];
		const api = stubApi({
			"POST /environments/env1/deploy-staged": (req) => {
				keys.push(req.headers.get("Idempotency-Key"));
				return Response.json(
					make("StagedResult", {
						items: [deployment("d1", "web"), deployment("d2", "db")],
						order: ["db", "web"],
					}),
					{ status: 202 },
				);
			},
		});
		await mount({ message: "   " });
		await user.click(screen.getByRole("button", { name: "deploy" }));
		expect(await screen.findByText("Deploying 2 services")).toBeInTheDocument();
		expect(api.calls[0]?.body).toEqual({});
		expect(keys[0]).toMatch(/^[0-9a-f-]{36}$/);
	});

	test("sends the chosen services and the trimmed note", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"POST /environments/env1/deploy-staged": {
				status: 202,
				body: make("StagedResult", {
					items: [deployment("d1", "web")],
					order: ["web"],
				}),
			},
		});
		await mount({ serviceIds: ["web"], message: "  ship it  " });
		await user.click(screen.getByRole("button", { name: "deploy" }));
		expect(await screen.findByText("Deploying 1 service")).toBeInTheDocument();
		expect(api.calls[0]?.body).toEqual({
			serviceIds: ["web"],
			message: "ship it",
		});
	});

	test("a failed deploy is shown as a toast", async () => {
		const user = userEvent.setup();
		stubApi({
			"POST /environments/env1/deploy-staged": problem(409, {
				code: "conflict",
				detail: "a deployment is running",
			}),
		});
		await mount({});
		await user.click(screen.getByRole("button", { name: "deploy" }));
		expect(
			await screen.findByText("Could not deploy the changes"),
		).toBeInTheDocument();
	});
});

describe("useStagedActions.redeployCurrent", () => {
	const deployed = (over: Partial<Service>) =>
		makeService({ currentDeploymentId: "dep", ...over });

	function mount(services: Service[]) {
		return renderWithProviders(
			<Actions services={services}>
				{(a) => (
					<button type="button" onClick={() => a.redeployCurrent.mutate()}>
						redeploy
					</button>
				)}
			</Actions>,
		);
	}

	test("redeploys deployed idle services, databases first", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"POST /services/:id/deployments": (req) =>
				Response.json(
					deployment("n", new URL(req.url).pathname.split("/")[4] ?? ""),
					{ status: 202 },
				),
		});
		await mount([
			deployed({ id: "web", name: "Web", kind: "app" }),
			deployed({ id: "db", name: "DB", kind: "database" }),
			deployed({ id: "staged", name: "Staged", hasStagedChanges: true }),
			makeService({ id: "never", name: "Never", currentDeploymentId: null }),
			deployed({
				id: "busy",
				name: "Busy",
				latestDeployment: { status: "building" } as Service["latestDeployment"],
			}),
		]);
		await user.click(screen.getByRole("button", { name: "redeploy" }));
		expect(
			await screen.findByText("Redeploying 2 services"),
		).toBeInTheDocument();
		expect(calls(api)).toEqual([
			"POST /services/db/deployments",
			"POST /services/web/deployments",
		]);
	});

	test("says so when there is nothing to redeploy", async () => {
		const user = userEvent.setup();
		const api = stubApi({});
		await mount([makeService({ id: "new", currentDeploymentId: null })]);
		await user.click(screen.getByRole("button", { name: "redeploy" }));
		expect(await screen.findByText("Nothing to redeploy")).toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});

	test("a single service reads in the singular", async () => {
		const user = userEvent.setup();
		stubApi({
			"POST /services/web/deployments": {
				status: 202,
				body: deployment("n", "web"),
			},
		});
		await mount([deployed({ id: "web", name: "Web" })]);
		await user.click(screen.getByRole("button", { name: "redeploy" }));
		expect(
			await screen.findByText("Redeploying 1 service"),
		).toBeInTheDocument();
	});

	test("names the services that failed and keeps going", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"POST /services/db/deployments": problem(500, {
				code: "internal_error",
				detail: "agent offline",
			}),
			"POST /services/web/deployments": {
				status: 202,
				body: deployment("n", "web"),
			},
		});
		await mount([
			deployed({ id: "web", name: "Web" }),
			deployed({ id: "db", name: "DB", kind: "database" }),
		]);
		await user.click(screen.getByRole("button", { name: "redeploy" }));
		expect(
			await screen.findByText("Redeploying 1, 1 failed"),
		).toBeInTheDocument();
		expect(screen.getByText(/DB: /)).toBeInTheDocument();
		expect(calls(api)).toEqual([
			"POST /services/db/deployments",
			"POST /services/web/deployments",
		]);
	});
});
