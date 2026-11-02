import type { UserEvent } from "@testing-library/user-event";
import type {
	ChannelDelivery,
	ChannelEventType,
	ChannelView,
	Project,
} from "#/api/gen/types.gen";

export const EVENT_TYPES: ChannelEventType[] = [
	{
		type: "deploy.failed",
		label: "Deployment failed",
		description: "A deployment ended in an error.",
		group: "Deployments",
		severity: "error",
		defaultOn: true,
	},
	{
		type: "deploy.succeeded",
		label: "Deployment succeeded",
		description: "A deployment went live.",
		group: "Deployments",
		severity: "info",
		defaultOn: false,
	},
	{
		type: "server.offline",
		label: "Server offline",
		description: "A server stopped answering.",
		group: "Servers",
		severity: "warning",
		defaultOn: true,
	},
];

export function channel(over: Partial<ChannelView> = {}): ChannelView {
	return {
		id: "c1",
		name: "Ops Slack",
		kind: "slack",
		enabled: true,
		config: {},
		secrets: { url: "https://hooks.slack.com/••••••" },
		rule: {
			eventTypes: ["deploy.failed"],
			minSeverity: "info",
			allProjects: true,
			projectIds: [],
		},
		test: { message: "" },
		stats: { delivered24h: 0, failed24h: 0 },
		createdAt: "2026-10-26T10:00:00Z",
		updatedAt: "2026-10-26T10:00:00Z",
		...over,
	};
}

export function delivery(over: Partial<ChannelDelivery> = {}): ChannelDelivery {
	return {
		id: "d1",
		channelId: "c1",
		attempts: 1,
		maxAttempts: 6,
		createdAt: "2026-10-26T10:00:00Z",
		eventType: "deploy.failed",
		isTest: false,
		lastError: "",
		severity: "error",
		status: "delivered",
		title: "checkout failed to deploy",
		...over,
	};
}

export function project(id: string, name: string): Project {
	return {
		id,
		name,
		slug: name.toLowerCase(),
		buildLocation: "inherit",
		buildServerId: "",
		createdAt: "2026-01-01T00:00:00Z",
		updatedAt: "2026-01-01T00:00:00Z",
		description: "",
		registryId: "",
		system: false,
	};
}

export const problem = (
	status: number,
	detail: string,
	code = "bad_request",
) => ({
	status,
	body: { status, title: "Error", detail, code },
});

/** Replaces a field's text in one step (typing key by key re-renders the whole form per character). */
export async function fill(
	user: UserEvent,
	field: HTMLElement,
	text: string,
): Promise<void> {
	await user.clear(field);
	await user.click(field);
	await user.paste(text);
}
