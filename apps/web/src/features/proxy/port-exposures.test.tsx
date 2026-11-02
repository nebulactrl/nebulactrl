import { afterEach, describe, expect, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ServiceView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { routeCreate, useRouteCreateRequest } from "./create-store";
import { routeService, routeView } from "./inspector/route-fixtures";
import { PortExposures } from "./port-exposures";

const SERVICE_ID = "svc-web";

function appService(overrides: Parameters<typeof routeService>[0] = {}) {
	return routeService({
		id: SERVICE_ID,
		name: "minecraft",
		kind: "app",
		sourceType: "image",
		...overrides,
	});
}

/** A tcp/udp route service publishing a port of the app. */
function exposure(
	id: string,
	route: {
		protocol: "tcp" | "udp";
		listenPort: number;
		targetPort: number;
		allowCidrs?: string[];
		openToInternet?: boolean;
	},
	overrides: Parameters<typeof routeView>[0] = {},
) {
	return routeView(
		{ id, name: `${route.protocol}-${route.listenPort}`, ...overrides },
		{
			protocol: route.protocol,
			listenPort: route.listenPort,
			allowCidrs: route.allowCidrs ?? [],
			openToInternet: route.openToInternet ?? false,
			upstreams: [{ serviceId: SERVICE_ID, port: route.targetPort }],
		},
	);
}

const SERVICES = "GET /environments/:environmentId/services";

/** A 200 reply listing `items` as the environment's services. */
const services = (...items: ServiceView[]) => ({
	body: make("PageServiceView", { items }),
});

function CreateRequest() {
	const request = useRouteCreateRequest();
	return <output aria-label="create request">{JSON.stringify(request)}</output>;
}

afterEach(() => routeCreate.close());

describe("PortExposures", () => {
	test("renders nothing for a route service and does not fetch its siblings", async () => {
		const api = stubApi({});
		const { container } = await renderWithProviders(
			<PortExposures service={routeService()} />,
		);
		expect(container).toBeEmptyDOMElement();
		expect(api.calls).toEqual([]);
		expect(api.unmatched).toEqual([]);
	});

	test("explains an empty list and still offers to expose a port", async () => {
		const api = stubApi({ [SERVICES]: services() });
		await renderWithProviders(<PortExposures service={appService()} />);
		expect(
			await screen.findByText(/Nothing exposes a port of this service/),
		).toBeInTheDocument();
		expect(screen.queryByRole("list", { name: "Port exposures" })).toBeNull();
		expect(screen.getByRole("button", { name: /Expose a port/ })).toBeEnabled();
		expect(api.unmatched).toEqual([]);
	});

	test("lists the exposures of this service by listen port, with protocol, target and access", async () => {
		stubApi({
			[SERVICES]: services(
				exposure("r-udp", {
					protocol: "udp",
					listenPort: 30000,
					targetPort: 19132,
				}),
				exposure("r-tcp", {
					protocol: "tcp",
					listenPort: 25565,
					targetPort: 25565,
					allowCidrs: ["10.0.0.0/8"],
				}),
				// Another service's exposure, and a plain http route: not listed.
				routeView(
					{ id: "r-other", name: "other" },
					{
						protocol: "tcp",
						listenPort: 2222,
						upstreams: [{ serviceId: "svc-other", port: 22 }],
					},
				),
				routeView({ id: "r-http" }),
			),
		});
		await renderWithProviders(<PortExposures service={appService()} />);
		const list = await screen.findByRole("list", { name: "Port exposures" });
		const rows = within(list).getAllByRole("listitem");
		expect(rows).toHaveLength(2);
		expect(rows[0]).toHaveTextContent("TCP");
		expect(rows[0]).toHaveTextContent(":25565 → :25565");
		expect(rows[0]).toHaveTextContent("Allow 10.0.0.0/8");
		expect(rows[1]).toHaveTextContent("UDP");
		expect(rows[1]).toHaveTextContent(":30000 → :19132");
		expect(rows[1]).toHaveTextContent("No IP filter (UDP)");
	});

	test("marks an exposure with staged changes", async () => {
		stubApi({
			[SERVICES]: services(
				exposure(
					"r-1",
					{ protocol: "tcp", listenPort: 25565, targetPort: 25565 },
					{ hasStagedChanges: true },
				),
				exposure("r-2", {
					protocol: "tcp",
					listenPort: 25566,
					targetPort: 25565,
				}),
			),
		});
		await renderWithProviders(<PortExposures service={appService()} />);
		const rows = within(
			await screen.findByRole("list", { name: "Port exposures" }),
		).getAllByRole("listitem");
		expect(rows[0]).toHaveTextContent("Staged");
		expect(rows[1]).not.toHaveTextContent("Staged");
	});

	test("opens the create dialog targeting this service and its configured port", async () => {
		stubApi({ [SERVICES]: services() });
		const service = appService();
		await renderWithProviders(
			<>
				<PortExposures
					service={{ ...service, config: { ...service.config, port: 25565 } }}
				/>
				<CreateRequest />
			</>,
		);
		expect(screen.getByLabelText("create request")).toHaveTextContent("null");
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: /Expose a port/ }));
		expect(
			JSON.parse(screen.getByLabelText("create request").textContent),
		).toEqual({
			mode: "port",
			target: { serviceId: SERVICE_ID, port: 25565 },
		});
	});

	test("leaves the port out of the target when the service has none", async () => {
		stubApi({ [SERVICES]: services() });
		const service = appService();
		await renderWithProviders(
			<>
				<PortExposures
					service={{ ...service, config: { ...service.config, port: 0 } }}
				/>
				<CreateRequest />
			</>,
		);
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: /Expose a port/ }));
		expect(
			JSON.parse(screen.getByLabelText("create request").textContent),
		).toEqual({
			mode: "port",
			target: { serviceId: SERVICE_ID },
		});
	});

	test("disables Expose a port until the service is on a server", async () => {
		stubApi({ [SERVICES]: services() });
		await renderWithProviders(
			<>
				<PortExposures service={appService({ serverId: "", server: null })} />
				<CreateRequest />
			</>,
		);
		const button = screen.getByRole("button", { name: /Expose a port/ });
		expect(button).toBeDisabled();
		expect(button).toHaveAttribute(
			"title",
			"Place the service on a server first",
		);
		await userEvent.setup().click(button);
		expect(screen.getByLabelText("create request")).toHaveTextContent("null");
	});

	test("removing an exposure asks first, then deletes its route and reports it", async () => {
		const api = stubApi({
			[SERVICES]: services(
				exposure("r-1", {
					protocol: "tcp",
					listenPort: 25565,
					targetPort: 25565,
				}),
			),
			"DELETE /routes/:routeId": { status: 204 },
		});
		const user = userEvent.setup();
		await renderWithProviders(<PortExposures service={appService()} />);
		await user.click(
			await screen.findByRole("button", { name: "Remove tcp port 25565" }),
		);
		expect(
			await screen.findByText("Stop exposing TCP 25565?"),
		).toBeInTheDocument();
		expect(screen.getByText(/tcp-25565 is deleted/)).toBeInTheDocument();
		expect(api.calls.some((call) => call.method === "DELETE")).toBe(false);

		await user.click(screen.getByRole("button", { name: "Remove exposure" }));
		expect(
			await screen.findByText("Port exposure removed"),
		).toBeInTheDocument();
		expect(
			api.calls.filter((call) => call.method === "DELETE").map((c) => c.path),
		).toEqual(["/routes/r-1"]);
		expect(api.unmatched).toEqual([]);
	});

	test("cancelling the confirmation deletes nothing", async () => {
		const api = stubApi({
			[SERVICES]: services(
				exposure("r-1", {
					protocol: "udp",
					listenPort: 30000,
					targetPort: 19132,
				}),
			),
		});
		const user = userEvent.setup();
		await renderWithProviders(<PortExposures service={appService()} />);
		await user.click(
			await screen.findByRole("button", { name: "Remove udp port 30000" }),
		);
		await user.click(await screen.findByRole("button", { name: "Cancel" }));
		expect(api.calls.every((call) => call.method === "GET")).toBe(true);
		expect(api.unmatched).toEqual([]);
	});

	test("a failed delete shows the server's reason and keeps the exposure listed", async () => {
		stubApi({
			[SERVICES]: services(
				exposure("r-1", {
					protocol: "tcp",
					listenPort: 25565,
					targetPort: 25565,
				}),
			),
			"DELETE /routes/:routeId": problem(409, {
				code: "route_busy",
				detail: "A deploy is running.",
			}),
		});
		const user = userEvent.setup();
		await renderWithProviders(<PortExposures service={appService()} />);
		await user.click(
			await screen.findByRole("button", { name: "Remove tcp port 25565" }),
		);
		await user.click(
			await screen.findByRole("button", { name: "Remove exposure" }),
		);
		expect(await screen.findByText("Could not remove it")).toBeInTheDocument();
		// The dialog stays open with the reason, and the exposure is still there.
		expect(screen.getAllByText("A deploy is running.").length).toBeGreaterThan(
			0,
		);
		expect(
			screen.getByRole("list", { name: "Port exposures", hidden: true }),
		).toBeInTheDocument();
	});
});
