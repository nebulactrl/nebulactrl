import { describe, expect, test } from "bun:test";
import { QueryClient } from "@tanstack/react-query";
import { screen } from "@testing-library/react";
import { getEnvironmentDeploymentsDailyOptions } from "#/api/gen/@tanstack/react-query.gen";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders, testQueryClient } from "#/test/render";
import {
	prefetchEnvDeployments,
	useEnvDeploymentCount,
	useEnvDeploymentOutcomes,
} from "./queries";

const env = make("Environment", {
	id: "env1",
	projectId: "p1",
	slug: "prod",
	name: "Production",
	kind: "standard",
	isProduction: true,
	parentEnvironmentId: null,
	prNumber: null,
	prTitle: "",
	createdAt: "2026-01-01T00:00:00Z",
});

const deployment = (id: string) =>
	make("EnvDeploymentView", { id, serviceId: "s1", status: "succeeded" });

describe("prefetchEnvDeployments", () => {
	const routes = () => ({
		"GET /projects/:id/environments": {
			body: make("PageEnvironment", { items: [env] }),
		},
		"GET /environments/:id/deployments": {
			body: make("PageEnvDeploymentView", { items: [deployment("d1")] }),
		},
		"GET /environments/:id/deployments/daily": { body: { days: [] } },
		"GET /environments/:id/services": { body: { items: [] } },
	});

	test("starts the page's four requests once the environment slug resolves", async () => {
		const api = stubApi(routes());
		await prefetchEnvDeployments(
			testQueryClient(),
			"p1",
			"prod",
			undefined,
			undefined,
		);
		expect(api.calls.map((c) => `${c.method} ${c.path}`).sort()).toEqual([
			"GET /environments/env1/deployments",
			"GET /environments/env1/deployments/daily",
			"GET /environments/env1/services",
			"GET /projects/p1/environments",
		]);
		const list = api.calls.find(
			(c) => c.path === "/environments/env1/deployments",
		);
		expect(list?.query.get("limit")).toBe("50");
		expect(list?.query.has("status")).toBe(false);
		const daily = api.calls.find((c) => c.path.endsWith("/daily"));
		expect(daily?.query.get("days")).toBe("30");
		expect(api.unmatched).toEqual([]);
	});

	test("translates a status filter into the control plane's comma list and passes the service", async () => {
		const api = stubApi(routes());
		await prefetchEnvDeployments(
			testQueryClient(),
			"p1",
			"prod",
			"failed",
			"svc-9",
		);
		const list = api.calls.find(
			(c) => c.path === "/environments/env1/deployments",
		);
		expect(list?.query.get("status")).toBe("failed,rollback_failed");
		expect(list?.query.get("serviceId")).toBe("svc-9");
	});

	test("ignores a status that is not a filter", async () => {
		const api = stubApi(routes());
		await prefetchEnvDeployments(
			testQueryClient(),
			"p1",
			"prod",
			"bogus",
			undefined,
		);
		const list = api.calls.find(
			(c) => c.path === "/environments/env1/deployments",
		);
		expect(list?.query.has("status")).toBe(false);
	});

	test("an unknown slug requests nothing more", async () => {
		const api = stubApi(routes());
		await prefetchEnvDeployments(
			testQueryClient(),
			"p1",
			"nope",
			undefined,
			undefined,
		);
		expect(api.calls.map((c) => c.path)).toEqual(["/projects/p1/environments"]);
	});

	test("a failing environment list is swallowed so the page can show the error", async () => {
		stubApi({
			"GET /projects/:id/environments": {
				status: 500,
				body: { status: 500, title: "x", detail: "boom", code: "internal" },
			},
		});
		await expect(
			prefetchEnvDeployments(
				testQueryClient(),
				"p1",
				"prod",
				undefined,
				undefined,
			),
		).resolves.toBeUndefined();
	});

	test("caches the first page, the strip and the services for the page to render from", async () => {
		stubApi(routes());
		const queryClient = testQueryClient();
		await prefetchEnvDeployments(
			queryClient,
			"p1",
			"prod",
			undefined,
			undefined,
		);
		const cached = queryClient.getQueryCache().getAll();
		const pages = cached.find(
			(q) => q.state.data && "pages" in (q.state.data as object),
		);
		if (!pages) throw new Error("no paged deployments query was cached");
		const paged = pages.state.data as {
			pages: { items: { id: string }[] }[];
		};
		expect(paged.pages[0]?.items.map((i) => i.id)).toEqual(["d1"]);
		expect(
			queryClient.getQueryData(
				getEnvironmentDeploymentsDailyOptions({
					path: { environmentId: "env1" },
					query: { days: 30 },
				}).queryKey,
			),
		).toEqual({ days: [] });
		expect(cached.every((q) => q.state.status === "success")).toBe(true);
		expect(cached).toHaveLength(4);
	});
});

function Count({ id }: { id: string | undefined }) {
	return (
		<output aria-label="count">{useEnvDeploymentCount(id) ?? "none"}</output>
	);
}

describe("useEnvDeploymentCount", () => {
	test("is the number of deployments when everything fits one page", async () => {
		stubApi({
			"GET /environments/:id/deployments": {
				body: make("PageEnvDeploymentView", {
					items: [deployment("a"), deployment("b"), deployment("c")],
				}),
			},
		});
		await renderWithProviders(<Count id="env1" />);
		expect(await screen.findByText("3")).toBeInTheDocument();
	});

	test("reads 50+ style once more pages exist", async () => {
		const items = Array.from({ length: 50 }, (_, i) => deployment(`d${i}`));
		stubApi({
			"GET /environments/:id/deployments": {
				body: make("PageEnvDeploymentView", { items, nextCursor: "next" }),
			},
		});
		await renderWithProviders(<Count id="env1" />);
		expect(await screen.findByText("50+")).toBeInTheDocument();
	});

	test("is 0 for an environment without deployments (a null list)", async () => {
		stubApi({
			"GET /environments/:id/deployments": {
				body: make("PageEnvDeploymentView", { items: null }),
			},
		});
		await renderWithProviders(<Count id="env1" />);
		expect(await screen.findByText("0")).toBeInTheDocument();
	});

	test("outside an environment it asks for nothing and has no count", async () => {
		const api = stubApi({});
		await renderWithProviders(<Count id={undefined} />);
		expect(screen.getByLabelText("count")).toHaveTextContent("none");
		expect(api.calls).toEqual([]);
		expect(api.unmatched).toEqual([]);
	});
});

function Probe() {
	useEnvDeploymentOutcomes("env1");
	return null;
}

describe("useEnvDeploymentOutcomes", () => {
	test("refetches on a 15 s interval as the polling fallback", async () => {
		stubApi({
			"GET /environments/:id/deployments/daily": { body: { days: [] } },
		});
		const queryClient = new QueryClient();
		await renderWithProviders(<Probe />, { queryClient });
		const query = queryClient.getQueryCache().getAll()[0];
		expect(query?.observers[0]?.options.refetchInterval).toBe(15_000);
	});
});
