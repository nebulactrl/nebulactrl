import { describe, expect, spyOn, test } from "bun:test";
import { useSearch } from "@tanstack/react-router";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ReactFlowProvider } from "@xyflow/react";
import type { Service } from "#/api/models";
import { DIMMED_OPACITY } from "#/features/canvas/canvas-context";
import { NO_PLUGS, type Plugs } from "#/features/canvas/lib/cable";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { makeProcess, makeProcessService, makeScaling } from "./fixtures";
import { NODE_PROCESS_LIMIT, PROCESS_NODE_WIDTH } from "./model";
import { ProcessCard } from "./process-card";

function Probe() {
	const search = useSearch({ strict: false }) as Record<string, unknown>;
	return <output aria-label="search">{JSON.stringify(search)}</output>;
}

async function renderCard(
	service: Service,
	over: { plugs?: Plugs; selected?: boolean; dimmed?: boolean } = {},
) {
	return renderWithProviders(
		<ReactFlowProvider>
			<ProcessCard
				service={service}
				plugs={over.plugs ?? NO_PLUGS}
				selected={over.selected ?? false}
				dimmed={over.dimmed ?? false}
			/>
			<Probe />
		</ReactFlowProvider>,
		{ path: "/?tab=settings" },
	);
}

const SERVICE = makeProcessService({
	processes: [
		makeProcess({
			name: "web",
			default: true,
			port: 3000,
			command: ["bin/rails", "server"],
			scaling: makeScaling({ replicas: 2, ready: 2 }),
		}),
		makeProcess({
			name: "worker",
			command: ["bundle", "exec", "sidekiq"],
			observed: {
				name: "worker",
				state: "running",
				health: "unhealthy",
				replicasDesired: 1,
				replicasReady: 0,
			},
		}),
		makeProcess({ name: "clock", pending: true, observed: null }),
	],
});

const rowsOf = () =>
	within(screen.getByRole("list", { name: "Processes of shop" })).getAllByRole(
		"listitem",
	);

