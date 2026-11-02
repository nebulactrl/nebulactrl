import {
	afterAll,
	beforeAll,
	beforeEach,
	describe,
	expect,
	test,
} from "bun:test";
import { QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { testQueryClient } from "#/test/render";
import {
	installDocsFixtures,
	loaders,
	metas,
	pages,
	resetLoaders,
	restoreEmptyDocs,
} from "./docs-fixtures";

let data: Awaited<ReturnType<typeof installDocsFixtures>>;

beforeAll(async () => {
	data = await installDocsFixtures();
});
beforeEach(resetLoaders);
afterAll(restoreEmptyDocs);

function renderPage(slug: string) {
	const client = testQueryClient();
	const wrapper = ({ children }: { children: ReactNode }) => (
		<QueryClientProvider client={client}>{children}</QueryClientProvider>
	);
	return renderHook(() => data.useDocPage(slug), { wrapper });
}

describe("docMeta", () => {
	test("finds a page by slug", () => {
		expect(data.docMeta("getting-started/concepts")?.title).toBe("Concepts");
	});

	test("is undefined for a slug the docs do not have", () => {
		expect(data.docMeta("nope")).toBeUndefined();
		expect(data.docMeta("")).toBeUndefined();
	});
});

describe("docOrder", () => {
	test("lists pages section by section with a parent before its children", () => {
		const slugs = data.docOrder.map((meta) => meta.slug);
		expect(slugs.slice(0, 5)).toEqual([
			"getting-started/overview",
			"getting-started/concepts",
			"getting-started/first-deploy",
			"getting-started/next-steps",
			"applications/page-1",
		]);
		expect(slugs.slice(-5)).toEqual([
			"reference/api",
			"reference/api/projects",
			"reference/api/servers",
			"reference/cli",
			"internals/virtual",
		]);
		expect(slugs).toHaveLength(metas.length);
	});
});

describe("childrenOf", () => {
	test("returns a page's children by their order, not their manifest position", () => {
		expect(data.childrenOf("reference/api").map((meta) => meta.slug)).toEqual([
			"reference/api/projects",
			"reference/api/servers",
		]);
	});

	test("is empty for a page without children", () => {
		expect(data.childrenOf("reference/cli")).toEqual([]);
	});
});

describe("docNav", () => {
	test("is the manifest's navigation", () => {
		expect(data.docNav.map((section) => section.id)).toEqual([
			"getting-started",
			"applications",
			"reference",
			"internals",
		]);
	});
});

describe("useDocPage", () => {
	test("loads the page body from its chunk", async () => {
		const { result } = renderPage("getting-started/concepts");
		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(result.current.data).toEqual(pages["getting-started/concepts"]);
	});

	test("does not fetch a page the manifest does not list", async () => {
		let calls = 0;
		const cli = pages["reference/cli"];
		if (!cli) throw new Error("fixture page reference/cli is missing");
		loaders.nope = () => {
			calls++;
			return Promise.resolve({ default: cli });
		};
		const { result } = renderPage("nope");
		expect(result.current.fetchStatus).toBe("idle");
		expect(result.current.isPending).toBe(true);
		expect(calls).toBe(0);
	});

	test("fails when a listed page has no loader", async () => {
		const { result } = renderPage("getting-started/next-steps");
		await waitFor(() => expect(result.current.isError).toBe(true));
		expect(result.current.error?.message).toBe(
			'No docs page "getting-started/next-steps"',
		);
	});

	test("fails when the page chunk cannot be fetched", async () => {
		loaders["reference/cli"] = () => Promise.reject(new Error("chunk gone"));
		const { result } = renderPage("reference/cli");
		await waitFor(() => expect(result.current.isError).toBe(true));
		expect(result.current.error?.message).toBe("chunk gone");
	});
});
