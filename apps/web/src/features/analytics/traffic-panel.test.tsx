import {
	beforeEach,
	describe,
	expect,
	mock,
	setDefaultTimeout,
	test,
} from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Server } from "#/api/gen/types.gen";
import { type ApiReply, stubApi } from "#/test/api";
import { make, type Overrides } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { makeReport } from "./test-fixtures";
import { TrafficPanel } from "./traffic-panel";

// A whole dashboard renders per test; a loaded CI box needs more than 5 s.
setDefaultTimeout(20_000);

beforeEach(() => {
	window.localStorage.clear();
});

function serviceWire(hostnames: string[] = ["app.example.com"]) {
	return make("ServiceView", {
		id: "svc-1",
		name: "web",
		serverId: "srv-1",
		domains: hostnames.map((hostname, i) =>
			make("DomainView", { id: `dom-${i}`, hostname }),
		),
	});
}

function serverWire(over: Overrides<Server> = {}) {
	return make("Server", {
		id: "srv-1",
		name: "edge-1",
		status: "online",
		proxy: { status: "ok", v2: true, generation: 1, appliedGeneration: 1 },
		trafficAnalyticsEnabled: true,
		lastSeenAt: "2026-10-31T12:00:00Z",
		...over,
	});
}

interface Setup {
	hostnames?: string[];
	server?: Overrides<Server>;
	report?: ApiReply;
}

/** Routes for a service on srv-1; analytics replies with `report`. */
function routes({ hostnames, server, report }: Setup = {}) {
	const analytics: ApiReply = report ?? { body: makeReport() };
	return {
		"GET /services/:serviceId": { body: serviceWire(hostnames) },
		"GET /servers/:serverId": { body: serverWire(server) },
		"GET /services/:serviceId/analytics": analytics,
		"GET /servers/:serverId/analytics": analytics,
		"GET /analytics/traffic": analytics,
	};
}

const analyticsCalls = (api: ReturnType<typeof stubApi>) =>
	api.calls.filter(
		(c) => c.path.endsWith("/analytics") || c.path === "/analytics/traffic",
	);

