import { describe, expect, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import type { Environment, Project, ServiceView } from "#/api/gen/types.gen";
import { type ApiRoutes, stubApi } from "#/test/api";
import { make, type Overrides } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { PlacedServices } from "./placed-services";
import { useServicesOnServer } from "./use-services-on-server";

const project = (id: string, name: string) => make("Project", { id, name });
const environment = (id: string, slug: string) =>
	make("Environment", { id, slug, name: slug });

/** A service placed on `serverId`; the embedded server reference follows it. */
function wireService(
	overrides: Overrides<ServiceView> & {
		id: string;
		name: string;
		serverId: string;
	},
): ServiceView {
	return make("ServiceView", {
		...overrides,
		server: { id: overrides.serverId },
	});
}

interface Tree {
	projects: Project[];
	environments: Record<string, Environment[]>;
	services: Record<string, ServiceView[]>;
}

/** One answer per project (its environments) and per environment (its services). */
function treeRoutes(tree: Tree): ApiRoutes {
	const routes: ApiRoutes = {
		"GET /projects": { body: make("PageProject", { items: tree.projects }) },
	};
	for (const { id } of tree.projects) {
		const items = tree.environments[id] ?? [];
		routes[`GET /projects/${id}/environments`] = {
			body: make("PageEnvironment", { items }),
		};
		for (const environment of items) {
			routes[`GET /environments/${environment.id}/services`] = {
				body: make("PageServiceView", {
					items: tree.services[environment.id] ?? [],
				}),
			};
		}
	}
	return routes;
}

const shop: Tree = {
	projects: [project("p1", "Shop"), project("p2", "Blog")],
	environments: {
		p1: [environment("e1", "production")],
		p2: [environment("e2", "staging")],
	},
	services: {
		e1: [
			wireService({
				id: "svc-web",
				name: "web",
				serverId: "srv-1",
				sourceType: "git",
				config: { gitUrl: "https://github.com/acme/shop.git" },
				domains: [
					make("DomainView", { hostname: "shop.example.com" }),
					make("DomainView", { hostname: "www.shop.example.com" }),
				],
				desiredState: "stopped",
			}),
			wireService({ id: "svc-elsewhere", name: "cache", serverId: "srv-2" }),
		],
		e2: [
			wireService({ id: "svc-api", name: "api", serverId: "srv-1" }),
			wireService({
				id: "svc-worker",
				name: "worker",
				serverId: "srv-1",
				latestDeployment: { status: "failed" },
			}),
		],
	},
};

describe("PlacedServices", () => {
	test("lists the services placed on the server, sorted by name, each linking to its inspector", async () => {
		const api = stubApi(treeRoutes(shop));
		await renderWithProviders(<PlacedServices serverId="srv-1" />);
		await screen.findByRole("link", { name: "web" });

		const links = screen.getAllByRole("link").map((l) => l.textContent);
		expect(links).toEqual(["api", "web", "worker"]);
		expect(screen.getByRole("link", { name: "web" })).toHaveAttribute(
			"href",
			"/p/p1/production?service=svc-web",
		);
		expect(screen.getByRole("link", { name: "api" })).toHaveAttribute(
			"href",
			"/p/p2/staging?service=svc-api",
		);
		expect(screen.queryByText("cache")).not.toBeInTheDocument();
		expect(api.unmatched).toEqual([]);
	});

	test("a row names where it lives, its source, its domains and its state", async () => {
		stubApi(treeRoutes(shop));
		await renderWithProviders(<PlacedServices serverId="srv-1" />);
		const web = (await screen.findByRole("link", { name: "web" })).closest(
			"tr",
		);
		if (!web) throw new Error("no row for web");
		const row = within(web);
		expect(row.getByText("Shop / production")).toBeInTheDocument();
		expect(row.getByText("GitHub")).toBeInTheDocument();
		expect(
			row.getByText("shop.example.com, www.shop.example.com"),
		).toBeInTheDocument();
		expect(row.getByText("Stopped")).toBeInTheDocument();

		const api = within(
			screen.getByRole("link", { name: "api" }).closest("tr") as HTMLElement,
		);
		expect(api.getByText("Blog / staging")).toBeInTheDocument();
		expect(api.getByText("image")).toBeInTheDocument();
		expect(api.getByText("none")).toBeInTheDocument();

		const worker = within(
			screen.getByRole("link", { name: "worker" }).closest("tr") as HTMLElement,
		);
		expect(worker.getByText("Failed")).toBeInTheDocument();
	});

	test("says no services are placed here when the team has none on this server", async () => {
		const api = stubApi(treeRoutes(shop));
		await renderWithProviders(<PlacedServices serverId="srv-other" />);
		expect(
			await screen.findByText("No services on this server"),
		).toBeInTheDocument();
		expect(screen.queryByRole("table")).not.toBeInTheDocument();
		expect(api.unmatched).toEqual([]);
	});

	test("says no services are placed here when the team has no projects", async () => {
		stubApi(treeRoutes({ projects: [], environments: {}, services: {} }));
		await renderWithProviders(<PlacedServices serverId="srv-1" />);
		expect(
			await screen.findByText("No services on this server"),
		).toBeInTheDocument();
	});

	test("shows a placeholder, not the empty state, while the projects load", async () => {
		stubApi({ "GET /projects": () => new Promise<Response>(() => {}) });
		const { container } = await renderWithProviders(
			<PlacedServices serverId="srv-1" />,
		);
		expect(container.querySelector('[data-slot="skeleton"]')).not.toBeNull();
		expect(
			screen.queryByText("No services on this server"),
		).not.toBeInTheDocument();
	});
});

/** Prints what the hook returns, so a test reads it as text. */
function Probe({ serverId }: { serverId: string }) {
	const { placed, everywhere, pending } = useServicesOnServer(serverId);
	return (
		<>
			<output aria-label="pending">{String(pending)}</output>
			<output aria-label="placed">
				{placed
					.map(
						(p) => `${p.service.name}@${p.project.name}/${p.environment.slug}`,
					)
					.join(",")}
			</output>
			<output aria-label="everywhere">
				{everywhere.map((s) => s.name).join(",")}
			</output>
		</>
	);
}

describe("useServicesOnServer", () => {
	test("walks projects, environments and services; placed are this server's, sorted by name", async () => {
		stubApi(treeRoutes(shop));
		await renderWithProviders(<Probe serverId="srv-1" />);
		await screen.findByText(
			"api@Blog/staging,web@Shop/production,worker@Blog/staging",
		);
		expect(screen.getByLabelText("pending")).toHaveTextContent("false");
	});

	test("everywhere lists every service of the team, wherever it runs", async () => {
		stubApi(treeRoutes(shop));
		await renderWithProviders(<Probe serverId="srv-1" />);
		await screen.findByText("web@Shop/production", { exact: false });
		const all = screen
			.getByLabelText("everywhere")
			.textContent?.split(",")
			.sort();
		expect(all).toEqual(["api", "cache", "web", "worker"]);
	});

	test("is pending until the first answer, with nothing placed", async () => {
		stubApi({ "GET /projects": () => new Promise<Response>(() => {}) });
		await renderWithProviders(<Probe serverId="srv-1" />);
		expect(screen.getByLabelText("pending")).toHaveTextContent("true");
		expect(screen.getByLabelText("placed")).toBeEmptyDOMElement();
	});

	test("an environment with no services contributes nothing", async () => {
		stubApi(
			treeRoutes({
				projects: [project("p1", "Shop")],
				environments: { p1: [environment("e1", "production")] },
				services: {},
			}),
		);
		await renderWithProviders(<Probe serverId="srv-1" />);
		await screen.findByText("false");
		expect(screen.getByLabelText("placed")).toBeEmptyDOMElement();
		expect(screen.getByLabelText("everywhere")).toBeEmptyDOMElement();
	});
});
