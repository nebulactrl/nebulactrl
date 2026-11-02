import { describe, expect, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import type {
	ProxyL4Port,
	ProxyRoute,
	ProxyRouteHost,
} from "#/api/gen/types.gen";
import { renderWithProviders } from "#/test/render";
import { L4PortsTable, RoutesTable } from "./routes-table";

const host = (over: Partial<ProxyRouteHost> = {}): ProxyRouteHost => ({
	host: "app.example.com",
	pathPrefix: "",
	kind: "serve",
	tlsMode: "auto",
	accessList: false,
	basicAuth: false,
	rateLimit: false,
	...over,
});

const route = (over: Partial<ProxyRoute> = {}): ProxyRoute => ({
	serviceId: "svc-1",
	serviceName: "web",
	kind: "service",
	protocol: "http",
	hosts: [host()],
	upstreams: ["10.0.0.5:3000"],
	allowCidrs: null,
	denyCidrs: null,
	...over,
});

const port = (over: Partial<ProxyL4Port> = {}): ProxyL4Port => ({
	port: 5432,
	protocol: "tcp",
	serviceId: "svc-db",
	serviceName: "postgres",
	deployed: true,
	...over,
});

/** The body row for a service, so assertions stay inside one row. */
function rowOf(name: string) {
	return screen.getByRole("row", { name: new RegExp(name) });
}

describe("RoutesTable", () => {
	test("says nothing is routed when there are no routes", async () => {
		await renderWithProviders(<RoutesTable routes={[]} />);
		expect(
			screen.getByText("Nothing is routed through this proxy yet."),
		).toBeInTheDocument();
		expect(screen.queryByRole("table")).not.toBeInTheDocument();
	});

	test("shows an http route's name, protocol, host and upstreams", async () => {
		await renderWithProviders(
			<RoutesTable
				routes={[route({ upstreams: ["10.0.0.5:3000", "10.0.0.6:3000"] })]}
			/>,
		);
		expect(
			screen.getAllByRole("columnheader").map((h) => h.textContent),
		).toEqual(["Route", "Listens on", "Forwards to", "Policy"]);
		const row = rowOf("web");
		expect(within(row).getByText("HTTP")).toBeInTheDocument();
		expect(within(row).getByText("app.example.com")).toBeInTheDocument();
		expect(
			within(row).getByText("10.0.0.5:3000, 10.0.0.6:3000"),
		).toBeInTheDocument();
		expect(within(row).queryByText("external")).not.toBeInTheDocument();
	});

	test("appends the path prefix and counts the extra hosts", async () => {
		await renderWithProviders(
			<RoutesTable
				routes={[
					route({
						hosts: [
							host({ host: "a.example.com", pathPrefix: "/api" }),
							host({ host: "b.example.com" }),
							host({ host: "c.example.com" }),
						],
					}),
				]}
			/>,
		);
		expect(
			within(rowOf("web")).getByText("a.example.com/api +2"),
		).toBeInTheDocument();
	});

	test("shows a single prefixed host without a count", async () => {
		await renderWithProviders(
			<RoutesTable
				routes={[route({ hosts: [host({ pathPrefix: "/v1" })] })]}
			/>,
		);
		expect(
			within(rowOf("web")).getByText("app.example.com/v1"),
		).toBeInTheDocument();
	});

	test("an http route without hosts reads 'no hostname', and no upstreams read as a dash", async () => {
		await renderWithProviders(
			<RoutesTable routes={[route({ hosts: null, upstreams: null })]} />,
		);
		const row = rowOf("web");
		expect(within(row).getByText("no hostname")).toBeInTheDocument();
		expect(within(row).getByText("—")).toBeInTheDocument();
	});

	test("an external route is badged and warns when nothing protects it", async () => {
		await renderWithProviders(
			<RoutesTable
				routes={[route({ serviceName: "legacy", kind: "external" })]}
			/>,
		);
		const row = rowOf("legacy");
		expect(within(row).getByText("external")).toBeInTheDocument();
		expect(within(row).getByText("no access control")).toBeInTheDocument();
	});

	test.each([
		["basic auth", { basicAuth: true }],
		["IP allowlist", { accessList: true }],
	] as const)(
		"an external route with %s has no 'no access control' warning",
		async (_name, over) => {
			await renderWithProviders(
				<RoutesTable
					routes={[
						route({
							serviceName: "legacy",
							kind: "external",
							hosts: [host(over)],
						}),
					]}
				/>,
			);
			expect(
				within(rowOf("legacy")).queryByText("no access control"),
			).not.toBeInTheDocument();
		},
	);

	test("a plain service route carries no policy chips", async () => {
		await renderWithProviders(<RoutesTable routes={[route()]} />);
		const policy = within(rowOf("web")).getAllByRole("cell")[3];
		expect(policy?.textContent).toBe("");
	});

	test("lists each host policy once as a chip", async () => {
		await renderWithProviders(
			<RoutesTable
				routes={[
					route({
						hosts: [
							host({
								host: "a.example.com",
								basicAuth: true,
								accessList: true,
								rateLimit: true,
								tlsMode: "custom",
							}),
							host({
								host: "b.example.com",
								basicAuth: true,
								tlsMode: "none",
							}),
						],
					}),
				]}
			/>,
		);
		const row = rowOf("web");
		expect(within(row).getAllByText("basic auth")).toHaveLength(1);
		expect(within(row).getByText("IP list")).toBeInTheDocument();
		expect(within(row).getByText("rate limit")).toBeInTheDocument();
		expect(within(row).getByText("tls custom")).toBeInTheDocument();
		expect(within(row).getByText("tls none")).toBeInTheDocument();
	});

	test("a redirect host shows where it redirects, or '?' when unset", async () => {
		await renderWithProviders(
			<RoutesTable
				routes={[
					route({
						serviceName: "redir",
						hosts: [
							host({
								host: "old.example.com",
								kind: "redirect",
								redirectTo: "https://new.example.com",
							}),
							host({ host: "older.example.com", kind: "redirect" }),
						],
					}),
				]}
			/>,
		);
		const row = rowOf("redir");
		expect(within(row).getByText("→ https://new.example.com")).toBeVisible();
		expect(within(row).getByText("→ ?")).toBeVisible();
	});

	test("a tcp route listens on its port and is open when no CIDR is allowed", async () => {
		await renderWithProviders(
			<RoutesTable
				routes={[
					route({
						serviceName: "db",
						protocol: "tcp",
						listenPort: 5432,
						hosts: null,
					}),
				]}
			/>,
		);
		const row = rowOf("db");
		expect(within(row).getByText("TCP")).toBeInTheDocument();
		expect(within(row).getByText("tcp :5432")).toBeInTheDocument();
		expect(within(row).getByText("open to the internet")).toBeInTheDocument();
		expect(within(row).queryByText(/denied/)).not.toBeInTheDocument();
	});

	test("a tcp route with CIDR filters counts allowed and denied", async () => {
		await renderWithProviders(
			<RoutesTable
				routes={[
					route({
						serviceName: "db",
						protocol: "tcp",
						listenPort: 5432,
						hosts: null,
						allowCidrs: ["10.0.0.0/8", "192.168.0.0/16"],
						denyCidrs: ["10.1.0.0/16"],
					}),
				]}
			/>,
		);
		const row = rowOf("db");
		expect(within(row).getByText("2 allowed")).toBeInTheDocument();
		expect(within(row).getByText("1 denied")).toBeInTheDocument();
		expect(
			within(row).queryByText("open to the internet"),
		).not.toBeInTheDocument();
	});

	test("a udp route cannot filter by IP, so it never shows allowed or denied counts", async () => {
		await renderWithProviders(
			<RoutesTable
				routes={[
					route({
						serviceName: "dns",
						protocol: "udp",
						listenPort: 53,
						hosts: null,
						allowCidrs: ["10.0.0.0/8"],
						denyCidrs: ["10.1.0.0/16"],
					}),
				]}
			/>,
		);
		const row = rowOf("dns");
		expect(within(row).getByText("UDP")).toBeInTheDocument();
		expect(within(row).getByText("udp :53")).toBeInTheDocument();
		expect(within(row).getByText("no IP filter")).toBeInTheDocument();
		expect(within(row).queryByText(/allowed|denied/)).not.toBeInTheDocument();
	});

	test("a tcp route without a listen port shows a question mark", async () => {
		await renderWithProviders(
			<RoutesTable
				routes={[route({ serviceName: "db", protocol: "tcp", hosts: null })]}
			/>,
		);
		expect(within(rowOf("db")).getByText("tcp :?")).toBeInTheDocument();
	});

	test("renders one row per route", async () => {
		await renderWithProviders(
			<RoutesTable
				routes={[
					route({ serviceId: "a", serviceName: "alpha" }),
					route({ serviceId: "b", serviceName: "beta" }),
				]}
			/>,
		);
		expect(screen.getAllByRole("row")).toHaveLength(3);
		expect(rowOf("alpha")).toBeInTheDocument();
		expect(rowOf("beta")).toBeInTheDocument();
	});
});

describe("L4PortsTable", () => {
	test("points at Create when no port is published", async () => {
		await renderWithProviders(<L4PortsTable ports={[]} />);
		expect(
			screen.getByText(
				"No TCP or UDP ports are published. Add one from Create → TCP/UDP port.",
			),
		).toBeInTheDocument();
		expect(screen.queryByRole("table")).not.toBeInTheDocument();
	});

	test("lists protocol, port, owner and whether the proxy listens", async () => {
		await renderWithProviders(
			<L4PortsTable
				ports={[
					port(),
					port({
						port: 53,
						protocol: "udp",
						serviceName: "dns",
						deployed: false,
					}),
				]}
			/>,
		);
		expect(
			screen.getAllByRole("columnheader").map((h) => h.textContent),
		).toEqual(["Protocol", "Port", "Service", "State"]);
		const tcp = screen.getByRole("row", { name: /postgres/ });
		expect(within(tcp).getByText("TCP")).toBeInTheDocument();
		expect(within(tcp).getByText("5432")).toBeInTheDocument();
		expect(within(tcp).getByText("Listening")).toBeInTheDocument();
		const udp = screen.getByRole("row", { name: /dns/ });
		expect(within(udp).getByText("UDP")).toBeInTheDocument();
		expect(within(udp).getByText("53")).toBeInTheDocument();
		expect(within(udp).getByText("Reserved")).toBeInTheDocument();
		expect(within(udp).queryByText("Listening")).not.toBeInTheDocument();
	});

	test("a reserved port explains when it opens", async () => {
		await renderWithProviders(
			<L4PortsTable ports={[port({ deployed: false })]} />,
		);
		expect(screen.getByText("Reserved")).toHaveAttribute(
			"title",
			"Reserved; the proxy opens it when the route is deployed",
		);
	});

	test("a port held by a service outside this project says 'another project'", async () => {
		await renderWithProviders(
			<L4PortsTable ports={[port({ serviceName: "" })]} />,
		);
		expect(screen.getByText("another project")).toBeInTheDocument();
	});
});
