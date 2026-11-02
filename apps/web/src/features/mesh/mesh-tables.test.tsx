import { describe, expect, mock, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithProviders } from "#/test/render";
import { LinksTable, NodesTable, RulesTable } from "./mesh-tables";
import { link, node, rule } from "./test-data";

function rowOf(text: string): HTMLElement {
	const row = screen.getByText(text).closest("tr");
	if (!row) throw new Error(`no row for ${text}`);
	return row;
}

describe("NodesTable", () => {
	test("lists each server with its state, address, peers, shares and devices", async () => {
		await renderWithProviders(
			<NodesTable
				nodes={[
					node({
						serverName: "web-01",
						serverId: "s-web",
						devices: 3,
						ips: ["100.64.0.1", "fd7a::1"],
						exposures: [
							{
								meshPort: 15432,
								purpose: "service",
								serviceId: "svc-pg",
								serviceName: "postgres",
							},
						],
						peers: [
							{ direct: true, ips: [], name: "db-01", online: true },
							{ direct: false, ips: [], name: "cache-01", online: true },
						],
					}),
				]}
			/>,
		);
		const row = within(rowOf("web-01"));
		expect(row.getByRole("link", { name: "web-01" })).toHaveAttribute(
			"href",
			"/servers/s-web/settings/mesh",
		);
		expect(row.getByText("Online")).toBeInTheDocument();
		expect(row.getByText("100.64.0.1 fd7a::1")).toBeInTheDocument();
		// A trailing dot of the tailnet name is not shown.
		expect(row.getByText("web-01.tail1234.ts.net")).toBeInTheDocument();
		expect(
			row.getByRole("button", { name: "Copy web-01 mesh address" }),
		).toBeInTheDocument();
		expect(
			row.getByText("2 peers online · 1 direct, 1 relayed"),
		).toBeInTheDocument();
		expect(row.getByText("postgres:15432")).toBeInTheDocument();
		expect(row.getByText("3")).toBeInTheDocument();
	});

	test("a node with nothing yet says so instead of showing empty cells", async () => {
		await renderWithProviders(
			<NodesTable
				nodes={[
					node({
						serverName: "db-01",
						serverId: "s-db",
						ips: null,
						fqdn: "",
						exposures: null,
						peers: null,
						needsReprovision: true,
						state: "needs_attention",
					}),
				]}
			/>,
		);
		const row = within(rowOf("db-01"));
		expect(row.getByText("Not assigned yet")).toBeInTheDocument();
		expect(row.queryByRole("button", { name: /Copy/ })).not.toBeInTheDocument();
		expect(row.getByText("Nothing")).toBeInTheDocument();
		expect(row.getByText("No peers")).toBeInTheDocument();
		expect(row.getByText("Needs attention")).toBeInTheDocument();
		expect(row.getByText("Needs re-provision")).toBeInTheDocument();
	});

	test("an online node last seen over a minute ago reads Unknown, not Online", async () => {
		await renderWithProviders(
			<NodesTable
				nodes={[
					node({
						serverName: "old-01",
						lastSeenAt: new Date(Date.now() - 10 * 60_000).toISOString(),
					}),
				]}
			/>,
		);
		const row = within(rowOf("old-01"));
		expect(row.getByText("Unknown")).toBeInTheDocument();
		expect(row.queryByText("Online")).not.toBeInTheDocument();
	});

	test("a node with a name but no address offers to copy the name", async () => {
		await renderWithProviders(
			<NodesTable nodes={[node({ serverName: "name-01", ips: [] })]} />,
		);
		const row = within(rowOf("name-01"));
		expect(row.getByText("web-01.tail1234.ts.net")).toBeInTheDocument();
		expect(
			row.getByRole("button", { name: "Copy name-01 mesh address" }),
		).toBeInTheDocument();
	});
});

