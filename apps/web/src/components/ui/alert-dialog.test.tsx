import { describe, expect, it, mock } from "bun:test";
import { AlertDialog as AlertDialogPrimitive } from "@base-ui/react/alert-dialog";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
	AlertDialog,
	AlertDialogBody,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogTitle,
} from "#/components/ui/alert-dialog";

function Example({
	onConfirm = () => {},
	onOpenChange,
	contentProps,
	cancelProps,
}: {
	onConfirm?: () => void;
	onOpenChange?: (open: boolean, details: { reason: string }) => void;
	contentProps?: Partial<React.ComponentProps<typeof AlertDialogContent>>;
	cancelProps?: Partial<React.ComponentProps<typeof AlertDialogCancel>>;
}) {
	return (
		<AlertDialog onOpenChange={onOpenChange}>
			<AlertDialogPrimitive.Trigger>
				Delete project
			</AlertDialogPrimitive.Trigger>
			<AlertDialogContent {...contentProps}>
				<AlertDialogBody>
					<AlertDialogTitle>Delete this project?</AlertDialogTitle>
					<AlertDialogDescription>
						This cannot be undone.
					</AlertDialogDescription>
				</AlertDialogBody>
				<AlertDialogFooter>
					<AlertDialogCancel {...cancelProps}>Keep it</AlertDialogCancel>
					<AlertDialogPrimitive.Close onClick={onConfirm}>
						Delete
					</AlertDialogPrimitive.Close>
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	);
}

async function open(user: ReturnType<typeof userEvent.setup>) {
	await user.click(screen.getByRole("button", { name: "Delete project" }));
	return screen.findByRole("alertdialog", { name: "Delete this project?" });
}

