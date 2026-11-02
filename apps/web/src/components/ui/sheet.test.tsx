import { afterEach, describe, expect, it, mock } from "bun:test";
import { Dialog as SheetPrimitive } from "@base-ui/react/dialog";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createRef, useState } from "react";
import {
	Sheet,
	SheetBody,
	SheetContent,
	SheetDescription,
	SheetFooter,
	SheetHeader,
	SheetTitle,
} from "#/components/ui/sheet";

type Details = { reason: string };

function Example({
	sheetProps,
	contentProps,
}: {
	sheetProps?: Partial<React.ComponentProps<typeof Sheet>>;
	contentProps?: Partial<React.ComponentProps<typeof SheetContent>>;
}) {
	return (
		<>
			<button type="button">outside</button>
			<button type="button" data-row="1">
				row
			</button>
			<Sheet {...sheetProps}>
				<SheetPrimitive.Trigger>Open sheet</SheetPrimitive.Trigger>
				<SheetContent {...contentProps}>
					<SheetHeader>
						<SheetTitle>Service</SheetTitle>
						<SheetDescription>api-server</SheetDescription>
					</SheetHeader>
					<SheetBody>
						<input aria-label="Name" />
					</SheetBody>
					<SheetFooter>
						<button type="button">Save</button>
					</SheetFooter>
				</SheetContent>
			</Sheet>
		</>
	);
}

/** A sheet whose open state the owner holds; it starts open. */
function OwnedSheet() {
	const [open, setOpen] = useState(true);
	return (
		<Sheet open={open} onOpenChange={setOpen}>
			<SheetContent>
				<SheetTitle>Owned</SheetTitle>
			</SheetContent>
		</Sheet>
	);
}

/** A closed sheet opened by a button beside it. */
function ShowSheet() {
	const [open, setOpen] = useState(false);
	return (
		<>
			<button type="button" onClick={() => setOpen(true)}>
				show
			</button>
			<Sheet open={open} onOpenChange={setOpen}>
				<SheetContent>
					<SheetTitle>Service</SheetTitle>
				</SheetContent>
			</Sheet>
		</>
	);
}

/** A resizable open sheet whose width the owner sets. */
function WidthSheet() {
	const [width, setWidth] = useState(600);
	return (
		<>
			<button type="button" onClick={() => setWidth(700)}>
				set 700
			</button>
			<Sheet open>
				<SheetContent resizable width={width} onWidthChange={() => {}}>
					<SheetTitle>Service</SheetTitle>
				</SheetContent>
			</Sheet>
		</>
	);
}

const querySheet = () =>
	document.querySelector<HTMLElement>('[data-slot="sheet-content"]');
const openSheet = async (user: ReturnType<typeof userEvent.setup>) => {
	await user.click(screen.getByRole("button", { name: "Open sheet" }));
	return screen.findByRole("dialog", { name: "Service" });
};

afterEach(() => {
	// Leave the keyboard-modality flag cleared for the next test.
	document.dispatchEvent(new Event("pointerdown", { bubbles: true }));
});

