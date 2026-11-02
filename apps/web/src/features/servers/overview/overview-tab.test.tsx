import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
	EnrollmentToken,
	Server as ServerWire,
	Summary,
} from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, type Overrides, problem } from "#/test/factories";
import { renderWithProviders, testQueryClient } from "#/test/render";
import { cacheEnrollment, enrollmentKey } from "../enrollment";
import { type ServerView, toServerView } from "../model";
import { serverWire } from "../server.fixture";
import { ServerProvider } from "../server-context";
import { ConnectCard } from "./connect-card";
import { FactsStrip } from "./facts-strip";
import { OverviewTab } from "./overview-tab";
import { TrafficSummary } from "./traffic-summary";

const GiB = 1024 ** 3;

const NOW = new Date().toISOString();

/** A server as the API sends it, with the fields these components read. */
function wire(over: Partial<ServerWire> = {}): ServerWire {
	return serverWire({
		id: "srv_1",
		name: "edge-1",
		agentVersion: "1.4.0",
		protocolVersion: 3,
		trafficAnalyticsEnabled: true,
		lastSeenAt: NOW,
		latest: {
			cpuPercent: 0,
			memUsed: 0,
			memTotal: 0,
			diskUsed: 0,
			diskTotal: 0,
			sampledAt: NOW,
		},
		...over,
	});
}

const patches = (
	over: Partial<ServerWire["patches"]>,
): ServerWire["patches"] => ({
	...serverWire().patches,
	...over,
});

const view = (over: Partial<ServerWire> = {}): ServerView =>
	toServerView(wire(over));

const summary = (over: Overrides<Summary> = {}) =>
	make("Summary", {
		botRequests: 0,
		bytesIn: 0,
		bytesOut: 5 * GiB,
		documents: 0,
		p50Ms: 20,
		p95Ms: 245,
		p99Ms: 400,
		requests: 12_400,
		visitors: 830,
		...over,
	});

const report = (over: Overrides<Summary> = {}) =>
	make("Report", { summary: summary(over) });

/** The text under a label in a `dl` strip; a KPI number's screen-reader text, not its animated digits. */
function shown(label: string): string | null {
	const term = screen.getByText(label, { selector: "dt" });
	const value = (term.parentElement as HTMLElement).querySelector("dd");
	return (
		value?.querySelector(".sr-only")?.textContent ?? value?.textContent ?? null
	);
}

describe("FactsStrip", () => {
	test("shows what the host reported about itself", async () => {
		await renderWithProviders(<FactsStrip server={view()} />);
		expect(shown("OS")).toBe("ubuntu 24.04 · arm64");
		expect(shown("CPUs")).toBe("4");
		expect(shown("Memory")).toBe("8 GiB");
		expect(shown("Disk")).toBe("160 GiB");
		expect(shown("Docker")).toBe("27.1.1");
		expect(shown("Agent")).toBe("1.4.0");
	});

	test("the disk figure is the newest heartbeat's, not the connect-time fact", async () => {
		await renderWithProviders(
			<FactsStrip
				server={view({
					latest: {
						cpuPercent: 0,
						memUsed: 0,
						memTotal: 0,
						sampledAt: NOW,
						diskTotal: 150 * GiB,
						diskUsed: GiB,
					},
				})}
			/>,
		);
		expect(shown("Disk")).toBe("150 GiB");
	});

	test("a host that never connected shows dashes and says the agent is not connected", async () => {
		await renderWithProviders(
			<FactsStrip
				server={view({ status: "pending", facts: null, agentVersion: null })}
			/>,
		);
		for (const label of ["OS", "CPUs", "Memory", "Disk", "Docker"]) {
			expect(shown(label)).toBe("–");
		}
		expect(shown("Agent")).toBe("not connected");
	});

	test("each value carries its full text as a tooltip for truncated cells", async () => {
		await renderWithProviders(<FactsStrip server={view()} />);
		const cell = screen.getByText("ubuntu 24.04 · arm64").parentElement;
		expect(cell).toHaveAttribute("title", "ubuntu 24.04 · arm64");
	});
});

