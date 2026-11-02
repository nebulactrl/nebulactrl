import { describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import { meQuery } from "#/api/hooks";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { meBody } from "./test-data";
import { useMeshAccess } from "./use-mesh-access";

function Probe() {
	const { canWrite, isAdmin } = useMeshAccess();
	return (
		<p>
			write:{String(canWrite)} admin:{String(isAdmin)}
		</p>
	);
}

async function accessFor(scopes: string[]) {
	stubApi({ "GET /me": { body: meBody(scopes) } });
	const { queryClient } = await renderWithProviders(<Probe />);
	// The answer has landed: a stale "false" would otherwise pass vacuously.
	await waitFor(() =>
		expect(queryClient.getQueryData(meQuery().queryKey)).toBeDefined(),
	);
}

describe("useMeshAccess", () => {
	test("can do nothing until the principal has loaded", async () => {
		stubApi({
			"GET /me": () => new Promise<Response>(() => {}),
		});
		await renderWithProviders(<Probe />);
		expect(screen.getByText("write:false admin:false")).toBeInTheDocument();
	});

	test("a principal without scopes is read-only", async () => {
		await accessFor([]);
		expect(
			await screen.findByText("write:false admin:false"),
		).toBeInTheDocument();
	});

	test("servers:write changes servers but not the access policy", async () => {
		await accessFor(["servers:write"]);
		expect(
			await screen.findByText("write:true admin:false"),
		).toBeInTheDocument();
	});

	test("admin may do both, even without servers:write", async () => {
		await accessFor(["admin"]);
		expect(
			await screen.findByText("write:true admin:true"),
		).toBeInTheDocument();
	});

	test("an unrelated scope grants nothing", async () => {
		await accessFor(["projects:read"]);
		expect(
			await screen.findByText("write:false admin:false"),
		).toBeInTheDocument();
	});
});
