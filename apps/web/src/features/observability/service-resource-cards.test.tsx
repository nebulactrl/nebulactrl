import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import type { ServiceMetricPoint } from "#/api/gen/types.gen";
import type { Service } from "#/api/models";
import { makeReport } from "#/features/analytics/test-fixtures";
import type { TrafficReport } from "#/features/analytics/types";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { ServiceResourceCards } from "./service-resource-cards";

const MIB = 1024 * 1024;

const service = (patch: Record<string, unknown> = {}): Service =>
	({
		id: "s1",
		name: "api",
		kind: "app",
		sourceType: "image",
		config: {},
		server: null,
		currentDeploymentId: null,
		latestDeployment: null,
		observed: null,
		stale: false,
		domains: [],
		desiredState: "running",
		scaling: { ready: 0, replicas: 0 },
		...patch,
	}) as unknown as Service;

const observed = { containerId: "c1", state: "running", health: "healthy" };

const sample = (minute: number, cpu: number, memMb: number, limitMb: number) =>
	({
		ts: `2026-10-29T10:${String(minute).padStart(2, "0")}:00Z`,
		cpuPercent: cpu,
		cpuMax: cpu,
		cpuPerReplica: cpu,
		memBytes: memMb * MIB,
		memLimitBytes: limitMb * MIB,
		memMax: memMb * MIB,
		replicaCount: 1,
	}) satisfies ServiceMetricPoint;

const report = (summary: Partial<TrafficReport["summary"]>) =>
	makeReport({
		summary: {
			...makeReport().summary,
			requests: 0,
			errorRate: 0,
			p95Ms: 0,
			...summary,
		},
	});

const render = (services: Service[]) =>
	renderWithProviders(
		<ServiceResourceCards services={services} projectId="p1" envSlug="prod" />,
	);

