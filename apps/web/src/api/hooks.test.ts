import { describe, expect, test } from "bun:test";
import { QueryClient } from "@tanstack/react-query";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import {
	canvasQuery,
	deploymentEventsQuery,
	deploymentQuery,
	deploymentsQuery,
	environmentConfigSourceQuery,
	environmentSharedVariablesQuery,
	environmentsQuery,
	linksQuery,
	meQuery,
	pendingChangesQuery,
	projectDeletionPlanQuery,
	projectSharedVariablesQuery,
	projectsQuery,
	serversQuery,
	serviceConfigSourceQuery,
	serviceQuery,
	servicesQuery,
	setupQuery,
	suggestedVariablesQuery,
	systemQuery,
	teamSharedVariablesQuery,
	variableHistoryQuery,
	variablesQuery,
	volumesQuery,
} from "./hooks";

const client = () => new QueryClient();

const bareService = make("ServiceView", {
	id: "svc-1",
	environmentId: "env-1",
	projectId: "p-1",
	name: "Web",
	slug: "web",
	kind: "app",
	sourceType: "image",
	config: {
		healthcheck: { type: "http" },
		startCommand: null,
		processes: null,
	},
	serverId: "",
	generation: 1,
	appliedGeneration: 1,
	hasStagedChanges: false,
	currentDeploymentId: "",
	stale: false,
	privateHost: "web.internal",
	gitTokenSet: false,
	registryAuthSet: false,
	createdAt: "2026-01-01T00:00:00Z",
	updatedAt: "2026-01-01T00:00:00Z",
	desiredState: "running",
	scaling: { replicas: 1 },
	pendingChangeCounts: {},
	stagedKind: "",
	maintenance: {},
});

describe("session queries", () => {
	test("setupQuery reads GET /setup and does not poll", async () => {
		const api = stubApi({
			"GET /setup": {
				body: make("SetupStatusOutputBody", {
					needsSetup: true,
					tokenRequired: false,
				}),
			},
		});
		const options = setupQuery();
		expect(await client().fetchQuery(options)).toEqual({
			needsSetup: true,
			tokenRequired: false,
		});
		expect(api.calls.map((c) => `${c.method} ${c.path}`)).toEqual([
			"GET /setup",
		]);
		expect(options.refetchInterval).toBe(false);
		expect(options.staleTime).toBe(30_000);
	});

	test("meQuery reads GET /me, never retries and does not poll", async () => {
		const api = stubApi({ "GET /me": { body: make("MeBody", { id: "u1" }) } });
		const options = meQuery();
		expect(await client().fetchQuery(options)).toMatchObject({ id: "u1" });
		expect(api.unmatched).toEqual([]);
		expect(options.retry).toBe(false);
		expect(options.refetchInterval).toBe(false);
	});

	test("projectsQuery asks for 200 per page and unwraps the items, tolerating a null list", async () => {
		const api = stubApi({
			"GET /projects": {
				body: make("PageProject", {
					items: [make("Project", { id: "p1", name: "Shop" })],
				}),
			},
		});
		expect(await client().fetchQuery(projectsQuery())).toMatchObject([
			{ id: "p1", name: "Shop" },
		]);
		expect(api.calls[0]?.query.get("limit")).toBe("200");

		stubApi({
			"GET /projects": { body: make("PageProject", { items: null }) },
		});
		expect(await client().fetchQuery(projectsQuery())).toEqual([]);
	});

	test("projectDeletionPlanQuery always refetches and is never cached", async () => {
		const api = stubApi({
			"GET /projects/:id/deletion-plan": {
				body: make("DeletionPlan", { services: 3 }),
			},
		});
		const options = projectDeletionPlanQuery("p-9");
		expect(await client().fetchQuery(options)).toMatchObject({
			services: 3,
		});
		expect(api.calls[0]?.path).toBe("/projects/p-9/deletion-plan");
		expect(options.gcTime).toBe(0);
		expect(options.staleTime).toBe(0);
		expect(options.refetchOnMount).toBe("always");
	});

	test("systemQuery reads GET /system and caches for five minutes", async () => {
		const api = stubApi({
			"GET /system": { body: make("SystemInfo", { version: "1.0.0" }) },
		});
		const options = systemQuery();
		expect(await client().fetchQuery(options)).toMatchObject({
			version: "1.0.0",
		});
		expect(api.unmatched).toEqual([]);
		expect(options.staleTime).toBe(300_000);
		expect(options.refetchInterval).toBe(false);
	});
});

