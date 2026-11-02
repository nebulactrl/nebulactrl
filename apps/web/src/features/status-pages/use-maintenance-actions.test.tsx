import { describe, expect, it } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { MaintenanceWindowView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { windowView } from "./sp-pages-fixtures";
import { useMaintenanceActions } from "./use-maintenance-actions";

function Harness({ window }: { window: MaintenanceWindowView }) {
	const actions = useMaintenanceActions();
	return (
		<div>
			<button type="button" onClick={() => actions.onEdit(window)}>
				edit
			</button>
			<button type="button" onClick={() => actions.onCancel(window)}>
				cancel
			</button>
			<button type="button" onClick={() => actions.onDelete(window)}>
				delete
			</button>
			<button type="button" onClick={actions.closeEditor}>
				close
			</button>
			<output aria-label="editing">{actions.editing?.title ?? "none"}</output>
		</div>
	);
}

const REFRESH = {
	"GET /status-pages": { body: { items: [] } },
};

describe("useMaintenanceActions", () => {
	it("opens a window for editing and closes the editor again", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Harness window={windowView()} />);
		expect(screen.getByLabelText("editing")).toHaveTextContent("none");
		await user.click(screen.getByRole("button", { name: "edit" }));
		expect(screen.getByLabelText("editing")).toHaveTextContent(
			"Database upgrade",
		);
		await user.click(screen.getByRole("button", { name: "close" }));
		expect(screen.getByLabelText("editing")).toHaveTextContent("none");
	});

	it("cancels an upcoming window after confirming, then says so", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"POST /maintenance-windows/:id/cancel": { body: windowView() },
			...REFRESH,
		});
		await renderWithProviders(<Harness window={windowView()} />);
		await user.click(screen.getByRole("button", { name: "cancel" }));

		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("Cancel “Database upgrade”?"),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText(/The window will not start/),
		).toBeInTheDocument();
		// Nothing is sent until the user confirms.
		expect(api.calls.filter((c) => c.method === "POST")).toEqual([]);

		await user.click(
			within(dialog).getByRole("button", { name: "Cancel maintenance" }),
		);
		expect(await screen.findByText("Maintenance canceled")).toBeInTheDocument();
		const post = api.calls.find((c) => c.method === "POST");
		expect(post?.path).toBe("/maintenance-windows/win-1/cancel");
		expect(api.unmatched).toEqual([]);
	});

	it("words the confirmation as ending for a window in progress", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"POST /maintenance-windows/:id/cancel": { body: windowView() },
			...REFRESH,
		});
		await renderWithProviders(
			<Harness window={windowView({ phase: "active" })} />,
		);
		await user.click(screen.getByRole("button", { name: "cancel" }));
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("End “Database upgrade” now?"),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText(/Its services leave maintenance right away/),
		).toBeInTheDocument();
		await user.click(
			within(dialog).getByRole("button", { name: "End maintenance" }),
		);
		expect(await screen.findByText("Maintenance ended")).toBeInTheDocument();
		expect(api.calls.some((c) => c.path.endsWith("/cancel"))).toBe(true);
	});

	it("sends nothing when the user keeps the window", async () => {
		const user = userEvent.setup();
		const api = stubApi({});
		await renderWithProviders(<Harness window={windowView()} />);
		await user.click(screen.getByRole("button", { name: "cancel" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Keep it" }));
		await waitFor(() =>
			expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
		);
		expect(api.calls).toEqual([]);
	});

	it("shows the server's reason in the dialog when cancelling fails", async () => {
		const user = userEvent.setup();
		stubApi({
			"POST /maintenance-windows/:id/cancel": {
				status: 409,
				body: { status: 409, code: "conflict", detail: "Already ended." },
			},
		});
		await renderWithProviders(<Harness window={windowView()} />);
		await user.click(screen.getByRole("button", { name: "cancel" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Cancel maintenance" }),
		);
		expect(
			await within(dialog).findByText("Already ended."),
		).toBeInTheDocument();
		expect(screen.queryByText("Maintenance canceled")).not.toBeInTheDocument();
	});

	it("deletes a window after confirming and toasts", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"DELETE /maintenance-windows/:id": { status: 204 },
			...REFRESH,
		});
		await renderWithProviders(<Harness window={windowView()} />);
		await user.click(screen.getByRole("button", { name: "delete" }));
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("Delete “Database upgrade”?"),
		).toBeInTheDocument();
		await user.click(
			within(dialog).getByRole("button", { name: "Delete window" }),
		);
		expect(await screen.findByText("Maintenance deleted")).toBeInTheDocument();
		const del = api.calls.find((c) => c.method === "DELETE");
		expect(del?.path).toBe("/maintenance-windows/win-1");
		expect(api.unmatched).toEqual([]);
	});
});
