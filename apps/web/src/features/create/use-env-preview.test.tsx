import { describe, expect, test } from "bun:test";
import { QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import type { EnvTemplatePreviewInput } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { testQueryClient } from "#/test/render";
import { scanBody, suggested } from "./fixtures";
import { useEnvTemplatePreview } from "./use-env-preview";

// Typing settles for 700 ms before the repository is read.
const SETTLED = { timeout: 4000 };

function setup(initial: EnvTemplatePreviewInput | null) {
	const client = testQueryClient();
	const wrapper = ({ children }: { children: ReactNode }) => (
		<QueryClientProvider client={client}>{children}</QueryClientProvider>
	);
	return renderHook(
		({ input }: { input: EnvTemplatePreviewInput | null }) =>
			useEnvTemplatePreview("env-1", input),
		{ wrapper, initialProps: { input: initial } },
	);
}

const REPO: EnvTemplatePreviewInput = {
	gitUrl: "https://github.com/acme/shop.git",
	branch: "main",
};

describe("useEnvTemplatePreview", () => {
	test("with no source yet it reads nothing and has nothing to show", async () => {
		const api = stubApi({});
		const { result } = setup(null);
		expect(result.current).toEqual({
			loading: false,
			scan: undefined,
			items: [],
		});
		// A settle period passes without any request.
		await new Promise((resolve) => setTimeout(resolve, 800));
		expect(api.calls).toEqual([]);
		expect(api.unmatched).toEqual([]);
	});

	test("a source present on mount is read right away and shows as loading until answered", async () => {
		const gate = Promise.withResolvers<void>();
		const api = stubApi({
			"POST /environments/:environmentId/env-template-preview": async () => {
				await gate.promise;
				return Response.json(
					scanBody({
						items: [
							suggested({ key: "LOG_LEVEL" }),
							suggested({ key: "DATABASE_URL", source: "database" }),
						],
					}),
				);
			},
		});
		const { result } = setup(REPO);
		await waitFor(() => expect(api.calls).toHaveLength(1));
		expect(api.calls[0]).toMatchObject({
			method: "POST",
			path: "/environments/env-1/env-template-preview",
			body: REPO,
		});
		expect(result.current.loading).toBe(true);
		expect(result.current.scan).toBeUndefined();

		gate.resolve();
		await waitFor(() => expect(result.current.scan).toBeDefined());
		expect(result.current.loading).toBe(false);
		// Only the repository's own example keys are reviewed.
		expect(result.current.items.map((item) => item.key)).toEqual(["LOG_LEVEL"]);
		expect(api.unmatched).toEqual([]);
	});

	test("a failed read is no answer: nothing to review and no longer loading", async () => {
		const api = stubApi({
			"POST /environments/:environmentId/env-template-preview": problem(500, {
				title: "Internal Server Error",
				status: 500,
			}),
		});
		const { result } = setup(REPO);
		await waitFor(() => expect(api.calls).toHaveLength(1), SETTLED);
		await waitFor(() => expect(result.current.loading).toBe(false));
		expect(result.current.scan).toBeUndefined();
		expect(result.current.items).toEqual([]);
	});

	test("changing the source drops the old answer at once and loads the new one", async () => {
		const api = stubApi({
			"POST /environments/:environmentId/env-template-preview": (request) =>
				request
					.clone()
					.json()
					.then((body: EnvTemplatePreviewInput) =>
						Response.json(
							scanBody({
								items: [
									suggested({
										key: body.branch === "main" ? "FROM_MAIN" : "FROM_DEV",
									}),
								],
							}),
						),
					),
		});
		const { result, rerender } = setup(REPO);
		await waitFor(
			() =>
				expect(result.current.items.map((i) => i.key)).toEqual(["FROM_MAIN"]),
			SETTLED,
		);

		rerender({ input: { ...REPO, branch: "dev" } });
		// The answer for the old branch must not be shown for the new one, and
		// the new branch is not read until typing has settled.
		expect(result.current.loading).toBe(true);
		expect(api.calls).toHaveLength(1);
		expect(result.current.scan).toBeUndefined();
		expect(result.current.items).toEqual([]);

		await waitFor(
			() =>
				expect(result.current.items.map((i) => i.key)).toEqual(["FROM_DEV"]),
			SETTLED,
		);
		expect(result.current.loading).toBe(false);
		expect(
			api.calls.map((call) => (call.body as { branch: string }).branch),
		).toEqual(["main", "dev"]);
	});

	test("clearing the source forgets the answer", async () => {
		stubApi({
			"POST /environments/:environmentId/env-template-preview": {
				body: scanBody(),
			},
		});
		const { result, rerender } = setup(REPO);
		await waitFor(() => expect(result.current.scan).toBeDefined(), SETTLED);

		rerender({ input: null });
		expect(result.current.scan).toBeUndefined();
		expect(result.current.items).toEqual([]);
		expect(result.current.loading).toBe(false);
		// And it stays forgotten once the settle period passes.
		await new Promise((resolve) => setTimeout(resolve, 800));
		expect(result.current.scan).toBeUndefined();
	});
});
