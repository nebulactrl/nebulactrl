import { describe, expect, it } from "bun:test";
import { QueryClientProvider, useQuery } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { projectsQuery } from "#/api/hooks";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { testQueryClient } from "#/test/render";
import {
	invalidateStatusPages,
	maintenanceWindowsQuery,
	statusIncidentQuery,
	statusIncidentsQuery,
	statusPageCloudflareQuery,
	statusPageDnsQuery,
	statusPageQuery,
	statusPagesQuery,
	useServiceStates,
} from "./queries";
import {
	cloudflareStatus,
	dnsCheck,
	incidentView,
	pageView,
	uptimeReply,
	uptimeService,
	windowView,
} from "./sp-pages-fixtures";

/** The client `Wrapper` provides: the one the current test got from `wrapper()`. */
let activeClient = testQueryClient();

function Wrapper({ children }: { children: ReactNode }) {
	return (
		<QueryClientProvider client={activeClient}>{children}</QueryClientProvider>
	);
}

/** A fresh query client for the current test and the wrapper that provides it. */
function wrapper() {
	activeClient = testQueryClient();
	return { client: activeClient, Wrapper };
}

const PAGE = pageView({ id: "page-1", title: "Acme status" });
const INCIDENT = incidentView({ id: "inc-1" });
const WINDOW = windowView({ id: "win-1" });

describe("list queries", () => {
	it("statusPagesQuery unwraps the items of the list", async () => {
		const api = stubApi({
			"GET /status-pages": { body: { items: [PAGE] } },
		});
		const { Wrapper } = wrapper();
		const { result } = renderHook(() => useQuery(statusPagesQuery()), {
			wrapper: Wrapper,
		});
		await waitFor(() => expect(result.current.data).toEqual([PAGE]));
		expect(api.calls.map((c) => `${c.method} ${c.path}`)).toEqual([
			"GET /status-pages",
		]);
	});

	it("statusPagesQuery reads a null list as no pages", async () => {
		stubApi({ "GET /status-pages": { body: { items: null } } });
		const { Wrapper } = wrapper();
		const { result } = renderHook(() => useQuery(statusPagesQuery()), {
			wrapper: Wrapper,
		});
		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(result.current.data).toEqual([]);
	});

	it("statusPageQuery reads the page by id", async () => {
		const api = stubApi({ "GET /status-pages/:id": { body: PAGE } });
		const { Wrapper } = wrapper();
		const { result } = renderHook(() => useQuery(statusPageQuery("page-1")), {
			wrapper: Wrapper,
		});
		await waitFor(() => expect(result.current.data).toEqual(PAGE));
		expect(api.calls[0]?.path).toBe("/status-pages/page-1");
	});

	it("statusIncidentsQuery asks for 100 and merges the filter", async () => {
		const api = stubApi({
			"GET /status-incidents": { body: { items: [INCIDENT] } },
		});
		const { Wrapper } = wrapper();
		const { result } = renderHook(
			() =>
				useQuery(statusIncidentsQuery({ pageId: "page-1", status: "active" })),
			{ wrapper: Wrapper },
		);
		await waitFor(() => expect(result.current.data).toEqual([INCIDENT]));
		const query = api.calls[0]?.query;
		expect(query?.get("limit")).toBe("100");
		expect(query?.get("pageId")).toBe("page-1");
		expect(query?.get("status")).toBe("active");
	});

	it("statusIncidentsQuery lets a filter override the page size and tolerates a null list", async () => {
		const api = stubApi({ "GET /status-incidents": { body: { items: null } } });
		const { Wrapper } = wrapper();
		const { result } = renderHook(
			() => useQuery(statusIncidentsQuery({ limit: 5 })),
			{ wrapper: Wrapper },
		);
		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(result.current.data).toEqual([]);
		expect(api.calls[0]?.query.get("limit")).toBe("5");
	});

	it("statusIncidentQuery reads one incident", async () => {
		const one = incidentView({ id: "inc-9" });
		const api = stubApi({ "GET /status-incidents/:id": { body: one } });
		const { Wrapper } = wrapper();
		const { result } = renderHook(
			() => useQuery(statusIncidentQuery("inc-9")),
			{
				wrapper: Wrapper,
			},
		);
		await waitFor(() => expect(result.current.data).toEqual(one));
		expect(api.calls[0]?.path).toBe("/status-incidents/inc-9");
	});

	it("maintenanceWindowsQuery filters by phase and unwraps the items", async () => {
		const api = stubApi({
			"GET /maintenance-windows": { body: { items: [WINDOW] } },
		});
		const { Wrapper } = wrapper();
		const { result } = renderHook(
			() => useQuery(maintenanceWindowsQuery({ phase: "active" })),
			{ wrapper: Wrapper },
		);
		await waitFor(() => expect(result.current.data).toEqual([WINDOW]));
		expect(api.calls[0]?.query.get("phase")).toBe("active");
		expect(api.calls[0]?.query.get("limit")).toBe("100");
	});
});

