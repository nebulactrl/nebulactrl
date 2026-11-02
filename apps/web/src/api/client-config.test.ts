import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import {
	ApiError,
	type ApiHandlers,
	errorCode,
	errorMessage,
	errorStatus,
	isApiError,
	reportFailure,
	setApiHandlers,
} from "./client-config";
import { getMe, listProjects, login } from "./gen/sdk.gen";

async function failureOf(call: () => Promise<unknown>): Promise<unknown> {
	try {
		await call();
	} catch (error) {
		return error;
	}
	throw new Error("the call was expected to fail");
}

let seen: { unauthorized: number; forbidden: ApiError[]; conflict: ApiError[] };

beforeEach(() => {
	seen = { unauthorized: 0, forbidden: [], conflict: [] };
	const handlers: ApiHandlers = {
		onUnauthorized: () => {
			seen.unauthorized++;
		},
		onForbidden: (error) => seen.forbidden.push(error),
		onConflict: (error) => seen.conflict.push(error),
	};
	setApiHandlers(handlers);
});

afterEach(() => setApiHandlers({}));

describe("error helpers", () => {
	test("read status and code only off an ApiError", () => {
		const error = new ApiError(404, "not_found", "Gone", { id: "x" });
		expect(isApiError(error)).toBe(true);
		expect(errorStatus(error)).toBe(404);
		expect(errorCode(error)).toBe("not_found");
		expect(error.details).toEqual({ id: "x" });
		expect(error.name).toBe("ApiError");
		expect(error.message).toBe("Gone");

		const plain = new Error("nope");
		expect(isApiError(plain)).toBe(false);
		expect(errorStatus(plain)).toBeUndefined();
		expect(errorCode(plain)).toBeUndefined();
		expect(errorStatus("string")).toBeUndefined();
	});

	test("errorMessage uses an Error's message and a generic line for anything else", () => {
		expect(errorMessage(new Error("Disk full"))).toBe("Disk full");
		expect(errorMessage(new Error(""))).toBe("Something went wrong.");
		expect(errorMessage("boom")).toBe("Something went wrong.");
		expect(errorMessage(undefined)).toBe("Something went wrong.");
	});
});

describe("reportFailure", () => {
	test("routes 401, 403 and 412 to their handlers and ignores other statuses", () => {
		const unauthorized = new ApiError(401, "unauthorized", "x");
		const forbidden = new ApiError(403, "forbidden", "x");
		const conflict = new ApiError(412, "generation_conflict", "x");
		reportFailure(unauthorized);
		reportFailure(forbidden);
		reportFailure(conflict);
		reportFailure(new ApiError(500, "internal", "x"));
		reportFailure(new ApiError(404, "not_found", "x"));
		expect(seen.unauthorized).toBe(1);
		expect(seen.forbidden).toEqual([forbidden]);
		expect(seen.conflict).toEqual([conflict]);
	});

	test("does nothing when no handler is installed", () => {
		setApiHandlers({});
		expect(() =>
			reportFailure(new ApiError(401, "unauthorized", "x")),
		).not.toThrow();
	});
});

