import type { ConnectionView, GitLinkView, Repo } from "#/api/gen/types.gen";
import type { Service } from "#/api/models";
import { make, type Overrides } from "#/test/factories";

/** A healthy GitHub App connection; override what a test is about. */
export function connection(
	over: Overrides<ConnectionView> = {},
): ConnectionView {
	return make("ConnectionView", {
		id: "c1",
		name: "Acme GitHub",
		provider: "github",
		authType: "github_app",
		baseUrl: "https://github.com",
		accountLogin: "acme",
		status: "active",
		statusMessage: "",
		serviceCount: 0,
		canListRepos: true,
		supportsPreviews: true,
		manualWebhook: false,
		webhookUrl: "https://ctrl.example.com/hooks/c1",
		github: {
			appId: 1,
			appSlug: "acme-app",
			appUrl: "https://github.com/apps/acme-app",
			installUrl: "https://github.com/apps/acme-app/installations/new",
			installationId: 7,
		},
		oauth: { authorized: false, clientId: "", redirectUri: "" },
		createdAt: "2026-01-01T00:00:00Z",
		updatedAt: "2026-01-01T00:00:00Z",
		...over,
	});
}

export function repo(over: Overrides<Repo> = {}): Repo {
	return make("Repo", {
		archived: false,
		cloneUrl: "https://github.com/acme/api.git",
		defaultBranch: "main",
		description: "",
		fullName: "acme/api",
		htmlUrl: "https://github.com/acme/api",
		private: false,
		...over,
	});
}

export function link(over: Overrides<GitLinkView> = {}): GitLinkView {
	return make("GitLinkView", {
		linked: true,
		autoDeploy: true,
		branch: "main",
		connectionId: "c1",
		connectionName: "Acme GitHub",
		provider: "github",
		queuedCommit: "",
		waitForCi: false,
		ciWarning: "",
		repo: "acme/api",
		repoUrl: "https://github.com/acme/api",
		watchPaths: null,
		webhook: { mode: "app", status: "active", message: "", url: "" },
		...over,
	});
}

/**
 * Replaces `window.location.assign` for one test (a real navigation would take
 * the page away). Returns the calls and a restore function.
 */
export function stubLocationAssign(): { urls: string[]; restore(): void } {
	const urls: string[] = [];
	const original = window.location;
	Object.defineProperty(window, "location", {
		value: {
			...original,
			search: original.search,
			pathname: original.pathname,
			hash: original.hash,
			origin: original.origin,
			assign: (url: string) => urls.push(url),
		},
		configurable: true,
	});
	return {
		urls,
		restore() {
			Object.defineProperty(window, "location", {
				value: original,
				configurable: true,
			});
		},
	};
}

/**
 * Fires what the browser fires when it restores the page from its
 * back/forward cache (the user pressed Back from the page the app navigated to).
 */
export function restoreFromBfcache(): void {
	const event = new Event("pageshow");
	Object.defineProperty(event, "persisted", { value: true });
	window.dispatchEvent(event);
}

/** The two fields the git section reads off a service. */
export function service(over: { id?: string; name?: string } = {}): Service {
	return { id: "svc-1", name: "api", ...over } as Service;
}

/** A link whose provider is not reported (the field is optional in the API). */
export function linkWithoutProvider(
	over: Overrides<GitLinkView> = {},
): GitLinkView {
	return link({ ...over, provider: undefined });
}