describe("TrafficSummary", () => {
	const ANALYTICS = "GET /servers/:serverId/analytics";

	test("with analytics off it says so and links to where it is turned on, asking for nothing", async () => {
		const api = stubApi({});
		await renderWithProviders(
			<TrafficSummary server={view({ trafficAnalyticsEnabled: false })} />,
		);
		expect(
			screen.getByText(/Traffic analytics is off for this server/),
		).toBeVisible();
		expect(
			screen.getByRole("link", { name: "Turn it on in settings" }),
		).toHaveAttribute("href", "/servers/srv_1/settings/monitoring");
		expect(api.calls).toEqual([]);
	});

	test("shows a placeholder while the report loads", async () => {
		stubApi({ [ANALYTICS]: () => new Promise<Response>(() => {}) });
		await renderWithProviders(<TrafficSummary server={view()} />);
		expect(
			screen.getByRole("status", { name: "Loading traffic" }),
		).toBeInTheDocument();
		expect(screen.queryByText("Requests")).not.toBeInTheDocument();
	});

	test("asks for the last 24 hours against the previous period, pages only", async () => {
		const api = stubApi({ [ANALYTICS]: { body: report() } });
		await renderWithProviders(<TrafficSummary server={view()} />);
		await screen.findByText("Requests");
		const call = api.calls[0];
		expect(call?.path).toBe("/servers/srv_1/analytics");
		expect(call?.query.get("range")).toBe("24h");
		expect(call?.query.get("compare")).toBe("prev");
		expect(call?.query.get("pages")).toBe("true");
	});

	test("shows requests, visitors, bandwidth and p95 latency as one link to the Traffic tab", async () => {
		stubApi({ [ANALYTICS]: { body: report() } });
		await renderWithProviders(<TrafficSummary server={view()} />);
		await screen.findByText("Requests");
		expect(shown("Requests")).toBe("12.4k");
		expect(shown("Visitors")).toBe("830");
		expect(shown("Bandwidth")).toBe("5 GiB");
		expect(shown("p95 latency")).toBe("245 ms");
		const link = screen.getByRole("link");
		expect(link).toHaveAttribute("href", "/servers/srv_1/traffic");
		expect(link).toHaveTextContent("Last 24 hours");
	});

	test("no requests means no latency to quote", async () => {
		stubApi({
			[ANALYTICS]: {
				body: report({ requests: 0, visitors: 0, bytesOut: 0, p95Ms: 0 }),
			},
		});
		await renderWithProviders(<TrafficSummary server={view()} />);
		await screen.findByText("Requests");
		expect(shown("Requests")).toBe("0");
		expect(shown("Bandwidth")).toBe("0 B");
		expect(shown("p95 latency")).toBe("–");
	});

	test("a host that never reported shows dashes, not zeros, and asks for nothing", async () => {
		const api = stubApi({});
		await renderWithProviders(
			<TrafficSummary server={view({ status: "pending", lastSeenAt: null })} />,
		);
		expect(shown("Requests")).toBe("–");
		expect(shown("Visitors")).toBe("–");
		expect(shown("Bandwidth")).toBe("–");
		expect(shown("p95 latency")).toBe("–");
		expect(screen.getByRole("link")).toHaveAttribute(
			"href",
			"/servers/srv_1/traffic",
		);
		expect(api.calls).toEqual([]);
	});

	test("a report that failed to load shows dashes instead of numbers", async () => {
		stubApi({
			[ANALYTICS]: problem(500, { title: "Internal" }),
		});
		await renderWithProviders(<TrafficSummary server={view()} />);
		await waitFor(() => expect(shown("Requests")).toBe("–"));
		expect(shown("p95 latency")).toBe("–");
	});
});

