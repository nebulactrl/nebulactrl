import { describe, expect, test } from "bun:test";
import { useQuery } from "@tanstack/react-query";
import { screen, waitFor } from "@testing-library/react";
import { meQuery } from "#/api/hooks";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders, testQueryClient } from "#/test/render";
import {
	invalidateRegistries,
	registriesQuery,
	registryImagesQuery,
	useRegistries,
} from "./queries";
import { wireRegistry } from "./test-data";

function me(scopes: string[]) {
	return make("MeBody", { principal: { scopes } });
}

function Names() {
	const registries = useRegistries();
	if (!registries.data) return <p>no data</p>;
	return <p>{registries.data.map((r) => r.name).join(",")}</p>;
}

function Observers() {
	useQuery(registriesQuery());
	useQuery(registryImagesQuery("reg-1", "live"));
	return null;
}

describe("useRegistries", () => {
	test("an admin gets the registry list", async () => {
		const api = stubApi({
			"GET /me": { body: me(["admin"]) },
			"GET /registries": { body: { items: [wireRegistry({ name: "Mine" })] } },
		});

		await renderWithProviders(<Names />);

		expect(await screen.findByText("Mine")).toBeInTheDocument();
		expect(api.unmatched).toEqual([]);
	});

	test("a caller without the admin scope never requests the list", async () => {
		const api = stubApi({
			"GET /me": { body: me(["read"]) },
			"GET /registries": { body: { items: [wireRegistry()] } },
		});
		const queryClient = testQueryClient();

		await renderWithProviders(<Names />, { queryClient });
		await waitFor(() =>
			expect(queryClient.getQueryState(meQuery().queryKey)?.status).toBe(
				"success",
			),
		);

		expect(screen.getByText("no data")).toBeInTheDocument();
		expect(api.calls.map((c) => c.path)).toEqual(["/me"]);
	});
});

describe("query keys and refresh", () => {
	test("the list is re-read every 15 seconds", () => {
		expect(registriesQuery().refetchInterval).toBe(15_000);
	});

	test("images are cached per registry and state", () => {
		expect(registryImagesQuery("a", "live").queryKey).not.toEqual(
			registryImagesQuery("a", "expired").queryKey,
		);
		expect(registryImagesQuery("a", "live").queryKey).not.toEqual(
			registryImagesQuery("b", "live").queryKey,
		);
	});

	test("invalidateRegistries refetches the list and the images on screen", async () => {
		let listCalls = 0;
		let imageCalls = 0;
		stubApi({
			"GET /registries": () => {
				listCalls += 1;
				return Response.json({ items: [] });
			},
			"GET /registries/:id/images": () => {
				imageCalls += 1;
				return Response.json({ items: [] });
			},
		});
		const queryClient = testQueryClient();
		await renderWithProviders(<Observers />, { queryClient });
		await waitFor(() => expect([listCalls, imageCalls]).toEqual([1, 1]));

		await invalidateRegistries(queryClient);

		expect([listCalls, imageCalls]).toEqual([2, 2]);
	});
});
