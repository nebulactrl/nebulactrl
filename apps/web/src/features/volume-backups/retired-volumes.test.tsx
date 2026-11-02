import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { RetiredVolumeView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { RetiredVolumes } from "./retired-volumes";

function retired(over: Partial<RetiredVolumeView> = {}): RetiredVolumeView {
	return {
		id: "rv-1",
		serviceId: "svc-1",
		dockerName: "nebula-data-old-1",
		volumeName: "data",
		createdAt: "2026-10-30T10:00:00Z",
		reason: "restore into a new volume",
		...over,
	};
}

describe("RetiredVolumes", () => {
	test("renders nothing when no volume was retired", async () => {
		const api = stubApi({
			"GET /services/:serviceId/retired-volumes": { body: { items: [] } },
		});
		const { container } = await renderWithProviders(
			<RetiredVolumes serviceId="svc-1" />,
		);
		await waitFor(() => expect(api.calls).toHaveLength(1));
		expect(api.calls[0]?.path).toBe("/services/svc-1/retired-volumes");
		expect(container.querySelector("ul")).toBeNull();
		expect(screen.queryByText("Retired volumes")).toBeNull();
		expect(api.unmatched).toEqual([]);
	});

	test("renders nothing when the list is null", async () => {
		const api = stubApi({
			"GET /services/:serviceId/retired-volumes": { body: { items: null } },
		});
		await renderWithProviders(<RetiredVolumes serviceId="svc-1" />);
		await waitFor(() => expect(api.calls).toHaveLength(1));
		expect(screen.queryByText("Retired volumes")).toBeNull();
	});

	test("lists each retired volume with its docker name, volume and reason", async () => {
		stubApi({
			"GET /services/:serviceId/retired-volumes": {
				body: {
					items: [
						retired(),
						retired({
							id: "rv-2",
							dockerName: "nebula-logs-old-2",
							volumeName: "logs",
							reason: "",
						}),
					],
				},
			},
		});
		await renderWithProviders(<RetiredVolumes serviceId="svc-1" />);
		const list = await screen.findByRole("list", { name: "Retired volumes" });
		const rows = within(list).getAllByRole("listitem");
		expect(rows).toHaveLength(2);
		expect(rows[0]).toHaveTextContent("nebula-data-old-1");
		expect(rows[0]).toHaveTextContent("data");
		expect(rows[0]).toHaveTextContent("retired");
		expect(rows[0]).toHaveTextContent("restore into a new volume");
		expect(rows[1]).toHaveTextContent("nebula-logs-old-2");
		expect(rows[1]).not.toHaveTextContent("restore into a new volume");
	});

	test("deleting asks first, then DELETEs that volume and confirms with a toast", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"GET /services/:serviceId/retired-volumes": {
				body: {
					items: [
						retired(),
						retired({ id: "rv-2", dockerName: "nebula-logs-old-2" }),
					],
				},
			},
			"DELETE /retired-volumes/:retiredId": { status: 202 },
		});
		await renderWithProviders(<RetiredVolumes serviceId="svc-1" />);
		const rows = within(
			await screen.findByRole("list", { name: "Retired volumes" }),
		).getAllByRole("listitem");
		const second = rows[1];
		if (!second) throw new Error("second row missing");

		await user.click(within(second).getByRole("button", { name: /Delete/ }));
		const dialog = await screen.findByRole("alertdialog");
		expect(dialog).toHaveTextContent("Delete nebula-logs-old-2?");
		expect(dialog).toHaveTextContent("This cannot be undone");
		// Nothing is sent until the dialog is confirmed.
		expect(api.calls.filter((c) => c.method === "DELETE")).toEqual([]);

		await user.click(
			within(dialog).getByRole("button", { name: "Delete volume" }),
		);
		expect(await screen.findByText("Deleting the old volume")).toBeVisible();
		const deletes = api.calls.filter((c) => c.method === "DELETE");
		expect(deletes).toHaveLength(1);
		expect(deletes[0]?.path).toBe("/retired-volumes/rv-2");
		expect(api.unmatched).toEqual([]);
	});

	test("refetches the list after a delete so the row can disappear", async () => {
		const user = userEvent.setup();
		let deleted = false;
		const api = stubApi({
			"GET /services/:serviceId/retired-volumes": () =>
				Response.json({ items: deleted ? [] : [retired()] }),
			"DELETE /retired-volumes/:retiredId": () => {
				deleted = true;
				return new Response(null, { status: 202 });
			},
		});
		await renderWithProviders(<RetiredVolumes serviceId="svc-1" />);
		await user.click(await screen.findByRole("button", { name: /Delete/ }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Delete volume" }),
		);
		await waitFor(() =>
			expect(
				screen.queryByRole("list", { name: "Retired volumes" }),
			).toBeNull(),
		);
		expect(api.calls.filter((c) => c.method === "GET")).toHaveLength(2);
	});

	test("cancelling the confirmation sends nothing", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"GET /services/:serviceId/retired-volumes": {
				body: { items: [retired()] },
			},
		});
		await renderWithProviders(<RetiredVolumes serviceId="svc-1" />);
		await user.click(await screen.findByRole("button", { name: /Delete/ }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
		expect(api.calls.filter((c) => c.method === "DELETE")).toEqual([]);
		expect(screen.getByText("nebula-data-old-1")).toBeVisible();
	});

	test("a failed delete keeps the dialog open and shows the server's reason", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"GET /services/:serviceId/retired-volumes": {
				body: { items: [retired()] },
			},
			"DELETE /retired-volumes/:retiredId": problem(409, {
				title: "Conflict",
				detail: "The server is offline.",
			}),
		});
		await renderWithProviders(<RetiredVolumes serviceId="svc-1" />);
		await user.click(await screen.findByRole("button", { name: /Delete/ }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Delete volume" }),
		);
		expect(
			await screen.findByText("Could not delete the volume"),
		).toBeVisible();
		expect(screen.queryByText("Deleting the old volume")).toBeNull();
		expect(screen.getByRole("alertdialog")).toBeVisible();
		expect(api.calls.filter((c) => c.method === "DELETE")).toHaveLength(1);
	});
});