describe("AlertDialog", () => {
	it("opens from its trigger as an alertdialog named by its title and described by its description", async () => {
		const user = userEvent.setup();
		render(<Example />);
		expect(screen.queryByRole("alertdialog")).toBeNull();

		const dialog = await open(user);

		expect(dialog).toHaveAccessibleDescription("This cannot be undone.");
		expect(dialog).toHaveAttribute("data-slot", "alert-dialog-content");
		expect(screen.getByRole("button", { name: "Keep it" })).toBeVisible();
	});

	it("Cancel closes it and reports the close", async () => {
		const user = userEvent.setup();
		const onOpenChange = mock(
			(_open: boolean, _details: { reason: string }) => {},
		);
		const onConfirm = mock(() => {});
		render(<Example onOpenChange={onOpenChange} onConfirm={onConfirm} />);
		await open(user);

		await user.click(screen.getByRole("button", { name: "Keep it" }));

		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
		expect(onOpenChange.mock.calls.at(-1)?.[0]).toBe(false);
		expect(onOpenChange.mock.calls.at(-1)?.[1].reason).toBe("close-press");
		expect(onConfirm).not.toHaveBeenCalled();
	});

	it("a confirming action runs its callback and closes the dialog", async () => {
		const user = userEvent.setup();
		const onConfirm = mock(() => {});
		render(<Example onConfirm={onConfirm} />);
		await open(user);

		await user.click(screen.getByRole("button", { name: "Delete" }));

		expect(onConfirm).toHaveBeenCalledTimes(1);
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
	});

	it("Escape dismisses it", async () => {
		const user = userEvent.setup();
		const onOpenChange = mock(
			(_open: boolean, _details: { reason: string }) => {},
		);
		render(<Example onOpenChange={onOpenChange} />);
		await open(user);

		await user.keyboard("{Escape}");

		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
		expect(onOpenChange.mock.calls.at(-1)?.[1].reason).toBe("escape-key");
	});

	it("a press on the scrim does not dismiss a confirmation", async () => {
		const user = userEvent.setup();
		render(<Example />);
		await open(user);
		const overlay = document.querySelector(
			'[data-slot="alert-dialog-overlay"]',
		);
		expect(overlay).not.toBeNull();

		await user.click(overlay as Element);

		expect(screen.getByRole("alertdialog")).toBeInTheDocument();
	});

	it("restores focus to the trigger after closing", async () => {
		const user = userEvent.setup();
		render(<Example />);
		const trigger = screen.getByRole("button", { name: "Delete project" });
		const dialog = await open(user);
		await waitFor(() =>
			expect(dialog).toContainElement(document.activeElement as HTMLElement),
		);

		await user.click(screen.getByRole("button", { name: "Keep it" }));

		await waitFor(() => expect(trigger).toHaveFocus());
	});

	it("traps Tab inside: last control wraps to the first and Shift+Tab back", async () => {
		const user = userEvent.setup();
		render(<Example />);
		await open(user);
		const keep = screen.getByRole("button", { name: "Keep it" });
		const del = screen.getByRole("button", { name: "Delete" });
		const original = HTMLElement.prototype.getClientRects;
		// happy-dom has no layout; make every element count as rendered.
		HTMLElement.prototype.getClientRects = (() => [
			{},
		]) as unknown as typeof original;
		try {
			del.focus();
			fireEvent.keyDown(del, { key: "Tab" });
			expect(keep).toHaveFocus();

			fireEvent.keyDown(keep, { key: "Tab", shiftKey: true });
			expect(del).toHaveFocus();
		} finally {
			HTMLElement.prototype.getClientRects = original;
		}
	});

	it("calls a caller's onKeyDown", async () => {
		const user = userEvent.setup();
		const onKeyDown = mock((_e: unknown) => {});
		render(<Example contentProps={{ onKeyDown }} />);
		const dialog = await open(user);
		fireEvent.keyDown(dialog, { key: "x" });
		expect(onKeyDown).toHaveBeenCalledTimes(1);
	});

	it("`instant` drops the entrance transition classes from popup and scrim", async () => {
		const user = userEvent.setup();
		render(<Example contentProps={{ instant: true }} />);
		const dialog = await open(user);
		expect(dialog).not.toHaveClass("t-modal");
		expect(
			document.querySelector('[data-slot="alert-dialog-overlay"]'),
		).not.toHaveClass("t-scrim");
	});

	it("animates by default", async () => {
		const user = userEvent.setup();
		render(<Example />);
		const dialog = await open(user);
		expect(dialog).toHaveClass("t-modal");
		expect(
			document.querySelector('[data-slot="alert-dialog-overlay"]'),
		).toHaveClass("t-scrim");
	});

	it("Cancel exposes its slot and accepts a variant and className", async () => {
		const user = userEvent.setup();
		render(
			<Example
				cancelProps={{ variant: "outline", size: "sm", className: "mine" }}
			/>,
		);
		await open(user);
		const cancel = screen.getByRole("button", { name: "Keep it" });
		expect(cancel).toHaveAttribute("data-slot", "alert-dialog-cancel");
		expect(cancel).toHaveClass("mine");
	});

	it("lays out title, description, body and footer in slots", async () => {
		const user = userEvent.setup();
		render(<Example />);
		const dialog = await open(user);
		for (const slot of [
			"alert-dialog-body",
			"alert-dialog-footer",
			"alert-dialog-title",
			"alert-dialog-description",
		]) {
			expect(dialog.querySelector(`[data-slot="${slot}"]`)).not.toBeNull();
		}
	});

	it("controlled: a requested close is announced but the dialog stays while `open` stays true", async () => {
		const user = userEvent.setup();
		const onOpenChange = mock((_open: boolean, _details: unknown) => {});
		render(
			<AlertDialog open onOpenChange={onOpenChange}>
				<AlertDialogContent>
					<AlertDialogTitle>Sure?</AlertDialogTitle>
					<AlertDialogCancel>No</AlertDialogCancel>
				</AlertDialogContent>
			</AlertDialog>,
		);
		await screen.findByRole("alertdialog", { name: "Sure?" });

		await user.click(screen.getByRole("button", { name: "No" }));

		expect(onOpenChange).toHaveBeenCalledTimes(1);
		expect(onOpenChange.mock.calls[0]?.[0]).toBe(false);
		expect(screen.getByRole("alertdialog")).toBeInTheDocument();
	});
});