describe("live lookups", () => {
	it("statusPageDnsQuery does not fetch until enabled, and sends fresh only on a Recheck", async () => {
		const api = stubApi({
			"GET /status-pages/:id/dns": { body: dnsCheck() },
		});
		const { Wrapper } = wrapper();
		const { result, rerender } = renderHook(
			({ enabled, fresh }) =>
				useQuery(statusPageDnsQuery("page-1", enabled, fresh)),
			{ wrapper: Wrapper, initialProps: { enabled: false, fresh: false } },
		);
		expect(result.current.fetchStatus).toBe("idle");
		expect(api.calls).toEqual([]);

		rerender({ enabled: true, fresh: false });
		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(api.calls[0]?.query.get("fresh")).toBe("false");

		rerender({ enabled: true, fresh: true });
		await waitFor(() => expect(api.calls).toHaveLength(2));
		expect(api.calls[1]?.query.get("fresh")).toBe("true");
	});

	it("statusPageDnsQuery shows a failed lookup at once, without retrying", async () => {
		const api = stubApi({
			"GET /status-pages/:id/dns": problem(500, {
				code: "internal",
				title: "boom",
			}),
		});
		const { Wrapper } = wrapper();
		const { result } = renderHook(
			() => useQuery(statusPageDnsQuery("page-1", true)),
			{ wrapper: Wrapper },
		);
		await waitFor(() => expect(result.current.isError).toBe(true));
		expect(api.calls).toHaveLength(1);
	});

	it("statusPageCloudflareQuery reads the page's Cloudflare state", async () => {
		const api = stubApi({
			"GET /status-pages/:id/cloudflare": { body: cloudflareStatus() },
		});
		const { Wrapper } = wrapper();
		const { result } = renderHook(
			() => useQuery(statusPageCloudflareQuery("page-1")),
			{ wrapper: Wrapper },
		);
		await waitFor(() =>
			expect(result.current.data).toEqual(cloudflareStatus()),
		);
		expect(api.calls[0]?.path).toBe("/status-pages/page-1/cloudflare");
	});
});

describe("invalidateStatusPages", () => {
	it("refetches status page data that is on screen and leaves other data alone", async () => {
		let pageTitle = "Before";
		const api = stubApi({
			"GET /status-pages": () =>
				Response.json({
					items: [pageView({ id: "page-1", title: pageTitle })],
				}),
			"GET /projects": { body: make("PageProject", { items: [] }) },
		});
		const { client, Wrapper } = wrapper();
		const { result } = renderHook(
			() => ({
				pages: useQuery(statusPagesQuery()),
				projects: useQuery(projectsQuery()),
			}),
			{ wrapper: Wrapper },
		);
		await waitFor(() => expect(result.current.pages.data).toHaveLength(1));
		await waitFor(() => expect(result.current.projects.isSuccess).toBe(true));
		expect(api.calls).toHaveLength(2);

		pageTitle = "After";
		await invalidateStatusPages(client);

		await waitFor(() =>
			expect(result.current.pages.data).toEqual([
				pageView({ id: "page-1", title: "After" }),
			]),
		);
		const paths = api.calls.map((c) => c.path);
		expect(paths.filter((p) => p === "/status-pages")).toHaveLength(2);
		expect(paths.filter((p) => p === "/projects")).toHaveLength(1);
	});
});

describe("useServiceStates", () => {
	const environment = (id: string) =>
		make("Environment", { id, projectId: "proj-1" });

	function stubFleet() {
		return stubApi({
			"GET /projects": {
				body: make("PageProject", {
					items: [make("Project", { id: "proj-1", name: "Acme" })],
				}),
			},
			"GET /projects/:id/environments": {
				body: make("PageEnvironment", {
					items: [environment("env-1"), environment("env-2")],
				}),
			},
			"GET /environments/env-1/uptime": uptimeReply(
				uptimeService("web", "up", true),
				uptimeService("paused-one", "paused", false),
			),
			"GET /environments/env-2/uptime": uptimeReply(
				uptimeService("api", "down", true),
			),
		});
	}

	it("maps each monitored service to its state across every environment", async () => {
		const api = stubFleet();
		const { Wrapper } = wrapper();
		const { result } = renderHook(() => useServiceStates(), {
			wrapper: Wrapper,
		});
		expect(result.current.pending).toBe(true);
		await waitFor(() => expect(result.current.pending).toBe(false));
		expect([...result.current.states]).toEqual([
			["web", "up"],
			["api", "down"],
		]);
		expect(api.unmatched).toEqual([]);
	});

	it("does not fetch, nor report pending, while disabled", async () => {
		const api = stubFleet();
		const { Wrapper } = wrapper();
		const { result } = renderHook(() => useServiceStates(false), {
			wrapper: Wrapper,
		});
		expect(result.current.pending).toBe(false);
		expect(result.current.states.size).toBe(0);
		expect(api.calls).toEqual([]);
	});

	it("tolerates an environment with a null uptime list", async () => {
		stubApi({
			"GET /projects": {
				body: make("PageProject", {
					items: [make("Project", { id: "proj-1", name: "Acme" })],
				}),
			},
			"GET /projects/:id/environments": {
				body: make("PageEnvironment", { items: [environment("env-1")] }),
			},
			"GET /environments/:id/uptime": uptimeReply(null),
		});
		const { Wrapper } = wrapper();
		const { result } = renderHook(() => useServiceStates(), {
			wrapper: Wrapper,
		});
		await waitFor(() => expect(result.current.pending).toBe(false));
		expect(result.current.states.size).toBe(0);
	});
});