describe("Sheet open and close", () => {
	it("opens from its trigger as a dialog named by title and described by description", async () => {
		const user = userEvent.setup();
		render(<Example />);
		expect(querySheet()).toBeNull();

		const sheet = await openSheet(user);

		expect(sheet).toHaveAccessibleDescription("api-server");
		expect(sheet).toHaveAttribute("data-slot", "sheet-content");
		for (const slot of [
			"sheet-header",
			"sheet-body",
			"sheet-footer",
			"sheet-title",
			"sheet-description",
		]) {
			expect(sheet.querySelector(`[data-slot="${slot}"]`)).not.toBeNull();
		}
	});

	it("the close button closes it, and showCloseButton={false} removes the button", async () => {
		const user = userEvent.setup();
		const onOpenChange = mock((_o: boolean, _d: Details) => {});
		const { unmount } = render(<Example sheetProps={{ onOpenChange }} />);
		await openSheet(user);

		await user.click(screen.getByRole("button", { name: "Close" }));

		await waitFor(() => expect(querySheet()).toBeNull());
		expect(onOpenChange.mock.calls.at(-1)?.[0]).toBe(false);
		expect(onOpenChange.mock.calls.at(-1)?.[1].reason).toBe("close-press");
		unmount();

		render(<Example contentProps={{ showCloseButton: false }} />);
		await openSheet(user);
		expect(screen.queryByRole("button", { name: "Close" })).toBeNull();
	});

	it("Escape closes it and focus returns to the trigger", async () => {
		const user = userEvent.setup();
		const onOpenChange = mock((_o: boolean, _d: Details) => {});
		render(<Example sheetProps={{ onOpenChange }} />);
		const trigger = screen.getByRole("button", { name: "Open sheet" });
		await openSheet(user);

		await user.keyboard("{Escape}");

		await waitFor(() => expect(querySheet()).toBeNull());
		expect(onOpenChange.mock.calls.at(-1)?.[1].reason).toBe("escape-key");
		await waitFor(() => expect(trigger).toHaveFocus());
	});

	it("Escape belongs to another open layer (a dialog rendered beside the sheet), not the sheet", async () => {
		const user = userEvent.setup();
		const onOpenChange = mock((_o: boolean, _d: Details) => {});
		render(<Example sheetProps={{ onOpenChange }} />);
		await openSheet(user);
		onOpenChange.mockClear();
		const other = document.createElement("div");
		other.setAttribute("role", "dialog");
		other.setAttribute("data-open", "");
		document.body.append(other);

		await user.keyboard("{Escape}");

		expect(querySheet()).not.toBeNull();
		expect(onOpenChange).not.toHaveBeenCalled();

		// Once that layer is gone, Escape closes the sheet again.
		other.remove();
		await user.keyboard("{Escape}");
		await waitFor(() => expect(querySheet()).toBeNull());
	});

	it("a modal sheet closes when its scrim is pressed", async () => {
		const user = userEvent.setup();
		const onOpenChange = mock((_o: boolean, _d: Details) => {});
		render(<Example sheetProps={{ onOpenChange }} />);
		await openSheet(user);
		const overlay = document.querySelector('[data-slot="sheet-overlay"]');
		expect(overlay).not.toBeNull();

		await user.click(overlay as Element);

		await waitFor(() => expect(querySheet()).toBeNull());
		expect(onOpenChange.mock.calls.at(-1)?.[1].reason).toBe("outside-press");
	});

	it("a press inside the sheet does not close it", async () => {
		const user = userEvent.setup();
		render(<Example />);
		const sheet = await openSheet(user);
		await user.click(screen.getByText("Service"));
		await user.click(sheet);
		expect(querySheet()).not.toBeNull();
	});

	it("edge sheets show a scrim by default; floating and docked ones do not", async () => {
		const user = userEvent.setup();
		const { unmount } = render(<Example />);
		await openSheet(user);
		expect(
			document.querySelector('[data-slot="sheet-overlay"]'),
		).not.toBeNull();
		unmount();

		const floating = render(
			<Example
				sheetProps={{ modal: false }}
				contentProps={{ variant: "floating" }}
			/>,
		);
		await openSheet(user);
		expect(document.querySelector('[data-slot="sheet-overlay"]')).toBeNull();
		floating.unmount();

		render(
			<Example
				sheetProps={{ modal: false }}
				contentProps={{ variant: "docked" }}
			/>,
		);
		await openSheet(user);
		expect(document.querySelector('[data-slot="sheet-overlay"]')).toBeNull();
	});

	it("showOverlay forces the scrim on a floating sheet", async () => {
		const user = userEvent.setup();
		render(
			<Example contentProps={{ variant: "floating", showOverlay: true }} />,
		);
		await openSheet(user);
		expect(
			document.querySelector('[data-slot="sheet-overlay"]'),
		).not.toBeNull();
	});

	it("controlled: announces the close request, and stays while `open` stays true", async () => {
		const user = userEvent.setup();
		const onOpenChange = mock((_o: boolean, _d: Details) => {});
		render(
			<Sheet open onOpenChange={onOpenChange}>
				<SheetContent>
					<SheetTitle>Pinned</SheetTitle>
				</SheetContent>
			</Sheet>,
		);
		await screen.findByRole("dialog", { name: "Pinned" });

		await user.keyboard("{Escape}");

		expect(onOpenChange).toHaveBeenCalledTimes(1);
		expect(onOpenChange.mock.calls[0]?.[0]).toBe(false);
		expect(querySheet()).not.toBeNull();
	});

	it("controlled: closes once the owner flips `open`", async () => {
		const user = userEvent.setup();
		render(<OwnedSheet />);
		await screen.findByRole("dialog", { name: "Owned" });
		await user.keyboard("{Escape}");
		await waitFor(() => expect(querySheet()).toBeNull());
	});
});

