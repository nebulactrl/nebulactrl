import { describe, expect, test } from "bun:test";
import { screen } from "@testing-library/react";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { BuiltOnChips, PlacementNotices } from "./deployment-placement";

const server = (id: string, name: string) => make("Server", { id, name });

describe("BuiltOnChips", () => {
	test("names both servers of a deployment built elsewhere", async () => {
		stubApi({
			"GET /servers": {
				body: make("PageServer", {
					items: [server("b", "build-01"), server("w", "web-02")],
				}),
			},
		});
		await renderWithProviders(<BuiltOnChips buildServerId="b" serverId="w" />);
		expect(
			await screen.findByRole("group", {
				name: "Built on build-01, deployed on web-02",
			}),
		).toBeInTheDocument();
		expect(screen.getByText("build-01")).toBeInTheDocument();
		expect(screen.getByText("web-02")).toBeInTheDocument();
	});

	test("shows nothing for an image that was only pulled", async () => {
		const api = stubApi({
			"GET /servers": { body: { items: [] } },
		});
		const { container } = await renderWithProviders(
			<BuiltOnChips buildServerId={null} serverId="w" />,
		);
		expect(container).not.toHaveTextContent("Built on");
		expect(screen.queryByRole("group")).not.toBeInTheDocument();
		expect(api.unmatched).toEqual([]);
	});

	test("shows nothing for a build on the target itself", async () => {
		stubApi({
			"GET /servers": {
				body: make("PageServer", { items: [server("w", "web-02")] }),
			},
		});
		await renderWithProviders(<BuiltOnChips buildServerId="w" serverId="w" />);
		expect(screen.queryByRole("group")).not.toBeInTheDocument();
	});

	test("names a build server that no longer exists as a removed server", async () => {
		stubApi({
			"GET /servers": {
				body: make("PageServer", { items: [server("w", "web-02")] }),
			},
		});
		await renderWithProviders(
			<BuiltOnChips buildServerId="gone" serverId="w" />,
		);
		expect(await screen.findByText("a removed server")).toBeInTheDocument();
		expect(screen.getByText("web-02")).toBeInTheDocument();
	});

	test("shows a placeholder for the deploy server while it is unknown", async () => {
		stubApi({
			"GET /servers": {
				body: make("PageServer", { items: [server("b", "build-01")] }),
			},
		});
		await renderWithProviders(
			<BuiltOnChips buildServerId="b" serverId={null} />,
		);
		expect(await screen.findByText("build-01")).toBeInTheDocument();
		expect(screen.getByText("Deployed on").nextSibling).toHaveTextContent("…");
	});
});

describe("PlacementNotices", () => {
	test("renders nothing without events", async () => {
		const { container } = await renderWithProviders(
			<PlacementNotices events={[]} />,
		);
		expect(container).toBeEmptyDOMElement();
	});

	test("titles each notice from its own words", async () => {
		await renderWithProviders(
			<PlacementNotices
				events={[
					{ id: 1, message: "Waiting for a build slot on build-01" },
					{ id: 2, message: "No build server available; building on web-02" },
					{ id: "x", message: "Picked build-01" },
				]}
			/>,
		);
		expect(screen.getByText("Waiting for a build slot")).toBeInTheDocument();
		expect(
			screen.getByText("Built on the app's server instead"),
		).toBeInTheDocument();
		expect(screen.getByText("Build placement")).toBeInTheDocument();
		expect(screen.getByText("Picked build-01")).toBeInTheDocument();
		expect(
			screen.getByText("Waiting for a build slot on build-01"),
		).toBeInTheDocument();
	});
});
