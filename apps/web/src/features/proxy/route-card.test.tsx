import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import { ReactFlowProvider } from "@xyflow/react";
import type { ServiceView } from "#/api/gen/types.gen";
import { toService } from "#/api/models";
import { NO_PLUGS, type Plugs } from "#/features/canvas/lib/cable";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { domainView, routeView, SERVER } from "./inspector/route-fixtures";
import { RouteCard } from "./route-card";

const SERVICES = "GET /environments/:environmentId/services";

function deployment(
	status: string,
	extra: Partial<ServiceView["latestDeployment"]> = {},
) {
	return {
		id: "dep-1",
		createdAt: new Date().toISOString(),
		currentStep: null,
		failedStep: null,
		finishedAt: new Date().toISOString(),
		message: null,
		releaseId: "",
		status,
		statusMessage: "",
		strategy: "route_only",
		trigger: "manual",
		...extra,
	};
}

async function renderCard(
	view: ServiceView,
	{
		plugs = NO_PLUGS,
		dimmed = false,
		selected = false,
		siblings = [view],
	}: {
		plugs?: Plugs;
		dimmed?: boolean;
		selected?: boolean;
		siblings?: ServiceView[];
	} = {},
) {
	const api = stubApi({
		[SERVICES]: { body: make("PageServiceView", { items: siblings }) },
	});
	const { queryClient } = await renderWithProviders(
		<ReactFlowProvider>
			<RouteCard
				service={toService(view)}
				plugs={plugs}
				selected={selected}
				dimmed={dimmed}
			/>
		</ReactFlowProvider>,
	);
	await waitFor(() => expect(queryClient.isFetching()).toBe(0));
	return api;
}

const pill = () =>
	screen.getByText((_, el) => el?.getAttribute("data-slot") === "status-pill");
const leds = () => screen.getByRole("img", { name: /Route / });

