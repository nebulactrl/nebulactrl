import { describe, expect, it, mock } from "bun:test";
import { Dialog as DialogPrimitive } from "@base-ui/react/dialog";
import {
	act,
	fireEvent,
	render,
	renderHook,
	screen,
	waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import {
	Dialog,
	DialogBody,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
	useOpenedByKeyboard,
} from "#/components/ui/dialog";

type ContentProps = Partial<React.ComponentProps<typeof DialogContent>>;

function Example({
	onOpenChange,
	contentProps,
	footerClose,
}: {
	onOpenChange?: (open: boolean, details: { reason: string }) => void;
	contentProps?: ContentProps;
	footerClose?: boolean;
}) {
	return (
		<Dialog onOpenChange={onOpenChange}>
			<DialogPrimitive.Trigger>Open dialog</DialogPrimitive.Trigger>
			<DialogContent {...contentProps}>
				<DialogHeader>
					<DialogTitle>Rename project</DialogTitle>
					<DialogDescription>Pick a new name.</DialogDescription>
				</DialogHeader>
				<DialogBody>
					<input aria-label="Name" />
				</DialogBody>
				<DialogFooter {...(footerClose ? { showCloseButton: true } : {})}>
					<button type="button">Save</button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}

/** A dialog the owner holds open until its own button flips `open`. */
function Controlled({
	onOpenChange,
}: {
	onOpenChange: (open: boolean, details: unknown) => void;
}) {
	const [open, setOpen] = useState(true);
	return (
		<>
			<button type="button" onClick={() => setOpen(false)}>
				outside close
			</button>
			<Dialog
				open={open}
				onOpenChange={(next, details) => {
					onOpenChange(next, details);
				}}
			>
				<DialogContent showCloseButton>
					<DialogTitle>Controlled</DialogTitle>
				</DialogContent>
			</Dialog>
		</>
	);
}

describe("Dialog", () => {
	it("opens from its trigger as a modal dialog named by its title and described by its description", async () => {
		const user = userEvent.setup();
		render(<Example />);
		expect(screen.queryByRole("dialog")).toBeNull();

		await user.click(screen.getByRole("button", { name: "Open dialog" }));

		const dialog = await screen.findByRole("dialog", {
			name: "Rename project",
		});
		expect(dialog).toHaveAccessibleDescription("Pick a new name.");
		expect(dialog).toHaveAttribute("data-slot", "dialog-content");
		expect(screen.getByText("Save")).toBeVisible();
	});

	it("reports open and close through onOpenChange with the reason", async () => {
		const user = userEvent.setup();
		const onOpenChange = mock(
			(_open: boolean, _details: { reason: string }) => {},
		);
		render(<Example onOpenChange={onOpenChange} />);

		await user.click(screen.getByRole("button", { name: "Open dialog" }));
		await screen.findByRole("dialog");
		expect(onOpenChange.mock.calls[0]?.[0]).toBe(true);
		expect(onOpenChange.mock.calls[0]?.[1].reason).toBe("trigger-press");

		await user.keyboard("{Escape}");
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		const last = onOpenChange.mock.calls.at(-1);
		expect(last?.[0]).toBe(false);
		expect(last?.[1].reason).toBe("escape-key");
	});

	it("closes when the backdrop is pressed", async () => {
		const user = userEvent.setup();
		const onOpenChange = mock(
			(_open: boolean, _details: { reason: string }) => {},
		);
		render(<Example onOpenChange={onOpenChange} />);
		await user.click(screen.getByRole("button", { name: "Open dialog" }));
		await screen.findByRole("dialog");

		const overlay = document.querySelector('[data-slot="dialog-overlay"]');
		expect(overlay).not.toBeNull();
		await user.click(overlay as Element);

		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		expect(onOpenChange.mock.calls.at(-1)?.[1].reason).toBe("outside-press");
	});

	it("does not close when the press lands inside the dialog", async () => {
		const user = userEvent.setup();
		render(<Example />);
		await user.click(screen.getByRole("button", { name: "Open dialog" }));
		const dialog = await screen.findByRole("dialog");
		await user.click(screen.getByText("Rename project"));
		await user.click(dialog);
		expect(screen.getByRole("dialog")).toBeInTheDocument();
	});

	it("moves focus into the dialog on open and back to the trigger on close", async () => {
		const user = userEvent.setup();
		render(<Example />);
		const trigger = screen.getByRole("button", { name: "Open dialog" });
		await user.click(trigger);
		const dialog = await screen.findByRole("dialog");
		await waitFor(() =>
			expect(dialog).toContainElement(document.activeElement as HTMLElement),
		);

		await user.keyboard("{Escape}");
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		await waitFor(() => expect(trigger).toHaveFocus());
	});

	it("traps Tab: from the last control focus wraps to the first, and Shift+Tab goes back", async () => {
		const user = userEvent.setup();
		render(<Example contentProps={{ showCloseButton: true }} />);
		await user.click(screen.getByRole("button", { name: "Open dialog" }));
		await screen.findByRole("dialog");

		const input = screen.getByLabelText("Name");
		const save = screen.getByRole("button", { name: "Save" });
		const close = screen.getByRole("button", { name: "Close" });
		// DOM order is Name, Save, then the close button last.
		expect(
			save.compareDocumentPosition(close) & Node.DOCUMENT_POSITION_FOLLOWING,
		).toBeTruthy();
		// happy-dom has no layout, so the trap sees no client rects: give it some.
		const original = HTMLElement.prototype.getClientRects;
		HTMLElement.prototype.getClientRects = (() => [
			{},
		]) as unknown as typeof original;
		try {
			close.focus();
			fireEvent.keyDown(close, { key: "Tab" });
			expect(input).toHaveFocus();

			fireEvent.keyDown(input, { key: "Tab", shiftKey: true });
			expect(close).toHaveFocus();
		} finally {
			HTMLElement.prototype.getClientRects = original;
		}
	});

	it("calls a caller's onKeyDown as well as trapping Tab", async () => {
		const user = userEvent.setup();
		const onKeyDown = mock((_e: unknown) => {});
		render(<Example contentProps={{ onKeyDown }} />);
		await user.click(screen.getByRole("button", { name: "Open dialog" }));
		await screen.findByRole("dialog");
		fireEvent.keyDown(screen.getByLabelText("Name"), { key: "x" });
		expect(onKeyDown).toHaveBeenCalledTimes(1);
	});

	it("shows no close button unless asked, and the close button closes it", async () => {
		const user = userEvent.setup();
		const { unmount } = render(<Example />);
		await user.click(screen.getByRole("button", { name: "Open dialog" }));
		await screen.findByRole("dialog");
		expect(screen.queryByRole("button", { name: "Close" })).toBeNull();
		unmount();

		render(<Example contentProps={{ showCloseButton: true }} />);
		await user.click(screen.getByRole("button", { name: "Open dialog" }));
		await screen.findByRole("dialog");
		await user.click(screen.getByRole("button", { name: "Close" }));
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
	});

	it("the footer's close button closes the dialog", async () => {
		const user = userEvent.setup();
		render(<Example footerClose />);
		await user.click(screen.getByRole("button", { name: "Open dialog" }));
		await screen.findByRole("dialog");
		await user.click(screen.getByRole("button", { name: "Close" }));
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
	});

	it("controlled: stays open until the owner flips `open`, announcing the request", async () => {
		const user = userEvent.setup();
		const onOpenChange = mock((_open: boolean, _details: unknown) => {});
		render(<Controlled onOpenChange={onOpenChange} />);
		expect(
			await screen.findByRole("dialog", { name: "Controlled" }),
		).toBeInTheDocument();

		await user.keyboard("{Escape}");
		expect(onOpenChange).toHaveBeenCalledTimes(1);
		expect(onOpenChange.mock.calls[0]?.[0]).toBe(false);
		// The owner ignored it, so the dialog is still there.
		expect(screen.getByRole("dialog")).toBeInTheDocument();

		act(() =>
			screen
				.getByRole("button", { hidden: true, name: "outside close" })
				.click(),
		);
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
	});

	// The exact width (`min(Npx, 92vw)`) is not asserted: happy-dom's style
	// parser drops `min()` values, so the inline width cannot be read back.
	it("applies size and top offset", async () => {
		const user = userEvent.setup();
		render(<Example contentProps={{ size: "lg", width: 500, top: 8 }} />);
		await user.click(screen.getByRole("button", { name: "Open dialog" }));
		const dialog = await screen.findByRole("dialog");
		expect(dialog).toHaveAttribute("data-size", "lg");
		expect(dialog.style.getPropertyValue("--dialog-top")).toBe("8vh");
	});

	it("defaults to the sm tier with no inline width or offset, and keeps a caller's style", async () => {
		const user = userEvent.setup();
		render(<Example contentProps={{ style: { color: "red" } }} />);
		await user.click(screen.getByRole("button", { name: "Open dialog" }));
		const dialog = await screen.findByRole("dialog");
		expect(dialog).toHaveAttribute("data-size", "sm");
		expect(dialog.style.width).toBe("");
		expect(dialog.style.getPropertyValue("--dialog-top")).toBe("");
		expect(dialog.style.color).toBe("red");
	});

	it("`instant` drops the entrance transition classes from popup and scrim", async () => {
		const user = userEvent.setup();
		render(<Example contentProps={{ instant: true }} />);
		await user.click(screen.getByRole("button", { name: "Open dialog" }));
		const dialog = await screen.findByRole("dialog");
		expect(dialog).not.toHaveClass("t-modal");
		expect(
			document.querySelector('[data-slot="dialog-overlay"]'),
		).not.toHaveClass("t-scrim");
	});

	it("animates by default", async () => {
		const user = userEvent.setup();
		render(<Example />);
		await user.click(screen.getByRole("button", { name: "Open dialog" }));
		const dialog = await screen.findByRole("dialog");
		expect(dialog).toHaveClass("t-modal");
		expect(document.querySelector('[data-slot="dialog-overlay"]')).toHaveClass(
			"t-scrim",
		);
	});

	it("renders header, body and footer parts with their slots", async () => {
		const user = userEvent.setup();
		render(<Example />);
		await user.click(screen.getByRole("button", { name: "Open dialog" }));
		const dialog = await screen.findByRole("dialog");
		for (const slot of [
			"dialog-header",
			"dialog-body",
			"dialog-footer",
			"dialog-title",
			"dialog-description",
		]) {
			expect(dialog.querySelector(`[data-slot="${slot}"]`)).not.toBeNull();
		}
	});
});

describe("useOpenedByKeyboard", () => {
	const key = () =>
		document.dispatchEvent(
			new KeyboardEvent("keydown", { key: "Enter", bubbles: true }),
		);
	const pointer = () =>
		document.dispatchEvent(new Event("pointerdown", { bubbles: true }));

	it("is true when the dialog opens right after a key press and stays so until the next opening", () => {
		pointer();
		const { result, rerender } = renderHook(
			({ open }) => useOpenedByKeyboard(open),
			{
				initialProps: { open: false },
			},
		);
		expect(result.current).toBe(false);

		key();
		rerender({ open: true });
		expect(result.current).toBe(true);

		// A pointer press while open does not retroactively change the answer.
		pointer();
		rerender({ open: true });
		expect(result.current).toBe(true);

		rerender({ open: false });
		expect(result.current).toBe(true);

		rerender({ open: true });
		expect(result.current).toBe(false);
	});

	it("is false when the pointer opened it", () => {
		pointer();
		const { result, rerender } = renderHook(
			({ open }) => useOpenedByKeyboard(open),
			{
				initialProps: { open: false },
			},
		);
		rerender({ open: true });
		expect(result.current).toBe(false);
	});

	it("starts true for a dialog mounted open after a key press", () => {
		key();
		const { result } = renderHook(() => useOpenedByKeyboard(true));
		expect(result.current).toBe(true);
		pointer();
	});

	it("starts false for a dialog mounted closed, even after a key press", () => {
		key();
		const { result } = renderHook(() => useOpenedByKeyboard(false));
		expect(result.current).toBe(false);
		pointer();
	});
});
