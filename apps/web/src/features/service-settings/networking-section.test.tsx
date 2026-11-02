import { describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type { Service } from "#/api/models";
import { makeService } from "#/features/canvas/lib/fixtures";
import { newProcessDraft } from "#/features/processes/draft";
import { type ApiStub, stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { type SettingsDraft, toDraft } from "./draft";
import { NetworkingSection } from "./networking-section";
import { OUTBOUND_HELP } from "./outbound-copy";

type ProcessView = Service["processes"][number];

const proc = (name: string, port: number, isDefault = false): ProcessView => ({
	name,
	default: isDefault,
	port,
	command: [],
	singleton: false,
	pending: false,
	removed: false,
	stopGraceSeconds: 30,
	scaling: makeService().scaling,
	observed: {
		name,
		state: "running",
		health: "healthy",
		replicasDesired: 1,
		replicasReady: 1,
	},
});

const NETWORK_REF = {
	id: "net-1",
	name: "shared",
	serverId: "server-1",
	alias: "web.production.shared",
};

/** The queries the child sections issue; a test overrides what it cares about. */
function api(routes: Parameters<typeof stubApi>[0] = {}): ApiStub {
	return stubApi({
		"GET /environments/:environmentId/services": { body: { items: [] } },
		"GET /networks": { body: { items: [] } },
		...routes,
	});
}

let latest: SettingsDraft;

function Harness({
	service,
	errors = {},
	initial,
}: {
	service: Service;
	errors?: Parameters<typeof NetworkingSection>[0]["errors"];
	initial?: Partial<SettingsDraft>;
}) {
	const [draft, setDraft] = useState<SettingsDraft>({
		...toDraft(service),
		...initial,
	});
	latest = draft;
	return (
		<NetworkingSection
			service={service}
			draft={draft}
			errors={errors}
			set={(key, value) => setDraft((d) => ({ ...d, [key]: value }))}
		/>
	);
}

const generateButton = () =>
	screen.getByRole("button", { name: "Generate domain" });

describe("NetworkingSection port", () => {
	test("shows the draft port and stages what is typed", async () => {
		api();
		const user = userEvent.setup();
		await renderWithProviders(<Harness service={makeService()} />);
		const port = screen.getByLabelText("Port");
		expect(port).toHaveValue("80");
		await user.clear(port);
		await user.type(port, "8080");
		expect(port).toHaveValue("8080");
		expect(latest.port).toBe("8080");
	});

	test("an app without a port says empty means no HTTP", async () => {
		api();
		const service = makeService();
		service.config = { ...service.config };
		delete service.config.port;
		await renderWithProviders(<Harness service={service} />);
		expect(screen.getByLabelText("Port")).toHaveValue("");
		expect(screen.getByLabelText("Port")).toHaveAttribute(
			"placeholder",
			"none",
		);
		expect(
			screen.getByText("The port the app listens on. Empty means no HTTP."),
		).toBeInTheDocument();
	});

	test("shows the port problem the form found", async () => {
		api();
		await renderWithProviders(
			<Harness
				service={makeService()}
				errors={{ port: "Port must be between 1 and 65535" }}
			/>,
		);
		expect(screen.getByLabelText("Port")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
		expect(
			screen.getByText("Port must be between 1 and 65535"),
		).toBeInTheDocument();
	});

	test("a service with processes names the default process in the port label", async () => {
		api();
		const service = makeService({
			processes: [proc("web", 3000, true), proc("worker", 0)],
		});
		await renderWithProviders(
			<Harness service={service} initial={{ processName: "api" }} />,
		);
		expect(screen.getByLabelText("Port of api")).toBeInTheDocument();
		expect(
			screen.getByText(
				"The port the default process listens on. Empty means no HTTP.",
			),
		).toBeInTheDocument();
	});

	test("warns that each process needs its own port once another process has one", async () => {
		api();
		const service = makeService({
			processes: [proc("web", 3000, true), proc("worker", 9000)],
		});
		await renderWithProviders(
			<Harness
				service={service}
				initial={{
					processes: [{ ...newProcessDraft("worker"), port: "9000" }],
				}}
			/>,
		);
		expect(screen.getByLabelText("Port of web")).toBeInTheDocument();
		expect(
			screen.getByText(/Each process needs its own port\./),
		).toBeInTheDocument();
	});
});

describe("NetworkingSection generate domain", () => {
	test("without a port it is disabled and the domain list says why", async () => {
		const service = makeService();
		service.config = { ...service.config };
		delete service.config.port;
		const stub = api();
		await renderWithProviders(<Harness service={service} />);
		expect(generateButton()).toBeDisabled();
		expect(generateButton()).toHaveAttribute("title", "Set a port first");
		expect(
			screen.getByText("A service needs a port before it can have a domain."),
		).toBeInTheDocument();
		expect(stub.calls.some((c) => c.method === "POST")).toBe(false);
	});

	test("with a port it generates a domain and tells it is staged", async () => {
		const service = makeService({ id: "svc-gen" });
		const stub = api({
			"POST /services/:serviceId/domains/generate": {
				status: 201,
				body: make("DomainView"),
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<Harness service={service} />);
		expect(
			screen.getByText(
				"No domain yet. Add one, or generate one from the server's base domain.",
			),
		).toBeInTheDocument();
		expect(generateButton()).toHaveAttribute(
			"title",
			"Generate a hostname from the server's base domain",
		);
		await user.click(generateButton());
		expect(await screen.findByText("Domain generated")).toBeInTheDocument();
		expect(
			screen.getByText("Staged until the next deploy."),
		).toBeInTheDocument();
		const posts = stub.calls.filter((c) => c.method === "POST");
		expect(posts).toHaveLength(1);
		expect(posts[0]?.path).toBe("/services/svc-gen/domains/generate");
		// A single-process service does not name a process.
		expect(posts[0]?.query.has("process")).toBe(false);
		// The service lists are invalidated so the new domain shows up.
		await waitFor(() =>
			expect(
				stub.calls.filter(
					(c) => c.path === `/environments/${service.environmentId}/services`,
				).length,
			).toBeGreaterThan(1),
		);
	});

	test("a refusal from the control plane is shown under the domains", async () => {
		api({
			"POST /services/:serviceId/domains/generate": problem(409, {
				code: "no_base_domain",
				detail: "The server has no base domain",
			}),
		});
		const user = userEvent.setup();
		await renderWithProviders(<Harness service={makeService()} />);
		await user.click(generateButton());
		expect(
			await screen.findByText("The server has no base domain"),
		).toBeInTheDocument();
		expect(screen.queryByText("Domain generated")).toBeNull();
		expect(generateButton()).toBeEnabled();
	});

	test("with processes it names the serving process; the default one is sent as empty", async () => {
		const stub = api({
			"POST /services/:serviceId/domains/generate": {
				status: 201,
				body: make("DomainView"),
			},
		});
		const service = makeService({
			processes: [proc("web", 3000, true), proc("worker", 9000)],
		});
		const user = userEvent.setup();
		await renderWithProviders(<Harness service={service} />);
		await user.click(generateButton());
		await screen.findByText("Domain generated");
		const post = stub.calls.find((c) => c.method === "POST");
		expect(post?.query.get("process")).toBe("");
	});

	test("with only a non-default process on a port, that process serves the domain", async () => {
		const stub = api({
			"POST /services/:serviceId/domains/generate": {
				status: 201,
				body: make("DomainView"),
			},
		});
		const service = makeService({
			processes: [proc("web", 0, true), proc("worker", 9000)],
		});
		const user = userEvent.setup();
		await renderWithProviders(<Harness service={service} />);
		expect(generateButton()).toBeEnabled();
		await user.click(generateButton());
		await screen.findByText("Domain generated");
		expect(
			stub.calls.find((c) => c.method === "POST")?.query.get("process"),
		).toBe("worker");
	});

	test("with several ported processes and no default port, the reader must choose first", async () => {
		const stub = api();
		const service = makeService({
			processes: [
				proc("web", 0, true),
				proc("worker", 9000),
				proc("admin", 9100),
			],
		});
		await renderWithProviders(<Harness service={service} />);
		expect(generateButton()).toBeDisabled();
		expect(generateButton()).toHaveAttribute(
			"title",
			"Choose which process serves the domain",
		);
		expect(stub.calls.some((c) => c.method === "POST")).toBe(false);
	});

	test("with processes but none on a port it asks for a port first", async () => {
		api();
		const service = makeService({
			processes: [proc("web", 0, true), proc("worker", 0)],
		});
		await renderWithProviders(<Harness service={service} />);
		expect(generateButton()).toBeDisabled();
		expect(generateButton()).toHaveAttribute(
			"title",
			"Give a process a port first",
		);
		expect(
			screen.getByText("A service needs a port before it can have a domain."),
		).toBeInTheDocument();
	});
});

describe("NetworkingSection outbound internet", () => {
	const blockSwitch = () =>
		screen.getByRole("switch", { name: "Block outbound internet" });

	test("blocking is staged in the draft and the cue follows it", async () => {
		api();
		const user = userEvent.setup();
		await renderWithProviders(<Harness service={makeService()} />);
		expect(blockSwitch()).not.toBeChecked();
		expect(screen.getByText(OUTBOUND_HELP.off)).toBeInTheDocument();
		await user.click(blockSwitch());
		expect(blockSwitch()).toBeChecked();
		expect(latest.blockEgress).toBe(true);
		expect(screen.getByText(OUTBOUND_HELP.on)).toBeInTheDocument();
		await user.click(blockSwitch());
		expect(latest.blockEgress).toBe(false);
	});

	test("a service on a shared network cannot block until it is detached", async () => {
		api({ "GET /networks": { body: { items: [] } } });
		const service = makeService({ networks: [NETWORK_REF] });
		await renderWithProviders(<Harness service={service} />);
		expect(blockSwitch()).toHaveAttribute("aria-disabled", "true");
		expect(screen.getByText(OUTBOUND_HELP.needsDetach)).toBeInTheDocument();
	});

	test("a service that already blocks can switch it off even when on a network", async () => {
		api();
		const service = makeService({ networks: [NETWORK_REF] });
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness service={service} initial={{ blockEgress: true }} />,
		);
		expect(blockSwitch()).toBeChecked();
		expect(blockSwitch()).not.toHaveAttribute("aria-disabled", "true");
		expect(screen.queryByText(OUTBOUND_HELP.needsDetach)).toBeNull();
		await user.click(blockSwitch());
		expect(latest.blockEgress).toBe(false);
	});

	test("lists the shared networks of the server", async () => {
		api({
			"GET /networks": {
				body: {
					items: [
						{
							id: "net-1",
							name: "shared",
							dockerName: "nebula-shared",
							serverId: "server-1",
							serverName: "hetzner-01",
							createdAt: "2026-01-01T00:00:00Z",
							services: [],
						},
					],
				},
			},
		});
		await renderWithProviders(<Harness service={makeService()} />);
		expect(await screen.findByText("shared")).toBeInTheDocument();
	});

	test("a route service has neither the outbound switch nor shared networks", async () => {
		api();
		const route = makeService({ kind: "route" });
		await renderWithProviders(<Harness service={route} />);
		expect(screen.queryByText("Block outbound internet")).toBeNull();
		expect(screen.queryByText("Shared networks")).toBeNull();
	});
});

describe("NetworkingSection exposures and public port", () => {
	test("lists the TCP/UDP ports that expose this service", async () => {
		const service = makeService({ id: "svc-app" });
		const tcp = make("ServiceView", {
			id: "svc-route-tcp",
			kind: "route",
			name: "app-tcp",
			config: {
				route: {
					protocol: "tcp",
					listenPort: 2222,
					upstreams: [{ serviceId: "svc-app", port: 22 }],
				},
			},
		});
		const unrelated = make("ServiceView", {
			id: "svc-route-other",
			kind: "route",
			config: {
				route: {
					protocol: "udp",
					listenPort: 5000,
					upstreams: [{ serviceId: "svc-other", port: 5000 }],
				},
			},
		});
		api({
			"GET /environments/:environmentId/services": {
				body: make("PageServiceView", { items: [unrelated, tcp] }),
			},
		});
		await renderWithProviders(<Harness service={service} />);
		const list = await screen.findByRole("list", { name: "Port exposures" });
		expect(list).toHaveTextContent("TCP");
		expect(list).toHaveTextContent(":2222");
		expect(list).toHaveTextContent(":22");
		expect(list).not.toHaveTextContent(":5000");
	});

	test("shows the public port only for a managed database", async () => {
		api({
			"GET /services/:serviceId/database/public-port": problem(500, {
				code: "internal",
				detail: "public port unavailable",
			}),
		});
		const db = makeService({
			kind: "database",
			config: {
				...makeService().config,
				database: { engine: "postgres", version: "17" },
			},
		});
		await renderWithProviders(<Harness service={db} />);
		expect(screen.getByText("Public port")).toBeInTheDocument();
		expect(
			await screen.findByText("public port unavailable"),
		).toBeInTheDocument();
	});

	test("an app has no public port section", async () => {
		const stub = api();
		await renderWithProviders(<Harness service={makeService()} />);
		expect(screen.queryByText("Public port")).toBeNull();
		await screen.findByText("Expose a port (TCP/UDP)");
		expect(
			stub.calls.some((c) => c.path.endsWith("/database/public-port")),
		).toBe(false);
	});
});
