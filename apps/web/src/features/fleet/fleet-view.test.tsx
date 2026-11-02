import { describe, expect, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { FleetServer, FleetService } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { FleetView } from "./fleet-view";

const GiB = 1024 ** 3;

const service = (overrides: Partial<FleetService> = {}): FleetService => ({
	id: "svc1",
	name: "api",
	slug: "api",
	kind: "app",
	status: "running",
	stateful: false,
	cpuPercent: 3,
	memBytes: GiB / 2,
	memLimitBytes: GiB,
	projectId: "p1",
	projectName: "Shop",
	environmentId: "e1",
	environmentName: "Production",
	environmentSlug: "production",
	...overrides,
});

const server = (overrides: Partial<FleetServer> = {}): FleetServer => ({
	id: "s1",
	name: "alpha",
	status: "online",
	drainState: "none",
	drainMessage: "",
	cpus: 4,
	cpuPercent: 20,
	memTotalBytes: 8 * GiB,
	memUsedBytes: 2 * GiB,
	committedMemBytes: 4 * GiB,
	unlimitedServices: 0,
	diskTotalBytes: 100 * GiB,
	diskUsedBytes: 10 * GiB,
	overcommitted: false,
	services: [service()],
	...overrides,
});

const totals = {
	servers: 1,
	services: 1,
	memTotalBytes: 8 * GiB,
	memUsedBytes: 2 * GiB,
	committedMemBytes: 4 * GiB,
};

const fleet = (servers: FleetServer[], t = totals) => ({
	"GET /fleet": { body: { servers, totals: t } },
});

const panel = (name: string) =>
	screen.getByRole("region", { name }) as HTMLElement;

describe("FleetView", () => {
	test("shows a loading state until the fleet arrives", async () => {
		stubApi(fleet([server()]));
		await renderWithProviders(<FleetView />);
		expect(
			screen.getByRole("status", { name: "Loading fleet" }),
		).toBeInTheDocument();
		expect(
			await screen.findByRole("region", { name: "alpha" }),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("status", { name: "Loading fleet" }),
		).not.toBeInTheDocument();
	});

	test("offers a retry when the fleet did not load, and recovers", async () => {
		let attempts = 0;
		stubApi({
			"GET /fleet": () => {
				attempts += 1;
				return attempts === 1
					? Response.json(
							{ status: 500, title: "x", detail: "boom", code: "internal" },
							{ status: 500 },
						)
					: Response.json({ servers: [server()], totals });
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<FleetView />);
		expect(
			await screen.findByText("The fleet did not load"),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: /try again|retry/i }));
		expect(
			await screen.findByRole("region", { name: "alpha" }),
		).toBeInTheDocument();
	});

	test("with no servers, points at adding one", async () => {
		stubApi(
			fleet([], {
				servers: 0,
				services: 0,
				memTotalBytes: 0,
				memUsedBytes: 0,
				committedMemBytes: 0,
			}),
		);
		await renderWithProviders(<FleetView />);
		expect(await screen.findByText("No servers yet")).toBeInTheDocument();
		const add = screen.getByRole("button", { name: "Add server" });
		expect(add).toHaveAttribute("href", expect.stringContaining("/servers"));
		expect(add.getAttribute("href")).toContain("enroll");
		expect(screen.queryByRole("region")).not.toBeInTheDocument();
	});

	test("totals show the fleet's servers, services and memory", async () => {
		stubApi(
			fleet([server(), server({ id: "s2", name: "beta", services: [] })], {
				...totals,
				servers: 2,
				services: 1,
			}),
		);
		await renderWithProviders(<FleetView />);
		await screen.findByRole("region", { name: "alpha" });
		expect(screen.getByText("Servers")).toBeInTheDocument();
		expect(screen.getByText("of 8 GiB across the fleet")).toBeInTheDocument();
		expect(screen.getByText("Within capacity")).toBeInTheDocument();
	});

	test("totals say memory is unreported until an agent connects", async () => {
		stubApi(
			fleet(
				[server({ memTotalBytes: 0, memUsedBytes: 0, status: "pending" })],
				{ ...totals, memTotalBytes: 0, memUsedBytes: 0, committedMemBytes: 0 },
			),
		);
		await renderWithProviders(<FleetView />);
		await screen.findByRole("region", { name: "alpha" });
		expect(
			screen.getByText("Reported once an agent connects"),
		).toBeInTheDocument();
	});

	test("an overcommitted server is called out in the totals and its own panel", async () => {
		stubApi(
			fleet([server({ committedMemBytes: 12 * GiB })], {
				...totals,
				committedMemBytes: 12 * GiB,
			}),
		);
		await renderWithProviders(<FleetView />);
		await screen.findByRole("region", { name: "alpha" });
		expect(screen.getByText(/1 server is\s+overcommitted/)).toBeInTheDocument();
		const alpha = within(panel("alpha"));
		expect(alpha.getByText("Overcommitted.")).toBeInTheDocument();
		expect(
			alpha.getByText(
				/Memory limits add up to 12 GiB, 4 GiB more than this host has/,
			),
		).toBeInTheDocument();
		expect(alpha.getByText("Limits promise 12 GiB")).toBeInTheDocument();
	});

	test("counts several overcommitted servers in the plural", async () => {
		stubApi(
			fleet(
				[
					server({ committedMemBytes: 12 * GiB }),
					server({ id: "s2", name: "beta", committedMemBytes: 9 * GiB }),
				],
				{ ...totals, servers: 2 },
			),
		);
		await renderWithProviders(<FleetView />);
		await screen.findByRole("region", { name: "alpha" });
		expect(
			screen.getByText(/2 servers are\s+overcommitted/),
		).toBeInTheDocument();
	});

	test("a server panel shows its capacity, link and service count", async () => {
		stubApi(
			fleet([
				server({
					unlimitedServices: 2,
					services: [service(), service({ id: "svc2", name: "web" })],
				}),
			]),
		);
		await renderWithProviders(<FleetView />);
		const alpha = within(await screen.findByRole("region", { name: "alpha" }));
		expect(alpha.getByRole("link", { name: "alpha" })).toHaveAttribute(
			"href",
			"/servers/s1",
		);
		expect(alpha.getByText(/4 vCPU · 2 services/)).toBeInTheDocument();
		expect(alpha.getByText("Online")).toBeInTheDocument();
		expect(alpha.getByText("2 GiB / 8 GiB")).toBeInTheDocument();
		expect(alpha.getByText("10 GiB / 100 GiB")).toBeInTheDocument();
		expect(
			alpha.getByText(/Limits promise 4 GiB · 2 without a limit/),
		).toBeInTheDocument();
		expect(alpha.queryByText("Overcommitted.")).not.toBeInTheDocument();
	});

	test("says when no memory limits are set and uses the singular for one service", async () => {
		stubApi(fleet([server({ committedMemBytes: 0, cpus: 0 })]));
		await renderWithProviders(<FleetView />);
		const alpha = within(await screen.findByRole("region", { name: "alpha" }));
		expect(alpha.getByText("No memory limits set")).toBeInTheDocument();
		// Without a CPU count only the service count shows.
		expect(alpha.getByText("1 service")).toBeInTheDocument();
	});

	test("a service row links to its canvas and shows state, project and memory", async () => {
		stubApi(
			fleet([
				server({
					services: [
						service(),
						service({
							id: "svc2",
							name: "pg",
							kind: "database",
							stateful: true,
							status: "unhealthy",
							memBytes: GiB,
							memLimitBytes: 0,
						}),
						service({
							id: "svc3",
							name: "cron",
							kind: "worker",
							status: "undeployed",
							memBytes: 0,
							memLimitBytes: 0,
						}),
					],
				}),
			]),
		);
		await renderWithProviders(<FleetView />);
		const alpha = within(await screen.findByRole("region", { name: "alpha" }));

		const api = alpha.getByRole("link", { name: /api/ });
		expect(api.getAttribute("href")).toContain("/p/p1/production");
		expect(api.getAttribute("href")).toContain("service=svc1");
		expect(within(api).getByText("Running")).toBeInTheDocument();
		expect(within(api).getByText("Shop / Production")).toBeInTheDocument();
		expect(within(api).getByText("512 MiB / 1 GiB")).toBeInTheDocument();

		const pg = within(alpha.getByRole("link", { name: /pg/ }));
		expect(pg.getByText("Unhealthy")).toBeInTheDocument();
		expect(pg.getByText("stateful")).toBeInTheDocument();
		expect(pg.getByText("1 GiB · no limit")).toBeInTheDocument();

		const cron = within(alpha.getByRole("link", { name: /cron/ }));
		expect(cron.getByText("Not deployed")).toBeInTheDocument();
		expect(cron.getByText("no usage yet")).toBeInTheDocument();
		expect(cron.queryByText("stateful")).not.toBeInTheDocument();
	});

	test("an empty server says nothing is placed on it", async () => {
		stubApi(fleet([server({ services: null })]));
		await renderWithProviders(<FleetView />);
		const alpha = within(await screen.findByRole("region", { name: "alpha" }));
		expect(
			alpha.getByText("No services placed on this server."),
		).toBeInTheDocument();
		expect(alpha.getByText("0 services", { exact: false })).toBeInTheDocument();
	});

	test("a server without a heartbeat explains why there is no capacity", async () => {
		stubApi(
			fleet([
				server({
					id: "s2",
					name: "fresh",
					status: "pending",
					memTotalBytes: 0,
					memUsedBytes: 0,
					diskTotalBytes: 0,
					services: [],
				}),
				server({
					id: "s3",
					name: "silent",
					status: "offline",
					memTotalBytes: 0,
					memUsedBytes: 0,
					diskTotalBytes: 0,
					services: [],
				}),
			]),
		);
		await renderWithProviders(<FleetView />);
		await screen.findByRole("region", { name: "fresh" });
		expect(
			within(panel("fresh")).getByText(
				"Waiting for the agent; capacity appears once it connects.",
			),
		).toBeInTheDocument();
		expect(
			within(panel("silent")).getByText("No heartbeat has been recorded yet."),
		).toBeInTheDocument();
	});

	test("drain states: draining explains, failed shows the message, drained is marked", async () => {
		stubApi(
			fleet([
				server({ id: "a", name: "draining", drainState: "draining" }),
				server({
					id: "b",
					name: "broken",
					drainState: "failed",
					drainMessage: "move of api failed",
				}),
				server({ id: "c", name: "empty", drainState: "drained" }),
				server({ id: "d", name: "plain" }),
			]),
		);
		await renderWithProviders(<FleetView />);
		await screen.findByRole("region", { name: "plain" });

		const draining = within(panel("draining"));
		expect(draining.getByText("Draining")).toBeInTheDocument();
		expect(
			draining.getByText(
				/Not accepting new services while its services move away/,
			),
		).toBeInTheDocument();

		const broken = within(panel("broken"));
		expect(broken.getByText("Drain failed")).toBeInTheDocument();
		expect(broken.getByRole("alert")).toHaveTextContent("move of api failed");

		expect(within(panel("empty")).getByText("Drained")).toBeInTheDocument();
		expect(panel("empty")).toHaveAttribute("data-drain-state", "drained");

		const plain = within(panel("plain"));
		expect(
			plain.queryByText(/Draining|Drained|Drain failed/),
		).not.toBeInTheDocument();
		expect(
			plain.queryByText(/Not accepting new services/),
		).not.toBeInTheDocument();
	});

	test("a revoked server with nothing placed is left out; one with services stays", async () => {
		stubApi(
			fleet([
				server({ id: "a", name: "gone", status: "revoked", services: [] }),
				server({ id: "b", name: "stuck", status: "revoked" }),
				server({ id: "c", name: "live" }),
			]),
		);
		await renderWithProviders(<FleetView />);
		await screen.findByRole("region", { name: "live" });
		expect(
			screen.queryByRole("region", { name: "gone" }),
		).not.toBeInTheDocument();
		expect(screen.getByRole("region", { name: "stuck" })).toBeInTheDocument();
	});
});
