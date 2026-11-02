import { afterEach, describe, expect, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { ApiError, setApiHandlers } from "#/api/client-config";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import {
	fetchPreview,
	uploadFile,
	useDirectory,
	useHostShellInfo,
	useTerminalInfo,
} from "./queries";
import { hostShellInfo, listing, terminalInfo } from "./test-support";

afterEach(() => setApiHandlers({}));

/** A client that would retry three times, so a hook that opts out is visible. */
function retryingClient() {
	return new QueryClient({
		defaultOptions: { queries: { retry: 3, retryDelay: 0 } },
	});
}

function wrapper(client: QueryClient) {
	return ({ children }: { children: ReactNode }) => (
		<QueryClientProvider client={client}>{children}</QueryClientProvider>
	);
}

describe("useTerminalInfo", () => {
	test("loads the info of the service", async () => {
		const api = stubApi({
			"GET /services/:serviceId/terminal": {
				body: terminalInfo({ maxSessions: 7 }),
			},
		});
		const { result } = renderHook(() => useTerminalInfo("svc-1"), {
			wrapper: wrapper(retryingClient()),
		});
		await waitFor(() => expect(result.current.data?.maxSessions).toBe(7));
		expect(api.calls.map((c) => `${c.method} ${c.path}`)).toEqual([
			"GET /services/svc-1/terminal",
		]);
	});

	test("asks for nothing without a service", async () => {
		const api = stubApi({});
		const { result } = renderHook(() => useTerminalInfo(null), {
			wrapper: wrapper(retryingClient()),
		});
		expect(result.current.fetchStatus).toBe("idle");
		expect(api.calls).toEqual([]);
		expect(api.unmatched).toEqual([]);
	});
});

describe("useHostShellInfo", () => {
	test("loads the info of the server", async () => {
		const api = stubApi({
			"GET /servers/:serverId/host-shell": {
				body: hostShellInfo({ serverName: "edge-9" }),
			},
		});
		const { result } = renderHook(() => useHostShellInfo("srv-9"), {
			wrapper: wrapper(retryingClient()),
		});
		await waitFor(() => expect(result.current.data?.serverName).toBe("edge-9"));
		expect(api.calls[0]?.path).toBe("/servers/srv-9/host-shell");
	});

	test("a 403 (not an owner or admin) is an error asked for once, not retried", async () => {
		const api = stubApi({
			"GET /servers/:serverId/host-shell": problem(403, {
				title: "Forbidden",
				code: "forbidden",
			}),
		});
		const { result } = renderHook(() => useHostShellInfo("srv-1"), {
			wrapper: wrapper(retryingClient()),
		});
		await waitFor(() => expect(result.current.isError).toBe(true));
		expect(api.calls).toHaveLength(1);
	});

	test("asks for nothing without a server", () => {
		const api = stubApi({});
		const { result } = renderHook(() => useHostShellInfo(null), {
			wrapper: wrapper(retryingClient()),
		});
		expect(result.current.fetchStatus).toBe("idle");
		expect(api.calls).toEqual([]);
	});
});

describe("useDirectory", () => {
	test("lists a directory of a replica, naming the process only when there is one", async () => {
		const api = stubApi({
			"GET /services/:serviceId/files": { body: listing([]) },
		});
		const plain = renderHook(() => useDirectory("svc-1", 2, "/var/log"), {
			wrapper: wrapper(retryingClient()),
		});
		await waitFor(() => expect(plain.result.current.isSuccess).toBe(true));
		const worker = renderHook(() => useDirectory("svc-1", 0, "/", "worker"), {
			wrapper: wrapper(retryingClient()),
		});
		await waitFor(() => expect(worker.result.current.isSuccess).toBe(true));

		const [first, second] = api.calls;
		expect(first?.path).toBe("/services/svc-1/files");
		expect(Object.fromEntries(first?.query ?? [])).toEqual({
			replica: "2",
			path: "/var/log",
		});
		expect(Object.fromEntries(second?.query ?? [])).toEqual({
			replica: "0",
			path: "/",
			process: "worker",
		});
	});

	test("a failed listing is an error asked for once", async () => {
		const api = stubApi({
			"GET /services/:serviceId/files": problem(404, {
				title: "Not Found",
				detail: "No such directory",
			}),
		});
		const { result } = renderHook(() => useDirectory("svc-1", 0, "/nope"), {
			wrapper: wrapper(retryingClient()),
		});
		await waitFor(() => expect(result.current.isError).toBe(true));
		expect(api.calls).toHaveLength(1);
	});

	test("asks for nothing without a service", () => {
		const api = stubApi({});
		const { result } = renderHook(() => useDirectory(null, 0, "/"), {
			wrapper: wrapper(retryingClient()),
		});
		expect(result.current.fetchStatus).toBe("idle");
		expect(api.calls).toEqual([]);
	});
});

describe("uploadFile", () => {
	test("POSTs the raw bytes to the directory, with the file's name", async () => {
		let seen: Request | undefined;
		const api = stubApi({
			"POST /services/:serviceId/files/upload": (request) => {
				seen = request;
				return Response.json({ path: "/data/in.json", size: 7 });
			},
		});
		const file = new File(['{"k":1}'], "in.json", { type: "text/plain" });
		const result = await uploadFile({
			serviceId: "svc-1",
			process: "worker",
			replica: 1,
			dir: "/data",
			file,
		});
		expect(result).toEqual({ path: "/data/in.json", size: 7 });
		const call = api.calls[0];
		expect(call?.method).toBe("POST");
		expect(call?.path).toBe("/services/svc-1/files/upload");
		expect(Object.fromEntries(call?.query ?? [])).toEqual({
			process: "worker",
			replica: "1",
			path: "/data",
			name: "in.json",
		});
		// The body is the file's bytes, not a form or JSON wrapper.
		expect(call?.body).toEqual({ k: 1 });
		expect(seen?.headers.get("Content-Type")).toBe("application/octet-stream");
		expect(seen?.credentials).toBe("include");
	});

	test("leaves the process out for a service without processes", async () => {
		const api = stubApi({
			"POST /services/:serviceId/files/upload": {
				body: { path: "/a", size: 2 },
			},
		});
		await uploadFile({
			serviceId: "svc-1",
			replica: 0,
			dir: "/",
			file: new File(["{}"], "a"),
		});
		expect(api.calls[0]?.query.has("process")).toBe(false);
	});

	test("a refusal becomes an ApiError with the server's code and reason", async () => {
		stubApi({
			"POST /services/:serviceId/files/upload": {
				status: 413,
				body: { code: "too_large", detail: "The file is over 50 MiB." },
			},
		});
		const error = await uploadFile({
			serviceId: "svc-1",
			replica: 0,
			dir: "/",
			file: new File(["{}"], "big.bin"),
		}).catch((e: unknown) => e);
		expect(error).toBeInstanceOf(ApiError);
		expect(error).toMatchObject({
			status: 413,
			code: "too_large",
			message: "The file is over 50 MiB.",
		});
	});

	test("an error that is not a problem document still has a message", async () => {
		stubApi({
			"POST /services/:serviceId/files/upload": () =>
				new Response("upstream exploded", { status: 502 }),
		});
		const error = await uploadFile({
			serviceId: "svc-1",
			replica: 0,
			dir: "/",
			file: new File(["{}"], "a"),
		}).catch((e: unknown) => e);
		expect(error).toMatchObject({
			status: 502,
			code: "unknown",
			message: "Request failed with status 502.",
		});
	});

	test("a 401 reaches the app's sign-in handler, a 403 its forbidden handler", async () => {
		const seen: string[] = [];
		setApiHandlers({
			onUnauthorized: () => seen.push("401"),
			onForbidden: (e) => seen.push(`403 ${e.code}`),
		});
		stubApi({
			"POST /services/:serviceId/files/upload": (request) =>
				new Response(JSON.stringify({ code: "forbidden", detail: "No." }), {
					status:
						new URL(request.url).searchParams.get("name") === "a" ? 401 : 403,
				}),
		});
		const input = { serviceId: "svc-1", replica: 0, dir: "/" };
		await uploadFile({ ...input, file: new File(["{}"], "a") }).catch(() => {});
		await uploadFile({ ...input, file: new File(["{}"], "b") }).catch(() => {});
		expect(seen).toEqual(["401", "403 forbidden"]);
	});

	test("an unreachable control plane says the file was not uploaded", async () => {
		stubApi({
			"POST /services/:serviceId/files/upload": () => {
				throw new TypeError("network down");
			},
		});
		const error = await uploadFile({
			serviceId: "svc-1",
			replica: 0,
			dir: "/",
			file: new File(["{}"], "a"),
		}).catch((e: unknown) => e);
		expect(error).toMatchObject({
			status: 0,
			code: "network_error",
			message: "The control plane did not answer. The file was not uploaded.",
		});
	});
});

describe("fetchPreview", () => {
	test("returns the bytes of the file", async () => {
		stubApi({
			"GET /services/:serviceId/files/content": () =>
				new Response(new Uint8Array([104, 105, 0, 255])),
		});
		const bytes = await fetchPreview(
			"/api/v1/services/svc-1/files/content?replica=0&path=%2Fa&preview=true",
		);
		expect(Array.from(bytes)).toEqual([104, 105, 0, 255]);
	});

	test("a failed read becomes an ApiError with the server's reason", async () => {
		stubApi({
			"GET /services/:serviceId/files/content": {
				status: 404,
				body: { code: "not_found", detail: "No such file." },
			},
		});
		const error = await fetchPreview(
			"/api/v1/services/svc-1/files/content?replica=0&path=%2Fa",
		).catch((e: unknown) => e);
		expect(error).toMatchObject({
			status: 404,
			code: "not_found",
			message: "No such file.",
		});
	});

	test("an unreachable control plane is a network error", async () => {
		stubApi({
			"GET /services/:serviceId/files/content": () => {
				throw new TypeError("offline");
			},
		});
		const error = await fetchPreview(
			"/api/v1/services/svc-1/files/content?replica=0&path=%2Fa",
		).catch((e: unknown) => e);
		expect(error).toMatchObject({
			status: 0,
			code: "network_error",
			message: "The control plane did not answer.",
		});
	});
});