describe("RouteCard", () => {
	test("an undeployed http route shows its address, protocol, server and a waiting strip", async () => {
		const api = await renderCard(
			routeView({
				domains: [domainView()],
			}),
		);
		expect(screen.getByText("homeassistant")).toBeInTheDocument();
		expect(screen.getByText("192.168.1.20:8123")).toBeInTheDocument();
		expect(screen.getByText("HTTP")).toBeInTheDocument();
		expect(screen.getByText("app.example.com")).toBeInTheDocument();
		expect(screen.getByText("edge-1")).toBeInTheDocument();
		expect(pill()).toHaveTextContent("Not deployed");
		expect(leds()).toHaveAccessibleName("Route waiting, Verify waiting");
		expect(api.unmatched).toEqual([]);
	});

	test("counts further hostnames on the host chip", async () => {
		await renderCard(
			routeView({
				domains: [
					domainView({ id: "d1", hostname: "a.example.com" }),
					domainView({ id: "d2", hostname: "b.example.com" }),
					domainView({ id: "d3", hostname: "c.example.com" }),
				],
			}),
		);
		expect(screen.getByText("a.example.com +2")).toBeInTheDocument();
		expect(
			screen.getByTitle("Answers for a.example.com +2"),
		).toBeInTheDocument();
	});

	test("has no host chip without a hostname", async () => {
		await renderCard(routeView());
		expect(screen.queryByTitle(/^Answers for/)).toBeNull();
	});

	test("a serving route reads Live with both LEDs done", async () => {
		await renderCard(
			routeView({
				currentDeploymentId: "dep-1",
				latestDeployment: deployment("succeeded"),
			}),
		);
		expect(screen.getByText("Live")).toBeInTheDocument();
		expect(leds()).toHaveAccessibleName("Route done, Verify done");
	});

	test("a failed verify marks the Verify LED failed", async () => {
		await renderCard(
			routeView({
				latestDeployment: deployment("failed", { failedStep: "verify" }),
			}),
		);
		expect(screen.getByText("Failed")).toBeInTheDocument();
		expect(leds()).toHaveAccessibleName(/Verify failed/);
		expect(screen.getByText("Failed at Verify")).toBeInTheDocument();
	});

	test("a deploy under way shows its progress", async () => {
		await renderCard(
			routeView({
				latestDeployment: deployment("verifying", {
					finishedAt: null,
					currentStep: "verify",
				}),
			}),
		);
		expect(screen.getByText("Deploying")).toBeInTheDocument();
		expect(screen.getByText(/^Verifying/)).toBeInTheDocument();
		expect(leds()).toHaveAccessibleName("Route done, Verify running");
	});

	test("a tcp route is badged TCP with one LED and a port subtitle naming the target service", async () => {
		const target = routeView({ id: "svc-mc", name: "minecraft", kind: "app" });
		const route = routeView(
			{ id: "svc-tcp", name: "mc-public" },
			{
				protocol: "tcp",
				listenPort: 25565,
				upstreams: [{ serviceId: "svc-mc", port: 25565 }],
			},
		);
		await renderCard(route, { siblings: [target, route] });
		expect(screen.getByText("TCP")).toBeInTheDocument();
		expect(
			await screen.findByText("tcp :25565 -> minecraft:25565"),
		).toBeInTheDocument();
		expect(leds()).toHaveAccessibleName("Route waiting");
	});

	test("a udp route is badged UDP and has no host chip", async () => {
		await renderCard(
			routeView(
				{ domains: [domainView()] },
				{
					protocol: "udp",
					listenPort: 30000,
					upstreams: [{ address: "10.0.0.5:19132" }],
				},
			),
		);
		expect(screen.getByText("UDP")).toBeInTheDocument();
		expect(
			screen.getByText("udp :30000 -> 10.0.0.5:19132"),
		).toBeInTheDocument();
	});

	test("a new route is badged Staged: not deployed yet", async () => {
		await renderCard(routeView({ stagedKind: "new" }));
		expect(screen.getByTitle("New route: not deployed yet")).toHaveTextContent(
			"Staged",
		);
	});

	test("a route with staged edits is badged with what waits", async () => {
		await renderCard(routeView({ stagedKind: "edited" }));
		expect(
			screen.getByTitle("Staged changes waiting to deploy"),
		).toHaveTextContent("Staged");
	});

	test("shows no Staged badge for a clean route", async () => {
		await renderCard(routeView());
		expect(screen.queryByText("Staged")).toBeNull();
	});

	test("a route that is not placed says so", async () => {
		await renderCard(routeView({ serverId: "", server: null }));
		expect(screen.getByText("no server")).toBeInTheDocument();
		expect(screen.getByTitle("Not placed on a server")).toBeInTheDocument();
	});

	test("names the server that publishes the route", async () => {
		await renderCard(routeView());
		expect(screen.getByTitle("Published by edge-1")).toHaveTextContent(
			"edge-1",
		);
	});

	test("a serving route on an offline server reads Offline", async () => {
		await renderCard(
			routeView({
				currentDeploymentId: "dep-1",
				latestDeployment: deployment("succeeded"),
				server: { ...SERVER, status: "offline" },
			}),
		);
		expect(screen.getByText("Offline")).toBeInTheDocument();
	});

	test("flags a route whose server has not reported recently", async () => {
		await renderCard(routeView({ stale: true }));
		expect(
			screen.getByTitle(/No recent report from the server/),
		).toHaveTextContent("Stale");
	});

	test("no Stale marker when reports are fresh", async () => {
		await renderCard(routeView());
		expect(screen.queryByText("Stale")).toBeNull();
	});

	test("a stopped route reads Stopped", async () => {
		await renderCard(routeView({ desiredState: "stopped" }));
		expect(pill()).toHaveTextContent("Stopped");
	});

	test("tells screen readers when the filter dims the card", async () => {
		await renderCard(routeView(), { dimmed: true });
		expect(screen.getByText("Does not match the filter")).toBeInTheDocument();
	});

	test("does not mention the filter when the card is not dimmed", async () => {
		await renderCard(routeView());
		expect(screen.queryByText("Does not match the filter")).toBeNull();
	});

	test("offers link jacks on both sides, and an Internet jack only when plugged on top", async () => {
		await renderCard(routeView());
		expect(
			screen.getByLabelText("Link jack on the left of homeassistant"),
		).toBeInTheDocument();
		expect(
			screen.getByLabelText("Link jack on the right of homeassistant"),
		).toBeInTheDocument();
		expect(screen.queryByLabelText(/Internet jack/)).toBeNull();
	});

	test("shows the Internet jack when something reaches the route from the top", async () => {
		await renderCard(routeView(), {
			plugs: { left: false, right: false, top: true },
		});
		expect(
			screen.getByLabelText("Internet jack on top of homeassistant"),
		).toBeInTheDocument();
	});

	test("marks the card with its service and protocol for the canvas", async () => {
		await renderCard(routeView({ id: "svc-x" }, { protocol: "tcp" }));
		const card = document.querySelector('[data-service-id="svc-x"]');
		expect(card).toHaveAttribute("data-route-protocol", "tcp");
		expect(within(card as HTMLElement).getByText("TCP")).toBeInTheDocument();
	});
});
