import { describe, expect, test } from "bun:test";
import { useLocation } from "@tanstack/react-router";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ReactFlowProvider } from "@xyflow/react";
import type { ReactNode } from "react";
import type { Service } from "#/api/models";
import { NO_PLUGS, type Plugs } from "#/features/canvas/lib/cable";
import { makeDeployment } from "#/features/canvas/lib/fixtures";
import { renderWithProviders } from "#/test/render";
import { StackCard } from "./stack-card";
import { COMPONENTS, makeObserved, makeStack, replica } from "./test-fixtures";

function Location() {
	const location = useLocation();
	return <output aria-label="search">{JSON.stringify(location.search)}</output>;
}

async function renderCard(
	service: Service,
	over: { plugs?: Plugs; selected?: boolean; dimmed?: boolean } = {},
) {
	const ui: ReactNode = (
		<ReactFlowProvider>
			<StackCard
				service={service}
				plugs={over.plugs ?? NO_PLUGS}
				selected={over.selected ?? false}
				dimmed={over.dimmed ?? false}
			/>
			<Location />
		</ReactFlowProvider>
	);
	return renderWithProviders(ui, { path: "/canvas" });
}

describe("StackCard", () => {
	test("shows the name and the number of components", async () => {
		await renderCard(makeStack());
		expect(screen.getByText("shop")).toBeInTheDocument();
		expect(screen.getByText("Compose · 3 components")).toBeInTheDocument();
	});

	test("a stack with one component reads in the singular", async () => {
		const [first] = COMPONENTS;
		if (!first) throw new Error("expected a fixture component");
		await renderCard(makeStack({ components: [first] }));
		expect(screen.getByText("Compose · 1 component")).toBeInTheDocument();
	});

	test("lists a row per component with its state, route and restarts", async () => {
		await renderCard(
			makeStack({
				domains: [
					{
						id: "d1",
						hostname: "shop.example.com",
						component: "web",
						targetPort: 80,
					} as Service["domains"][number],
				],
				observed: makeObserved({
					replicas: [
						replica(0, "db"),
						replica(1, "web", { health: "unhealthy", restartCount: 3 }),
					],
				}),
			}),
		);
		const list = screen.getByRole("list", { name: "Components of shop" });
		const rows = within(list).getAllByRole("button");
		expect(rows.map((row) => row.getAttribute("title"))).toEqual([
			"db: Healthy",
			"web: Unhealthy",
			"worker: Not started",
		]);
		expect(rows[0]).toHaveTextContent(":5432");
		expect(rows[1]).toHaveTextContent("shop.example.com");
		expect(rows[1]).toHaveTextContent("3↻ restarts");
		expect(rows[0]).not.toHaveTextContent("restarts");
		expect(rows[2]).toHaveTextContent("worker");
	});

	test("says so when the stack has no components yet", async () => {
		await renderCard(makeStack({ components: [] }));
		expect(screen.getByText("No components yet")).toBeInTheDocument();
		expect(screen.getByText("Compose · 0 components")).toBeInTheDocument();
	});

	test("folds components beyond the limit into '+N more', failed ones first", async () => {
		const many = Array.from({ length: 10 }, (_, i) => ({
			name: `c${i}`,
			index: i,
			image: "x",
			ports: [],
		}));
		await renderCard(
			makeStack({
				components: many,
				observed: makeObserved({
					replicas: [
						...many.map((component) =>
							replica(component.index, component.name),
						),
					].map((r) =>
						r.component === "c9"
							? { ...r, state: "exited", health: "none" }
							: r,
					),
				}),
			}),
		);
		const list = screen.getByRole("list", { name: "Components of shop" });
		expect(within(list).getAllByRole("button")).toHaveLength(8);
		expect(within(list).getByText("+2 more")).toBeInTheDocument();
		// c9 failed: it leads, so the problem is never behind "+N".
		expect(
			within(list).getByRole("button", { name: /c9/ }),
		).toBeInTheDocument();
	});

	test("clicking a component opens the inspector on its Overview", async () => {
		const user = userEvent.setup();
		await renderCard(makeStack({ id: "stack-9" }));
		await user.click(
			within(
				screen.getByRole("list", { name: "Components of shop" }),
			).getByRole("button", { name: /web/ }),
		);
		await waitFor(() =>
			expect(
				JSON.parse(screen.getByLabelText("search").textContent ?? ""),
			).toEqual({ service: "stack-9", tab: "overview", component: "web" }),
		);
	});

	test("a staged new stack wears a Staged chip that says it is not deployed", async () => {
		await renderCard(makeStack({ stagedKind: "new" }));
		expect(screen.getByText("Staged")).toHaveAttribute(
			"title",
			"New stack: not deployed yet",
		);
	});

	test("an edited stack's chip says changes wait to deploy", async () => {
		await renderCard(makeStack({ stagedKind: "edited" }));
		expect(screen.getByText("Staged")).toHaveAttribute(
			"title",
			"Staged changes waiting to deploy",
		);
	});

	test("no chip without staged changes", async () => {
		await renderCard(makeStack());
		expect(screen.queryByText("Staged")).not.toBeInTheDocument();
	});

	test("shows the aggregate state of the latest deployment", async () => {
		await renderCard(
			makeStack({
				latestDeployment: makeDeployment("building"),
			}),
		);
		expect(screen.getAllByText("Building").length).toBeGreaterThan(0);
	});

	test("names the server it runs on, or says there is none", async () => {
		const { unmount } = await renderCard(makeStack());
		expect(screen.getByText("hetzner-01")).toBeInTheDocument();
		expect(screen.getByTitle("Runs on hetzner-01")).toBeInTheDocument();
		unmount();
		await renderCard(makeStack({ server: null, serverId: null }));
		expect(screen.getByText("no server")).toBeInTheDocument();
		expect(screen.getByTitle("Not placed on a server")).toBeInTheDocument();
	});

	test("an unreachable server is flagged in the footer", async () => {
		await renderCard(
			makeStack({
				server: {
					id: "server-1",
					name: "hetzner-01",
					status: "offline",
					lastSeenAt: null,
				},
			}),
		);
		expect(screen.getByTitle("Runs on hetzner-01").className).toContain(
			"text-status-degraded-on",
		);
	});

	test("a dimmed card tells assistive tech it does not match the filter", async () => {
		await renderCard(makeStack(), { dimmed: true });
		expect(screen.getByText("Does not match the filter")).toBeInTheDocument();
	});

	test("a card that matches the filter has no such note", async () => {
		await renderCard(makeStack());
		expect(
			screen.queryByText("Does not match the filter"),
		).not.toBeInTheDocument();
	});

	test("offers link jacks on both sides; an internet jack only when routed", async () => {
		const { unmount } = await renderCard(makeStack());
		expect(
			screen.getByLabelText("Link jack on the left of shop"),
		).toBeInTheDocument();
		expect(
			screen.getByLabelText("Link jack on the right of shop"),
		).toBeInTheDocument();
		expect(
			screen.queryByLabelText("Internet jack on top of shop"),
		).not.toBeInTheDocument();
		unmount();
		await renderCard(makeStack(), { plugs: { ...NO_PLUGS, top: true } });
		expect(
			screen.getByLabelText("Internet jack on top of shop"),
		).toBeInTheDocument();
	});
});