describe("ConnectCard", () => {
	const TOKEN = "POST /servers/:serverId/enrollment-token";
	const server = view({ status: "pending", facts: null, lastSeenAt: null });

	const enrollment = (expiresAt: string): EnrollmentToken => ({
		expiresAt,
		installCommand:
			"curl -fsSL https://ctrl.example/install-agent.sh | sudo sh -s -- --url https://ctrl.example --token nct_enroll_abc",
		server: wire({ status: "pending" }),
		token: "nct_enroll_abc",
	});
	const inAnHour = () => new Date(Date.now() + 3_600_000).toISOString();
	const anHourAgo = () => new Date(Date.now() - 3_600_000).toISOString();

	const SERVER = {
		"GET /servers/:serverId": { body: wire({ status: "pending" }) },
	};

	test("without a held command it explains why and offers to make one", async () => {
		stubApi(SERVER);
		await renderWithProviders(<ConnectCard server={server} />);
		expect(
			screen.getByText(/edge-1 is waiting for its agent/),
		).toBeInTheDocument();
		expect(screen.getByText(/shown once, when it is made/)).toBeInTheDocument();
		expect(screen.queryByLabelText("Install command")).not.toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "New install command" }),
		).toBeEnabled();
		expect(
			screen.queryByText(/stops the one above from working/),
		).not.toBeInTheDocument();
	});

	test("making a command asks the control plane with an idempotency key and shows it", async () => {
		let key: string | null = null;
		const api = stubApi({
			...SERVER,
			[TOKEN]: (request) => {
				key = request.headers.get("Idempotency-Key");
				return Response.json(enrollment(inAnHour()));
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<ConnectCard server={server} />);
		await user.click(
			screen.getByRole("button", { name: "New install command" }),
		);

		const field = await screen.findByLabelText("Install command");
		// One flag per line.
		expect((field as HTMLTextAreaElement).value).toBe(
			[
				"curl -fsSL https://ctrl.example/install-agent.sh",
				"| sudo sh -s --",
				"--url https://ctrl.example",
				"--token nct_enroll_abc",
			].join("\n"),
		);
		expect(
			screen.getByText(/A new command stops the one above from working/),
		).toBeInTheDocument();
		const post = api.calls.find((c) => c.method === "POST");
		expect(post?.path).toBe("/servers/srv_1/enrollment-token");
		expect(key).toMatch(/^[0-9a-f-]{36}$/);
	});

	test("a command still held for this server is shown straight away", async () => {
		const queryClient = testQueryClient();
		cacheEnrollment(queryClient, enrollment(inAnHour()));
		stubApi(SERVER);
		await renderWithProviders(<ConnectCard server={server} />, { queryClient });
		expect(await screen.findByLabelText("Install command")).toBeInTheDocument();
		expect(queryClient.getQueryData(enrollmentKey("srv_1"))).toBeDefined();
	});

	test("an expired command says so and is not shown", async () => {
		const queryClient = testQueryClient();
		cacheEnrollment(queryClient, enrollment(anHourAgo()));
		stubApi(SERVER);
		await renderWithProviders(<ConnectCard server={server} />, { queryClient });
		expect(
			await screen.findByText(/The last install command has expired/),
		).toBeInTheDocument();
		expect(screen.queryByLabelText("Install command")).not.toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "New install command" }),
		).toBeEnabled();
	});

	test("the key is off while the command is being made", async () => {
		stubApi({ ...SERVER, [TOKEN]: () => new Promise<Response>(() => {}) });
		const user = userEvent.setup();
		await renderWithProviders(<ConnectCard server={server} />);
		const key = screen.getByRole("button", { name: "New install command" });
		await user.click(key);
		await waitFor(() => expect(key).toBeDisabled());
	});

	test("a failure shows the reason and no command", async () => {
		stubApi({
			...SERVER,
			[TOKEN]: problem(500, {
				title: "Internal",
				detail: "token store is down",
			}),
		});
		const user = userEvent.setup();
		await renderWithProviders(<ConnectCard server={server} />);
		await user.click(
			screen.getByRole("button", { name: "New install command" }),
		);
		expect(await screen.findByText("token store is down")).toBeInTheDocument();
		expect(screen.queryByLabelText("Install command")).not.toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "New install command" }),
		).toBeEnabled();
	});

	test("keeps asking the control plane for the server while it waits", async () => {
		const api = stubApi(SERVER);
		await renderWithProviders(<ConnectCard server={server} />);
		await waitFor(() => expect(api.calls.length).toBeGreaterThan(0));
		expect(api.calls[0]?.path).toBe("/servers/srv_1");
	});
});

