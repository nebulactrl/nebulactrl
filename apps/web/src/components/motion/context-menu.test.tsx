import { afterEach, describe, expect, jest, test } from "bun:test";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import {
	ContextMenu,
	ContextMenuContent,
	ContextMenuItem,
	ContextMenuLabel,
	ContextMenuSeparator,
	ContextMenuShortcut,
	ContextMenuTrigger,
	useContextMenuOpener,
} from "./context-menu";

afterEach(() => {
	jest.useRealTimers();
});

interface HarnessProps {
	onSelect?: () => void;
	onTrigger?: (target: HTMLElement | null) => void;
	ignore?: (target: HTMLElement) => boolean;
	disabled?: boolean;
	longPress?: boolean;
	onOpenChange?: (open: boolean) => void;
	open?: boolean;
}

function Harness({
	onSelect,
	onTrigger,
	ignore,
	disabled,
	longPress,
	onOpenChange,
	open,
}: HarnessProps) {
	return (
		<ContextMenu
			{...(open === undefined ? {} : { open })}
			{...(onOpenChange ? { onOpenChange } : {})}
		>
			<ContextMenuTrigger
				{...(onTrigger ? { onTrigger } : {})}
				{...(ignore ? { ignore } : {})}
				{...(disabled === undefined ? {} : { disabled })}
				{...(longPress === undefined ? {} : { longPress })}
			>
				<button data-testid="surface" type="button">
					<span data-testid="inner">canvas</span>
				</button>
			</ContextMenuTrigger>
			<ContextMenuContent title="checkout-api · production">
				<ContextMenuLabel>Service</ContextMenuLabel>
				<ContextMenuItem
					{...(onSelect ? { onSelect } : {})}
					textValue="Restart"
				>
					Restart
					<ContextMenuShortcut>R</ContextMenuShortcut>
				</ContextMenuItem>
				<ContextMenuItem textValue="Redeploy">Redeploy</ContextMenuItem>
				<ContextMenuSeparator />
				<ContextMenuItem disabled textValue="Archive">
					Archive
				</ContextMenuItem>
				<ContextMenuItem
					tone="destructive"
					closeOnSelect={false}
					textValue="Delete"
				>
					Delete
				</ContextMenuItem>
			</ContextMenuContent>
		</ContextMenu>
	);
}

/** Lets the animation frame the menu queued to move focus run (frames fire in order). */
async function nextFrame() {
	await act(async () => {
		await new Promise((resolve) => requestAnimationFrame(resolve));
	});
}

/** Gives every element a fixed layout box; returns the restore. */
function stubBox(width: number, height: number): () => void {
	const widthDesc = Object.getOwnPropertyDescriptor(
		HTMLElement.prototype,
		"offsetWidth",
	);
	const heightDesc = Object.getOwnPropertyDescriptor(
		HTMLElement.prototype,
		"offsetHeight",
	);
	Object.defineProperty(HTMLElement.prototype, "offsetWidth", {
		configurable: true,
		get: () => width,
	});
	Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
		configurable: true,
		get: () => height,
	});
	return () => {
		if (widthDesc)
			Object.defineProperty(HTMLElement.prototype, "offsetWidth", widthDesc);
		if (heightDesc)
			Object.defineProperty(HTMLElement.prototype, "offsetHeight", heightDesc);
	};
}

const surface = () => screen.getByTestId("surface");

function rightClick(target: HTMLElement = surface(), x = 100, y = 120) {
	fireEvent.contextMenu(target, { clientX: x, clientY: y });
}

/** A menu child that opens the menu at a fixed point, with no trigger event. */
function Opener() {
	const open = useContextMenuOpener();
	return (
		<button type="button" onClick={() => open({ x: 40, y: 60 }, "keyboard")}>
			open it
		</button>
	);
}

/** The harness beside a button that re-renders its parent. */
function Wrapper() {
	const [count, setCount] = useState(0);
	return (
		<>
			<button type="button" onClick={() => setCount(count + 1)}>
				rerender {count}
			</button>
			<Harness />
		</>
	);
}