describe("Sheet non-modal dismissal", () => {
	const nonModal = { modal: false } as const;

	it("a press on the page behind closes it", async () => {
		const user = userEvent.setup();
		const onOpenChange = mock((_o: boolean, _d: Details) => {});
		render(
			<Example
				sheetProps={{ ...nonModal, onOpenChange }}
				contentProps={{ variant: "floating" }}
			/>,
		);
		await openSheet(user);

		await user.click(screen.getByRole("button", { name: "outside" }));

		await waitFor(() => expect(querySheet()).toBeNull());
		expect(onOpenChange.mock.calls.at(-1)?.[1].reason).toBe("outside-press");
	});

	it("a press on a `retarget` element leaves it open for that element to retarget it", async () => {
		const user = userEvent.setup();
		const onOpenChange = mock((_o: boolean, _d: Details) => {});
		render(
			<Example
				sheetProps={{ ...nonModal, retarget: "[data-row]", onOpenChange }}
				contentProps={{ variant: "floating" }}
			/>,
		);
		await openSheet(user);
		onOpenChange.mockClear();

		await user.click(screen.getByRole("button", { name: "row" }));

		expect(querySheet()).not.toBeNull();
		expect(onOpenChange).not.toHaveBeenCalled();
	});

	it("a press inside another layer (a menu beside the sheet) does not close it", async () => {
		const user = userEvent.setup();
		render(
			<Example sheetProps={nonModal} contentProps={{ variant: "floating" }} />,
		);
		await openSheet(user);
		const menu = document.createElement("div");
		menu.setAttribute("role", "menu");
		const item = document.createElement("button");
		item.textContent = "menu item";
		menu.append(item);
		document.body.append(menu);

		await user.click(item);

		expect(querySheet()).not.toBeNull();
	});

	it("a secondary-button press does not close it", async () => {
		const user = userEvent.setup();
		render(
			<Example sheetProps={nonModal} contentProps={{ variant: "floating" }} />,
		);
		await openSheet(user);

		await user.pointer({
			keys: "[MouseRight]",
			target: screen.getByRole("button", { name: "outside" }),
		});

		expect(querySheet()).not.toBeNull();
	});

	it("tabbing focus out of it does not close it", async () => {
		const user = userEvent.setup();
		const onOpenChange = mock((_o: boolean, _d: Details) => {});
		render(
			<Example
				sheetProps={{ ...nonModal, onOpenChange }}
				contentProps={{ variant: "floating" }}
			/>,
		);
		await openSheet(user);
		onOpenChange.mockClear();

		screen.getByRole("button", { name: "outside" }).focus();

		expect(onOpenChange).not.toHaveBeenCalled();
		expect(querySheet()).not.toBeNull();
	});
});

describe("Sheet instant", () => {
	it("opened by key, the sheet is marked data-instant; opened by pointer, it is not", async () => {
		const user = userEvent.setup();
		const { unmount } = render(<ShowSheet />);
		await user.click(screen.getByRole("button", { name: "show" }));
		expect(await screen.findByRole("dialog")).not.toHaveAttribute(
			"data-instant",
		);
		unmount();

		render(<ShowSheet />);
		screen.getByRole("button", { name: "show" }).focus();
		await user.keyboard("{Enter}");
		expect(await screen.findByRole("dialog")).toHaveAttribute(
			"data-instant",
			"true",
		);
	});
});

