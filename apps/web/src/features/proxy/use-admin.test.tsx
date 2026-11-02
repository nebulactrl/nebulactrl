import { describe, expect, test } from "bun:test";
import { QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { testQueryClient } from "#/test/render";
import { useIsAdmin } from "./use-admin";

function wrapper(): ({ children }: { children: ReactNode }) => ReactNode {
	const client = testQueryClient();
	return ({ children }) => (
		<QueryClientProvider client={client}>{children}</QueryClientProvider>
	);
}

const me = (scopes: string[] | null) => ({
	body: make("MeBody", { principal: { label: "robin", type: "user", scopes } }),
});

describe("useIsAdmin", () => {
	test("is false until the principal has loaded", () => {
		stubApi({ "GET /me": me(["admin"]) });
		const { result } = renderHook(() => useIsAdmin(), { wrapper: wrapper() });
		expect(result.current).toBe(false);
	});

	test("is true when the principal holds the admin scope", async () => {
		stubApi({ "GET /me": me(["read", "admin"]) });
		const { result } = renderHook(() => useIsAdmin(), { wrapper: wrapper() });
		await waitFor(() => expect(result.current).toBe(true));
	});

	test("is false for a principal without the admin scope", async () => {
		const api = stubApi({ "GET /me": me(["read", "write"]) });
		const { result } = renderHook(() => useIsAdmin(), { wrapper: wrapper() });
		await waitFor(() => expect(api.calls).toHaveLength(1));
		await waitFor(() => expect(result.current).toBe(false));
	});

	test("is false when the scopes list is null", async () => {
		const api = stubApi({ "GET /me": me(null) });
		const { result } = renderHook(() => useIsAdmin(), { wrapper: wrapper() });
		await waitFor(() => expect(api.calls).toHaveLength(1));
		expect(result.current).toBe(false);
	});

	test("stays false when /me fails", async () => {
		const api = stubApi({
			"GET /me": problem(401, { code: "unauthorized" }),
		});
		const { result } = renderHook(() => useIsAdmin(), { wrapper: wrapper() });
		await waitFor(() => expect(api.calls).toHaveLength(1));
		expect(result.current).toBe(false);
	});
});
