import { describe, expect, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import { renderWithProviders } from "#/test/render";
import type { MeshPlan } from "./model";
import { MeshBlockers, MeshPlanBlock } from "./plan-block";

function plan(overrides: Partial<MeshPlan> = {}): MeshPlan {
	return {
		crossServer: true,
		links: [],
		devicesAdded: 0,
		blockers: [],
		...overrides,
	};
}

const newLink = {
	fromServerId: "s-web",
	fromServerName: "web-02",
	toServerId: "s-db",
	toServerName: "db-01",
	purpose: "service" as const,
	state: "new",
	providers: ["postgres", "redis"],
};

describe("MeshPlanBlock", () => {
	test("renders nothing when no service crosses a server", async () => {
		await renderWithProviders(
			<MeshPlanBlock mesh={plan({ crossServer: false })} />,
		);
		expect(screen.queryByTestId("mesh-plan")).not.toBeInTheDocument();
	});

	test("renders nothing without a plan", async () => {
		await renderWithProviders(<MeshPlanBlock mesh={null} />);
		expect(screen.queryByTestId("mesh-plan")).not.toBeInTheDocument();
	});

	test("says how many devices a new link adds and lists the link with its providers", async () => {
		await renderWithProviders(
			<MeshPlanBlock mesh={plan({ devicesAdded: 1, links: [newLink] })} />,
		);
		const block = screen.getByRole("region", { name: "Mesh" });
		expect(
			within(block).getByText("Connect over the mesh: adds 1 device"),
		).toBeInTheDocument();
		expect(within(block).getByText("web-02 → db-01")).toBeInTheDocument();
		expect(within(block).getByText("services")).toBeInTheDocument();
		expect(within(block).getByText("postgres, redis")).toBeInTheDocument();
		expect(
			within(block).getByText("new link, adds a device"),
		).toBeInTheDocument();
	});

	test("an existing link shows its state instead of 'adds a device', and a replication link says replication", async () => {
		await renderWithProviders(
			<MeshPlanBlock
				mesh={plan({
					links: [
						{
							...newLink,
							state: "online",
							purpose: "replication",
							providers: [],
						},
					],
				})}
			/>,
		);
		const block = screen.getByTestId("mesh-plan");
		expect(
			within(block).getByText(
				"Connect over the mesh: uses links that already exist, adds no device",
			),
		).toBeInTheDocument();
		expect(within(block).getByText("replication")).toBeInTheDocument();
		expect(within(block).getByText("Online")).toBeInTheDocument();
		expect(
			within(block).queryByText("new link, adds a device"),
		).not.toBeInTheDocument();
	});

	test("a blocker changes the headline and links to the server that needs the mesh", async () => {
		await renderWithProviders(
			<MeshPlanBlock
				mesh={plan({
					blockers: [
						{
							code: "mesh_server_required",
							message: "db-01 is not in the mesh.",
							serverId: "s-db",
							serverName: "db-01",
						},
					],
				})}
			/>,
		);
		const block = screen.getByTestId("mesh-plan");
		expect(
			within(block).getByText("These servers cannot connect over the mesh yet"),
		).toBeInTheDocument();
		expect(
			within(block).getByText(/db-01 is not in the mesh\./),
		).toBeInTheDocument();
		expect(
			within(block).getByRole("link", { name: "Enable mesh on db-01" }),
		).toHaveAttribute("href", "/servers/s-db/settings/mesh");
	});
});

describe("MeshBlockers", () => {
	test("links a blocker without a server to the team's mesh settings", async () => {
		await renderWithProviders(
			<MeshBlockers
				blockers={[
					{ code: "mesh_not_configured", message: "No tailnet is connected." },
				]}
			/>,
		);
		expect(screen.getByText(/No tailnet is connected\./)).toBeInTheDocument();
		expect(
			screen.getByRole("link", {
				name: "Connect Tailscale or set up Headscale first",
			}),
		).toHaveAttribute("href", "/settings/server-mesh");
	});

	test("shows a blocker with no known way forward as plain text", async () => {
		await renderWithProviders(
			<MeshBlockers
				blockers={[{ code: "something_new", message: "Mesh says no." }]}
			/>,
		);
		expect(screen.getByText("Mesh says no.")).toBeInTheDocument();
		expect(screen.queryByRole("link")).not.toBeInTheDocument();
	});

	test("shows every blocker, even two for the same code on different servers", async () => {
		await renderWithProviders(
			<MeshBlockers
				blockers={[
					{
						code: "mesh_no_tun",
						message: "web-02 has no tun.",
						serverId: "s1",
					},
					{
						code: "mesh_no_tun",
						message: "db-01 has no tun.",
						serverId: "s2",
					},
				]}
			/>,
		);
		expect(screen.getByText(/web-02 has no tun\./)).toBeInTheDocument();
		expect(screen.getByText(/db-01 has no tun\./)).toBeInTheDocument();
		const links = screen.getAllByRole("link", {
			name: "See the server's mesh status",
		});
		expect(links.map((l) => l.getAttribute("href"))).toEqual([
			"/servers/s1/settings/mesh",
			"/servers/s2/settings/mesh",
		]);
	});
});
