import { describe, expect, it } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { RenameProjectDialog } from "./rename-project-dialog";

const PROJECT = { id: "p1", name: "Shop" };

function Harness({ onChange }: { onChange?: (open: boolean) => void }) {
	const [open, setOpen] = useState(true);
	return (
		<RenameProjectDialog
			project={PROJECT}
			open={open}
			onOpenChange={(next) => {
				setOpen(next);
				onChange?.(next);
			}}
		/>
	);
}

const listProjects = { "GET /projects": { body: { items: [] } } };

describe("RenameProjectDialog", () => {
	it("starts from the current name with Save locked until it changes", async () => {
		stubApi({});
		await renderWithProviders(<Harness />);
		expect(await screen.findByLabelText("Name")).toHaveValue("Shop");
		expect(screen.getByRole("button", { name: "Save name" })).toBeDisabled();
	});

	it("keeps Save locked for a blank or whitespace-padded unchanged name", async () => {
		const user = userEvent.setup();
		stubApi({});
		await renderWithProviders(<Harness />);
		const input = await screen.findByLabelText("Name");
		await user.clear(input);
		expect(screen.getByRole("button", { name: "Save name" })).toBeDisabled();
		await user.type(input, "  Shop  ");
		expect(screen.getByRole("button", { name: "Save name" })).toBeDisabled();
	});

	it("patches only the trimmed name, toasts the new name and closes", async () => {
		const user = userEvent.setup();
		const changes: boolean[] = [];
		const api = stubApi({
			"PATCH /projects/:id": {
				body: make("Project", { ...PROJECT, name: "Store" }),
			},
			...listProjects,
		});
		await renderWithProviders(<Harness onChange={(o) => changes.push(o)} />);
		const input = await screen.findByLabelText("Name");
		await user.clear(input);
		await user.type(input, "  Store  ");
		await user.click(screen.getByRole("button", { name: "Save name" }));

		expect(await screen.findByText("Renamed to Store")).toBeInTheDocument();
		const patch = api.calls.find((call) => call.method === "PATCH");
		expect(patch?.path).toBe("/projects/p1");
		expect(patch?.body).toEqual({ name: "Store" });
		expect(changes).toContain(false);
		expect(api.unmatched).toEqual([]);
	});

	it("submits with Enter", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"PATCH /projects/:id": { body: make("Project", PROJECT) },
			...listProjects,
		});
		await renderWithProviders(<Harness />);
		const input = await screen.findByLabelText("Name");
		await user.type(input, "2{Enter}");
		await waitFor(() =>
			expect(api.calls.find((c) => c.method === "PATCH")?.body).toEqual({
				name: "Shop2",
			}),
		);
	});

	it("shows the server's reason and stays open when the rename fails", async () => {
		const user = userEvent.setup();
		const changes: boolean[] = [];
		stubApi({
			"PATCH /projects/:id": problem(409, {
				title: "Conflict",
				status: 409,
				detail: "A project with that name exists",
			}),
		});
		await renderWithProviders(<Harness onChange={(o) => changes.push(o)} />);
		const input = await screen.findByLabelText("Name");
		await user.type(input, "x");
		await user.click(screen.getByRole("button", { name: "Save name" }));

		expect(
			await screen.findByText("A project with that name exists"),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Name")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
		expect(changes).toEqual([]);
	});

	it("Cancel closes without a request", async () => {
		const user = userEvent.setup();
		const changes: boolean[] = [];
		const api = stubApi({});
		await renderWithProviders(<Harness onChange={(o) => changes.push(o)} />);
		await user.click(await screen.findByRole("button", { name: "Cancel" }));
		expect(changes).toEqual([false]);
		expect(api.calls).toEqual([]);
	});
});
