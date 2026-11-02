import { describe, expect, mock, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { RemoveDialog } from "./remove-dialog";

async function renderRemove() {
	const onOpenChange = mock((_open: boolean) => {});
	await renderWithProviders(
		<RemoveDialog
			view={{ id: "ps-1", kind: "headscale" }}
			label="Headscale"
			open
			onOpenChange={onOpenChange}
		/>,
	);
	const dialog = await screen.findByRole("alertdialog");
	return { dialog, onOpenChange, user: userEvent.setup() };
}

describe("RemoveDialog", () => {
	test("explains the impact and stays locked until the kind is typed", async () => {
		const api = stubApi({});
		const { dialog, user } = await renderRemove();
		expect(within(dialog).getByText("Remove Headscale?")).toBeInTheDocument();
		expect(
			within(dialog).getByText(
				/removes the Tailscale integration it created first/,
			),
		).toBeInTheDocument();
		const confirm = within(dialog).getByRole("button", { name: "Remove" });
		expect(confirm).toBeDisabled();
		await user.type(within(dialog).getByRole("textbox"), "headscal");
		expect(confirm).toBeDisabled();
		await user.type(within(dialog).getByRole("textbox"), "e");
		expect(confirm).toBeEnabled();
		expect(api.calls).toEqual([]);
	});

	test("removes keeping the data volume by default", async () => {
		const api = stubApi({
			"DELETE /platform-services/:id": { status: 204 },
		});
		const { dialog, onOpenChange, user } = await renderRemove();
		await user.type(within(dialog).getByRole("textbox"), "headscale");
		await user.click(within(dialog).getByRole("button", { name: "Remove" }));
		expect(await screen.findByText("Removed Headscale")).toBeInTheDocument();
		expect(
			screen.getByText("Its data volume stays on the server."),
		).toBeInTheDocument();
		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]).toMatchObject({
			method: "DELETE",
			path: "/platform-services/ps-1",
		});
		expect(Object.fromEntries(api.calls[0]?.query ?? [])).toEqual({
			confirm: "headscale",
		});
		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
	});

	test("deletes the data too when asked", async () => {
		const api = stubApi({
			"DELETE /platform-services/:id": { status: 204 },
		});
		const { dialog, user } = await renderRemove();
		await user.click(within(dialog).getByRole("checkbox"));
		await user.type(within(dialog).getByRole("textbox"), "headscale");
		await user.click(within(dialog).getByRole("button", { name: "Remove" }));
		expect(
			await screen.findByText("Its services and data were deleted."),
		).toBeInTheDocument();
		expect(Object.fromEntries(api.calls[0]?.query ?? [])).toEqual({
			confirm: "headscale",
			deleteData: "true",
		});
	});

	test("a refusal keeps the dialog open with the reason and a hint", async () => {
		stubApi({
			"DELETE /platform-services/:id": problem(409, {
				title: "Conflict",
				code: "integration_in_use",
				detail: "The tailnet is used by 2 servers.",
			}),
		});
		const { dialog, onOpenChange, user } = await renderRemove();
		await user.type(within(dialog).getByRole("textbox"), "headscale");
		await user.click(within(dialog).getByRole("button", { name: "Remove" }));
		expect(
			await within(dialog).findByText(
				"The tailnet is used by 2 servers. Stop whatever uses the integration, then remove the service.",
			),
		).toBeInTheDocument();
		expect(screen.queryByText("Removed Headscale")).toBeNull();
		expect(onOpenChange).not.toHaveBeenCalledWith(false);
	});

	test("another refusal shows the server's sentence alone", async () => {
		stubApi({
			"DELETE /platform-services/:id": problem(500, {
				title: "Internal",
				detail: "The agent is unreachable.",
			}),
		});
		const { dialog, user } = await renderRemove();
		await user.type(within(dialog).getByRole("textbox"), "headscale");
		await user.click(within(dialog).getByRole("button", { name: "Remove" }));
		const alert = await within(dialog).findByText("The agent is unreachable.");
		expect(alert.textContent).toBe("The agent is unreachable.");
	});
});
