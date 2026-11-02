import { describe, expect, test } from "bun:test";
import { QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { stubApi } from "#/test/api";
import { testQueryClient } from "#/test/render";
import { useDockerPolicy } from "./docker-policy";

function wrapper(client = testQueryClient()) {
	return ({ children }: { children: ReactNode }) => (
		<QueryClientProvider client={client}>{children}</QueryClientProvider>
	);
}

const catalog = {
	safeCapabilities: ["CHOWN"],
	warnCapabilities: ["SYS_PTRACE"],
	unrestrictedCapabilities: ["NET_ADMIN"],
	knownCapabilities: ["CHOWN", "NET_ADMIN"],
	allowedSysctls: ["net.core.somaxconn"],
	hostSysctlKeys: ["vm.max_map_count"],
	gpuCapabilities: ["compute"],
	reservedLabels: ["traefik."],
	stopSignals: ["SIGTERM"],
	unrestrictedFields: ["privileged"],
};

describe("useDockerPolicy", () => {
	test("is pending with no policy until the catalog arrives, then maps it", async () => {
		const api = stubApi({
			"GET /instance/docker-options": { body: catalog },
		});
		const { result } = renderHook(() => useDockerPolicy(), {
			wrapper: wrapper(),
		});
		expect(result.current).toEqual({
			policy: undefined,
			pending: true,
			failed: false,
		});

		await waitFor(() => expect(result.current.pending).toBe(false));
		expect(result.current.failed).toBe(false);
		expect(result.current.policy).toEqual({
			safeCapabilities: ["CHOWN"],
			warnCapabilities: ["SYS_PTRACE"],
			unrestrictedCapabilities: ["NET_ADMIN"],
			knownCapabilities: ["CHOWN", "NET_ADMIN"],
			allowedSysctls: ["net.core.somaxconn"],
			hostSysctlKeys: ["vm.max_map_count"],
			gpuCapabilities: ["compute"],
			reservedLabels: ["traefik."],
			stopSignals: ["SIGTERM"],
		});
		expect(api.calls.map((call) => `${call.method} ${call.path}`)).toEqual([
			"GET /instance/docker-options",
		]);
	});

	test("null lists in the catalog become empty lists", async () => {
		stubApi({
			"GET /instance/docker-options": {
				body: {
					safeCapabilities: null,
					warnCapabilities: null,
					unrestrictedCapabilities: null,
					knownCapabilities: null,
					allowedSysctls: null,
					hostSysctlKeys: null,
					gpuCapabilities: null,
					reservedLabels: null,
					stopSignals: null,
					unrestrictedFields: null,
				},
			},
		});
		const { result } = renderHook(() => useDockerPolicy(), {
			wrapper: wrapper(),
		});
		await waitFor(() => expect(result.current.policy).toBeDefined());
		expect(result.current.policy?.safeCapabilities).toEqual([]);
		expect(result.current.policy?.stopSignals).toEqual([]);
	});

	test("a failed load reports failure and keeps the policy undefined", async () => {
		stubApi({
			"GET /instance/docker-options": {
				status: 500,
				body: { title: "Internal", status: 500, code: "internal" },
			},
		});
		const { result } = renderHook(() => useDockerPolicy(), {
			wrapper: wrapper(),
		});
		await waitFor(() => expect(result.current.failed).toBe(true));
		expect(result.current.pending).toBe(false);
		expect(result.current.policy).toBeUndefined();
	});

	test("two readers share one request", async () => {
		const api = stubApi({
			"GET /instance/docker-options": { body: catalog },
		});
		const client = testQueryClient();
		const first = renderHook(() => useDockerPolicy(), {
			wrapper: wrapper(client),
		});
		const second = renderHook(() => useDockerPolicy(), {
			wrapper: wrapper(client),
		});
		await waitFor(() => expect(first.result.current.policy).toBeDefined());
		await waitFor(() => expect(second.result.current.policy).toBeDefined());
		expect(api.calls).toHaveLength(1);
	});
});
