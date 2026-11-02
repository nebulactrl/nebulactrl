// Test-only builders for the deployments feature: a domain `Deployment` and
// `Service` (what components take as props) and the wire views the API stub
// answers with.
import type * as Gen from "#/api/gen/types.gen";
import type { Deployment, Service } from "#/api/models";
import { make, type Overrides } from "#/test/factories";

export function deployment(overrides: Partial<Deployment> = {}): Deployment {
	return {
		id: "dep-1",
		serviceId: "svc-1",
		releaseId: "rel-1",
		serverId: "srv-1",
		buildServerId: null,
		trigger: "manual",
		actorType: "user",
		actorLabel: "Ada",
		status: "succeeded",
		strategy: "rolling",
		statusMessage: null,
		message: null,
		rolledBackFrom: null,
		generation: 1,
		createdAt: "2026-10-31T10:00:00Z",
		startedAt: "2026-10-31T10:00:01Z",
		finishedAt: "2026-10-31T10:01:01Z",
		release: null,
		waitingFor: null,
		commit: null,
		cancelRequestedAt: null,
		canceledAtStep: null,
		...overrides,
	};
}

export function service(overrides: Partial<Service> = {}): Service {
	return {
		id: "svc-1",
		environmentId: "env-prod",
		projectId: "proj-1",
		name: "api",
		slug: "api",
		kind: "app",
		sourceType: "image",
		config: { gitUrl: "" },
		serverId: "srv-1",
		server: { id: "srv-1", name: "edge-1", status: "online", lastSeenAt: null },
		currentDeploymentId: null,
		latestDeployment: null,
		currentRelease: null,
		...overrides,
	} as unknown as Service;
}

/** A DeploymentView as the API sends it: a finished manual deploy of release "rel-1" to "srv-1". */
export function deploymentView(
	overrides: Overrides<Gen.DeploymentView> = {},
): Gen.DeploymentView {
	return make("DeploymentView", {
		id: "dep-1",
		serviceId: "svc-1",
		releaseId: "rel-1",
		serverId: "srv-1",
		buildServerId: "",
		trigger: "manual",
		actorType: "user",
		actorLabel: "Ada",
		status: "succeeded",
		strategy: "rolling",
		statusMessage: "",
		message: null,
		rolledBackFrom: "",
		generation: 1,
		createdAt: "2026-10-31T10:00:00Z",
		startedAt: "2026-10-31T10:00:01Z",
		finishedAt: "2026-10-31T10:01:01Z",
		cancelRequestedAt: null,
		cancelRequestedBy: "",
		canceledAtStep: "",
		...overrides,
	});
}

/** What a rollback answers with: the new deployment plus the data disclaimer. */
export function rollbackView(
	overrides: Overrides<Gen.RollbackView> = {},
): Gen.RollbackView {
	return make("RollbackView", {
		...deploymentView(),
		disclaimer: "Application image and configuration only.",
		...overrides,
	});
}

export function eventView(
	overrides: Overrides<Gen.EventView> & { id: number },
): Gen.EventView {
	return make("EventView", {
		createdAt: "2026-10-31T10:00:02Z",
		message: "",
		status: "succeeded",
		step: "build",
		...overrides,
	});
}

/** A text/event-stream reply carrying the given log frames, then `end`. */
export function logStreamResponse(
	frames: { line: string; stream?: string; ts?: string }[],
): Response {
	const body = `${frames
		.map((frame) => `data: ${JSON.stringify(frame)}\n\n`)
		.join("")}event: end\ndata: {}\n\n`;
	return new Response(body, {
		status: 200,
		headers: { "Content-Type": "text/event-stream" },
	});
}

/** A text/event-stream reply that delivers the frames and then stays open, like a live follow. */
export function openLogStreamResponse(
	frames: { line: string; stream?: string; ts?: string }[],
): Response {
	const encoder = new TextEncoder();
	return new Response(
		new ReadableStream({
			start(controller) {
				for (const frame of frames) {
					controller.enqueue(
						encoder.encode(`data: ${JSON.stringify(frame)}\n\n`),
					);
				}
			},
		}),
		{ status: 200, headers: { "Content-Type": "text/event-stream" } },
	);
}

/** A complete `PlacementView` (a build that runs on the target server), with `over` applied. */
export function placementWire(
	over: Overrides<Gen.PlacementView> = {},
): Gen.PlacementView {
	return make("PlacementView", {
		location: "target",
		buildServerId: "",
		buildServerName: "",
		registryId: "",
		registryHost: "",
		platform: "linux/amd64",
		reason: "",
		sticky: false,
		emulated: false,
		fallback: false,
		reuse: false,
		maxConcurrent: 0,
		activeBuilds: 0,
		warnings: [],
		...over,
	});
}

/**
 * A complete deployment plan as `POST /services/{id}/deployments/plan` sends
 * it: a rolling deploy of an image onto an online server, with nothing to
 * change, warn about or block, and no build placement.
 */
export function planWire(over: Overrides<Gen.Plan> = {}): Gen.Plan {
	return make("Plan", {
		strategy: "rolling",
		reasons: [],
		warnings: [],
		blocking: [],
		changes: [],
		processes: [],
		mesh: { crossServer: false, links: [], devicesAdded: 0, blockers: [] },
		placement: null,
		server: { id: "srv-1", name: "edge", status: "online", lastSeenAt: null },
		source: { type: "image", image: "nginx:1" },
		...over,
	});
}
