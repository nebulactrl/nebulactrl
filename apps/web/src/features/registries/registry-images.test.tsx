import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { RegistryImages } from "./registry-images";
import { wireImage } from "./test-data";

const DIGEST = `sha256:${"0a1b2c3d4e5f".padEnd(64, "6")}`;

describe("RegistryImages", () => {
	test("lists live images with a shortened digest, platform and size", async () => {
		const api = stubApi({
			"GET /registries/:id/images": {
				body: {
					items: [
						wireImage({ sizeBytes: 5 * 1024 * 1024, digest: DIGEST }),
						wireImage({
							id: "img-2",
							repository: "ghcr.io/acme/shop-web",
							platform: "linux/arm64",
						}),
					],
				},
			},
		});

		await renderWithProviders(<RegistryImages registryId="reg-1" />);

		const apiRow = (await screen.findByText("ghcr.io/acme/shop-api")).closest(
			"tr",
		) as HTMLElement;
		expect(within(apiRow).getByText("sha256:0a1b2c3d4e5f")).toHaveAttribute(
			"title",
			DIGEST,
		);
		expect(within(apiRow).getByText("linux/amd64")).toBeInTheDocument();
		expect(within(apiRow).getByText("5 MiB")).toBeInTheDocument();
		const webRow = screen.getByText("ghcr.io/acme/shop-web").closest("tr");
		expect(
			within(webRow as HTMLElement).getByText("linux/arm64"),
		).toBeVisible();
		// An image with no recorded size shows a dash, not 0 B.
		expect(within(webRow as HTMLElement).getByText("—")).toBeInTheDocument();
		expect(api.calls[0]?.query.get("state")).toBe("live");
		expect(screen.getByText("Images a rollback can still pull.")).toBeVisible();
	});

	test("an empty live list says nothing has been pushed yet", async () => {
		stubApi({ "GET /registries/:id/images": { body: { items: [] } } });

		await renderWithProviders(<RegistryImages registryId="reg-1" />);

		expect(
			await screen.findByText(/Nothing has been pushed yet/),
		).toBeInTheDocument();
		expect(screen.queryByRole("table")).not.toBeInTheDocument();
	});

	test("choosing Expired re-queries that state and shows its note and empty text", async () => {
		const api = stubApi({
			"GET /registries/:id/images": (request) => {
				const state = new URL(request.url).searchParams.get("state");
				return Response.json({
					items:
						state === "live"
							? [wireImage()]
							: state === "expired"
								? []
								: [wireImage({ id: "img-d", repository: "ghcr.io/acme/old" })],
				});
			},
		});
		const user = userEvent.setup();

		await renderWithProviders(<RegistryImages registryId="reg-1" />);
		await screen.findByText("ghcr.io/acme/shop-api");

		await user.click(screen.getByRole("button", { name: "Expired" }));

		expect(await screen.findByText("No expired images.")).toBeInTheDocument();
		expect(
			screen.getByText(/Past the retention window; removed from the registry/),
		).toBeInTheDocument();
		expect(screen.queryByText("ghcr.io/acme/shop-api")).not.toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: "Deleted" }));

		expect(await screen.findByText("ghcr.io/acme/old")).toBeInTheDocument();
		expect(screen.getByText("Removed from the registry.")).toBeInTheDocument();
		expect(api.calls.map((c) => c.query.get("state"))).toEqual([
			"live",
			"expired",
			"deleted",
		]);
	});

	test("a failed load shows the server's reason", async () => {
		stubApi({
			"GET /registries/:id/images": {
				status: 502,
				body: {
					status: 502,
					code: "registry_unreachable",
					detail: "registry is down",
				},
			},
		});

		await renderWithProviders(<RegistryImages registryId="reg-1" />);

		await waitFor(() =>
			expect(
				screen.getByText(/The images did not load: registry is down/),
			).toBeInTheDocument(),
		);
	});
});