describe("OverviewTab", () => {
	const CHARTS = {
		"GET /servers/:serverId/metrics": () => new Promise<Response>(() => {}),
		"GET /servers/:serverId": () => Response.json(wire()),
		"GET /servers/:serverId/analytics": { body: report() },
		"GET /servers/:serverId/unrestricted": {
			body: {
				available: true,
				composeVersion: "2.29.1",
				dockerServices: 0,
				stacks: 0,
				unrestricted: false,
			},
		},
		"GET /projects": { body: { items: [] } },
	};

	function renderTab(server: ServerView) {
		return renderWithProviders(
			<ServerProvider value={{ server, openDrain: () => {} }}>
				<OverviewTab />
			</ServerProvider>,
		);
	}

	const headings = () =>
		screen.getAllByRole("heading", { level: 2 }).map((h) => h.textContent);

	test("a healthy server shows facts, charts, traffic, services and host details, with no warnings", async () => {
		stubApi(CHARTS);
		await renderTab(view());
		expect(headings()).toEqual([
			"Charts",
			"Traffic",
			"Services on this host",
			"Host details",
		]);
		expect(shown("OS")).toBe("ubuntu 24.04 · arm64");
		expect(await screen.findByText("No services on this server")).toBeVisible();
		expect(await screen.findByText("Kernel")).toBeInTheDocument();
		expect(screen.queryByRole("alert")).not.toBeInTheDocument();
		expect(screen.queryByText("Connect this host")).not.toBeInTheDocument();
	});

	test("a pending server leads with connecting and has no charts or host details", async () => {
		stubApi({
			...CHARTS,
			"GET /servers/:serverId": () =>
				Response.json(wire({ status: "pending" })),
		});
		await renderTab(view({ status: "pending", facts: null, lastSeenAt: null }));
		expect(headings()).toEqual([
			"Connect this host",
			"Traffic",
			"Services on this host",
		]);
		expect(
			screen.getByRole("button", { name: "New install command" }),
		).toBeInTheDocument();
		expect(screen.queryByText("Charts")).not.toBeInTheDocument();
		expect(screen.queryByText("Host details")).not.toBeInTheDocument();
	});

	test("an offline agent warns that the data is the last that came in", async () => {
		stubApi(CHARTS);
		await renderTab(view({ status: "offline" }));
		const alert = screen.getByRole("alert");
		expect(alert).toHaveTextContent("The agent is not reporting");
		expect(alert).toHaveTextContent(/last that came in/);
	});

	test("a full disk warns with its threshold and links to Docker cleanup", async () => {
		stubApi(CHARTS);
		await renderTab(view({ diskAlert: true, diskAlertThresholdPercent: 85 }));
		const alert = screen.getByRole("alert");
		expect(alert).toHaveTextContent("Disk is over its alert threshold");
		expect(alert).toHaveTextContent("at or above 85%");
		expect(
			within(alert).getByRole("link", { name: "Open Docker cleanup" }),
		).toHaveAttribute("href", "/servers/srv_1/settings/logs-and-disk");
	});

	test("a pending reboot links to Updates, but only once updates are known", async () => {
		stubApi(CHARTS);
		const { unmount } = await renderTab(
			view({
				patches: patches({ known: true, rebootRequired: true }),
			}),
		);
		const alert = screen.getByRole("alert");
		expect(alert).toHaveTextContent("Reboot required");
		expect(
			within(alert).getByRole("link", { name: "Open Updates" }),
		).toHaveAttribute("href", "/servers/srv_1/updates");
		unmount();

		await renderTab(
			view({
				patches: patches({ known: false, rebootRequired: true }),
			}),
		);
		expect(screen.queryByRole("alert")).not.toBeInTheDocument();
	});

	test("an unrestricted server warns and links to the danger zone", async () => {
		stubApi(CHARTS);
		await renderTab(view({ unrestricted: true }));
		const alert = screen.getByRole("alert");
		expect(alert).toHaveTextContent("Unrestricted");
		expect(
			within(alert).getByRole("link", { name: "Open the danger zone" }),
		).toHaveAttribute("href", "/servers/srv_1/settings/danger");
	});

	test("every true condition gets its own callout", async () => {
		stubApi(CHARTS);
		await renderTab(
			view({
				status: "offline",
				diskAlert: true,
				unrestricted: true,
				patches: patches({ known: true, rebootRequired: true }),
			}),
		);
		expect(screen.getAllByRole("alert")).toHaveLength(4);
	});
});
