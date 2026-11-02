import { afterEach, describe, expect, test } from "bun:test";
import { useQuery } from "@tanstack/react-query";
import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { DeleteStackDialog } from "./delete-stack-dialog";
import { instancesQuery } from "./queries";
import { templatesActions, useTemplatesState } from "./store";
import { holdToConfirm, instance, resetTemplatesStore } from "./test-fixtures";

afterEach(() => {
	act(() => {
		resetTemplatesStore();
	});
});

const stack = instance({
	id: "i1",
	name: "Umami",
	templateId: "umami",
	services: [
		{ id: "s1", key: "web", name: "Web", slug: "web" },
		{ id: "s2", key: "db", name: "DB", slug: "db" },
	],
});

function Probe() {
	const { deleting } = useTemplatesState();
	// Mounted like the canvas's stack list, to see it refetch after a delete.
	useQuery(instancesQuery("env1"));
	return <output aria-label="deleting">{deleting?.id ?? "none"}</output>;
}

const instancesRoute = {
	"GET /environments/:environmentId/template-instances": { body: [stack] },
};

const render = () =>
	renderWithProviders(
		<>
			<DeleteStackDialog />
			<Probe />
		</>,
	);

async function confirmTyped(user: ReturnType<typeof userEvent.setup>) {
	await user.type(
		await screen.findByLabelText(/Type umami to confirm/),
		"umami",
	);
	return holdToConfirm(/Hold to delete stack/i);
}

describe("DeleteStackDialog", () => {
	test("shows nothing until a delete is requested", async () => {
		stubApi(instancesRoute);
		await render();
		expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
	});

	test("names the stack and lists the services that go with it", async () => {
		stubApi(instancesRoute);
		await render();
		act(() => templatesActions.requestDelete(stack));
		expect(
			await screen.findByText("Delete the Umami stack?"),
		).toBeInTheDocument();
		expect(
			screen.getByText(/This stops and deletes 2 services \(Web, DB\)/),
		).toBeInTheDocument();
		expect(
			screen.getByRole("checkbox", { name: /Also delete volumes/ }),
		).not.toBeChecked();
	});

	test("a one-service stack is not pluralised", async () => {
		stubApi(instancesRoute);
		await render();
		act(() =>
			templatesActions.requestDelete({
				...stack,
				services: [{ id: "s1", key: "web", name: "Web", slug: "web" }],
			}),
		);
		expect(
			await screen.findByText(/This stops and deletes 1 service \(Web\)/),
		).toBeInTheDocument();
	});

	test("a stack with no service list still opens", async () => {
		stubApi(instancesRoute);
		await render();
		act(() => templatesActions.requestDelete({ ...stack, services: null }));
		expect(
			await screen.findByText(/This stops and deletes 0 services/),
		).toBeInTheDocument();
	});

	test("holding before the template id is typed deletes nothing", async () => {
		const api = stubApi(instancesRoute);
		await render();
		act(() => templatesActions.requestDelete(stack));
		await screen.findByText("Delete the Umami stack?");
		// the hold is refused: it never starts, so no timer is armed
		expect(holdToConfirm(/Hold to delete stack/i)).toBe(false);
		expect(api.calls.some((c) => c.method === "DELETE")).toBe(false);
		expect(screen.getByLabelText("deleting")).toHaveTextContent("i1");
	});

	test("typing the template id and holding deletes the stack, keeping its volumes by default", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			...instancesRoute,
			"DELETE /template-instances/:instanceId": { status: 204 },
		});
		await render();
		act(() => templatesActions.requestDelete(stack));
		expect(await confirmTyped(user)).toBe(true);
		expect(
			await screen.findByText("Deleted the Umami stack"),
		).toBeInTheDocument();
		expect(
			screen.getByText(/the volumes were kept on the server/),
		).toBeInTheDocument();
		const call = api.calls.find((c) => c.method === "DELETE");
		expect(call?.path).toBe("/template-instances/i1");
		expect(call?.query.get("confirm")).toBe("umami");
		expect(call?.query.get("deleteVolumes")).toBe("false");
		await waitFor(() =>
			expect(screen.getByLabelText("deleting")).toHaveTextContent("none"),
		);
		expect(
			screen.queryByText("Delete the Umami stack?"),
		).not.toBeInTheDocument();
	});

	test("ticking the volumes box deletes the volumes too and says so", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			...instancesRoute,
			"DELETE /template-instances/:instanceId": { status: 204 },
		});
		await render();
		act(() => templatesActions.requestDelete(stack));
		await user.click(
			await screen.findByRole("checkbox", { name: /Also delete volumes/ }),
		);
		expect(await confirmTyped(user)).toBe(true);
		expect(
			await screen.findByText("Its services and volumes were removed."),
		).toBeInTheDocument();
		expect(
			api.calls.find((c) => c.method === "DELETE")?.query.get("deleteVolumes"),
		).toBe("true");
	});

	test("refreshes the stack list after a delete", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			...instancesRoute,
			"DELETE /template-instances/:instanceId": { status: 204 },
		});
		await render();
		await waitFor(() =>
			expect(api.calls.filter((c) => c.method === "GET")).toHaveLength(1),
		);
		act(() => templatesActions.requestDelete(stack));
		await confirmTyped(user);
		await waitFor(() =>
			expect(
				api.calls.filter(
					(c) => c.method === "GET" && c.path.endsWith("/template-instances"),
				).length,
			).toBeGreaterThan(1),
		);
	});

	test("a refused delete shows the server's reason and keeps the request open", async () => {
		const user = userEvent.setup();
		stubApi({
			...instancesRoute,
			"DELETE /template-instances/:instanceId": {
				status: 409,
				body: {
					status: 409,
					title: "x",
					detail: "a deployment is running",
					code: "conflict",
				},
			},
		});
		await render();
		act(() => templatesActions.requestDelete(stack));
		await confirmTyped(user);
		expect(
			await screen.findByText("a deployment is running"),
		).toBeInTheDocument();
		expect(screen.getByLabelText("deleting")).toHaveTextContent("i1");
		expect(
			screen.queryByText("Deleted the Umami stack"),
		).not.toBeInTheDocument();
	});

	test("cancelling closes the request and the next one starts with the volumes box clear", async () => {
		const user = userEvent.setup();
		stubApi(instancesRoute);
		await render();
		act(() => templatesActions.requestDelete(stack));
		await user.click(
			await screen.findByRole("checkbox", { name: /Also delete volumes/ }),
		);
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.getByLabelText("deleting")).toHaveTextContent("none"),
		);
		act(() => templatesActions.requestDelete(stack));
		expect(
			await screen.findByRole("checkbox", { name: /Also delete volumes/ }),
		).not.toBeChecked();
	});
});
