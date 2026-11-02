import { afterEach, describe, expect, test } from "bun:test";
import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { useTemplatesState } from "./store";
import { TemplateOrigin } from "./template-origin";
import {
	contextRoutes,
	ENV_PATH,
	ENV_ROUTE,
	instance,
	resetTemplatesStore,
	summary,
} from "./test-fixtures";

afterEach(() => act(() => resetTemplatesStore()));

function Probe() {
	const { updating } = useTemplatesState();
	return <output aria-label="updating">{updating?.id ?? "none"}</output>;
}

const catalog = {
	categories: [],
	items: [summary({ id: "umami", name: "Umami" })],
};

function setup(stacks: ReturnType<typeof instance>[], serviceId = "svc1") {
	const api = stubApi({
		...contextRoutes(),
		"GET /environments/:environmentId/template-instances": { body: stacks },
		"GET /catalog/templates": { body: catalog },
	});
	const rendered = renderWithProviders(
		<>
			<TemplateOrigin serviceId={serviceId} />
			<Probe />
		</>,
		{ path: ENV_PATH, routePath: ENV_ROUTE },
	);
	return { api, rendered };
}

describe("TemplateOrigin", () => {
	test("names the template, stack, revision and version of a service installed from one", async () => {
		const { rendered } = setup([
			instance({
				id: "i1",
				name: "My blog",
				templateRevision: 4,
				templateVersion: "3.4.0",
			}),
		]);
		await rendered;
		expect(await screen.findByText("Umami")).toBeInTheDocument();
		expect(
			screen.getByText(/stack My blog · revision 4 · 3\.4\.0/),
		).toBeInTheDocument();
	});

	test("leaves the version out when the template has none", async () => {
		const { rendered } = setup([
			instance({ id: "i1", name: "Blog", templateRevision: 2 }),
		]);
		await rendered;
		const text = await screen.findByText(/stack Blog · revision 2/);
		expect(text.textContent).not.toMatch(/revision 2 ·/);
	});

	test("falls back to the template id while the catalog does not know it", async () => {
		const { rendered } = setup([instance({ id: "i1", templateId: "retired" })]);
		await rendered;
		expect(await screen.findByText("retired")).toBeInTheDocument();
	});

	test("renders nothing for a service that is in no stack, and does not fetch the catalog", async () => {
		const { api, rendered } = setup([instance({ id: "i1" })], "other-service");
		const { container } = await rendered;
		await waitFor(() =>
			expect(
				api.calls.some((c) => c.path.endsWith("/template-instances")),
			).toBe(true),
		);
		expect(container.querySelector("[data-slot=template-origin]")).toBeNull();
		expect(api.calls.some((c) => c.path === "/catalog/templates")).toBe(false);
	});

	test("offers the update only when a newer revision exists, and opens that stack's update", async () => {
		const user = userEvent.setup();
		const { rendered } = setup([instance({ id: "i9", updateAvailable: true })]);
		await rendered;
		await user.click(
			await screen.findByRole("button", { name: "Update available" }),
		);
		expect(screen.getByLabelText("updating")).toHaveTextContent("i9");
	});

	test("shows no update button for an up-to-date stack", async () => {
		const { rendered } = setup([
			instance({ id: "i1", updateAvailable: false }),
		]);
		await rendered;
		await screen.findByText("Umami");
		expect(
			screen.queryByRole("button", { name: "Update available" }),
		).not.toBeInTheDocument();
	});
});
