import { describe, expect, test } from "bun:test";
import { QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { stubApi } from "#/test/api";
import { testQueryClient } from "#/test/render";
import { previewsQuery } from "./queries";
import { wirePreview, wireSettings } from "./test-data";
import { usePreviewSettings, usePreviews } from "./use-previews";

function setup() {
	const queryClient = testQueryClient();
	const wrapper = ({ children }: { children: ReactNode }) => (
		<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
	);
	return { queryClient, wrapper };
}

/** The refetch interval the observer currently asks for. */
function intervalOf(
	queryClient: ReturnType<typeof testQueryClient>,
	projectId: string,
): number | false | undefined {
	const query = queryClient
		.getQueryCache()
		.find({ queryKey: previewsQuery(projectId).queryKey });
	const option = query?.observers[0]?.options.refetchInterval;
	return typeof option === "function"
		? (option as (q: typeof query) => number | false | undefined)(query)
		: option;
}

describe("usePreviews", () => {
	test("lists the project's previews", async () => {
		const api = stubApi({
			"GET /projects/:id/previews": {
				body: {
					items: [wirePreview(), wirePreview({ id: "pv-2", prNumber: 13 })],
				},
			},
		});
		const { wrapper } = setup();

		const { result } = renderHook(() => usePreviews("proj-1"), { wrapper });

		await waitFor(() => expect(result.current.data).toHaveLength(2));
		expect(api.calls[0]?.path).toBe("/projects/proj-1/previews");
		expect(result.current.data?.map((p) => p.prNumber)).toEqual([12, 13]);
	});

	test("a null item list reads as no previews", async () => {
		stubApi({ "GET /projects/:id/previews": { body: { items: null } } });
		const { wrapper } = setup();

		const { result } = renderHook(() => usePreviews("proj-1"), { wrapper });

		await waitFor(() => expect(result.current.data).toEqual([]));
	});

	test("without a project id nothing is requested", () => {
		const api = stubApi({});
		const { wrapper } = setup();

		const { result } = renderHook(() => usePreviews(undefined), { wrapper });

		expect(result.current.fetchStatus).toBe("idle");
		expect(api.calls).toEqual([]);
		expect(api.unmatched).toEqual([]);
	});

	test.each(["creating", "deploying", "seeding", "deleting"] as const)(
		"polls every 4 seconds while a preview is %s",
		async (status) => {
			stubApi({
				"GET /projects/:id/previews": {
					body: {
						items: [
							wirePreview({ status: "ready" }),
							wirePreview({ id: "pv-2", status }),
						],
					},
				},
			});
			const { queryClient, wrapper } = setup();

			const { result } = renderHook(() => usePreviews("proj-1"), { wrapper });
			await waitFor(() => expect(result.current.data).toHaveLength(2));

			expect(intervalOf(queryClient, "proj-1")).toBe(4000);
		},
	);

	test.each(["ready", "failed", "paused"] as const)(
		"polls every 15 seconds when every preview is %s",
		async (status) => {
			stubApi({
				"GET /projects/:id/previews": {
					body: { items: [wirePreview({ status })] },
				},
			});
			const { queryClient, wrapper } = setup();

			const { result } = renderHook(() => usePreviews("proj-1"), { wrapper });
			await waitFor(() => expect(result.current.data).toHaveLength(1));

			expect(intervalOf(queryClient, "proj-1")).toBe(15_000);
		},
	);

	test("polls every 15 seconds before the first answer", async () => {
		stubApi({
			"GET /projects/:id/previews": () =>
				new Promise<Response>(() => undefined),
		});
		const { queryClient, wrapper } = setup();

		renderHook(() => usePreviews("proj-1"), { wrapper });
		await waitFor(() =>
			expect(
				queryClient.getQueryCache().find({
					queryKey: previewsQuery("proj-1").queryKey,
				}),
			).toBeDefined(),
		);

		expect(intervalOf(queryClient, "proj-1")).toBe(15_000);
	});
});

describe("usePreviewSettings", () => {
	test("reads the project's settings", async () => {
		const api = stubApi({
			"GET /projects/:id/previews/settings": {
				body: wireSettings({ maxConcurrent: 9 }),
			},
		});
		const { wrapper } = setup();

		const { result } = renderHook(() => usePreviewSettings("proj-1"), {
			wrapper,
		});

		await waitFor(() => expect(result.current.data?.maxConcurrent).toBe(9));
		expect(api.calls[0]?.path).toBe("/projects/proj-1/previews/settings");
	});
});