describe("TrafficPanel for a service", () => {
	test("loads the report with the 24h default, comparison and pages-only and shows the dashboard", async () => {
		const api = stubApi(routes());
		await renderWithProviders(<TrafficPanel scope={{ serviceId: "svc-1" }} />);
		expect(await screen.findByText("12.4k")).toBeInTheDocument();
		expect(
			screen.getByRole("heading", { name: "Top pages" }),
		).toBeInTheDocument();
		const [call] = analyticsCalls(api);
		expect(call?.path).toBe("/services/svc-1/analytics");
		expect(call?.query.get("range")).toBe("24h");
		expect(call?.query.get("compare")).toBe("prev");
		expect(call?.query.get("pages")).toBe("true");
		expect(call?.query.has("host")).toBe(false);
		expect(api.unmatched).toEqual([]);
	});

	test("shows a loading placeholder until the report arrives", async () => {
		const gate = Promise.withResolvers<void>();
		stubApi({
			...routes(),
			"GET /services/:serviceId/analytics": async () => {
				await gate.promise;
				return Response.json(makeReport());
			},
		});
		await renderWithProviders(<TrafficPanel scope={{ serviceId: "svc-1" }} />);
		expect(
			await screen.findByRole("status", { name: "Loading traffic" }),
		).toBeInTheDocument();
		gate.resolve();
		expect(await screen.findByText("12.4k")).toBeInTheDocument();
		expect(
			screen.queryByRole("status", { name: "Loading traffic" }),
		).not.toBeInTheDocument();
	});

	test("picking a range refetches for it and remembers it for next time", async () => {
		const user = userEvent.setup();
		const api = stubApi(routes());
		await renderWithProviders(<TrafficPanel scope={{ serviceId: "svc-1" }} />);
		await screen.findByText("12.4k");
		await user.click(screen.getByRole("button", { name: "7d" }));
		await waitFor(() =>
			expect(
				analyticsCalls(api).some((c) => c.query.get("range") === "7d"),
			).toBe(true),
		);
		expect(
			window.localStorage.getItem("nebulactrl:traffic-range-service"),
		).toBe("7d");
		// The previous report stays up while the new range loads.
		expect(screen.getByText("12.4k")).toBeInTheDocument();
	});

	test("starts on the stored range", async () => {
		window.localStorage.setItem("nebulactrl:traffic-range-service", "6h");
		const api = stubApi(routes());
		await renderWithProviders(<TrafficPanel scope={{ serviceId: "svc-1" }} />);
		await screen.findByText("12.4k");
		expect(analyticsCalls(api)[0]?.query.get("range")).toBe("6h");
		expect(screen.getByRole("button", { name: "6h" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
	});

	test("ignores a stored range it does not know", async () => {
		window.localStorage.setItem("nebulactrl:traffic-range-service", "90d");
		const api = stubApi(routes());
		await renderWithProviders(<TrafficPanel scope={{ serviceId: "svc-1" }} />);
		await screen.findByText("12.4k");
		expect(analyticsCalls(api)[0]?.query.get("range")).toBe("24h");
	});

	test("the Live cue shows on short ranges only", async () => {
		const user = userEvent.setup();
		stubApi(routes());
		await renderWithProviders(<TrafficPanel scope={{ serviceId: "svc-1" }} />);
		await screen.findByText("12.4k");
		expect(screen.queryByText("Live")).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "1h" }));
		expect(await screen.findByText(/^Live/)).toBeInTheDocument();
	});

	test("unticking Compare asks again without the previous period", async () => {
		const user = userEvent.setup();
		const api = stubApi(routes());
		await renderWithProviders(<TrafficPanel scope={{ serviceId: "svc-1" }} />);
		await screen.findByText("12.4k");
		await user.click(
			screen.getByRole("checkbox", { name: "Compare to previous period" }),
		);
		await waitFor(() =>
			expect(analyticsCalls(api).some((c) => !c.query.has("compare"))).toBe(
				true,
			),
		);
	});

	test("the Pages/All toggle asks for every request", async () => {
		const user = userEvent.setup();
		const api = stubApi(routes());
		await renderWithProviders(<TrafficPanel scope={{ serviceId: "svc-1" }} />);
		await screen.findByText("12.4k");
		await user.click(screen.getByRole("button", { name: "All requests" }));
		await waitFor(() =>
			expect(
				analyticsCalls(api).some((c) => c.query.get("pages") === "false"),
			).toBe(true),
		);
	});

	test("a hostname picker appears with several domains and narrows the report", async () => {
		const user = userEvent.setup();
		const api = stubApi(
			routes({ hostnames: ["app.example.com", "www.example.com"] }),
		);
		await renderWithProviders(<TrafficPanel scope={{ serviceId: "svc-1" }} />);
		await screen.findByText("12.4k");
		await user.click(screen.getByRole("combobox", { name: "Hostname" }));
		await user.click(
			await screen.findByRole("option", { name: "www.example.com" }),
		);
		await waitFor(() =>
			expect(
				analyticsCalls(api).some(
					(c) => c.query.get("host") === "www.example.com",
				),
			).toBe(true),
		);
		// Back to all hosts drops the filter.
		await user.click(screen.getByRole("combobox", { name: "Hostname" }));
		await user.click(await screen.findByRole("option", { name: "All hosts" }));
		await waitFor(() => {
			const calls = analyticsCalls(api);
			expect(calls.at(-1)?.query.has("host")).toBe(false);
		});
	});

	test("clicking a host in the Hosts list filters the whole tab to it", async () => {
		const user = userEvent.setup();
		const api = stubApi(
			routes({ hostnames: ["app.example.com", "www.example.com"] }),
		);
		await renderWithProviders(<TrafficPanel scope={{ serviceId: "svc-1" }} />);
		await screen.findByText("12.4k");
		await user.click(screen.getByRole("button", { name: "Hosts" }));
		await user.click(screen.getByRole("button", { name: /www\.example\.com/ }));
		await waitFor(() =>
			expect(
				analyticsCalls(api).some(
					(c) => c.query.get("host") === "www.example.com",
				),
			).toBe(true),
		);
	});

	test("with one domain there is no hostname picker and hosts are not clickable", async () => {
		const user = userEvent.setup();
		stubApi(routes());
		await renderWithProviders(<TrafficPanel scope={{ serviceId: "svc-1" }} />);
		await screen.findByText("12.4k");
		expect(
			screen.queryByRole("combobox", { name: "Hostname" }),
		).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Hosts" }));
		expect(
			screen.queryByRole("button", { name: /www\.example\.com/ }),
		).not.toBeInTheDocument();
	});
});

