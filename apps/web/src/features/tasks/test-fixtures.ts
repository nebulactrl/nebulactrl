import type { UserEvent } from "@testing-library/user-event";
import type { RunView, ServiceView, TaskView } from "#/api/gen/types.gen";
import type { Service } from "#/api/models";
import { make, type Overrides } from "#/test/factories";

/** The ServiceView the tasks UI opens: the default scene's service as "api" of svc-1. */
export function serviceBody(overrides: Overrides<ServiceView> = {}) {
	return make("ServiceView", {
		id: "svc-1",
		name: "api",
		slug: "api",
		environmentId: "env-1",
		projectId: "proj-1",
		kind: "app",
		sourceType: "image",
		config: { healthcheck: { type: "none" }, processes: null },
		currentDeploymentId: "dep-1",
		domains: null,
		volumes: null,
		networks: null,
		processes: null,
		...overrides,
	});
}

/** The slice of a Service the Tasks section reads. */
export function service(overrides: Partial<Service> = {}): Service {
	return {
		id: "svc-1",
		name: "api",
		currentDeploymentId: "dep-1",
		...overrides,
	} as Service;
}

/** `undefined` drops a field (it is left out of the JSON). */
export function runView(
	overrides: { [K in keyof RunView]?: RunView[K] | undefined } = {},
): RunView {
	return {
		id: "run-1",
		serviceId: "svc-1",
		name: "nightly",
		command: ["rake", "cleanup"],
		source: "cron",
		status: "succeeded",
		taskId: "task-1",
		timeoutSeconds: 1800,
		logLines: 0,
		createdAt: "2026-10-27T03:00:00Z",
		startedAt: "2026-10-27T03:00:01Z",
		finishedAt: "2026-10-27T03:00:11Z",
		exitCode: 0,
		...overrides,
	} as RunView;
}

export function taskView(overrides: Partial<TaskView> = {}): TaskView {
	return {
		id: "task-1",
		serviceId: "svc-1",
		name: "nightly",
		action: "command",
		schedule: "0 3 * * *",
		timezone: "UTC",
		command: ["rake", "cleanup"],
		enabled: true,
		concurrency: "forbid",
		timeoutSeconds: 1800,
		createdAt: "2026-10-01T00:00:00Z",
		updatedAt: "2026-10-01T00:00:00Z",
		...overrides,
	};
}

/** The empty SSE log stream every run screen opens for its output. */
export function emptyLogs(): Response {
	return new Response("event: end\ndata: {}\n\n", {
		headers: { "Content-Type": "text/event-stream" },
	});
}

export const problem = (
	status: number,
	detail: string,
	code = "bad_request",
) => ({
	status,
	body: { status, title: "Error", detail, code },
});

/**
 * Sets a text field to `text` by pasting. The dialogs re-render heavily, so
 * typing key by key costs ~100ms a character under happy-dom.
 */
export async function fill(
	user: UserEvent,
	field: HTMLElement,
	text: string,
): Promise<void> {
	await user.clear(field);
	await user.paste(text);
}
