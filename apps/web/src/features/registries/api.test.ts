import { describe, expect, test } from "bun:test";
import { stubApi } from "#/test/api";
import {
	checkRegistry,
	createRegistry,
	deleteRegistry,
	fetchImages,
	fetchRegistries,
	updateRegistry,
} from "./api";
import { wireBuiltin, wireCheck, wireImage, wireRegistry } from "./test-data";

const signal = new AbortController().signal;

describe("fetchRegistries", () => {
	test("maps an external registry and defaults the omitted usernames to empty", async () => {
		const api = stubApi({
			"GET /registries": {
				body: {
					items: [
						wireRegistry({
							id: "reg-9",
							hasPullAuth: true,
							pullUsername: "reader",
							lastCheck: {
								"srv-1": wireCheck({ ok: false, pull: false, error: "denied" }),
								"srv-2": wireCheck(),
							},
						}),
						wireRegistry({ id: "reg-10" }),
					],
				},
			},
		});

		const [first, second] = await fetchRegistries(signal);

		expect(api.calls.map((c) => `${c.method} ${c.path}`)).toEqual([
			"GET /registries",
		]);
		expect(first).toMatchObject({
			id: "reg-9",
			kind: "external",
			host: "ghcr.io",
			namespace: "acme",
			pushUsername: "deploy-bot",
			hasPullAuth: true,
			pullUsername: "reader",
			builtin: null,
		});
		expect(first?.lastCheck["srv-1"]).toEqual({
			ok: false,
			push: true,
			pull: false,
			error: "denied",
			at: "2026-10-30T10:00:00Z",
		});
		// An omitted check error is the empty string, not undefined.
		expect(first?.lastCheck["srv-2"]?.error).toBe("");
		expect(second?.pullUsername).toBe("");
	});

	test("an omitted pushUsername and lastCheck become empty values", async () => {
		// A function reply on purpose: the contract requires `lastCheck`, and this
		// is the answer of a control plane that leaves it out.
		const wire: Record<string, unknown> = wireRegistry({
			pushUsername: undefined,
		});
		delete wire.lastCheck;
		stubApi({
			"GET /registries": () => Response.json({ items: [wire] }),
		});

		const [item] = await fetchRegistries(signal);

		expect(item?.pushUsername).toBe("");
		expect(item?.lastCheck).toEqual({});
	});

	test("maps the built-in registry's state and a missing last cleanup to null", async () => {
		stubApi({
			"GET /registries": {
				body: {
					items: [
						wireBuiltin({
							sizeBytes: 2048,
							imageCount: 3,
							builtin: {
								status: "unavailable",
								statusMessage: "dial tcp: refused",
								gcState: "readonly",
								lastSweepDeleted: 4,
							},
						}),
						wireBuiltin({
							id: "reg-b2",
							builtin: {
								status: "ready",
								statusMessage: "",
								gcState: "idle",
								lastGcAt: "2026-10-29T00:00:00Z",
								lastSweepDeleted: 0,
							},
						}),
					],
				},
			},
		});

		const [down, up] = await fetchRegistries(signal);

		expect(down).toMatchObject({
			kind: "builtin",
			sizeBytes: 2048,
			imageCount: 3,
			builtin: {
				status: "unavailable",
				statusMessage: "dial tcp: refused",
				gcState: "readonly",
				lastGcAt: null,
			},
		});
		expect(up?.builtin?.lastGcAt).toBe("2026-10-29T00:00:00Z");
	});

	test("a null item list is an empty list", async () => {
		stubApi({ "GET /registries": { body: { items: null } } });
		expect(await fetchRegistries(signal)).toEqual([]);
	});

	test("a failed call rejects", async () => {
		stubApi({
			"GET /registries": {
				status: 403,
				body: { status: 403, code: "forbidden", detail: "admin only" },
			},
		});
		await expect(fetchRegistries(signal)).rejects.toBeDefined();
	});
});

describe("fetchImages", () => {
	test("asks for the registry's images in one state and maps them", async () => {
		const api = stubApi({
			"GET /registries/:id/images": {
				body: {
					items: [
						wireImage({
							sizeBytes: 1024,
							serviceId: "svc-1",
							releaseId: "rel-1",
						}),
						wireImage({ id: "img-2" }),
					],
				},
			},
		});

		const images = await fetchImages("reg-1", "expired", signal);

		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]?.path).toBe("/registries/reg-1/images");
		expect(api.calls[0]?.query.get("state")).toBe("expired");
		expect(api.calls[0]?.query.get("limit")).toBe("100");
		expect(images[0]).toMatchObject({
			id: "img-1",
			repository: "ghcr.io/acme/shop-api",
			platform: "linux/amd64",
			sizeBytes: 1024,
			state: "live",
			serviceId: "svc-1",
			releaseId: "rel-1",
		});
		// Omitted optionals are null, never undefined.
		expect(images[1]).toMatchObject({
			sizeBytes: null,
			serviceId: null,
			releaseId: null,
		});
	});

	test("a null item list is an empty list", async () => {
		stubApi({ "GET /registries/:id/images": { body: { items: null } } });
		expect(await fetchImages("reg-1", "live", signal)).toEqual([]);
	});
});

describe("registry mutations", () => {
	test("createRegistry posts the body and returns the mapped registry", async () => {
		const api = stubApi({
			"POST /registries": {
				status: 201,
				body: wireRegistry({ id: "reg-new", name: "Docker Hub" }),
			},
		});
		const body = {
			name: "Docker Hub",
			host: "docker.io",
			namespace: "acme",
			pushUsername: "me",
			pushPassword: "s3cret",
		};

		const created = await createRegistry(body);

		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]).toMatchObject({
			method: "POST",
			path: "/registries",
			body,
		});
		expect(created).toMatchObject({ id: "reg-new", name: "Docker Hub" });
	});

	test("updateRegistry patches only the given fields at the registry's path", async () => {
		const api = stubApi({
			"PATCH /registries/:id": { body: wireRegistry({ name: "Renamed" }) },
		});

		const updated = await updateRegistry("reg-1", { name: "Renamed" });

		expect(api.calls[0]).toMatchObject({
			method: "PATCH",
			path: "/registries/reg-1",
			body: { name: "Renamed" },
		});
		expect(updated.name).toBe("Renamed");
	});

	test("deleteRegistry sends DELETE and resolves with nothing", async () => {
		const api = stubApi({
			"DELETE /registries/:id": { status: 204 },
		});

		await expect(deleteRegistry("reg-1")).resolves.toBeUndefined();

		expect(api.calls[0]).toMatchObject({
			method: "DELETE",
			path: "/registries/reg-1",
		});
	});

	test("a refused delete rejects", async () => {
		stubApi({
			"DELETE /registries/:id": {
				status: 409,
				body: { status: 409, code: "in_use", detail: "registry is in use" },
			},
		});
		await expect(deleteRegistry("reg-1")).rejects.toBeDefined();
	});

	test("checkRegistry posts the server id as a query and maps the result", async () => {
		const api = stubApi({
			"POST /registries/:id/check": {
				body: wireCheck({ ok: false, pull: false, error: "unauthorized" }),
			},
		});

		const check = await checkRegistry("reg-1", "srv-7");

		expect(api.calls[0]).toMatchObject({
			method: "POST",
			path: "/registries/reg-1/check",
		});
		expect(api.calls[0]?.query.get("serverId")).toBe("srv-7");
		expect(check).toEqual({
			ok: false,
			push: true,
			pull: false,
			error: "unauthorized",
			at: "2026-10-30T10:00:00Z",
		});
	});
});