describe("SheetContent layout", () => {
	it("describes its side, variant and motion", async () => {
		const user = userEvent.setup();
		const { unmount } = render(<Example />);
		let sheet = await openSheet(user);
		expect(sheet).toHaveAttribute("data-side", "right");
		expect(sheet).toHaveAttribute("data-variant", "edge");
		// An edge sheet has no `motion`.
		expect(sheet).not.toHaveAttribute("data-motion");
		unmount();

		render(
			<Example
				sheetProps={{ modal: false }}
				contentProps={{ variant: "floating", motion: "slide", side: "left" }}
			/>,
		);
		sheet = await openSheet(user);
		expect(sheet).toHaveAttribute("data-variant", "floating");
		expect(sheet).toHaveAttribute("data-motion", "slide");
		expect(sheet).toHaveAttribute("data-side", "left");
	});

	it("applies a numeric width as px, a string width verbatim, and merges a style object", async () => {
		const user = userEvent.setup();
		const { unmount } = render(
			<Example contentProps={{ width: 500, style: { color: "red" } }} />,
		);
		let sheet = await openSheet(user);
		expect(sheet.style.width).toBe("500px");
		expect(sheet.style.color).toBe("red");
		unmount();

		render(<Example contentProps={{ width: "40rem" }} />);
		sheet = await openSheet(user);
		expect(sheet.style.width).toBe("40rem");
	});

	it("merges a function style with the width", async () => {
		const user = userEvent.setup();
		render(
			<Example
				contentProps={{ width: 480, style: () => ({ color: "blue" }) }}
			/>,
		);
		const sheet = await openSheet(user);
		expect(sheet.style.width).toBe("480px");
		expect(sheet.style.color).toBe("blue");
	});

	it("leaves the width alone when none is given, and ignores it for top and bottom sheets", async () => {
		const user = userEvent.setup();
		const { unmount } = render(<Example />);
		let sheet = await openSheet(user);
		expect(sheet.style.width).toBe("");
		unmount();

		render(<Example contentProps={{ side: "bottom", width: 500 }} />);
		sheet = await openSheet(user);
		expect(sheet.style.width).toBe("");
	});

	it("forwards a caller's object ref and callback ref to the popup", async () => {
		const user = userEvent.setup();
		const ref = createRef<HTMLDivElement>();
		const { unmount } = render(<Example contentProps={{ ref }} />);
		const sheet = await openSheet(user);
		expect(ref.current as HTMLElement | null).toBe(sheet);
		unmount();

		const seen: (HTMLDivElement | null)[] = [];
		render(<Example contentProps={{ ref: (node) => void seen.push(node) }} />);
		const again = await openSheet(user);
		expect(seen as (HTMLElement | null)[]).toContain(again);
	});
});

