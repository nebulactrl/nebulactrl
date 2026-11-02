import { spyOn } from "bun:test";
import { act, fireEvent, screen } from "@testing-library/react";
import type {
	CatalogTemplate,
	Environment,
	Project,
	Server,
	TemplateInstance,
	TemplateSummary,
	TemplateUpdate,
} from "#/api/gen/types.gen";
import { templatesActions } from "./store";

/** Test-only builders for the wire shapes the templates feature reads. */

export function summary(
	over: Partial<TemplateSummary> & { id: string },
): TemplateSummary {
	return {
		name: over.id,
		summary: `${over.id} summary`,
		category: "analytics",
		databases: [],
		license: "MIT",
		licenseClass: "permissive",
		memoryMb: 512,
		publishesPorts: false,
		revision: 1,
		services: 1,
		tags: [],
		...over,
	};
}

export function template(
	over: Partial<CatalogTemplate> & { id: string },
): CatalogTemplate {
	return {
		name: over.id,
		summary: `${over.id} summary`,
		category: "analytics",
		inputs: [],
		links: {},
		notes: [],
		requires: { arch: [], diskGb: 0, memoryMb: 512 },
		revision: 1,
		schema: 1,
		services: [{ key: "web", kind: "app", name: "Web", image: "umami:1" }],
		tags: [],
		upstream: { class: "permissive", license: "MIT" },
		...over,
	};
}

export function instance(
	over: Partial<TemplateInstance> & { id: string },
): TemplateInstance {
	return {
		createdAt: "2026-01-01T00:00:00Z",
		currentRevision: 1,
		currentVersion: "",
		environmentId: "env1",
		groupId: "g1",
		inputs: {},
		name: "Umami",
		services: [{ id: "svc1", key: "web", name: "Web", slug: "web" }],
		templateId: "umami",
		templateRevision: 1,
		templateVersion: "",
		updateAvailable: false,
		...over,
	};
}

export function update(over: Partial<TemplateUpdate> = {}): TemplateUpdate {
	return {
		applied: 0,
		apply: 0,
		available: true,
		changes: [],
		currentRevision: 2,
		currentVersion: "",
		inputs: [],
		installedRevision: 1,
		installedVersion: "",
		instanceId: "inst1",
		kept: 0,
		manual: 0,
		name: "Umami",
		notes: [],
		templateId: "umami",
		...over,
	};
}

const project: Project = {
	buildLocation: "inherit",
	buildServerId: "",
	createdAt: "2026-01-01T00:00:00Z",
	description: "",
	id: "p1",
	name: "Shop",
	registryId: "",
	slug: "shop",
	system: false,
	updatedAt: "2026-01-01T00:00:00Z",
};

export function environment(over: Partial<Environment> = {}): Environment {
	return {
		createdAt: "2026-01-01T00:00:00Z",
		id: "env1",
		isProduction: false,
		kind: "standard",
		name: "Staging",
		parentEnvironmentId: null,
		prNumber: null,
		prTitle: "",
		projectId: "p1",
		slug: "staging",
		...over,
	};
}

export function server(over: Partial<Server> & { id: string }): Server {
	return {
		name: over.id,
		status: "online",
		drainState: "none",
		acceptWorkloads: true,
		unrestricted: false,
		serviceCount: 0,
		...over,
	} as Server;
}

/** Where components that read the project context are mounted. */
export const ENV_ROUTE = "/p/$projectId/$envSlug";
export const ENV_PATH = "/p/p1/staging";

/** The routes the project context reads. */
export function contextRoutes(env: Environment = environment()) {
	return {
		"GET /projects": { body: { items: [project] } },
		"GET /projects/:projectId/environments": { body: { items: [env] } },
	};
}

/** The store is module-level: put it back to its initial state. */
export function resetTemplatesStore() {
	templatesActions.close();
	templatesActions.select(null);
	templatesActions.setQuery("");
	templatesActions.setCategory(null);
	templatesActions.setLicense(null);
	templatesActions.cancelDelete();
	templatesActions.cancelUpdate();
}

/**
 * Holds a confirm button the way a person does. The hold is a real 1.3 s
 * timer, so its callback is captured instead of waited for.
 */
export function holdToConfirm(name: RegExp) {
	const button = screen.getByRole("button", { name });
	const realSetTimeout = window.setTimeout.bind(window);
	let finishHold: (() => void) | undefined;
	const spy = spyOn(window, "setTimeout").mockImplementation(((
		handler: () => void,
		ms?: number,
	) => {
		if (ms === 1300) {
			finishHold = handler;
			return 0;
		}
		return realSetTimeout(handler, ms);
	}) as typeof window.setTimeout);
	try {
		fireEvent.keyDown(button, { key: "Enter" });
	} finally {
		spy.mockRestore();
	}
	if (!finishHold) return false;
	act(() => finishHold?.());
	return true;
}
