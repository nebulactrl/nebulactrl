import { describe, expect, test } from "bun:test";
import { checkResponseBody, findOperation } from "./contract";
import { make } from "./factories";

function operation(method: string, path: string) {
	const found = findOperation(method, path);
	if (!found) throw new Error(`no operation ${method} ${path}`);
	return found;
}

describe("the contract tells the truth about what Go sends", () => {
	test("a nil pointer without omitempty is null, and the spec allows it", () => {
		const server = operation("GET", "/servers/:serverId");
		expect(
			checkResponseBody(
				server,
				200,
				make("Server", { facts: null, latest: null }),
			),
		).toEqual([]);

		const service = operation("GET", "/services/:serviceId");
		const view = make("ServiceView", {
			currentDeploymentId: null,
			server: null,
			latestDeployment: null,
			observed: null,
			git: null,
		});
		expect(checkResponseBody(service, 200, view)).toEqual([]);
	});

	test("an object that is present still has to fit", () => {
		const server = operation("GET", "/servers/:serverId");
		const ok = make("Server", { facts: { os: "linux" } });
		expect(checkResponseBody(server, 200, ok)).toEqual([]);

		const wrong = { ...make("Server"), facts: { ...make("Facts"), os: 7 } };
		expect(checkResponseBody(server, 200, wrong)).toEqual([
			"$.facts.os: expected string, got integer",
		]);
	});

	test("a property the spec requires is still required, null or not", () => {
		const server = operation("GET", "/servers/:serverId");
		const { facts: _facts, ...without } = make("Server");
		expect(checkResponseBody(server, 200, without)).toEqual([
			'$: missing required property "facts"',
		]);
	});

	test("every operation answers any error status with a problem document", () => {
		const patch = operation("PATCH", "/services/:serviceId");
		expect(
			checkResponseBody(
				patch,
				409,
				make("Error", { status: 409, code: "conflict" }),
			),
		).toEqual([]);
		expect(checkResponseBody(patch, 502, { code: 1 })).not.toEqual([]);
	});

	test("a status the spec does not list, a body where there is none, no body where there is one", () => {
		const get = operation("GET", "/projects/:projectId");
		expect(checkResponseBody(get, 200, undefined)).toEqual([
			"$: the spec documents a body, the reply has none",
		]);
		// POST .../restart documents 202 (ActionResult) and the error statuses, so
		// a 200 falls under the default (error) response and a 202 with no body is missing one.
		const restart = operation("POST", "/services/:serviceId/restart");
		expect(checkResponseBody(restart, 202, undefined)).toEqual([
			"$: the spec documents a body, the reply has none",
		]);
	});
});
