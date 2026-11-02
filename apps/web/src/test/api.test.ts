import { describe, expect, test } from "bun:test";
import { stubApi, takeApiViolations } from "./api";
import { make } from "./factories";

// Against the real contract (openapi/openapi.json): GET/POST /projects and
// GET/PATCH /projects/{projectId}, whose replies are Project and PageProject.
const project = make("Project");

const call = (method: string, path: string, body?: unknown) =>
	fetch(`/api/v1${path}`, {
		method,
		...(body === undefined
			? {}
			: {
					body: JSON.stringify(body),
					headers: { "Content-Type": "application/json" },
				}),
	});

describe("stubApi against the contract", () => {
	test("answers a canned reply that fits the contract", async () => {
		stubApi({ "GET /projects/:projectId": { body: project } });
		const res = await call("GET", "/projects/p1");
		expect(res.status).toBe(200);
		expect(await res.json()).toEqual(project);
	});

	test("a key that is not an operation throws when the stub is built", () => {
		expect(() =>
			// @ts-expect-error "projectz" is not an API route: the key type rejects it
			stubApi({ "GET /projectz": { body: [] } }),
		).toThrow('"GET /projectz" is not an operation of the API');
		expect(() =>
			// A parameter is a segment; the spec has no deeper /projects/{id}/nope route.
			stubApi({ "GET /projects/:id/nope": { body: {} } }),
		).toThrow("is not an operation");
	});

	test("a body with a wrong or missing field throws, naming route, status and path", () => {
		const { createdAt: _omitted, ...incomplete } = project;
		expect(() =>
			stubApi({ "GET /projects/:projectId": { body: incomplete } }),
		).toThrow(
			/the 200 reply for "GET \/projects\/:projectId" breaks the contract \(GET \/projects\/\{projectId\}\):\s+\$: missing required property "createdAt"/,
		);
		expect(() =>
			stubApi({
				"GET /projects/:projectId": { body: { ...project, system: "no" } },
			}),
		).toThrow("$.system: expected boolean, got string");
		expect(() =>
			stubApi({
				"GET /projects": { body: { items: [{ ...project, id: 3 }] } },
			}),
		).toThrow("$.items[0].id: expected string, got integer");
	});

	test("an unknown member fails where the schema is closed", () => {
		expect(() =>
			stubApi({
				"GET /projects/:projectId": { body: { ...project, extra: 1 } },
			}),
		).toThrow('unexpected property "extra"');
	});

	test("a status the spec does not list falls under the default error response", () => {
		// GET /projects/{projectId} lists 200 and "default" (the error document), so
		// a 202 carrying the project is held to the error schema.
		expect(() =>
			stubApi({ "GET /projects/:projectId": { status: 202, body: project } }),
		).toThrow('$: unexpected property "id"');
	});

	test("an error status is checked against the problem schema", () => {
		expect(() =>
			stubApi({
				"GET /projects/:projectId": { status: 404, body: { detail: 5 } },
			}),
		).toThrow("$.detail: expected string, got integer");
	});

	test("a function reply is not checked", async () => {
		stubApi({
			"GET /projects/:projectId": () =>
				new Response("not json at all", { status: 418 }),
		});
		expect((await call("GET", "/projects/p1")).status).toBe(418);
	});

	test("a request body the spec does not accept rejects the call and is reported", async () => {
		const api = stubApi({
			"POST /projects": { status: 201, body: project },
		});
		await expect(
			call("POST", "/projects", { name: "Shop", colour: "red" }),
		).rejects.toThrow('$: unexpected property "colour"');
		expect(api.calls).toHaveLength(1);
		const reported = takeApiViolations();
		expect(reported).toHaveLength(1);
		expect(reported[0]).toContain("POST /projects");
		expect(takeApiViolations()).toEqual([]);
	});

	test("a request body that fits goes through", async () => {
		stubApi({ "POST /projects": { status: 201, body: project } });
		expect((await call("POST", "/projects", { name: "Shop" })).status).toBe(
			201,
		);
		expect(takeApiViolations()).toEqual([]);
	});

	test("a body sent where the spec has none is a violation", async () => {
		stubApi({
			"POST /services/:serviceId/restart": () =>
				new Response(null, { status: 202 }),
		});
		await expect(
			call("POST", "/services/s1/restart", { force: true }),
		).rejects.toThrow("documents no request body");
		expect(takeApiViolations()).toHaveLength(1);
	});
});