describe("environment and service queries", () => {
	test("environmentsQuery unwraps a project's environments", async () => {
		const api = stubApi({
			"GET /projects/:id/environments": {
				body: make("PageEnvironment", {
					items: [make("Environment", { id: "e1" })],
				}),
			},
		});
		expect(await client().fetchQuery(environmentsQuery("p-1"))).toMatchObject([
			{ id: "e1" },
		]);
		expect(api.calls[0]?.path).toBe("/projects/p-1/environments");
	});

	test("servicesQuery converts each service into the app model", async () => {
		const api = stubApi({
			"GET /environments/:id/services": {
				body: make("PageServiceView", { items: [bareService] }),
			},
		});
		const [service] = await client().fetchQuery(servicesQuery("env-1"));
		expect(api.calls[0]?.path).toBe("/environments/env-1/services");
		expect(api.calls[0]?.query.get("limit")).toBe("200");
		expect(service?.serverId).toBeNull();
		expect(service?.config.startCommand).toEqual([]);
		expect(service?.domains).toEqual([]);
	});

	test("serviceQuery converts one service", async () => {
		const api = stubApi({
			"GET /services/:id": {
				body: make("ServiceView", { ...bareService, serverId: "srv-1" }),
			},
		});
		const service = await client().fetchQuery(serviceQuery("svc-1"));
		expect(api.calls[0]?.path).toBe("/services/svc-1");
		expect(service.serverId).toBe("srv-1");
	});

	test("linksQuery unwraps a null list to empty", async () => {
		const api = stubApi({
			"GET /environments/:id/links": { body: { items: null } },
		});
		expect(await client().fetchQuery(linksQuery("env-1"))).toEqual([]);
		expect(api.calls[0]?.path).toBe("/environments/env-1/links");
	});

	test("serversQuery converts servers and asks for 200", async () => {
		const api = stubApi({
			"GET /servers": {
				body: make("PageServer", {
					items: [
						make("Server", {
							id: "s1",
							name: "edge",
							status: "online",
							createdAt: "2026-01-01T00:00:00Z",
							serviceCount: 0,
						}),
					],
				}),
			},
		});
		const [server] = await client().fetchQuery(serversQuery());
		expect(api.calls[0]?.query.get("limit")).toBe("200");
		expect(server?.proxy.status).toBe("");
	});

	test("pendingChangesQuery and volumesQuery unwrap their items", async () => {
		const api = stubApi({
			"GET /services/:id/pending-changes": {
				body: make("ChangesOutputBody", {
					items: [make("FieldChange", { field: "image" })],
				}),
			},
			"GET /services/:id/volumes": {
				body: make("VolumeListOutputBody", { items: [] }),
			},
		});
		expect(
			await client().fetchQuery(pendingChangesQuery("svc-1")),
		).toMatchObject([{ field: "image" }]);
		expect(await client().fetchQuery(volumesQuery("svc-1"))).toEqual([]);
		expect(api.calls.map((c) => c.path)).toEqual([
			"/services/svc-1/pending-changes",
			"/services/svc-1/volumes",
		]);
	});

	test("config source queries hit the service and environment endpoints", async () => {
		const api = stubApi({
			"GET /services/:id/config-source": {
				body: make("ServiceConfigSource", { state: "drift" }),
			},
			"GET /environments/:id/config-source": {
				body: make("EnvironmentConfigSource", { status: "in_sync" }),
			},
		});
		expect(
			await client().fetchQuery(serviceConfigSourceQuery("svc-1")),
		).toMatchObject({ state: "drift" });
		expect(
			await client().fetchQuery(environmentConfigSourceQuery("env-1")),
		).toMatchObject({ status: "in_sync" });
		expect(api.calls.map((c) => c.path)).toEqual([
			"/services/svc-1/config-source",
			"/environments/env-1/config-source",
		]);
	});
});

describe("canvasQuery", () => {
	test("converts the document and never refetches by itself", async () => {
		const api = stubApi({
			// A function reply: the contract never sends null layout members, and
			// canvasQuery must still turn them into empty ones.
			"GET /projects/:p/environments/:e/canvas": () =>
				Response.json({
					version: 3,
					layout: { nodes: null, groups: null, viewport: null },
				}),
		});
		const options = canvasQuery("p-1", "env-1");
		expect(await client().fetchQuery(options)).toEqual({
			version: 3,
			layout: { nodes: {}, groups: [], viewport: null },
		});
		expect(api.calls[0]?.path).toBe("/projects/p-1/environments/env-1/canvas");
		expect(options.staleTime).toBe(Number.POSITIVE_INFINITY);
		expect(options.refetchInterval).toBe(false);
		expect(options.refetchOnWindowFocus).toBe(false);
	});
});

