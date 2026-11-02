import { type APIRequestContext, expect } from "@playwright/test";

// Arrangement and cleanup through the REST API, with the session cookie of the
// browser context (`page.request`). What a flow is about is driven through the
// UI; the API only prepares a state or checks it from the side.

export interface Item {
	id: string;
	name: string;
	slug: string;
	status?: string;
}

export async function list(
	request: APIRequestContext,
	path: string,
): Promise<Item[]> {
	const res = await request.get(`/api/v1${path}`);
	expect(res.ok(), `GET ${path}: ${res.status()}`).toBe(true);
	const body: { items: Item[] } = await res.json();
	return body.items;
}

export async function getJson<T>(
	request: APIRequestContext,
	path: string,
): Promise<T> {
	const res = await request.get(`/api/v1${path}`);
	expect(res.ok(), `GET ${path}: ${res.status()}`).toBe(true);
	return (await res.json()) as T;
}

/** The project with this name, or undefined. */
export async function findProject(
	request: APIRequestContext,
	name: string,
): Promise<Item | undefined> {
	return (await list(request, "/projects")).find((p) => p.name === name);
}

/** The services of every environment of a project. */
export async function projectServices(
	request: APIRequestContext,
	projectId: string,
): Promise<Item[]> {
	const services: Item[] = [];
	for (const env of await list(
		request,
		`/projects/${projectId}/environments`,
	)) {
		services.push(...(await list(request, `/environments/${env.id}/services`)));
	}
	return services;
}

/**
 * Deletes a project the way the flows leave nothing behind: its services (with
 * their volumes, which stops and removes the containers) and then the project.
 * A project that does not exist is fine, so a flow that failed early cleans up too.
 */
export async function deleteProject(
	request: APIRequestContext,
	name: string,
): Promise<void> {
	const project = await findProject(request, name);
	if (!project) return;
	for (const svc of await projectServices(request, project.id)) {
		const res = await request.delete(
			`/api/v1/services/${svc.id}?confirm=${svc.slug}&deleteVolumes=true`,
		);
		expect(res.ok(), `delete service ${svc.slug}: ${res.status()}`).toBe(true);
	}
	const res = await request.delete(`/api/v1/projects/${project.id}`);
	expect(res.ok(), `delete project ${name}: ${res.status()}`).toBe(true);
}

/** A name no other run or flow uses, for projects, services and domains. */
export function uniqueName(prefix: string): string {
	return `${prefix}-${Math.random().toString(36).slice(2, 8)}`;
}