describe("ProcessCard", () => {
	test("names the service, counts its processes and says where it comes from", async () => {
		stubApi({});
		await renderCard(SERVICE);
		expect(screen.getByText("shop")).toBeInTheDocument();
		expect(
			screen.getByText(/3 processes · traefik\/whoami:latest/),
		).toBeInTheDocument();
	});

	test("a row per process: name, command, replicas, port and state in words", async () => {
		stubApi({});
		await renderCard(SERVICE);
		const [web, worker, clock] = rowsOf();
		expect(web).toHaveTextContent("web");
		expect(web).toHaveTextContent("bin/rails server");
		expect(web).toHaveTextContent("2/2");
		expect(web).toHaveTextContent(":3000");
		expect(web).toHaveTextContent("Healthy");
		expect(worker).toHaveTextContent("bundle exec sidekiq");
		expect(worker).toHaveTextContent("0/1");
		expect(worker).toHaveTextContent("Unhealthy");
		expect(worker).not.toHaveTextContent(":");
		expect(clock).toHaveTextContent("staged");
		expect(clock).toHaveTextContent("Staged");
	});

	test("a default process without a port is tagged default", async () => {
		stubApi({});
		await renderCard(
			makeProcessService({
				processes: [
					makeProcess({ name: "app", default: true, port: 0 }),
					makeProcess({ name: "worker" }),
				],
			}),
		);
		const [app, worker] = rowsOf();
		expect(app).toHaveTextContent("default");
		expect(worker).not.toHaveTextContent("default");
	});

	test("a removed process is badged removed", async () => {
		stubApi({});
		await renderCard(
			makeProcessService({
				processes: [
					makeProcess({ name: "web", default: true, port: 3000 }),
					makeProcess({ name: "old", removed: true }),
				],
			}),
		);
		expect(rowsOf()[1]).toHaveTextContent("removed");
	});

	test("clicking a row opens the inspector's Overview on that process", async () => {
		stubApi({});
		const user = userEvent.setup();
		await renderCard(SERVICE);
		await user.click(screen.getByRole("button", { name: /^worker/ }));
		await waitFor(() =>
			expect(
				JSON.parse(screen.getByLabelText("search").textContent ?? ""),
			).toEqual({
				tab: "overview",
				service: "svc-shop",
				process: "worker",
			}),
		);
	});

	test("a service with no processes says so", async () => {
		stubApi({});
		await renderCard(makeProcessService({ processes: [] }));
		expect(screen.getByText("No processes yet")).toBeInTheDocument();
		expect(screen.getByText(/0 processes/)).toBeInTheDocument();
	});

	test("past the row limit failed processes lead and the rest is +N more", async () => {
		stubApi({});
		const many = Array.from({ length: NODE_PROCESS_LIMIT + 2 }, (_, i) =>
			makeProcess({
				name: `p${i}`,
				default: i === 0,
				port: i === 0 ? 3000 : 0,
			}),
		);
		many[NODE_PROCESS_LIMIT + 1] = makeProcess({
			name: "broken",
			observed: {
				name: "broken",
				state: "exited",
				health: "",
				replicasDesired: 1,
				replicasReady: 0,
			},
		});
		await renderCard(makeProcessService({ processes: many }));
		const rows = rowsOf();
		// The "+N more" line is the last list item and not a button.
		expect(rows).toHaveLength(NODE_PROCESS_LIMIT + 1);
		expect(rows[NODE_PROCESS_LIMIT]).toHaveTextContent("+2 more");
		expect(
			within(rows[0] as HTMLElement).getByRole("button"),
		).toHaveTextContent("broken");
		expect(
			screen.queryByRole("button", { name: /^p9/ }),
		).not.toBeInTheDocument();
	});

	test("shows the server the service runs on, or that it has none", async () => {
		stubApi({});
		const { unmount } = await renderCard(SERVICE);
		expect(screen.getByTitle("Runs on hetzner-01")).toHaveTextContent(
			"hetzner-01",
		);
		unmount();
		await renderCard({ ...SERVICE, server: null, serverId: null });
		expect(screen.getByTitle("Not placed on a server")).toHaveTextContent(
			"no server",
		);
	});

	test("marks a card that has staged changes", async () => {
		stubApi({});
		const { unmount } = await renderCard({ ...SERVICE, stagedKind: "edited" });
		expect(
			screen.getByTitle("Staged changes waiting to deploy"),
		).toHaveTextContent("Staged");
		unmount();
		await renderCard({ ...SERVICE, stagedKind: "new" });
		expect(
			screen.getByTitle("New service: not deployed yet"),
		).toBeInTheDocument();
	});

	test("without staged changes there is no Staged badge", async () => {
		stubApi({});
		await renderCard(SERVICE);
		expect(
			screen.queryByTitle(/Staged changes|New service/),
		).not.toBeInTheDocument();
	});

	test("a dimmed card says it does not match the filter and fades", async () => {
		stubApi({});
		const { container } = await renderCard(SERVICE, { dimmed: true });
		expect(screen.getByText("Does not match the filter")).toBeInTheDocument();
		const card = container.querySelector("[data-process-card]") as HTMLElement;
		expect(card.style.opacity).toBe(String(DIMMED_OPACITY));
		expect(card.style.width).toBe(`${PROCESS_NODE_WIDTH}px`);
	});

	test("a card that matches is opaque and silent", async () => {
		stubApi({});
		const { container } = await renderCard(SERVICE);
		expect(
			screen.queryByText("Does not match the filter"),
		).not.toBeInTheDocument();
		const card = container.querySelector("[data-process-card]") as HTMLElement;
		expect(card.style.opacity).toBe("1");
		expect(card).toHaveAttribute("data-service-id", "svc-shop");
	});

	test("selection draws an outline", async () => {
		stubApi({});
		const { container } = await renderCard(SERVICE, { selected: true });
		const card = container.querySelector(
			"[data-process-card] > .surface-raised",
		) as HTMLElement;
		expect(card.style.outline).toContain("var(--color-accent-ink)");
	});

	test("an unselected card draws none", async () => {
		stubApi({});
		const { container } = await renderCard(SERVICE);
		const card = container.querySelector(
			"[data-process-card] > .surface-raised",
		) as HTMLElement;
		expect(card.style.outline).toBe("");
	});

	test("an unreachable server is flagged on the card", async () => {
		stubApi({});
		await renderCard({
			...SERVICE,
			server: {
				...(SERVICE.server as NonNullable<Service["server"]>),
				status: "offline",
			},
		});
		const chip = screen.getByTitle("Runs on hetzner-01");
		expect(chip.className).toContain("text-status-degraded-on");
	});

	test("shows the nebula.toml chip the environment reports for the service", async () => {
		stubApi({
			"GET /environments/:id/config-source": {
				body: make("EnvironmentConfigSource", {
					services: [
						make("EnvironmentConfigService", {
							slug: SERVICE.slug,
							state: "drift",
							changes: 1,
						}),
					],
				}),
			},
		});
		await renderCard(SERVICE);
		expect(await screen.findByText("edited here")).toBeInTheDocument();
	});
});

describe("ProcessCard jacks", () => {
	test("offers a link jack on each side, named after the service", async () => {
		stubApi({});
		const errors = spyOn(console, "error").mockImplementation(() => {});
		try {
			await renderCard(SERVICE);
			expect(
				document.querySelector('[data-handleid="left"]'),
			).toBeInTheDocument();
			expect(
				document.querySelector('[data-handleid="right"]'),
			).toBeInTheDocument();
			expect(document.querySelector('[data-handleid="top"]')).toBeNull();
		} finally {
			errors.mockRestore();
		}
	});

	test("an internet cable adds the top jack", async () => {
		stubApi({});
		const errors = spyOn(console, "error").mockImplementation(() => {});
		try {
			await renderCard(SERVICE, { plugs: { ...NO_PLUGS, top: true } });
			expect(
				document.querySelector('[data-handleid="top"]'),
			).toBeInTheDocument();
		} finally {
			errors.mockRestore();
		}
	});
});