describe("deployment queries", () => {
	const deployment = make("DeploymentView", {
		id: "dep-1",
		serviceId: "svc-1",
		releaseId: "",
		serverId: "",
		trigger: "manual",
		actorType: "user",
		actorLabel: "Ada",
		status: "bogus",
		strategy: "recreate",
		statusMessage: "",
		message: "",
		rolledBackFrom: "",
		generation: 1,
		createdAt: "2026-01-01T00:00:00Z",
		canceledAtStep: "",
	});

	test("deploymentsQuery asks for 50 and converts each", async () => {
		const api = stubApi({
			"GET /services/:id/deployments": {
				body: make("PageDeploymentView", { items: [deployment] }),
			},
		});
		const [first] = await client().fetchQuery(deploymentsQuery("svc-1"));
		expect(api.calls[0]?.query.get("limit")).toBe("50");
		expect(first?.status).toBe("failed");
		expect(first?.releaseId).toBeNull();
	});

	test("deploymentQuery converts one", async () => {
		const api = stubApi({ "GET /deployments/:id": { body: deployment } });
		expect(
			(await client().fetchQuery(deploymentQuery("dep-1"))).message,
		).toBeNull();
		expect(api.calls[0]?.path).toBe("/deployments/dep-1");
	});

	test("deploymentEventsQuery unwraps the events", async () => {
		const api = stubApi({
			"GET /deployments/:id/events": {
				body: make("EventsOutputBody", {
					items: [make("EventView", { step: "build" })],
				}),
			},
		});
		expect(
			await client().fetchQuery(deploymentEventsQuery("dep-1")),
		).toMatchObject([{ step: "build" }]);
		expect(api.calls[0]?.path).toBe("/deployments/dep-1/events");
	});
});

describe("variable queries", () => {
	test("variablesQuery hides secret values and defaults the side lists", async () => {
		const api = stubApi({
			"GET /services/:id/variables": {
				body: make("VariableList", {
					items: [
						make("Variable", {
							key: "A",
							isSecret: true,
							value: "s3cret",
							source: "manual",
							scope: "runtime",
						}),
						make("Variable", {
							key: "B",
							isSecret: false,
							value: "plain",
							source: "manual",
							scope: "runtime",
						}),
					],
				}),
			},
		});
		const result = await client().fetchQuery(variablesQuery("svc-1"));
		expect(api.calls[0]?.path).toBe("/services/svc-1/variables");
		expect(result.items.map((v) => [v.key, v.value])).toEqual([
			["A", undefined],
			["B", "plain"],
		]);
		expect(result.platform).toEqual([]);
		expect(result.exports).toEqual([]);
		expect(result.warnings).toEqual([]);
		expect(result.restaged).toEqual([]);
	});

	test("shared-variable queries unwrap project, environment and team layers", async () => {
		const api = stubApi({
			"GET /projects/:id/shared-variables": {
				body: make("SharedVariableList", {
					items: [make("SharedVariableView", { key: "P" })],
				}),
			},
			"GET /environments/:id/shared-variables": {
				body: make("SharedVariableList", {
					items: [make("SharedVariableView", { key: "E" })],
				}),
			},
			"GET /team/shared-variables": {
				body: make("SharedVariableList", { items: [] }),
			},
		});
		expect(
			await client().fetchQuery(projectSharedVariablesQuery("p-1")),
		).toMatchObject([{ key: "P" }]);
		expect(
			await client().fetchQuery(environmentSharedVariablesQuery("env-1")),
		).toMatchObject([{ key: "E" }]);
		expect(await client().fetchQuery(teamSharedVariablesQuery())).toEqual([]);
		expect(api.calls.map((c) => c.path)).toEqual([
			"/projects/p-1/shared-variables",
			"/environments/env-1/shared-variables",
			"/team/shared-variables",
		]);
	});

	test("suggestedVariablesQuery keeps the commit and branch it was proposed from", async () => {
		stubApi({
			"GET /services/:id/suggested-variables": {
				body: make("SuggestedVariableList", { commit: "abc", branch: "main" }),
			},
		});
		expect(await client().fetchQuery(suggestedVariablesQuery("svc-1"))).toEqual(
			{
				items: [],
				commit: "abc",
				branch: "main",
			},
		);
	});

	test("variableHistoryQuery targets one key's history", async () => {
		const api = stubApi({
			"GET /services/:id/variables/:key/history": {
				body: make("HistoryOutputBody", {
					items: [make("VariableHistoryEntry", { version: 2 })],
				}),
			},
		});
		expect(
			await client().fetchQuery(variableHistoryQuery("svc-1", "DB_URL")),
		).toMatchObject([{ version: 2 }]);
		expect(api.calls[0]?.path).toBe("/services/svc-1/variables/DB_URL/history");
	});
});
