import { describe, expect, test } from "bun:test";
import { QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { ApiError } from "#/api/client-config";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { testQueryClient } from "#/test/render";
import {
	exportConfig,
	useApplyFromGit,
	useCheckBinding,
	useClearBinding,
	useEnvironmentConfigSource,
	usePlanFromGit,
	useReleaseFields,
	useServiceConfigSource,
	useUpdateBinding,
} from "./queries";

function wrapper(client = testQueryClient()) {
	return ({ children }: { children: ReactNode }) => (
		<QueryClientProvider client={client}>{children}</QueryClientProvider>
	);
}

const target = { projectId: "p1", environmentSlug: "staging" };

const serviceSource = make("ServiceConfigSource", {
	managed: true,
	state: "in_sync",
	fields: [],
	pending: [],
});
const envSource = make("EnvironmentConfigSource", {
	mode: "explicit",
	status: "in_sync",
	services: [],
});
const serviceLater = { ...serviceSource, state: "drift" };
const envLater = { ...envSource, status: "differs" };

/** The first read answers `first`, every later one `later`: a refetch shows. */
function changing(first: unknown, later: unknown) {
	let reads = 0;
	return () => Response.json(reads++ === 0 ? first : later);
}

describe("useServiceConfigSource", () => {
	test("reads the service's source", async () => {
		const api = stubApi({
			"GET /services/:id/config-source": { body: serviceSource },
		});
		const { result } = renderHook(() => useServiceConfigSource("svc_1"), {
			wrapper: wrapper(),
		});
		await waitFor(() => expect(result.current.data?.managed).toBe(true));
		expect(api.calls.map((c) => c.path)).toEqual([
			"/services/svc_1/config-source",
		]);
	});

	test("asks nothing without a service id or when disabled", async () => {
		const api = stubApi({});
		const missing = renderHook(() => useServiceConfigSource(undefined), {
			wrapper: wrapper(),
		});
		const off = renderHook(() => useServiceConfigSource("svc_1", false), {
			wrapper: wrapper(),
		});
		await act(async () => {});
		expect(missing.result.current.fetchStatus).toBe("idle");
		expect(off.result.current.fetchStatus).toBe("idle");
		expect(api.calls).toEqual([]);
		expect(api.unmatched).toEqual([]);
	});
});

describe("useEnvironmentConfigSource", () => {
	test("reads the environment's binding", async () => {
		const api = stubApi({
			"GET /environments/:id/config-source": { body: envSource },
		});
		const { result } = renderHook(() => useEnvironmentConfigSource("env_1"), {
			wrapper: wrapper(),
		});
		await waitFor(() => expect(result.current.data?.mode).toBe("explicit"));
		expect(api.calls[0]?.path).toBe("/environments/env_1/config-source");
	});

	test("asks nothing without an environment id or when disabled", async () => {
		const api = stubApi({});
		const missing = renderHook(() => useEnvironmentConfigSource(undefined), {
			wrapper: wrapper(),
		});
		const off = renderHook(() => useEnvironmentConfigSource("env_1", false), {
			wrapper: wrapper(),
		});
		await act(async () => {});
		expect(missing.result.current.fetchStatus).toBe("idle");
		expect(off.result.current.fetchStatus).toBe("idle");
		expect(api.calls).toEqual([]);
	});
});

describe("usePlanFromGit", () => {
	test("plans the environment's file at its newest commit", async () => {
		const api = stubApi({
			"POST /projects/:id/config/plan": {
				body: make("ConfigPlan", { planId: "pl_1" }),
			},
		});
		const { result } = renderHook(() => usePlanFromGit(target), {
			wrapper: wrapper(),
		});
		let data: unknown;
		await act(async () => {
			data = await result.current.mutateAsync(undefined);
		});
		expect(data).toMatchObject({ planId: "pl_1" });
		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]?.path).toBe("/projects/p1/config/plan");
		expect(api.calls[0]?.body).toEqual({
			environment: "staging",
			source: { git: {} },
		});
	});

	test("names the ref and the only-list when given", async () => {
		const api = stubApi({
			"POST /projects/:id/config/plan": {
				body: make("ConfigPlan", { planId: "pl_1" }),
			},
		});
		const { result } = renderHook(() => usePlanFromGit(target), {
			wrapper: wrapper(),
		});
		await act(async () => {
			await result.current.mutateAsync({
				ref: "abc123",
				only: ["services.web.port"],
			});
		});
		expect(api.calls[0]?.body).toEqual({
			environment: "staging",
			source: { git: { ref: "abc123" } },
			only: ["services.web.port"],
		});
	});

	test("rejects with the control plane's error", async () => {
		stubApi({
			"POST /projects/:id/config/plan": problem(422, {
				code: "config_invalid",
				detail: "bad file",
			}),
		});
		const { result } = renderHook(() => usePlanFromGit(target), {
			wrapper: wrapper(),
		});
		let caught: unknown;
		await act(async () => {
			caught = await result.current.mutateAsync(undefined).catch((e) => e);
		});
		expect(caught).toBeInstanceOf(ApiError);
		expect((caught as ApiError).code).toBe("config_invalid");
		expect((caught as ApiError).message).toBe("bad file");
	});
});

