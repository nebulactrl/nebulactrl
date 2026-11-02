// Wire-shape builders and small harnesses for the previews tests.

import { useLocation } from "@tanstack/react-router";
import type {
	Environment,
	PreviewSettings,
	PreviewView,
	ServiceView,
} from "#/api/gen/types.gen";
import { make, type Overrides } from "#/test/factories";

export function wirePreview(patch: Partial<PreviewView> = {}): PreviewView {
	return {
		id: "pv-1",
		projectId: "proj-1",
		environmentId: "env-pr-12",
		environmentSlug: "pr-12",
		provider: "github",
		repo: "acme/shop",
		prNumber: 12,
		prTitle: "Add checkout",
		prUrl: "https://github.com/acme/shop/pull/12",
		prAuthor: "ada",
		headBranch: "feat/checkout",
		headSha: "0123456789abcdef",
		fromFork: false,
		status: "ready",
		statusMessage: "",
		pausedAt: null,
		urls: ["https://pr-12.shop.example.com"],
		createdAt: "2026-10-30T10:00:00Z",
		lastPushAt: "2026-10-31T09:00:00Z",
		...patch,
	};
}

export function wireSettings(
	patch: Partial<PreviewSettings> = {},
): PreviewSettings {
	return {
		projectId: "proj-1",
		enabled: true,
		baseEnvironmentId: "env-prod",
		forkPrs: false,
		maxConcurrent: 5,
		idleDays: 7,
		excludedServices: [],
		seedCommands: [],
		updatedAt: null,
		...patch,
	};
}

export function wireEnv(patch: Partial<Environment> = {}): Environment {
	return {
		id: "env-prod",
		projectId: "proj-1",
		name: "Production",
		slug: "production",
		kind: "standard",
		isProduction: true,
		parentEnvironmentId: null,
		prNumber: null,
		prTitle: "",
		createdAt: "2026-01-01T00:00:00Z",
		...patch,
	};
}

export function wirePreviewEnv(patch: Partial<Environment> = {}): Environment {
	return wireEnv({
		id: "env-pr-12",
		name: "PR #12 · Add checkout",
		slug: "pr-12",
		kind: "preview",
		isProduction: false,
		parentEnvironmentId: "env-prod",
		prNumber: 12,
		prTitle: "Add checkout",
		...patch,
	});
}

/** A service row as GET /environments/{id}/services answers. */
export function wireService(slug: string, patch: Overrides<ServiceView> = {}) {
	return make("ServiceView", {
		id: `svc-${slug}`,
		environmentId: "env-prod",
		projectId: "proj-1",
		name: slug[0]?.toUpperCase() + slug.slice(1),
		slug,
		kind: "app",
		sourceType: "image",
		config: { healthcheck: { type: "http" }, processes: [] },
		...patch,
	});
}

/** Prints the router's current path so a test can see where a click went. */
export function Where() {
	const location = useLocation();
	return <output aria-label="location">{location.pathname}</output>;
}
