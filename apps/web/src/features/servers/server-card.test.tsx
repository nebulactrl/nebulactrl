import { describe, expect, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import { getInstanceUpgradeQueryKey } from "#/api/gen/@tanstack/react-query.gen";
import type { Server as ServerWire } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders, testQueryClient } from "#/test/render";
import { type ServerView, toServerView } from "./model";
import { ProxyLine, ServerCard } from "./server-card";

const GiB = 1024 ** 3;
const minutesAgo = (n: number) =>
	new Date(Date.now() - n * 60_000).toISOString();

function wireServer(overrides: Partial<ServerWire> = {}): ServerWire {
	return {
		acceptWorkloads: true,
		acmeEmail: null,
		agentVersion: "v0.4.2",
		baseDomain: null,
		buildAllowEmulation: false,
		buildAllowPreviews: false,
		buildEnabled: false,
		buildMaxConcurrent: 2,
		canDecommission: true,
		composeVersion: "",
		controlPlane: false,
		createdAt: "2026-01-01T00:00:00Z",
		diskAlert: false,
		diskAlertThresholdPercent: 90,
		drainMessage: "",
		drainStartedAt: null,
		drainState: "none",
		facts: {
			arch: "arm64",
			cgroupVersion: "2",
			cpus: 4,
			diskTotal: 100 * GiB,
			dockerVersion: "27.3",
			kernel: "6.8",
			memTotal: 16 * GiB,
			os: "Ubuntu 24.04",
			port443Free: true,
			port80Free: true,
		},
		geoEnabled: false,
		geoLevel: "country",
		geoRefreshDays: 7,
		geoSource: "dbip",
		id: "srv-1",
		lastSeenAt: minutesAgo(5),
		latest: {
			cpuPercent: 42,
			diskTotal: 100 * GiB,
			diskUsed: 60 * GiB,
			memTotal: 16 * GiB,
			memUsed: 8 * GiB,
			sampledAt: minutesAgo(5),
		},
		metricsRetentionDays: 14,
		name: "hetzner-01",
		patches: {
			busy: "",
			checkedAt: null,
			known: false,
			rebootRequired: false,
			security: 0,
			supported: true,
			updates: 0,
		},
		protocolVersion: 1,
		proxy: {
			appliedGeneration: 1,
			generation: 1,
			status: "ok",
			tailnet: {
				appliedMode: "off",
				hostname: "",
				ips: null,
				mode: "off",
				state: "",
			},
			v2: true,
		},
		publicIp: "203.0.113.10",
		serviceCount: 3,
		status: "online",
		trafficAnalyticsEnabled: false,
		trafficDailyRetentionDays: 30,
		trafficHourlyRetentionDays: 7,
		tunnel: {
			active: false,
			connections: 0,
			enabled: false,
			observedAt: null,
			state: "off",
		},
		unrestricted: false,
		...overrides,
	};
}

const view = (overrides: Partial<ServerWire> = {}) =>
	toServerView(wireServer(overrides));

const instanceUpgrade = (
	servers: { id: string; skew: "current" | "behind" }[],
	controlPlaneVersion = "v0.4.2",
) => ({
	agentAutoUpgrade: true,
	behind: servers.filter((s) => s.skew === "behind").length,
	controlPlaneVersion,
	servers: servers.map(({ id, skew }) => ({
		agentVersion: "v0.4.2",
		canSelfUpgrade: true,
		name: id,
		serverId: id,
		skew,
		status: "online",
	})),
	upgradable: 0,
});

/**
 * Renders a card with the instance-upgrade query pre-seeded (the card's skew
 * badge reads it), so no request happens and nothing updates after the render.
 */
async function renderCard(
	server: ServerWire = wireServer(),
	skew: "current" | "behind" = "current",
	controlPlaneVersion?: string,
) {
	const api = stubApi({});
	const queryClient = testQueryClient();
	queryClient.setQueryData(
		getInstanceUpgradeQueryKey(),
		instanceUpgrade([{ id: server.id, skew }], controlPlaneVersion),
	);
	await renderWithProviders(<ServerCard server={toServerView(server)} />, {
		queryClient,
	});
	return api;
}

