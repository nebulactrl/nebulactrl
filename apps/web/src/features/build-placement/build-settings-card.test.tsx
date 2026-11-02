import { describe, expect, mock, test } from "bun:test";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithProviders } from "#/test/render";
import { BuildSettingsCard } from "./build-settings-card";

const card = (over: Partial<Parameters<typeof BuildSettingsCard>[0]> = {}) => {
	const onSave = mock(() => {});
	const onDiscard = mock(() => {});
	const ui = (
		<BuildSettingsCard
			dirty={false}
			saving={false}
			error={null}
			onSave={onSave}
			onDiscard={onDiscard}
			{...over}
		>
			<p>the control</p>
		</BuildSettingsCard>
	);
	return { ui, onSave, onDiscard };
};

describe("BuildSettingsCard", () => {
	test("clean: says nothing is unsaved, Save is disabled and there is no Discard", async () => {
		const { ui } = card();
		await renderWithProviders(ui);
		expect(screen.getByText("the control")).toBeInTheDocument();
		expect(screen.getByText("No unsaved changes.")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled();
		expect(
			screen.queryByRole("button", { name: "Discard" }),
		).not.toBeInTheDocument();
	});

	test("dirty: says so, and Save and Discard call back", async () => {
		const { ui, onSave, onDiscard } = card({ dirty: true });
		await renderWithProviders(ui);
		const user = userEvent.setup();
		expect(screen.getByText("You have unsaved changes.")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Save changes" }));
		expect(onSave).toHaveBeenCalledTimes(1);
		await user.click(screen.getByRole("button", { name: "Discard" }));
		expect(onDiscard).toHaveBeenCalledTimes(1);
	});

	test("saving disables Save so it cannot be sent twice", async () => {
		const { ui } = card({ dirty: true, saving: true });
		await renderWithProviders(ui);
		expect(screen.getByRole("button", { name: /Save changes/ })).toBeDisabled();
		expect(screen.getByRole("status", { name: "Loading" })).toBeInTheDocument();
	});

	test("an error replaces the unsaved-state line", async () => {
		const { ui } = card({ dirty: true, error: "Registry not found" });
		await renderWithProviders(ui);
		expect(screen.getByText("Registry not found")).toBeInTheDocument();
		expect(
			screen.queryByText("You have unsaved changes."),
		).not.toBeInTheDocument();
	});
});