describe("ContextMenu", () => {
	test("is closed until the trigger is right-clicked", () => {
		render(<Harness />);
		expect(screen.queryByRole("menu")).toBeNull();
		expect(surface()).toHaveAttribute("aria-haspopup", "menu");
		expect(surface()).toHaveAttribute("aria-expanded", "false");
		expect(surface()).not.toHaveAttribute("aria-controls");
	});

	test("right-click opens the menu at the pointer, titled, and cancels the native menu", () => {
		render(<Harness />);
		const notPrevented = fireEvent.contextMenu(surface(), {
			clientX: 100,
			clientY: 120,
		});
		expect(notPrevented).toBe(false);
		const menu = screen.getByRole("menu", {
			name: "checkout-api · production",
		});
		expect(menu).toHaveStyle({ left: "100px", top: "120px" });
		expect(surface()).toHaveAttribute("aria-expanded", "true");
		expect(surface()).toHaveAttribute("aria-controls", menu.id);
		expect(
			screen.getAllByRole("menuitem").map((item) => item.textContent),
		).toEqual(["RestartR", "Redeploy", "Archive", "Delete"]);
		expect(screen.getByText("Service")).toBeInTheDocument();
		expect(screen.getByRole("separator")).toBeInTheDocument();
	});

	test("the menu itself swallows the native context menu", () => {
		render(<Harness />);
		rightClick();
		expect(fireEvent.contextMenu(screen.getByRole("menu"))).toBe(false);
	});

	test("the menu is clamped into the viewport", () => {
		// happy-dom has no layout: give every element a 236x200 box.
		const restore = stubBox(236, 200);
		try {
			render(<Harness />);
			// Past the right edge: pulled back so the menu ends 8px inside.
			rightClick(surface(), 5000, 100);
			let menu = screen.getByRole("menu");
			expect(menu.style.left).toBe(`${window.innerWidth - 236 - 8}px`);
			expect(menu.style.top).toBe("100px");
			fireEvent.keyDown(menu, { key: "Escape" });

			// Near the bottom: opens upward from the pointer, where it fits.
			rightClick(surface(), 100, window.innerHeight - 40);
			menu = screen.getByRole("menu");
			expect(menu.style.top).toBe(`${window.innerHeight - 40 - 200}px`);
			fireEvent.keyDown(menu, { key: "Escape" });

			// Before the top-left corner: held 8px inside.
			rightClick(surface(), -50, -50);
			menu = screen.getByRole("menu");
			expect([menu.style.left, menu.style.top]).toEqual(["8px", "8px"]);
		} finally {
			restore();
		}
	});

	test("a menu taller than the space above and below slides up to fit the bottom edge", () => {
		const restore = stubBox(236, 700);
		try {
			render(<Harness />);
			rightClick(surface(), 100, 300);
			const menu = screen.getByRole("menu");
			// Neither below (300+700 > 760) nor flipped above (300-700 < 8) fits.
			expect(menu.style.top).toBe(`${window.innerHeight - 700 - 8}px`);
		} finally {
			restore();
		}
	});

	test("onTrigger receives the element under the pointer", () => {
		const onTrigger = jest.fn();
		render(<Harness onTrigger={onTrigger} />);
		rightClick(screen.getByTestId("inner"));
		expect(onTrigger).toHaveBeenCalledTimes(1);
		expect(onTrigger).toHaveBeenCalledWith(screen.getByTestId("inner"));
	});

	test("a disabled trigger leaves the native menu alone", () => {
		const onTrigger = jest.fn();
		render(<Harness disabled onTrigger={onTrigger} />);
		const notPrevented = fireEvent.contextMenu(surface());
		expect(notPrevented).toBe(true);
		expect(screen.queryByRole("menu")).toBeNull();
		expect(onTrigger).not.toHaveBeenCalled();
	});

	test("ignored targets keep the platform menu", () => {
		render(<Harness ignore={(target) => target.dataset.testid === "inner"} />);
		expect(fireEvent.contextMenu(screen.getByTestId("inner"))).toBe(true);
		expect(screen.queryByRole("menu")).toBeNull();
		// The rest of the surface still opens it.
		rightClick(surface());
		expect(screen.getByRole("menu")).toBeInTheDocument();
	});

	test("choosing an item runs it and closes the menu", () => {
		const onSelect = jest.fn();
		render(<Harness onSelect={onSelect} />);
		rightClick();
		fireEvent.click(screen.getByRole("menuitem", { name: /Restart/ }));
		expect(onSelect).toHaveBeenCalledTimes(1);
		expect(screen.queryByRole("menu")).toBeNull();
		expect(surface()).toHaveAttribute("aria-expanded", "false");
	});

	test("an item with closeOnSelect=false leaves the menu open", () => {
		render(<Harness />);
		rightClick();
		fireEvent.click(screen.getByRole("menuitem", { name: "Delete" }));
		expect(screen.getByRole("menu")).toBeInTheDocument();
	});

	test("a disabled item cannot be chosen", () => {
		render(<Harness />);
		rightClick();
		const archive = screen.getByRole("menuitem", { name: "Archive" });
		expect(archive).toBeDisabled();
		expect(archive).toHaveAttribute("data-disabled", "true");
		fireEvent.click(archive);
		expect(screen.getByRole("menu")).toBeInTheDocument();
	});

	test("Escape closes the menu and returns focus to where it was", () => {
		render(<Harness />);
		surface().focus();
		rightClick();
		fireEvent.keyDown(screen.getByRole("menu"), { key: "Escape" });
		expect(screen.queryByRole("menu")).toBeNull();
		expect(surface()).toHaveFocus();
	});

	test("Tab closes the menu", () => {
		render(<Harness />);
		rightClick();
		fireEvent.keyDown(screen.getByRole("menu"), { key: "Tab" });
		expect(screen.queryByRole("menu")).toBeNull();
	});

	test("pressing outside closes it; pressing inside does not", () => {
		render(<Harness />);
		rightClick();
		fireEvent.pointerDown(screen.getByRole("menu"));
		expect(screen.getByRole("menu")).toBeInTheDocument();
		fireEvent.pointerDown(document.body);
		expect(screen.queryByRole("menu")).toBeNull();
	});

	test.each(["resize", "blur", "scroll"])("window %s closes it", (type) => {
		render(<Harness />);
		rightClick();
		act(() => {
			window.dispatchEvent(new Event(type));
		});
		expect(screen.queryByRole("menu")).toBeNull();
	});

	test("a pointer opening focuses the menu itself, not an item", async () => {
		render(<Harness />);
		rightClick();
		await nextFrame();
		expect(screen.getByRole("menu")).toHaveFocus();
	});

	test("the ContextMenu key opens it beside the focused element and focuses the first item", async () => {
		const onTrigger = jest.fn();
		render(<Harness onTrigger={onTrigger} />);
		surface().focus();
		const notPrevented = fireEvent.keyDown(surface(), { key: "ContextMenu" });
		expect(notPrevented).toBe(false);
		expect(onTrigger).toHaveBeenCalledWith(surface());
		await nextFrame();
		expect(screen.getByRole("menuitem", { name: /Restart/ })).toHaveFocus();
	});

	test("Shift+F10 opens it too, plain F10 does not", () => {
		render(<Harness />);
		fireEvent.keyDown(surface(), { key: "F10" });
		expect(screen.queryByRole("menu")).toBeNull();
		fireEvent.keyDown(surface(), { key: "F10", shiftKey: true });
		expect(screen.getByRole("menu")).toBeInTheDocument();
	});

	test("keyboard opening honours disabled and ignore", () => {
		const { unmount } = render(<Harness disabled />);
		fireEvent.keyDown(surface(), { key: "ContextMenu" });
		expect(screen.queryByRole("menu")).toBeNull();
		unmount();
		render(<Harness ignore={() => true} />);
		fireEvent.keyDown(surface(), { key: "ContextMenu" });
		expect(screen.queryByRole("menu")).toBeNull();
	});

	test("arrow keys move focus between enabled items and wrap; disabled rows are skipped", async () => {
		render(<Harness />);
		surface().focus();
		fireEvent.keyDown(surface(), { key: "ContextMenu" });
		const menu = screen.getByRole("menu");
		const restart = screen.getByRole("menuitem", { name: /Restart/ });
		const redeploy = screen.getByRole("menuitem", { name: "Redeploy" });
		const del = screen.getByRole("menuitem", { name: "Delete" });
		await nextFrame();
		expect(restart).toHaveFocus();
		fireEvent.keyDown(menu, { key: "ArrowDown" });
		expect(redeploy).toHaveFocus();
		fireEvent.keyDown(menu, { key: "ArrowDown" });
		expect(del).toHaveFocus();
		fireEvent.keyDown(menu, { key: "ArrowDown" });
		expect(restart).toHaveFocus();
		fireEvent.keyDown(menu, { key: "ArrowUp" });
		expect(del).toHaveFocus();
	});

	test("arrow keys with nothing focused start at the first item", () => {
		render(<Harness />);
		rightClick();
		const menu = screen.getByRole("menu");
		menu.focus();
		fireEvent.keyDown(menu, { key: "ArrowDown" });
		expect(screen.getByRole("menuitem", { name: /Restart/ })).toHaveFocus();
	});

	test("Home and End jump to the first and last enabled item", () => {
		render(<Harness />);
		rightClick();
		const menu = screen.getByRole("menu");
		fireEvent.keyDown(menu, { key: "End" });
		expect(screen.getByRole("menuitem", { name: "Delete" })).toHaveFocus();
		fireEvent.keyDown(menu, { key: "Home" });
		expect(screen.getByRole("menuitem", { name: /Restart/ })).toHaveFocus();
	});

	test("typing letters focuses the matching item, accumulating the prefix", () => {
		jest.useFakeTimers();
		render(<Harness />);
		rightClick();
		const menu = screen.getByRole("menu");
		fireEvent.keyDown(menu, { key: "d" });
		expect(screen.getByRole("menuitem", { name: "Delete" })).toHaveFocus();
		fireEvent.keyDown(menu, { key: "e" });
		fireEvent.keyDown(menu, { key: "p" });
		expect(
			screen.getByRole("menuitem", { name: "Redeploy" }),
		).not.toHaveFocus();
		// "dep" matches nothing starting with it, so focus stays; after the pause the buffer resets.
		act(() => {
			jest.advanceTimersByTime(600);
		});
		fireEvent.keyDown(menu, { key: "r" });
		fireEvent.keyDown(menu, { key: "e" });
		fireEvent.keyDown(menu, { key: "d" });
		expect(screen.getByRole("menuitem", { name: "Redeploy" })).toHaveFocus();
	});

	test("modifier chords are not typeahead", () => {
		render(<Harness />);
		rightClick();
		fireEvent.keyDown(screen.getByRole("menu"), { key: "d", ctrlKey: true });
		expect(screen.getByRole("menuitem", { name: "Delete" })).not.toHaveFocus();
	});

	test("moving the pointer over an item focuses it, except for touch", () => {
		render(<Harness />);
		rightClick();
		const redeploy = screen.getByRole("menuitem", { name: "Redeploy" });
		fireEvent.pointerMove(redeploy, { pointerType: "touch" });
		expect(redeploy).not.toHaveFocus();
		fireEvent.pointerMove(redeploy, { pointerType: "mouse" });
		expect(redeploy).toHaveFocus();
	});

	test("a second right-click while open moves the menu", () => {
		render(<Harness />);
		rightClick(surface(), 100, 100);
		rightClick(surface(), 300, 200);
		expect(screen.getAllByRole("menu")).toHaveLength(1);
		expect(screen.getByRole("menu")).toHaveStyle({
			left: "300px",
			top: "200px",
		});
	});

	test("reports open changes and works controlled", () => {
		const onOpenChange = jest.fn();
		const { rerender } = render(
			<Harness open={false} onOpenChange={onOpenChange} />,
		);
		rightClick();
		expect(onOpenChange).toHaveBeenCalledWith(true);
		// Controlled: the parent decides, so it stays shut until told.
		expect(screen.queryByRole("menu")).toBeNull();
		rerender(<Harness open onOpenChange={onOpenChange} />);
		expect(screen.getByRole("menu")).toBeInTheDocument();
		fireEvent.keyDown(screen.getByRole("menu"), { key: "Escape" });
		expect(onOpenChange).toHaveBeenLastCalledWith(false);
		expect(screen.getByRole("menu")).toBeInTheDocument();
	});

	test("defaultOpen starts it open", () => {
		render(
			<ContextMenu defaultOpen>
				<ContextMenuTrigger>
					<div>surface</div>
				</ContextMenuTrigger>
				<ContextMenuContent ariaLabel="Row actions">
					<ContextMenuItem>One</ContextMenuItem>
				</ContextMenuContent>
			</ContextMenu>,
		);
		expect(
			screen.getByRole("menu", { name: "Row actions" }),
		).toBeInTheDocument();
	});

	test("a menu without a title or label is called Context menu", () => {
		render(
			<ContextMenu defaultOpen>
				<ContextMenuTrigger>
					<div>surface</div>
				</ContextMenuTrigger>
				<ContextMenuContent>
					<ContextMenuItem inset>One</ContextMenuItem>
				</ContextMenuContent>
			</ContextMenu>,
		);
		expect(
			screen.getByRole("menu", { name: "Context menu" }),
		).toBeInTheDocument();
	});

	test("the trigger keeps its child's own handlers and classes", () => {
		const onKeyDown = jest.fn();
		const onContextMenu = jest.fn();
		render(
			<ContextMenu>
				<ContextMenuTrigger className="trigger-extra">
					{/* biome-ignore lint/a11y/noStaticElementInteractions: a bare element standing in for the trigger's child, not UI */}
					<div
						data-testid="surface"
						className="child-extra"
						onKeyDown={onKeyDown}
						onContextMenu={onContextMenu}
					/>
				</ContextMenuTrigger>
				<ContextMenuContent>
					<ContextMenuItem>One</ContextMenuItem>
				</ContextMenuContent>
			</ContextMenu>,
		);
		expect(surface()).toHaveClass("child-extra", "trigger-extra");
		fireEvent.keyDown(surface(), { key: "a" });
		expect(onKeyDown).toHaveBeenCalledTimes(1);
		rightClick();
		expect(onContextMenu).toHaveBeenCalledTimes(1);
		expect(screen.getByRole("menu")).toBeInTheDocument();
	});

	test("a child that already handled the event keeps the menu shut", () => {
		render(
			<ContextMenu>
				<ContextMenuTrigger>
					{/* biome-ignore lint/a11y/noStaticElementInteractions: a bare element standing in for the trigger's child, not UI */}
					<div
						data-testid="surface"
						onContextMenu={(event) => event.preventDefault()}
					/>
				</ContextMenuTrigger>
				<ContextMenuContent>
					<ContextMenuItem>One</ContextMenuItem>
				</ContextMenuContent>
			</ContextMenu>,
		);
		rightClick();
		expect(screen.queryByRole("menu")).toBeNull();
	});

	test("items and the trigger outside a ContextMenu say so", () => {
		const spy = jest.spyOn(console, "error").mockImplementation(() => {});
		try {
			expect(() => render(<ContextMenuItem>x</ContextMenuItem>)).toThrow(
				"ContextMenuItem must be used within <ContextMenu>",
			);
			expect(() =>
				render(
					<ContextMenuTrigger>
						<div />
					</ContextMenuTrigger>,
				),
			).toThrow("ContextMenuTrigger must be used within <ContextMenu>");
		} finally {
			spy.mockRestore();
		}
	});

	test("a trigger needs a single element", () => {
		const spy = jest.spyOn(console, "error").mockImplementation(() => {});
		try {
			expect(() =>
				render(
					<ContextMenu>
						<ContextMenuTrigger>{"text" as never}</ContextMenuTrigger>
					</ContextMenu>,
				),
			).toThrow("requires a single React element");
		} finally {
			spy.mockRestore();
		}
	});

	test("useContextMenuOpener opens the menu at a point with no trigger event", () => {
		render(
			<ContextMenu>
				<Opener />
				<ContextMenuContent ariaLabel="Canvas">
					<ContextMenuItem>One</ContextMenuItem>
				</ContextMenuContent>
			</ContextMenu>,
		);
		fireEvent.click(screen.getByRole("button", { name: "open it" }));
		expect(screen.getByRole("menu", { name: "Canvas" })).toHaveStyle({
			left: "40px",
			top: "60px",
		});
	});
});