describe("ServerCard", () => {
	test("links to the server and shows its identity, status and versions", async () => {
		const api = await renderCard(
			wireServer({ baseDomain: "apps.example.com", name: "hetzner-01" }),
		);
		const link = screen.getByRole("link", { name: /hetzner-01/ });
		expect(link).toHaveAttribute("href", "/servers/srv-1");
		expect(
			within(link).getByText("203.0.113.10 · apps.example.com"),
		).toBeInTheDocument();
		expect(within(link).getByText("Online")).toBeInTheDocument();
		expect(
			within(link).getByText("agent v0.4.2 · docker 27.3 · arm64"),
		).toBeInTheDocument();
		expect(within(link).getByText("3 services")).toBeInTheDocument();
		expect(within(link).getByText(/^seen/)).toHaveTextContent("seen 5m ago");
		expect(within(link).getByText("Proxy ok")).toBeInTheDocument();
		expect(api.unmatched).toEqual([]);
	});

	test("says so for a missing public IP, a control-plane host and a server that takes no workloads", async () => {
		await renderCard(
			wireServer({
				publicIp: null,
				controlPlane: true,
				acceptWorkloads: false,
			}),
		);
		expect(
			screen.getByText("no public IP · control plane · no workloads"),
		).toBeInTheDocument();
	});

	test("a single service is not pluralised", async () => {
		await renderCard(wireServer({ serviceCount: 1 }));
		expect(screen.getByText("1 service")).toBeInTheDocument();
	});

	test("meters for CPU, memory and disk carry the latest heartbeat", async () => {
		await renderCard();
		expect(screen.getByRole("progressbar", { name: "cpu" })).toHaveAttribute(
			"aria-valuetext",
			"42%",
		);
		expect(screen.getByRole("progressbar", { name: "memory" })).toHaveAttribute(
			"aria-valuetext",
			"50%",
		);
		expect(screen.getByRole("progressbar", { name: "disk" })).toHaveAttribute(
			"aria-valuetext",
			"60%",
		);
		expect(screen.getByText("8 / 16 GiB")).toBeInTheDocument();
		expect(screen.getByText("60 / 100 GiB")).toBeInTheDocument();
		expect(screen.queryByRole("alert")).not.toBeInTheDocument();
		expect(screen.queryByText(/last figures/)).not.toBeInTheDocument();
	});

	test("a pending server waits for its agent instead of showing meters", async () => {
		await renderCard(
			wireServer({
				status: "pending",
				agentVersion: null,
				facts: undefined as unknown as ServerWire["facts"],
				latest: undefined as unknown as ServerWire["latest"],
				lastSeenAt: null,
			}),
		);
		expect(screen.getByText("Waiting for agent")).toBeInTheDocument();
		expect(
			screen.getByText(/Waiting for the agent\. Open the server/),
		).toBeInTheDocument();
		expect(screen.getByText("agent not connected")).toBeInTheDocument();
		expect(screen.getByText("never seen")).toBeInTheDocument();
		expect(screen.queryByText("cpu")).not.toBeInTheDocument();
	});

	test("an online server without a heartbeat says none was recorded", async () => {
		await renderCard(
			wireServer({ latest: undefined as unknown as ServerWire["latest"] }),
		);
		expect(
			screen.getByText("No heartbeat has been recorded yet."),
		).toBeInTheDocument();
		expect(screen.queryByText("cpu")).not.toBeInTheDocument();
	});

	test("an offline server says its figures are the last it sent", async () => {
		await renderCard(wireServer({ status: "offline" }));
		expect(screen.getByText("Offline")).toBeInTheDocument();
		expect(
			screen.getByText("Not reporting: these are the last figures it sent."),
		).toBeInTheDocument();
	});

	test("a revoked server says its figures are the last it reported", async () => {
		await renderCard(wireServer({ status: "revoked" }));
		expect(screen.getByText("Revoked")).toBeInTheDocument();
		expect(
			screen.getByText("Revoked: these are the last figures it reported."),
		).toBeInTheDocument();
	});

	test("a disk over its threshold raises an alert naming the threshold", async () => {
		await renderCard(
			wireServer({ diskAlert: true, diskAlertThresholdPercent: 80 }),
		);
		const alert = screen.getByRole("alert");
		expect(alert).toHaveTextContent("Disk alert.");
		expect(alert).toHaveTextContent("Disk is over its 80% threshold");
	});

	test("an unrestricted server is flagged with what it allows", async () => {
		await renderCard(wireServer({ unrestricted: true }));
		const note = screen.getByRole("note");
		expect(note).toHaveTextContent("Unrestricted.");
		expect(note).toHaveTextContent("privileged mode, host networking");
	});

	test("a plain deployment server has no role badge or note", async () => {
		await renderCard();
		expect(screen.queryByText("Build server")).not.toBeInTheDocument();
		expect(screen.queryByText("Apps and builds")).not.toBeInTheDocument();
		expect(screen.queryByRole("note")).not.toBeInTheDocument();
	});

	test("build servers wear their role badge", async () => {
		await renderCard(
			wireServer({ buildEnabled: true, acceptWorkloads: false }),
		);
		expect(screen.getByText("Build server")).toBeInTheDocument();
	});

	test("a server that runs apps and builds says both", async () => {
		await renderCard(wireServer({ buildEnabled: true, acceptWorkloads: true }));
		expect(screen.getByText("Apps and builds")).toBeInTheDocument();
	});

	test("a draining server shows its drain state", async () => {
		await renderCard(wireServer({ drainState: "draining" }));
		expect(screen.getByText("Draining")).toBeInTheDocument();
	});

	test("shows the pending update count and a reboot warning when the host needs one", async () => {
		await renderCard(
			wireServer({
				patches: {
					busy: "",
					checkedAt: minutesAgo(10),
					known: true,
					rebootRequired: true,
					security: 1,
					supported: true,
					updates: 3,
				},
			}),
		);
		expect(
			screen.getAllByText("3 updates (1 security)").length,
		).toBeGreaterThan(0);
		expect(screen.getByText("Reboot required")).toBeInTheDocument();
	});

	test("an up-to-date host shows no reboot badge", async () => {
		await renderCard(
			wireServer({
				patches: {
					busy: "",
					checkedAt: minutesAgo(10),
					known: true,
					rebootRequired: false,
					security: 0,
					supported: true,
					updates: 0,
				},
			}),
		);
		expect(screen.getAllByText("Up to date").length).toBeGreaterThan(0);
		expect(screen.queryByText("Reboot required")).not.toBeInTheDocument();
	});

	test("says nothing about updates before a check has run", async () => {
		await renderCard(wireServer());
		expect(screen.queryByText(/updates/i)).not.toBeInTheDocument();
		expect(screen.queryByText("Up to date")).not.toBeInTheDocument();
	});

	test("an agent that is behind the control plane gets a skew badge", async () => {
		await renderCard(wireServer(), "behind", "v0.5.0");
		expect(screen.getByText(/v0\.5\.0/)).toBeInTheDocument();
	});

	test("a current agent gets no skew badge", async () => {
		await renderCard(wireServer(), "current", "v0.5.0");
		expect(screen.queryByText(/v0\.5\.0/)).not.toBeInTheDocument();
	});
});

describe("ProxyLine", () => {
	const proxy = (status: string, error?: string) =>
		({
			...view().proxy,
			status,
			...(error ? { error } : {}),
		}) as ServerView["proxy"];

	test("an erroring proxy shows its reason", async () => {
		await renderWithProviders(
			<ProxyLine proxy={proxy("error", "port 443 in use")} />,
		);
		const line = screen.getByText("Proxy error: port 443 in use");
		expect(line).toHaveAttribute("title", "port 443 in use");
	});

	test("an erroring proxy without a reason says only that it failed", async () => {
		await renderWithProviders(<ProxyLine proxy={proxy("error")} />);
		expect(screen.getByText("Proxy error")).toBeInTheDocument();
	});

	test("a starting proxy says so", async () => {
		await renderWithProviders(<ProxyLine proxy={proxy("starting")} />);
		expect(screen.getByText("Proxy starting")).toBeInTheDocument();
	});

	test("a proxy that has not reported says so", async () => {
		await renderWithProviders(<ProxyLine proxy={proxy("")} />);
		expect(screen.getByText("Proxy not reported")).toBeInTheDocument();
	});
});
