import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import type { ServiceCardModel } from "./lib";
import { ServicePlanCard } from "./service-plan-card";

function card(over: Partial<ServiceCardModel> = {}): ServiceCardModel {
	return {
		key: "web",
		name: "Web",
		kind: "app",
		database: null,
		image: null,
		httpPort: null,
		ports: [],
		volumes: 0,
		oneshot: false,
		memoryMb: null,
		...over,
	};
}

const renderCard = (model: ServiceCardModel) =>
	render(
		<ul>
			<ServicePlanCard card={model} />
		</ul>,
	);

describe("ServicePlanCard", () => {
	test("shows the name, the image without registry host or digest, and the http port", () => {
		renderCard(
			card({
				name: "Umami",
				image: "ghcr.io/umami-software/umami:postgresql-latest@sha256:abc",
				httpPort: 3000,
			}),
		);
		expect(screen.getByText("Umami")).toBeInTheDocument();
		expect(
			screen.getByText("umami-software/umami:postgresql-latest"),
		).toBeInTheDocument();
		expect(screen.getByText("http :3000")).toBeInTheDocument();
	});

	test.each([
		["docker.io/library/redis:7", "library/redis:7"],
		["quay.io/org/app:1", "org/app:1"],
		["registry.example.com/app:1", "registry.example.com/app:1"],
	])("trims only a well-known registry host: %s", (image, shown) => {
		renderCard(card({ image }));
		expect(screen.getByText(shown)).toBeInTheDocument();
	});

	test("a managed database names its engine and says it has its own volume", () => {
		renderCard(
			card({
				name: "DB",
				kind: "database",
				database: "postgres 17",
				volumes: 1,
			}),
		);
		expect(screen.getByText("PostgreSQL 17")).toBeInTheDocument();
		expect(
			screen.getByText("Managed database on its own volume"),
		).toBeInTheDocument();
		expect(screen.getByText("1 volume")).toBeInTheDocument();
	});

	test("a card with neither image nor database says there is no image", () => {
		renderCard(card());
		expect(screen.getByText("No image")).toBeInTheDocument();
	});

	test("a one-shot job is badged, and a worker is badged as a worker", () => {
		const { unmount } = renderCard(card({ oneshot: true, kind: "worker" }));
		expect(screen.getByText("One-shot job")).toBeInTheDocument();
		expect(screen.queryByText("Worker")).not.toBeInTheDocument();
		unmount();
		renderCard(card({ kind: "worker" }));
		expect(screen.getByText("Worker")).toBeInTheDocument();
		expect(screen.queryByText("One-shot job")).not.toBeInTheDocument();
	});

	test("lists only the published ports and pluralises volumes", () => {
		renderCard(
			card({
				ports: [
					{ port: 25565, protocol: "tcp", publish: true },
					{ port: 19132, protocol: "udp", publish: true },
					{ port: 9000, protocol: "tcp", publish: false },
				],
				volumes: 2,
			}),
		);
		expect(screen.getByText("tcp/25565 · udp/19132")).toBeInTheDocument();
		expect(screen.queryByText(/9000/)).not.toBeInTheDocument();
		expect(screen.getByText("2 volumes")).toBeInTheDocument();
	});
});
