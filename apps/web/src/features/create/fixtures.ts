import type {
	ConnectionView,
	EnvTemplateScan,
	Server as ServerBody,
	ServiceView,
	SuggestedVariable,
} from "#/api/gen/types.gen";
import { serverWire } from "#/features/servers/server.fixture";
import { make, type Overrides } from "#/test/factories";

// Payloads shaped like the API's, for the create tests only.

/** A server as `GET /servers` returns it: online, with a heartbeat sample and no facts reported yet. */
export function serverBody(over: Partial<ServerBody> = {}): ServerBody {
	return serverWire({ facts: null, ...over });
}

/** A service as the create endpoint returns it: staged, with nothing deployed or observed yet. */
export function serviceBody(over: Overrides<ServiceView> = {}): ServiceView {
	return make("ServiceView", {
		id: "svc-new",
		name: "shop",
		slug: "shop",
		kind: "app",
		sourceType: "image",
		environmentId: "env-1",
		projectId: "proj-1",
		serverId: "srv-1",
		server: { id: "srv-1" },
		config: {
			healthcheck: { type: "none" },
			imageRetention: 3,
			stopGraceSeconds: 10,
		},
		currentDeploymentId: null,
		latestDeployment: null,
		currentRelease: null,
		observed: null,
		observedAt: null,
		...over,
	});
}

/** One key of a repository's `.env.example`. */
export function suggested(
	over: Overrides<SuggestedVariable> = {},
): SuggestedVariable {
	return make("SuggestedVariable", {
		key: "LOG_LEVEL",
		value: "info",
		needsInput: false,
		proposedSecret: false,
		reason: "",
		source: "env-example",
		warnings: null,
		...over,
	});
}

/** The answer of the env template preview. */
export function scanBody(
	over: Overrides<EnvTemplateScan> = {},
): EnvTemplateScan {
	return make("EnvTemplateScan", {
		status: "found",
		method: "provider",
		message: "",
		files: [{ path: ".env.example", keys: 1 }],
		items: [suggested()],
		skipped: null,
		...over,
	});
}

/** An active git connection that can list repositories. */
export function connectionBody(
	over: Overrides<ConnectionView> = {},
): ConnectionView {
	return make("ConnectionView", {
		id: "conn-1",
		name: "acme on GitHub",
		accountLogin: "acme",
		provider: "github",
		authType: "github_app",
		status: "active",
		statusMessage: "",
		canListRepos: true,
		baseUrl: "",
		manualWebhook: false,
		serviceCount: 0,
		supportsPreviews: true,
		webhookUrl: "",
		createdAt: "2026-01-01T00:00:00Z",
		updatedAt: "2026-01-01T00:00:00Z",
		...over,
	});
}
