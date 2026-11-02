import { describe, expect, it } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { slice } from "./fixtures";
import { GIB, type TeamRow, teamRows } from "./model";
import { QuotaDialog } from "./quota-dialog";

function rowOf(over: Parameters<typeof slice>[0] = {}): TeamRow {
	const [row] = teamRows([slice(over)]);
	if (!row) throw new Error("no row");
	return row;
}

const field = () => screen.getByLabelText("Quota (GiB)");

describe("QuotaDialog", () => {
	it("renders nothing while no team is being edited", async () => {
		await renderWithProviders(
			<QuotaDialog
				row={null}
				defaultQuotaBytes={50 * GIB}
				onClose={() => {}}
			/>,
		);
		expect(screen.queryByRole("dialog")).toBeNull();
	});

	it("opens on the team's effective quota and names the default", async () => {
		await renderWithProviders(
			<QuotaDialog
				row={rowOf({ effectiveQuotaBytes: 20 * GIB })}
				defaultQuotaBytes={50 * GIB}
				onClose={() => {}}
			/>,
		);
		expect(
			await screen.findByRole("dialog", { name: "Backup quota for Acme" }),
		).toBeInTheDocument();
		expect(field()).toHaveValue("20");
		expect(screen.getByText(/The default is 50 GiB/)).toBeInTheDocument();
		// A team on the default has nothing to reset.
		expect(
			screen.queryByRole("button", { name: "Use the default" }),
		).toBeNull();
	});

	it("saves the new quota for the slice, confirms and closes", async () => {
		const user = userEvent.setup();
		let closed = 0;
		const api = stubApi({
			"PUT /admin/team-slices/:sliceId/quota": {
				status: 200,
				body: slice({ id: "slice-9", quotaBytes: 12.5 * GIB }),
			},
		});
		await renderWithProviders(
			<QuotaDialog
				row={rowOf({ id: "slice-9" })}
				defaultQuotaBytes={50 * GIB}
				onClose={() => {
					closed++;
				}}
			/>,
		);
		await user.clear(await screen.findByLabelText("Quota (GiB)"));
		await user.type(field(), "12.5");
		await user.click(screen.getByRole("button", { name: "Save quota" }));
		expect(await screen.findByText("Quota saved")).toBeInTheDocument();
		expect(screen.getByText("Acme")).toBeInTheDocument();
		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]).toMatchObject({
			method: "PUT",
			path: "/admin/team-slices/slice-9/quota",
			body: { quotaBytes: 12.5 * GIB },
		});
		expect(closed).toBe(1);
	});

	it("warns when the quota is below what the team stores", async () => {
		const user = userEvent.setup();
		await renderWithProviders(
			<QuotaDialog
				row={rowOf({ usedBytes: 30 * GIB })}
				defaultQuotaBytes={50 * GIB}
				onClose={() => {}}
			/>,
		);
		expect(screen.queryByText(/already stores/)).toBeNull();
		await user.clear(await screen.findByLabelText("Quota (GiB)"));
		await user.type(field(), "10");
		expect(
			screen.getByText(/The team already stores 30 GiB/),
		).toBeInTheDocument();
		await user.clear(field());
		await user.type(field(), "30");
		expect(screen.queryByText(/already stores/)).toBeNull();
	});

	it("rejects bad input locally and clears the complaint on edit", async () => {
		const user = userEvent.setup();
		const api = stubApi({});
		await renderWithProviders(
			<QuotaDialog
				row={rowOf()}
				defaultQuotaBytes={50 * GIB}
				onClose={() => {}}
			/>,
		);
		await user.clear(await screen.findByLabelText("Quota (GiB)"));
		await user.click(screen.getByRole("button", { name: "Save quota" }));
		expect(screen.getByText("Enter a size in GiB.")).toBeInTheDocument();
		expect(field()).toHaveAttribute("aria-invalid", "true");
		await user.type(field(), "0");
		expect(screen.queryByText("Enter a size in GiB.")).toBeNull();
		await user.click(screen.getByRole("button", { name: "Save quota" }));
		expect(
			screen.getByText("The smallest quota is 1 GiB."),
		).toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});

	it("shows the server's refusal and stays open", async () => {
		const user = userEvent.setup();
		let closed = 0;
		stubApi({
			"PUT /admin/team-slices/:sliceId/quota": problem(409, {
				detail: "Slice is being deleted.",
			}),
		});
		await renderWithProviders(
			<QuotaDialog
				row={rowOf()}
				defaultQuotaBytes={50 * GIB}
				onClose={() => {
					closed++;
				}}
			/>,
		);
		await screen.findByRole("dialog");
		await user.click(screen.getByRole("button", { name: "Save quota" }));
		expect(
			await screen.findByText("Slice is being deleted."),
		).toBeInTheDocument();
		expect(closed).toBe(0);
		expect(screen.getByRole("dialog")).toBeInTheDocument();
	});

	it("Use the default sends a null quota for a team with its own", async () => {
		const user = userEvent.setup();
		let closed = 0;
		const api = stubApi({
			"PUT /admin/team-slices/:sliceId/quota": {
				status: 200,
				body: slice({ id: "slice-3", quotaBytes: null }),
			},
		});
		await renderWithProviders(
			<QuotaDialog
				row={rowOf({ id: "slice-3", quotaBytes: 20 * GIB })}
				defaultQuotaBytes={50 * GIB}
				onClose={() => {
					closed++;
				}}
			/>,
		);
		await user.click(
			await screen.findByRole("button", { name: "Use the default" }),
		);
		expect(
			await screen.findByText("Back on the default quota"),
		).toBeInTheDocument();
		expect(api.calls[0]).toMatchObject({
			path: "/admin/team-slices/slice-3/quota",
			body: { quotaBytes: null },
		});
		await waitFor(() => expect(closed).toBe(1));
	});

	it("Cancel closes without a request", async () => {
		const user = userEvent.setup();
		let closed = 0;
		const api = stubApi({});
		await renderWithProviders(
			<QuotaDialog
				row={rowOf()}
				defaultQuotaBytes={50 * GIB}
				onClose={() => {
					closed++;
				}}
			/>,
		);
		await user.click(await screen.findByRole("button", { name: "Cancel" }));
		expect(closed).toBe(1);
		expect(api.calls).toEqual([]);
	});
});