describe("useApplyFromGit", () => {
	test("sends the reviewed plan with an idempotency key and refreshes config reads", async () => {
		const client = testQueryClient();
		const api = stubApi({
			"POST /projects/:id/config/apply": {
				status: 201,
				body: make("ConfigApplyResult", { status: "staged" }),
			},
			"GET /services/:id/config-source": changing(serviceSource, serviceLater),
		});
		// A mounted service-source read must be refetched once the apply settles.
		const reads = renderHook(() => useServiceConfigSource("svc_1"), {
			wrapper: wrapper(client),
		});
		await waitFor(() => expect(reads.result.current.data).toBeDefined());
		const { result } = renderHook(() => useApplyFromGit(target), {
			wrapper: wrapper(client),
		});
		await act(async () => {
			await result.current.mutateAsync({
				planId: "pl_1",
				deploy: true,
				only: ["services.web.port"],
				idempotencyKey: "key-1",
			});
		});
		const apply = api.calls.find((c) => c.method === "POST");
		expect(apply?.path).toBe("/projects/p1/config/apply");
		expect(apply?.body).toEqual({
			environment: "staging",
			source: { git: {} },
			planId: "pl_1",
			only: ["services.web.port"],
			deploy: true,
		});
		await waitFor(() => expect(reads.result.current.data?.state).toBe("drift"));
	});

	test("deploy defaults to false and optional fields are left out", async () => {
		const api = stubApi({
			"POST /projects/:id/config/apply": {
				status: 201,
				body: make("ConfigApplyResult", { status: "staged" }),
			},
		});
		const { result } = renderHook(() => useApplyFromGit(target), {
			wrapper: wrapper(),
		});
		await act(async () => {
			await result.current.mutateAsync({ idempotencyKey: "key-2" });
		});
		expect(api.calls[0]?.body).toEqual({
			environment: "staging",
			source: { git: {} },
			deploy: false,
		});
	});

	test("the Idempotency-Key header carries the caller's key", async () => {
		let header: string | null = null;
		stubApi({
			"POST /projects/:id/config/apply": (request) => {
				header = request.headers.get("Idempotency-Key");
				return Response.json(make("ConfigApplyResult", { status: "staged" }));
			},
		});
		const { result } = renderHook(() => useApplyFromGit(target), {
			wrapper: wrapper(),
		});
		await act(async () => {
			await result.current.mutateAsync({ idempotencyKey: "key-3" });
		});
		expect(header === "key-3").toBe(true);
	});
});

describe("useReleaseFields", () => {
	test("posts the paths and refetches the service's source", async () => {
		const client = testQueryClient();
		const api = stubApi({
			"POST /services/:id/config-source/release": { body: serviceSource },
			"GET /services/:id/config-source": changing(serviceSource, serviceLater),
		});
		const reads = renderHook(() => useServiceConfigSource("svc_1"), {
			wrapper: wrapper(client),
		});
		await waitFor(() => expect(reads.result.current.data).toBeDefined());
		const { result } = renderHook(() => useReleaseFields("svc_1"), {
			wrapper: wrapper(client),
		});
		await act(async () => {
			await result.current.mutateAsync(["config.port", "variables.A"]);
		});
		const post = api.calls.find((c) => c.method === "POST");
		expect(post?.path).toBe("/services/svc_1/config-source/release");
		expect(post?.body).toEqual({ paths: ["config.port", "variables.A"] });
		await waitFor(() => expect(reads.result.current.data?.state).toBe("drift"));
	});
});

