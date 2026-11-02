import { describe, expect, it, mock } from "bun:test";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ComponentProps } from "react";
import { renderWithProviders } from "#/test/render";
import { OptionGroupPanel } from "./option-group-panel";

function panel(props: Partial<ComponentProps<typeof OptionGroupPanel>> = {}) {
	return (
		<OptionGroupPanel
			title="Limits"
			summary=""
			staged={false}
			dirty={false}
			invalid={false}
			saving={false}
			onSave={() => {}}
			onDiscard={() => {}}
			{...props}
		>
			<input aria-label="Inner field" />
		</OptionGroupPanel>
	);
}

describe("OptionGroupPanel", () => {
	it("starts collapsed with the title as a button and no body", async () => {
		await renderWithProviders(panel());
		const header = screen.getByRole("button", { name: "Limits" });
		expect(header).toHaveAttribute("aria-expanded", "false");
		expect(screen.queryByLabelText("Inner field")).toBeNull();
	});

	it("opens and closes from the header", async () => {
		const user = userEvent.setup({ delay: null });
		await renderWithProviders(panel());
		const header = screen.getByRole("button", { name: "Limits" });
		await user.click(header);
		expect(header).toHaveAttribute("aria-expanded", "true");
		expect(screen.getByLabelText("Inner field")).toBeInTheDocument();
		await user.click(header);
		expect(header).toHaveAttribute("aria-expanded", "false");
	});

	it("opens at first render with defaultOpen", async () => {
		await renderWithProviders(panel({ defaultOpen: true }));
		expect(screen.getByRole("button", { name: /Limits/ })).toHaveAttribute(
			"aria-expanded",
			"true",
		);
		expect(screen.getByLabelText("Inner field")).toBeInTheDocument();
	});

	it("shows the summary only when something is set", async () => {
		const { unmount } = await renderWithProviders(panel());
		expect(screen.queryByText("2 rule(s)")).toBeNull();
		unmount();
		await renderWithProviders(panel({ summary: "2 rule(s)" }));
		expect(screen.getByText("2 rule(s)")).toBeInTheDocument();
	});

	it("badges unsaved edits and staged changes, with the staged tooltip text", async () => {
		await renderWithProviders(
			panel({
				dirty: true,
				staged: true,
				stagedTitle: "Staged: generation 3, deployed 2",
			}),
		);
		expect(screen.getByText("Unsaved")).toBeInTheDocument();
		expect(screen.getByText("Staged")).toHaveAttribute(
			"title",
			"Staged: generation 3, deployed 2",
		);
	});

	it("shows no badges for a clean, unstaged group", async () => {
		await renderWithProviders(panel());
		expect(screen.queryByText("Unsaved")).toBeNull();
		expect(screen.queryByText("Staged")).toBeNull();
	});

	it("shows the notice and general error above the footer when open", async () => {
		await renderWithProviders(
			panel({
				defaultOpen: true,
				notice: "A redirect has no options.",
				error: "The proxy refused it.",
			}),
		);
		expect(screen.getByText("A redirect has no options.")).toBeInTheDocument();
		expect(screen.getByText("The proxy refused it.")).toBeInTheDocument();
	});

	it("hides the footer until the group is edited", async () => {
		await renderWithProviders(panel({ defaultOpen: true }));
		expect(screen.queryByRole("button", { name: "Discard" })).toBeNull();
		expect(screen.queryByRole("button", { name: /^Save/ })).toBeNull();
	});

	it("saves and discards through the footer buttons, naming the group", async () => {
		const user = userEvent.setup({ delay: null });
		const onSave = mock(() => {});
		const onDiscard = mock(() => {});
		await renderWithProviders(
			panel({
				title: "Headers & CORS",
				dirty: true,
				defaultOpen: true,
				onSave,
				onDiscard,
			}),
		);
		await user.click(
			screen.getByRole("button", { name: "Save headers & cors" }),
		);
		expect(onSave).toHaveBeenCalledTimes(1);
		expect(onDiscard).not.toHaveBeenCalled();
		await user.click(screen.getByRole("button", { name: "Discard" }));
		expect(onDiscard).toHaveBeenCalledTimes(1);
	});

	it("disables Save while the group is invalid but still lets you discard", async () => {
		const user = userEvent.setup({ delay: null });
		const onSave = mock(() => {});
		const onDiscard = mock(() => {});
		await renderWithProviders(
			panel({
				dirty: true,
				invalid: true,
				defaultOpen: true,
				onSave,
				onDiscard,
			}),
		);
		const save = screen.getByRole("button", { name: "Save limits" });
		expect(save).toBeDisabled();
		await user.click(save);
		expect(onSave).not.toHaveBeenCalled();
		expect(screen.getByRole("button", { name: "Discard" })).toBeEnabled();
	});

	it("disables both buttons while saving", async () => {
		await renderWithProviders(
			panel({ dirty: true, saving: true, defaultOpen: true }),
		);
		expect(screen.getByRole("button", { name: /Save limits/ })).toBeDisabled();
		expect(screen.getByRole("status", { name: "Loading" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Discard" })).toBeDisabled();
	});

	it("disables the fields and Save of a locked group", async () => {
		await renderWithProviders(
			panel({ dirty: true, locked: true, defaultOpen: true }),
		);
		expect(screen.getByLabelText("Inner field")).toBeDisabled();
		expect(screen.getByRole("button", { name: "Save limits" })).toBeDisabled();
	});

	it("leaves fields enabled when not locked", async () => {
		await renderWithProviders(panel({ defaultOpen: true }));
		expect(screen.getByLabelText("Inner field")).toBeEnabled();
	});
});