describe("LinksTable", () => {
	test("shows environment, path, purpose, state and what the link may reach", async () => {
		await renderWithProviders(
			<LinksTable
				links={[
					link({
						grants: [
							{
								exposureId: "x1",
								meshPort: 15432,
								providerSlug: "postgres",
								refId: "r1",
								refLabel: "DATABASE_URL",
								source: "reference",
							},
							{
								exposureId: "x2",
								meshPort: 16379,
								providerSlug: "redis",
								refId: "r2",
								refLabel: "",
								source: "replica",
							},
						],
					}),
				]}
				canWrite
				reprovisioning={null}
				onReprovision={() => {}}
			/>,
		);
		const row = within(rowOf("production"));
		expect(row.getByText("shop")).toBeInTheDocument();
		expect(row.getByText("web-02 → db-01")).toBeInTheDocument();
		expect(row.getByText("Services")).toBeInTheDocument();
		expect(row.getByText("Online")).toBeInTheDocument();
		expect(
			row.getByText("postgres:15432 by a reference in DATABASE_URL"),
		).toBeInTheDocument();
		expect(
			row.getByText("redis:16379 for replica of this database"),
		).toBeInTheDocument();
	});

	test("a link with no grants says it may reach nothing yet; replication is named", async () => {
		await renderWithProviders(
			<LinksTable
				links={[
					link({ purpose: "replication", grants: null, projectName: "" }),
				]}
				canWrite={false}
				reprovisioning={null}
				onReprovision={() => {}}
			/>,
		);
		const row = within(rowOf("production"));
		expect(row.getByText("Nothing yet")).toBeInTheDocument();
		expect(row.getByText("Replication")).toBeInTheDocument();
	});

	test("Re-provision hands the link to the caller", async () => {
		const onReprovision = mock((_link: unknown) => {});
		const target = link({ id: "link-9" });
		const user = userEvent.setup();
		await renderWithProviders(
			<LinksTable
				links={[target]}
				canWrite
				reprovisioning={null}
				onReprovision={onReprovision}
			/>,
		);
		await user.click(screen.getByRole("button", { name: "Re-provision" }));
		expect(onReprovision).toHaveBeenCalledTimes(1);
		expect(onReprovision.mock.calls[0]?.[0]).toEqual(target);
	});

	test("without write access there is no Re-provision button", async () => {
		await renderWithProviders(
			<LinksTable
				links={[link()]}
				canWrite={false}
				reprovisioning={null}
				onReprovision={() => {}}
			/>,
		);
		expect(
			screen.queryByRole("button", { name: "Re-provision" }),
		).not.toBeInTheDocument();
	});

	test("only the link being re-provisioned is disabled", async () => {
		await renderWithProviders(
			<LinksTable
				links={[
					link({ id: "a", environmentName: "staging" }),
					link({ id: "b", environmentName: "production" }),
				]}
				canWrite
				reprovisioning="b"
				onReprovision={() => {}}
			/>,
		);
		expect(
			within(rowOf("production")).getByRole("button", { name: "Re-provision" }),
		).toBeDisabled();
		expect(
			within(rowOf("staging")).getByRole("button", { name: "Re-provision" }),
		).toBeEnabled();
	});
});

describe("RulesTable", () => {
	test("names the link, the target address and the reason of each rule", async () => {
		await renderWithProviders(<RulesTable rules={[rule()]} links={[link()]} />);
		const row = within(rowOf("production · web-02 → db-01"));
		expect(row.getByText("100.64.0.7:15432")).toBeInTheDocument();
		expect(row.getByText("postgres is referenced by api")).toBeInTheDocument();
	});

	test("a rule whose link is gone falls back to its source address, and a missing reason to a generic one", async () => {
		await renderWithProviders(
			<RulesTable
				rules={[rule({ linkId: "gone", src: "100.64.0.99", because: "" })]}
				links={[link()]}
			/>,
		);
		const row = within(rowOf("100.64.0.99"));
		expect(row.getByText("A grant of this link")).toBeInTheDocument();
	});
});