describe("ContextMenu long press", () => {
	function touchDown(x = 50, y = 60, pointerType = "touch") {
		fireEvent.pointerDown(surface(), {
			pointerType,
			clientX: x,
			clientY: y,
			pointerId: 3,
		});
	}

	test("a touch held in place opens the menu at the press after 520ms", () => {
		jest.useFakeTimers();
		const onTrigger = jest.fn();
		render(<Harness onTrigger={onTrigger} />);
		touchDown();
		act(() => {
			jest.advanceTimersByTime(519);
		});
		expect(screen.queryByRole("menu")).toBeNull();
		act(() => {
			jest.advanceTimersByTime(1);
		});
		expect(screen.getByRole("menu")).toHaveStyle({ left: "50px", top: "60px" });
		expect(onTrigger).toHaveBeenCalledTimes(1);
	});

	test("a pen press opens it the same way", () => {
		jest.useFakeTimers();
		render(<Harness />);
		touchDown(10, 10, "pen");
		act(() => {
			jest.advanceTimersByTime(520);
		});
		expect(screen.getByRole("menu")).toBeInTheDocument();
	});

	test("a mouse press does not long-press open", () => {
		jest.useFakeTimers();
		render(<Harness />);
		touchDown(10, 10, "mouse");
		act(() => {
			jest.advanceTimersByTime(2000);
		});
		expect(screen.queryByRole("menu")).toBeNull();
	});

	test("lifting the finger before the delay cancels it", () => {
		jest.useFakeTimers();
		render(<Harness />);
		touchDown();
		act(() => {
			jest.advanceTimersByTime(300);
		});
		fireEvent.pointerUp(surface(), { pointerType: "touch", pointerId: 3 });
		act(() => {
			jest.advanceTimersByTime(2000);
		});
		expect(screen.queryByRole("menu")).toBeNull();
	});

	test("a cancelled pointer cancels it", () => {
		jest.useFakeTimers();
		render(<Harness />);
		touchDown();
		fireEvent.pointerCancel(surface(), { pointerType: "touch", pointerId: 3 });
		act(() => {
			jest.advanceTimersByTime(2000);
		});
		expect(screen.queryByRole("menu")).toBeNull();
	});

	test("dragging past the tolerance cancels it; a small wobble does not", () => {
		jest.useFakeTimers();
		const { unmount } = render(<Harness />);
		touchDown(50, 60);
		fireEvent.pointerMove(surface(), {
			clientX: 54,
			clientY: 63,
			pointerType: "touch",
		});
		act(() => {
			jest.advanceTimersByTime(520);
		});
		expect(screen.getByRole("menu")).toBeInTheDocument();
		unmount();

		render(<Harness />);
		touchDown(50, 60);
		fireEvent.pointerMove(surface(), {
			clientX: 80,
			clientY: 60,
			pointerType: "touch",
		});
		act(() => {
			jest.advanceTimersByTime(2000);
		});
		expect(screen.queryByRole("menu")).toBeNull();
	});

	test("the press holds text selection off for its duration only", () => {
		render(<Harness />);
		touchDown();
		expect(surface().style.getPropertyValue("user-select")).toBe("none");
		fireEvent.pointerUp(surface(), { pointerType: "touch", pointerId: 3 });
		expect(surface().style.getPropertyValue("user-select")).toBe("");
	});

	test("longPress=false ignores the press and keeps selection available", () => {
		jest.useFakeTimers();
		render(<Harness longPress={false} />);
		touchDown();
		expect(surface().style.getPropertyValue("user-select")).toBe("");
		act(() => {
			jest.advanceTimersByTime(2000);
		});
		expect(screen.queryByRole("menu")).toBeNull();
	});

	test("a disabled trigger ignores the press", () => {
		jest.useFakeTimers();
		render(<Harness disabled />);
		touchDown();
		act(() => {
			jest.advanceTimersByTime(2000);
		});
		expect(screen.queryByRole("menu")).toBeNull();
	});

	test("a right-click mid-press ends the press so the timer cannot reopen it later", () => {
		jest.useFakeTimers();
		render(<Harness />);
		touchDown();
		rightClick(surface(), 200, 200);
		fireEvent.keyDown(screen.getByRole("menu"), { key: "Escape" });
		act(() => {
			jest.advanceTimersByTime(2000);
		});
		expect(screen.queryByRole("menu")).toBeNull();
	});
});

describe("ContextMenu open state wiring", () => {
	test("re-opening after a close starts with no highlighted item", () => {
		render(<Wrapper />);
		rightClick();
		fireEvent.focus(screen.getByRole("menuitem", { name: "Redeploy" }));
		expect(screen.getByRole("menuitem", { name: "Redeploy" })).toHaveClass(
			"bg-white/7",
		);
		fireEvent.keyDown(screen.getByRole("menu"), { key: "Escape" });
		rightClick();
		for (const item of screen.getAllByRole("menuitem")) {
			expect(item).not.toHaveClass("bg-white/7");
		}
	});
});
