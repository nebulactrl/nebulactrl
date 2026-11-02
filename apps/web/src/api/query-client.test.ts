import { describe, expect, test } from "bun:test";
import { ApiError } from "./client-config";
import { queryClient } from "./query-client";

const defaults = queryClient.getDefaultOptions().queries;
const retry = defaults?.retry as (
	failureCount: number,
	error: unknown,
) => boolean;

describe("the app query client", () => {
	test("polls as a fallback, but not in a hidden tab or on window focus", () => {
		expect(defaults?.refetchInterval).toBe(15_000);
		expect(defaults?.refetchIntervalInBackground).toBe(false);
		expect(defaults?.refetchOnWindowFocus).toBe(false);
		expect(defaults?.staleTime).toBe(5_000);
	});

	test("never retries a 4xx, which asking again will not change", () => {
		for (const status of [400, 401, 403, 404, 412, 422, 499]) {
			expect(retry(0, new ApiError(status, "x", "x"))).toBe(false);
		}
	});

	test("retries a 5xx or a network error once", () => {
		expect(retry(0, new ApiError(500, "internal", "x"))).toBe(true);
		expect(retry(0, new ApiError(0, "network_error", "x"))).toBe(true);
		expect(retry(0, new Error("boom"))).toBe(true);
		expect(retry(1, new ApiError(503, "x", "x"))).toBe(false);
		expect(retry(1, new Error("boom"))).toBe(false);
	});
});