describe("the generated client's error interceptor", () => {
	test("turns a problem+json body into an ApiError with its status, code, detail and details", async () => {
		stubApi({
			"GET /projects": problem(422, {
				code: "validation_failed",
				detail: "Name is required.",
				title: "Unprocessable",
				details: { field: "name" },
			}),
		});
		const error = await failureOf(() => listProjects());
		expect(error).toBeInstanceOf(ApiError);
		expect(errorStatus(error)).toBe(422);
		expect(errorCode(error)).toBe("validation_failed");
		expect((error as ApiError).message).toBe("Name is required.");
		expect((error as ApiError).details).toEqual({ field: "name" });
	});

	test("joins field errors when there is no detail, then falls back to the title", async () => {
		stubApi({
			"GET /projects": problem(422, {
				code: "validation_failed",
				title: "Unprocessable",
				errors: [
					{ message: "Name is required" },
					{ message: "" },
					{ message: "Slug taken" },
				],
			}),
		});
		expect(((await failureOf(() => listProjects())) as ApiError).message).toBe(
			"Name is required; Slug taken",
		);

		stubApi({
			// A function reply: GET /projects documents no 409, the interceptor
			// must read any problem document.
			"GET /projects": () =>
				Response.json(
					{ status: 409, code: "conflict", title: "Conflict" },
					{ status: 409 },
				),
		});
		expect(((await failureOf(() => listProjects())) as ApiError).message).toBe(
			"Conflict",
		);
	});

	test("a body without a code reads as code unknown", async () => {
		// A function reply: the contract requires `code`, the interceptor must
		// still cope with a failure body that lacks it.
		stubApi({
			"GET /projects": () => Response.json({ detail: "Oops" }, { status: 500 }),
		});
		const error = await failureOf(() => listProjects());
		expect(errorCode(error)).toBe("unknown");
		expect(errorStatus(error)).toBe(500);
	});

	test("a plain-text failure body becomes the message", async () => {
		stubApi({
			"GET /projects": () =>
				new Response("upstream exploded", {
					status: 502,
					headers: { "Content-Type": "text/plain" },
				}),
		});
		const error = (await failureOf(() => listProjects())) as ApiError;
		expect(error.status).toBe(502);
		expect(error.message).toBe("upstream exploded");
	});

	test("an empty failure body falls back to the status text, or to the status number", async () => {
		stubApi({
			"GET /projects": () =>
				new Response(null, { status: 503, statusText: "Service Unavailable" }),
		});
		expect(((await failureOf(() => listProjects())) as ApiError).message).toBe(
			"Service Unavailable",
		);

		stubApi({ "GET /projects": () => new Response(null, { status: 504 }) });
		expect(((await failureOf(() => listProjects())) as ApiError).message).toBe(
			"Request failed with status 504.",
		);
	});

	test("an unreachable server becomes a network_error with status 0 and reaches no handler", async () => {
		globalThis.fetch = (() =>
			Promise.reject(
				new TypeError("Failed to fetch"),
			)) as unknown as typeof fetch;
		const error = (await failureOf(() => listProjects())) as ApiError;
		expect(error).toBeInstanceOf(ApiError);
		expect(error.status).toBe(0);
		expect(error.code).toBe("network_error");
		expect(error.message).toContain("did not answer");
		expect(seen).toEqual({ unauthorized: 0, forbidden: [], conflict: [] });
	});

	test("an aborted request is left as the abort, not reported as a network failure", async () => {
		globalThis.fetch = (() =>
			Promise.reject(
				new DOMException("aborted", "AbortError"),
			)) as unknown as typeof fetch;
		const error = await failureOf(() => listProjects());
		expect(error).not.toBeInstanceOf(ApiError);
		expect((error as DOMException).name).toBe("AbortError");
	});

	test("401 from a session-protected call ends the session", async () => {
		stubApi({
			"GET /projects": problem(401, {
				code: "unauthorized",
				detail: "Sign in.",
			}),
		});
		await failureOf(() => listProjects());
		expect(seen.unauthorized).toBe(1);
	});

	test.each([
		["GET /me", () => getMe(), { code: "unauthorized" }],
		[
			"POST /auth/login",
			() => login({ body: { email: "a@b.c", password: "x" } as never }),
			{ code: "invalid_credentials", detail: "Wrong password." },
		],
	] as const)(
		"401 from %s is an expected answer and does not end the session",
		async (route, call, body) => {
			stubApi({ [route]: problem(401, body) });
			const error = await failureOf(call);
			expect(errorStatus(error)).toBe(401);
			expect(seen.unauthorized).toBe(0);
		},
	);

	test("403 and 412 reach their handlers with the typed error", async () => {
		stubApi({
			"GET /projects": problem(403, {
				code: "forbidden",
				detail: "Missing scope.",
			}),
		});
		await failureOf(() => listProjects());
		expect(seen.forbidden.map((e) => [e.status, e.code, e.message])).toEqual([
			[403, "forbidden", "Missing scope."],
		]);

		stubApi({
			// A function reply: GET /projects documents no 412; the interceptor
			// handles it by status wherever it comes from.
			"GET /projects": () =>
				Response.json(
					{ status: 412, code: "generation_conflict", detail: "Changed." },
					{ status: 412 },
				),
		});
		await failureOf(() => listProjects());
		expect(seen.conflict.map((e) => e.code)).toEqual(["generation_conflict"]);
	});

	test("sends the session cookie with every call", async () => {
		let credentials: RequestCredentials | undefined;
		stubApi({
			"GET /projects": (request) => {
				credentials = request.credentials;
				return Response.json({ items: [] });
			},
		});
		await listProjects();
		expect(credentials).toBe("include");
	});
});