describe("TrafficPanel states", () => {
	test("a service without a domain explains why and offers to add one, with no analytics request", async () => {
		const user = userEvent.setup();
		const onAddDomain = mock(() => {});
		const api = stubApi(routes({ hostnames: [] }));
		await renderWithProviders(
			<TrafficPanel scope={{ serviceId: "svc-1" }} onAddDomain={onAddDomain} />,
		);
		expect(
			await screen.findByText("Add a domain to collect traffic analytics"),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Add a domain" }));
		expect(onAddDomain).toHaveBeenCalledTimes(1);
		expect(analyticsCalls(api)).toEqual([]);
	});

	test("without onAddDomain the empty state has no button", async () => {
		stubApi(routes({ hostnames: [] }));
		await renderWithProviders(<TrafficPanel scope={{ serviceId: "svc-1" }} />);
		await screen.findByText("Add a domain to collect traffic analytics");
		expect(
			screen.queryByRole("button", { name: "Add a domain" }),
		).not.toBeInTheDocument();
	});

	test("a server with analytics off says so and links to its settings", async () => {
		const api = stubApi(routes({ server: { trafficAnalyticsEnabled: false } }));
		await renderWithProviders(<TrafficPanel scope={{ serviceId: "svc-1" }} />);
		expect(
			await screen.findByText("Traffic analytics is off for edge-1"),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Open server settings" }),
		).toHaveAttribute("href", "/servers/srv-1/settings/monitoring");
		// The settings answer after the report was asked for; the report is never shown.
		expect(screen.queryByText("12.4k")).not.toBeInTheDocument();
		expect(analyticsCalls(api).length).toBeLessThanOrEqual(1);
	});

	test("a failed report shows an error with a working retry", async () => {
		const user = userEvent.setup();
		let fail = true;
		const api = stubApi({
			...routes(),
			"GET /services/:serviceId/analytics": () =>
				fail
					? Response.json({ title: "boom" }, { status: 500 })
					: Response.json(makeReport()),
		});
		await renderWithProviders(<TrafficPanel scope={{ serviceId: "svc-1" }} />);
		expect(await screen.findByText("Traffic did not load")).toBeInTheDocument();
		expect(
			screen.getByText(
				"The control plane did not answer. Try again in a moment.",
			),
		).toBeInTheDocument();
		fail = false;
		await user.click(screen.getByRole("button", { name: /retry|try again/i }));
		expect(await screen.findByText("12.4k")).toBeInTheDocument();
		expect(analyticsCalls(api).length).toBeGreaterThanOrEqual(2);
	});

	test("a window with no requests says so and offers a curl command for the first host", async () => {
		const empty = makeReport();
		stubApi(
			routes({
				report: {
					body: {
						...empty,
						summary: { ...empty.summary, requests: 0 },
						series: [],
					},
				},
			}),
		);
		await renderWithProviders(<TrafficPanel scope={{ serviceId: "svc-1" }} />);
		expect(await screen.findByText("No requests yet")).toBeInTheDocument();
		expect(
			screen.getByText(/Nothing reached this service in the last 24h\./),
		).toBeInTheDocument();
		expect(
			screen.getByText("curl https://app.example.com"),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Copy curl command" }),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("heading", { name: "Latency" }),
		).not.toBeInTheDocument();
	});

	test("zero requests on a server scope with unrouted traffic still shows the dashboard", async () => {
		const empty = makeReport();
		stubApi(
			routes({
				report: {
					body: {
						...empty,
						summary: { ...empty.summary, requests: 0 },
						unrouted: {
							requests: 40,
							hosts: [{ k: "scanner.example.net", n: 40 }],
						},
					},
				},
			}),
		);
		await renderWithProviders(<TrafficPanel scope={{ serverId: "srv-1" }} />);
		expect(
			await screen.findByRole("heading", { name: "Unrouted hosts" }),
		).toBeInTheDocument();
		expect(screen.queryByText("No requests yet")).not.toBeInTheDocument();
	});

	test("no-request state without any known hostname leaves out the curl line", async () => {
		const empty = makeReport();
		stubApi(
			routes({
				report: {
					body: { ...empty, summary: { ...empty.summary, requests: 0 } },
				},
			}),
		);
		await renderWithProviders(<TrafficPanel scope={{ serverId: "srv-1" }} />);
		expect(await screen.findByText("No requests yet")).toBeInTheDocument();
		expect(
			screen.getByText(/Nothing reached this server in the last 24h\./),
		).toBeInTheDocument();
		expect(screen.queryByText(/^curl /)).not.toBeInTheDocument();
	});

	test("a proxy in error is called out above the dashboard", async () => {
		stubApi(
			routes({
				server: {
					proxy: {
						status: "error",
						error: "port 443 busy",
						v2: true,
						generation: 1,
						appliedGeneration: 0,
					},
				},
			}),
		);
		await renderWithProviders(<TrafficPanel scope={{ serviceId: "svc-1" }} />);
		const alert = await screen.findByRole("alert");
		expect(alert).toHaveTextContent(
			"The proxy on edge-1 reports an error: port 443 busy. Traffic cannot be collected until it is fixed.",
		);
	});

	test("a proxy error without detail drops the colon clause", async () => {
		stubApi(
			routes({
				server: {
					proxy: {
						status: "error",
						v2: true,
						generation: 1,
						appliedGeneration: 0,
					},
				},
			}),
		);
		await renderWithProviders(<TrafficPanel scope={{ serviceId: "svc-1" }} />);
		const alert = await screen.findByRole("alert");
		expect(alert).toHaveTextContent(
			"The proxy on edge-1 reports an error. Traffic cannot",
		);
	});

	test("an offline server warns that the figures are the last that arrived and ends the Live cue", async () => {
		const user = userEvent.setup();
		stubApi(routes({ server: { status: "offline" } }));
		await renderWithProviders(<TrafficPanel scope={{ serviceId: "svc-1" }} />);
		const note = await screen.findByText(/edge-1 is offline/);
		expect(note.closest('[role="alert"]')).toHaveTextContent(
			"The figures below are the last that arrived.",
		);
		expect(note.closest('[role="alert"]')).toHaveTextContent("last seen");
		await screen.findByText("12.4k");
		await user.click(screen.getByRole("button", { name: "1h" }));
		expect(screen.queryByText(/^Live/)).not.toBeInTheDocument();
	});
});

describe("TrafficPanel for a server or environment", () => {
	test("a server scope reads the server endpoint and has no hostname picker", async () => {
		const api = stubApi(routes());
		await renderWithProviders(<TrafficPanel scope={{ serverId: "srv-1" }} />);
		expect(await screen.findByText("12.4k")).toBeInTheDocument();
		const [call] = analyticsCalls(api);
		expect(call?.path).toBe("/servers/srv-1/analytics");
		expect(api.calls.some((c) => c.path.startsWith("/services/"))).toBe(false);
		expect(
			screen.queryByRole("combobox", { name: "Hostname" }),
		).not.toBeInTheDocument();
	});

	test("a server scope with analytics off is blocked too", async () => {
		const api = stubApi(routes({ server: { trafficAnalyticsEnabled: false } }));
		await renderWithProviders(<TrafficPanel scope={{ serverId: "srv-1" }} />);
		expect(
			await screen.findByText("Traffic analytics is off for edge-1"),
		).toBeInTheDocument();
		expect(screen.queryByText("12.4k")).not.toBeInTheDocument();
		expect(analyticsCalls(api).length).toBeLessThanOrEqual(1);
	});

	test("an environment scope reads the traffic endpoint and never fetches a service or server", async () => {
		const api = stubApi(routes());
		await renderWithProviders(
			<TrafficPanel scope={{ environmentId: "env-1" }} variant="summary" />,
		);
		expect(await screen.findByText("12.4k")).toBeInTheDocument();
		expect(analyticsCalls(api)[0]?.path).toBe("/analytics/traffic");
		expect(analyticsCalls(api)[0]?.query.get("environmentId")).toBe("env-1");
		expect(api.calls.map((c) => c.path)).toEqual(["/analytics/traffic"]);
		// Summary: KPIs, status chart and the origins card, no lists.
		expect(
			screen.getByRole("heading", { name: "Where requests come from" }),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("heading", { name: "Top pages" }),
		).not.toBeInTheDocument();
		// The range choice is stored per scope kind.
		const user = userEvent.setup();
		await user.click(screen.getByRole("button", { name: "30d" }));
		expect(
			window.localStorage.getItem("nebulactrl:traffic-range-environment"),
		).toBe("30d");
	});

	test("the control row stays above a blocked body", async () => {
		stubApi(routes({ hostnames: [] }));
		await renderWithProviders(<TrafficPanel scope={{ serviceId: "svc-1" }} />);
		await screen.findByText("Add a domain to collect traffic analytics");
		expect(screen.getByRole("button", { name: "24h" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		expect(
			screen.getByRole("checkbox", { name: "Compare to previous period" }),
		).toBeChecked();
	});
});