describe("SheetContent resize handle", () => {
	function stubWidth(width: number) {
		const original = Object.getOwnPropertyDescriptor(
			HTMLElement.prototype,
			"offsetWidth",
		);
		Object.defineProperty(HTMLElement.prototype, "offsetWidth", {
			configurable: true,
			get: () => width,
		});
		return () => {
			if (original)
				Object.defineProperty(HTMLElement.prototype, "offsetWidth", original);
			else
				delete (HTMLElement.prototype as { offsetWidth?: number }).offsetWidth;
		};
	}
	function stubViewport(width: number) {
		const original = window.innerWidth;
		Object.defineProperty(window, "innerWidth", {
			configurable: true,
			value: width,
		});
		return () =>
			Object.defineProperty(window, "innerWidth", {
				configurable: true,
				value: original,
			});
	}

	it("is absent unless `resizable`, and for top and bottom sheets", async () => {
		const user = userEvent.setup();
		const { unmount } = render(<Example />);
		await openSheet(user);
		expect(screen.queryByRole("separator")).toBeNull();
		unmount();

		render(<Example contentProps={{ resizable: true, side: "bottom" }} />);
		await openSheet(user);
		expect(screen.queryByRole("separator")).toBeNull();
	});

	it("is a labelled vertical separator with the width bounds", async () => {
		const user = userEvent.setup();
		const restore = stubViewport(1000);
		try {
			render(<Example contentProps={{ resizable: true }} />);
			await openSheet(user);
			const handle = screen.getByRole("separator", { name: "Resize panel" });
			expect(handle).toHaveAttribute("aria-orientation", "vertical");
			expect(handle).toHaveAttribute("aria-valuemin", "360");
			expect(handle).toHaveAttribute("aria-valuemax", "900");
			expect(handle).toHaveAttribute("tabindex", "0");
			expect(handle).not.toHaveAttribute("aria-valuenow");
		} finally {
			restore();
		}
	});

	it("never offers a maximum below the 360px minimum on a narrow viewport", async () => {
		const user = userEvent.setup();
		const restore = stubViewport(300);
		try {
			render(<Example contentProps={{ resizable: true }} />);
			await openSheet(user);
			expect(screen.getByRole("separator")).toHaveAttribute(
				"aria-valuemax",
				"360",
			);
		} finally {
			restore();
		}
	});

	it("reports the measured width when focused", async () => {
		const user = userEvent.setup();
		const restoreWidth = stubWidth(512.4);
		try {
			render(<Example contentProps={{ resizable: true }} />);
			await openSheet(user);
			const handle = screen.getByRole("separator");
			handle.focus();
			await waitFor(() =>
				expect(handle).toHaveAttribute("aria-valuenow", "512"),
			);
		} finally {
			restoreWidth();
		}
	});

	it("on a right-hand sheet, ArrowLeft widens by 16px and ArrowRight narrows by 16px", async () => {
		const user = userEvent.setup();
		const restoreWidth = stubWidth(500);
		const restoreVp = stubViewport(1400);
		try {
			const onWidthChange = mock((_px: number) => {});
			render(<Example contentProps={{ resizable: true, onWidthChange }} />);
			const sheet = await openSheet(user);
			const handle = screen.getByRole("separator");

			fireEvent.keyDown(handle, { key: "ArrowLeft" });
			expect(onWidthChange).toHaveBeenLastCalledWith(516);
			expect(handle).toHaveAttribute("aria-valuenow", "516");
			// With no `width` prop the sheet keeps the dragged width itself.
			expect(sheet.style.width).toBe("516px");

			fireEvent.keyDown(handle, { key: "ArrowRight" });
			expect(onWidthChange).toHaveBeenLastCalledWith(484);
		} finally {
			restoreWidth();
			restoreVp();
		}
	});

	it("on a left-hand sheet the handle sits on the right edge and ArrowRight widens", async () => {
		const user = userEvent.setup();
		const restoreWidth = stubWidth(500);
		const restoreVp = stubViewport(1400);
		try {
			const onWidthChange = mock((_px: number) => {});
			render(
				<Example
					contentProps={{ resizable: true, side: "left", onWidthChange }}
				/>,
			);
			await openSheet(user);
			const handle = screen.getByRole("separator");

			fireEvent.keyDown(handle, { key: "ArrowRight" });
			expect(onWidthChange).toHaveBeenLastCalledWith(516);
			fireEvent.keyDown(handle, { key: "ArrowLeft" });
			expect(onWidthChange).toHaveBeenLastCalledWith(484);
		} finally {
			restoreWidth();
			restoreVp();
		}
	});

	it("a floating sheet is resized from its left edge too", async () => {
		const user = userEvent.setup();
		const restoreWidth = stubWidth(500);
		const restoreVp = stubViewport(1400);
		try {
			const onWidthChange = mock((_px: number) => {});
			render(
				<Example
					sheetProps={{ modal: false }}
					contentProps={{ resizable: true, variant: "floating", onWidthChange }}
				/>,
			);
			await openSheet(user);
			fireEvent.keyDown(screen.getByRole("separator"), { key: "ArrowLeft" });
			expect(onWidthChange).toHaveBeenLastCalledWith(516);
		} finally {
			restoreWidth();
			restoreVp();
		}
	});

	it("Home goes to the 360px minimum and End to 90% of the viewport; keys clamp to that range", async () => {
		const user = userEvent.setup();
		const restoreWidth = stubWidth(370);
		const restoreVp = stubViewport(1000);
		try {
			const onWidthChange = mock((_px: number) => {});
			render(<Example contentProps={{ resizable: true, onWidthChange }} />);
			await openSheet(user);
			const handle = screen.getByRole("separator");

			fireEvent.keyDown(handle, { key: "End" });
			expect(onWidthChange).toHaveBeenLastCalledWith(900);
			fireEvent.keyDown(handle, { key: "Home" });
			expect(onWidthChange).toHaveBeenLastCalledWith(360);
			// 370 narrowed by 16 would be 354: clamped.
			fireEvent.keyDown(handle, { key: "ArrowRight" });
			expect(onWidthChange).toHaveBeenLastCalledWith(360);
		} finally {
			restoreWidth();
			restoreVp();
		}
	});

	it("other keys do nothing", async () => {
		const user = userEvent.setup();
		const onWidthChange = mock((_px: number) => {});
		render(<Example contentProps={{ resizable: true, onWidthChange }} />);
		await openSheet(user);
		const prevented = !fireEvent.keyDown(screen.getByRole("separator"), {
			key: "a",
		});
		expect(prevented).toBe(false);
		expect(onWidthChange).not.toHaveBeenCalled();
	});

	it("dragging the left edge left widens the sheet, right narrows it; releasing ends the drag", async () => {
		const user = userEvent.setup();
		const restoreWidth = stubWidth(500);
		const restoreVp = stubViewport(1400);
		try {
			const onWidthChange = mock((_px: number) => {});
			render(<Example contentProps={{ resizable: true, onWidthChange }} />);
			const sheet = await openSheet(user);
			const handle = screen.getByRole("separator");
			const captured = new Set<number>();
			handle.setPointerCapture = (id: number) => void captured.add(id);
			handle.hasPointerCapture = (id: number) => captured.has(id);
			handle.releasePointerCapture = (id: number) => void captured.delete(id);

			fireEvent.pointerDown(handle, { pointerId: 1, button: 0, clientX: 800 });
			expect(handle).toHaveAttribute("data-dragging", "true");
			expect(captured.has(1)).toBe(true);

			fireEvent.pointerMove(handle, { pointerId: 1, clientX: 760 });
			expect(onWidthChange).toHaveBeenLastCalledWith(540);
			expect(sheet.style.width).toBe("540px");

			fireEvent.pointerMove(handle, { pointerId: 1, clientX: 850 });
			expect(onWidthChange).toHaveBeenLastCalledWith(450);

			// Dragged beyond the bounds, the width clamps.
			fireEvent.pointerMove(handle, { pointerId: 1, clientX: 0 });
			expect(onWidthChange).toHaveBeenLastCalledWith(1260);
			fireEvent.pointerMove(handle, { pointerId: 1, clientX: 5000 });
			expect(onWidthChange).toHaveBeenLastCalledWith(360);

			fireEvent.pointerUp(handle, { pointerId: 1 });
			expect(handle).not.toHaveAttribute("data-dragging");
			expect(captured.size).toBe(0);

			onWidthChange.mockClear();
			fireEvent.pointerMove(handle, { pointerId: 1, clientX: 700 });
			expect(onWidthChange).not.toHaveBeenCalled();
		} finally {
			restoreWidth();
			restoreVp();
		}
	});

	it("ignores a secondary-button press, a move from another pointer, and cancels like a release", async () => {
		const user = userEvent.setup();
		const restoreWidth = stubWidth(500);
		const restoreVp = stubViewport(1400);
		try {
			const onWidthChange = mock((_px: number) => {});
			render(<Example contentProps={{ resizable: true, onWidthChange }} />);
			await openSheet(user);
			const handle = screen.getByRole("separator");
			handle.setPointerCapture = () => {};
			handle.hasPointerCapture = () => false;

			fireEvent.pointerDown(handle, { pointerId: 1, button: 2, clientX: 800 });
			expect(handle).not.toHaveAttribute("data-dragging");
			fireEvent.pointerMove(handle, { pointerId: 1, clientX: 700 });
			expect(onWidthChange).not.toHaveBeenCalled();

			fireEvent.pointerDown(handle, { pointerId: 1, button: 0, clientX: 800 });
			fireEvent.pointerMove(handle, { pointerId: 2, clientX: 700 });
			expect(onWidthChange).not.toHaveBeenCalled();
			fireEvent.pointerUp(handle, { pointerId: 2 });
			expect(handle).toHaveAttribute("data-dragging", "true");

			fireEvent.pointerCancel(handle, { pointerId: 1 });
			expect(handle).not.toHaveAttribute("data-dragging");
		} finally {
			restoreWidth();
			restoreVp();
		}
	});

	it("a controlled `width` takes over from the dragged width when it changes", async () => {
		const restoreWidth = stubWidth(500);
		const restoreVp = stubViewport(1400);
		try {
			render(<WidthSheet />);
			const sheet = await screen.findByRole("dialog");
			expect(sheet.style.width).toBe("600px");

			fireEvent.keyDown(screen.getByRole("separator"), { key: "ArrowLeft" });
			expect(sheet.style.width).toBe("516px");

			fireEvent.click(
				screen.getByRole("button", { name: "set 700", hidden: true }),
			);
			await waitFor(() => expect(sheet.style.width).toBe("700px"));
		} finally {
			restoreWidth();
			restoreVp();
		}
	});
});
