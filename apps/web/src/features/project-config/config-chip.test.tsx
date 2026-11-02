import { describe, expect, test } from "bun:test";
import type { QueryClient } from "@tanstack/react-query";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ServiceConfigSource } from "#/api/gen/types.gen";
import { type ApiStub, stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { NodeConfigChip, ServiceConfigChip } from "./config-chip";

/** Waits for the one read to land and the cache to go quiet. */
async function settled(queryClient: QueryClient, api: ApiStub) {
	await waitFor(() => expect(api.calls).toHaveLength(1));
	await waitFor(() => expect(queryClient.isFetching()).toBe(0));
}

function serviceSource(state: ServiceConfigSource["state"], managed = true) {
	return make("ServiceConfigSource", {
		managed,
		state,
		fields: [],
		pending: [],
	});
}

describe("ServiceConfigChip", () => {
	test.each<[NonNullable<ServiceConfigSource["state"]>, string]>([
		["file_changed", "nebula.toml differs"],
		["conflict", "nebula.toml differs"],
		["drift", "edited here"],
		["in_sync", "from nebula.toml"],
	])("a %s service wears the chip %p", async (state, label) => {
		stubApi({
			"GET /services/:id/config-source": { body: serviceSource(state) },
		});
		await renderWithProviders(<ServiceConfigChip serviceId="svc_1" />);
		expect(await screen.findByText(label)).toBeInTheDocument();
	});

	test("the sentence behind the chip is its tooltip", async () => {
		stubApi({
			"GET /services/:id/config-source": { body: serviceSource("drift") },
		});
		await renderWithProviders(<ServiceConfigChip serviceId="svc_1" />);
		await userEvent.setup().hover(await screen.findByText("edited here"));
		expect(
			await screen.findByText(/Changed in the dashboard since the last apply/),
		).toBeInTheDocument();
	});

	test("a service the file does not own wears nothing", async () => {
		const api = stubApi({
			"GET /services/:id/config-source": {
				body: serviceSource("in_sync", false),
			},
		});
		const { container, queryClient } = await renderWithProviders(
			<ServiceConfigChip serviceId="svc_1" />,
		);
		await settled(queryClient, api);
		expect(container).not.toHaveTextContent(/nebula\.toml|edited here/);
	});

	test("a service the file only plans to create wears nothing", async () => {
		const api = stubApi({
			// A function reply: "new" is not a state the contract lists for a
			// service, the chip must still wear nothing for it.
			"GET /services/:id/config-source": () =>
				Response.json({ ...serviceSource("in_sync"), state: "new" }),
		});
		const { container, queryClient } = await renderWithProviders(
			<ServiceConfigChip serviceId="svc_1" />,
		);
		await settled(queryClient, api);
		expect(container).not.toHaveTextContent(/nebula\.toml|edited here/);
	});
});

describe("NodeConfigChip", () => {
	const source = make("EnvironmentConfigSource", {
		mode: "explicit",
		status: "differs",
		services: [
			{ slug: "web", state: "file_changed", changes: 2 },
			{ slug: "api", state: "drift", changes: 1 },
			{ slug: "fresh", state: "new", changes: 4 },
		],
	});

	test("reads its own service's state out of the environment's one answer", async () => {
		const api = stubApi({
			"GET /environments/:id/config-source": { body: source },
		});
		await renderWithProviders(
			<>
				<NodeConfigChip environmentId="env_1" slug="web" />
				<NodeConfigChip environmentId="env_1" slug="api" />
			</>,
		);
		expect(await screen.findByText("nebula.toml differs")).toBeInTheDocument();
		expect(screen.getByText("edited here")).toBeInTheDocument();
		expect(api.calls).toHaveLength(1);
	});

	test("a service the file names but has not created yet wears nothing", async () => {
		const api = stubApi({
			"GET /environments/:id/config-source": { body: source },
		});
		const { container, queryClient } = await renderWithProviders(
			<NodeConfigChip environmentId="env_1" slug="fresh" />,
		);
		await settled(queryClient, api);
		expect(container).toBeEmptyDOMElement();
	});

	test("a service the answer does not list wears nothing", async () => {
		const api = stubApi({
			"GET /environments/:id/config-source": { body: source },
		});
		const { container, queryClient } = await renderWithProviders(
			<NodeConfigChip environmentId="env_1" slug="other" />,
		);
		await settled(queryClient, api);
		expect(container).toBeEmptyDOMElement();
	});
});