describe("binding mutations", () => {
	test("useUpdateBinding sends the binding and refetches", async () => {
		const client = testQueryClient();
		const api = stubApi({
			"PUT /environments/:id/config-source": { body: envSource },
			"GET /environments/:id/config-source": changing(envSource, envLater),
		});
		const reads = renderHook(() => useEnvironmentConfigSource("env_1"), {
			wrapper: wrapper(client),
		});
		await waitFor(() => expect(reads.result.current.data).toBeDefined());
		const { result } = renderHook(() => useUpdateBinding("env_1"), {
			wrapper: wrapper(client),
		});
		await act(async () => {
			await result.current.mutateAsync({
				connectionId: "c1",
				repo: "acme/shop",
				branch: "main",
				autoApply: true,
			});
		});
		const write = api.calls.find((c) => c.method !== "GET");
		expect(write?.path).toBe("/environments/env_1/config-source");
		expect(write?.body).toEqual({
			connectionId: "c1",
			repo: "acme/shop",
			branch: "main",
			autoApply: true,
		});
		await waitFor(() =>
			expect(reads.result.current.data?.status).toBe("differs"),
		);
	});

	test("useClearBinding deletes the binding and refetches", async () => {
		const client = testQueryClient();
		const api = stubApi({
			"DELETE /environments/:id/config-source": { status: 204 },
			"GET /environments/:id/config-source": changing(envSource, envLater),
		});
		const reads = renderHook(() => useEnvironmentConfigSource("env_1"), {
			wrapper: wrapper(client),
		});
		await waitFor(() => expect(reads.result.current.data).toBeDefined());
		const { result } = renderHook(() => useClearBinding("env_1"), {
			wrapper: wrapper(client),
		});
		await act(async () => {
			await result.current.mutateAsync();
		});
		expect(api.calls.find((c) => c.method === "DELETE")?.path).toBe(
			"/environments/env_1/config-source",
		);
		await waitFor(() =>
			expect(reads.result.current.data?.status).toBe("differs"),
		);
	});

	test("useCheckBinding posts to the check endpoint and returns the result", async () => {
		const api = stubApi({
			"POST /environments/:id/config-source/check": {
				body: { ...envSource, status: "differs" },
			},
		});
		const { result } = renderHook(() => useCheckBinding("env_1"), {
			wrapper: wrapper(),
		});
		let data: { status?: string } | undefined;
		await act(async () => {
			data = await result.current.mutateAsync();
		});
		expect(data?.status).toBe("differs");
		expect(api.calls[0]?.path).toBe("/environments/env_1/config-source/check");
	});
});

describe("exportConfig", () => {
	test("asks for JSON and returns the file with its warnings", async () => {
		let accept: string | null = null;
		const api = stubApi({
			"GET /projects/:id/config/export": (request) => {
				accept = request.headers.get("Accept");
				return Response.json({
					file: "[project]\n",
					warnings: [{ code: "secret_flag_differs", message: "x" }],
				});
			},
		});
		const out = await exportConfig({ projectId: "p1", environment: "all" });
		expect(out).toEqual({
			file: "[project]\n",
			warnings: [{ code: "secret_flag_differs", message: "x" }],
		});
		expect(api.calls[0]?.query.get("environment")).toBe("all");
		expect(accept === "application/json").toBe(true);
	});

	test("accepts the plain-text form: the body is the file", async () => {
		stubApi({
			"GET /projects/:id/config/export": () =>
				new Response("[project]\nname = 'x'\n", {
					headers: { "Content-Type": "text/plain" },
				}),
		});
		const out = await exportConfig({ projectId: "p1", environment: "prod" });
		expect(out).toEqual({ file: "[project]\nname = 'x'\n", warnings: [] });
	});

	test("an object without file or warnings gives an empty file", async () => {
		stubApi({
			// A function reply: the contract always sends file and warnings, and
			// exportConfig must still cope with an object that lacks them.
			"GET /projects/:id/config/export": () => Response.json({ other: 1 }),
		});
		const out = await exportConfig({ projectId: "p1", environment: "prod" });
		expect(out).toEqual({ file: "", warnings: [] });
	});

	test("non-string file and non-array warnings are ignored", async () => {
		stubApi({
			// A function reply: the contract types both, exportConfig must still
			// ignore a body that does not.
			"GET /projects/:id/config/export": () =>
				Response.json({ file: 5, warnings: "nope" }),
		});
		const out = await exportConfig({ projectId: "p1", environment: "prod" });
		expect(out).toEqual({ file: "", warnings: [] });
	});

	test("a failed export throws the control plane's error", async () => {
		stubApi({
			"GET /projects/:id/config/export": problem(404, {
				code: "not_found",
				detail: "no such project",
			}),
		});
		await expect(
			exportConfig({ projectId: "p1", environment: "prod" }),
		).rejects.toThrow("no such project");
	});
});