describe("ServiceResourceCards", () => {
	test("renders one card per service, each linking to its service on the canvas", async () => {
		await render([
			service({ id: "s1", name: "api" }),
			service({ id: "s2", name: "worker" }),
		]);
		const api = screen.getByRole("link", { name: "api" });
		expect(api.getAttribute("href")).toBe("/p/p1/prod?service=s1");
		expect(
			screen.getByRole("link", { name: "worker" }).getAttribute("href"),
		).toBe("/p/p1/prod?service=s2");
	});

	test("names the source and the server under the service", async () => {
		await render([
			service({
				server: {
					id: "srv1",
					name: "alpha",
					status: "online",
					lastSeenAt: null,
				},
			}),
		]);
		expect(screen.getByText("image · alpha")).toBeInTheDocument();
	});

	test("a git service is labelled by its host and shows no server when unplaced", async () => {
		await render([
			service({
				sourceType: "git",
				config: { gitUrl: "https://github.com/acme/app.git" },
			}),
		]);
		expect(screen.getByText("GitHub")).toBeInTheDocument();
	});

	test("the status line says what state the service is in", async () => {
		await render([service({ desiredState: "stopped" })]);
		expect(screen.getByText("Stopped")).toBeInTheDocument();
	});

	test("a service that has not been observed asks for no metrics and shows empty meters", async () => {
		const api = stubApi({});
		await render([service()]);
		expect(screen.getByRole("progressbar", { name: "CPU" })).toHaveAttribute(
			"aria-valuetext",
			"not measured",
		);
		const card = screen
			.getByRole("link", { name: "api" })
			.closest(".surface-raised") as HTMLElement;
		expect(within(card).getByText("Memory").nextSibling).toHaveTextContent("–");
		expect(screen.queryByRole("img", { name: /^CPU of api/ })).toBeNull();
		expect(api.calls).toEqual([]);
	});

	describe("resource use", () => {
		const routes = (points: ServiceMetricPoint[]) => ({
			"GET /services/:id/metrics": {
				body: make("MetricsOutputBody", { points }),
			},
		});

		test("asks for the last 30 minutes of the service", async () => {
			const api = stubApi(routes([sample(0, 10, 100, 0)]));
			await render([service({ observed })]);
			await waitFor(() => expect(api.calls).toHaveLength(1));
			expect(api.calls[0]?.path).toBe("/services/s1/metrics");
			expect(api.calls[0]?.query.get("range")).toBe("30m");
		});

		test("shows the newest CPU reading and draws a trend from two samples on", async () => {
			stubApi(routes([sample(0, 20, 256, 1024), sample(1, 42, 512, 1024)]));
			await render([service({ observed })]);
			await waitFor(() =>
				expect(
					screen.getByRole("progressbar", { name: "CPU" }),
				).toHaveAttribute("aria-valuetext", "42%"),
			);
			expect(screen.getByText(/last 30m/)).toBeInTheDocument();
			expect(
				screen.getByRole("img", {
					name: "CPU of api, last 30m: latest 42%, low 20%, high 42%",
				}),
			).toBeInTheDocument();
		});

		test("a single sample draws no trend yet", async () => {
			stubApi(routes([sample(0, 42, 512, 1024)]));
			await render([service({ observed })]);
			await waitFor(() =>
				expect(
					screen.getByRole("progressbar", { name: "CPU" }),
				).toHaveAttribute("aria-valuetext", "42%"),
			);
			expect(screen.queryByRole("img", { name: /^CPU of api/ })).toBeNull();
		});

		test("memory under a limit is a meter with used over limit", async () => {
			stubApi(routes([sample(0, 5, 512, 2048)]));
			await render([service({ observed })]);
			const memory = await screen.findByRole("progressbar", { name: "Memory" });
			expect(memory).toHaveAttribute("aria-valuetext", "25%");
			expect(screen.getByText(/512 MiB \/ 2 GiB/)).toBeInTheDocument();
		});

		test("memory without a limit is a plain figure marked 'no limit'", async () => {
			stubApi(routes([sample(0, 5, 512, 0)]));
			await render([service({ observed })]);
			expect(await screen.findByText(/no limit/)).toBeInTheDocument();
			expect(screen.getByText("512 MiB", { exact: false })).toBeInTheDocument();
			expect(screen.queryByRole("progressbar", { name: "Memory" })).toBeNull();
		});

		test("a stale service dims its figures", async () => {
			stubApi(routes([sample(0, 5, 512, 0)]));
			await render([service({ observed, stale: true })]);
			const note = await screen.findByText(/no limit/);
			expect(note.closest("div.flex")).toHaveClass("opacity-50");
		});
	});

	describe("replicas", () => {
		test("a release with all replicas ready shows ready over desired", async () => {
			await render([
				service({
					currentDeploymentId: "d1",
					scaling: { ready: 2, replicas: 2 },
				}),
			]);
			const badge = screen.getByText(/2\/2 replicas/);
			expect(badge.closest("[title]")).toHaveAttribute(
				"title",
				"All replicas are ready",
			);
		});

		test("short of replicas is flagged with words for screen readers", async () => {
			await render([
				service({
					currentDeploymentId: "d1",
					scaling: { ready: 1, replicas: 3 },
				}),
			]);
			expect(screen.getByText(/1\/3 replicas/)).toBeInTheDocument();
			expect(
				screen.getByText("ready; some are not", { exact: false }),
			).toHaveClass("sr-only");
			expect(
				screen.getByTitle("Some replicas are not ready"),
			).toBeInTheDocument();
		});

		test("one replica reads in the singular", async () => {
			await render([
				service({
					currentDeploymentId: "d1",
					scaling: { ready: 1, replicas: 1 },
				}),
			]);
			expect(screen.getByText(/1\/1 replica$/)).toBeInTheDocument();
		});

		test("a service with no release has no replica badge", async () => {
			await render([service({ scaling: { ready: 0, replicas: 1 } })]);
			expect(screen.queryByText(/replica/)).toBeNull();
		});
	});

	describe("traffic", () => {
		const withDomain = {
			domains: [{ id: "dom1", hostname: "api.example.com" }],
		};

		test("a service without a domain says no traffic is collected and asks for none", async () => {
			const api = stubApi({});
			await render([service()]);
			expect(
				screen.getByText("No domain, so no traffic is collected"),
			).toBeInTheDocument();
			expect(api.calls).toEqual([]);
		});

		test("shows the last hour's requests, error rate and p95", async () => {
			const api = stubApi({
				"GET /services/:id/analytics": {
					body: report({ requests: 12_400, errorRate: 0.025, p95Ms: 182 }),
				},
			});
			await render([service(withDomain)]);
			expect(await screen.findByText("12.4k")).toBeInTheDocument();
			expect(screen.getByText("req / h")).toBeInTheDocument();
			expect(screen.getByText("2.5%")).toBeInTheDocument();
			expect(screen.getByText("errors")).toBeInTheDocument();
			expect(screen.getByText("182 ms")).toBeInTheDocument();
			expect(screen.getByText("p95")).toBeInTheDocument();
			expect(api.calls[0]?.path).toBe("/services/s1/analytics");
			expect(api.calls[0]?.query.get("range")).toBe("1h");
			expect(api.calls[0]?.query.has("compare")).toBe(false);
		});

		test("with no requests p95 is a dash, not a made-up speed", async () => {
			stubApi({ "GET /services/:id/analytics": { body: report({}) } });
			await render([service(withDomain)]);
			await screen.findByText("req / h");
			const p95 = screen.getByText("p95").previousElementSibling as HTMLElement;
			expect(p95).toHaveTextContent("–");
			expect(
				screen.getByText("errors").previousElementSibling,
			).toHaveTextContent("0%");
		});

		test("says traffic is loading, then unavailable when it fails", async () => {
			stubApi({
				"GET /services/:id/analytics": problem(500, {
					status: 500,
					title: "x",
					detail: "no",
					code: "internal",
				}),
			});
			await render([service(withDomain)]);
			expect(screen.getByText("Loading traffic…")).toBeInTheDocument();
			expect(
				await screen.findByText("Traffic unavailable"),
			).toBeInTheDocument();
		});
	});
});
