import { describe, expect, it, mock } from "bun:test";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createRef } from "react";
import {
	Popover,
	PopoverContent,
	PopoverDescription,
	PopoverHeader,
	PopoverTitle,
	PopoverTrigger,
} from "#/components/ui/popover";

function Example({
	onOpenChange,
	contentProps,
	open,
}: {
	onOpenChange?: (open: boolean, details: { reason: string }) => void;
	contentProps?: Partial<React.ComponentProps<typeof PopoverContent>>;
	open?: boolean;
}) {
	return (
		<>
			<button type="button">outside</button>
			<Popover
				{...(open === undefined ? {} : { open })}
				onOpenChange={onOpenChange}
			>
				<PopoverTrigger>Filters</PopoverTrigger>
				<PopoverContent {...contentProps}>
					<PopoverHeader>
						<PopoverTitle>Filter builds</PopoverTitle>
						<PopoverDescription>Narrow the list.</PopoverDescription>
					</PopoverHeader>
					<button type="button">Apply</button>
				</PopoverContent>
			</Popover>
		</>
	);
}

const queryPop = () => document.querySelector('[data-slot="popover-content"]');

describe("Popover", () => {
	it("opens from its trigger, toggling aria-expanded, and shows labelled content", async () => {
		const user = userEvent.setup();
		render(<Example />);
		const trigger = screen.getByRole("button", { name: "Filters" });
		expect(trigger).toHaveAttribute("aria-expanded", "false");
		expect(queryPop()).toBeNull();

		await user.click(trigger);

		const dialog = await screen.findByRole("dialog", { name: "Filter builds" });
		expect(dialog).toHaveAccessibleDescription("Narrow the list.");
		expect(dialog).toHaveAttribute("data-slot", "popover-content");
		expect(trigger).toHaveAttribute("aria-expanded", "true");
	});

	it("a second click on the trigger closes it", async () => {
		const user = userEvent.setup();
		const onOpenChange = mock((_o: boolean, _d: { reason: string }) => {});
		render(<Example onOpenChange={onOpenChange} />);
		const trigger = screen.getByRole("button", { name: "Filters" });
		await user.click(trigger);
		await screen.findByRole("dialog");

		await user.click(trigger);

		await waitFor(() => expect(queryPop()).toBeNull());
		expect(onOpenChange.mock.calls.map((c) => c[0])).toEqual([true, false]);
		expect(onOpenChange.mock.calls[1]?.[1].reason).toBe("trigger-press");
	});

	it("Escape closes it and focus returns to the trigger", async () => {
		const user = userEvent.setup();
		const onOpenChange = mock((_o: boolean, _d: { reason: string }) => {});
		render(<Example onOpenChange={onOpenChange} />);
		const trigger = screen.getByRole("button", { name: "Filters" });
		await user.click(trigger);
		await screen.findByRole("dialog");

		await user.keyboard("{Escape}");

		await waitFor(() => expect(queryPop()).toBeNull());
		expect(onOpenChange.mock.calls.at(-1)?.[1].reason).toBe("escape-key");
		await waitFor(() => expect(trigger).toHaveFocus());
	});

	it("a press outside closes it; a press inside does not", async () => {
		const user = userEvent.setup();
		const onOpenChange = mock((_o: boolean, _d: { reason: string }) => {});
		render(<Example onOpenChange={onOpenChange} />);
		await user.click(screen.getByRole("button", { name: "Filters" }));
		await screen.findByRole("dialog");

		await user.click(screen.getByRole("button", { name: "Apply" }));
		await user.click(screen.getByText("Filter builds"));
		expect(queryPop()).not.toBeNull();

		await user.click(screen.getByRole("button", { name: "outside" }));

		await waitFor(() => expect(queryPop()).toBeNull());
		// The press may be reported as the focus it moves out of the popover.
		expect(["outside-press", "focus-out"]).toContain(
			onOpenChange.mock.calls.at(-1)?.[1].reason as string,
		);
	});

	it("opens from the keyboard (Enter on the focused trigger)", async () => {
		const user = userEvent.setup();
		render(<Example />);
		screen.getByRole("button", { name: "Filters" }).focus();
		await user.keyboard("{Enter}");
		await screen.findByRole("dialog", { name: "Filter builds" });
	});

	it("marks the popup data-instant when the keyboard opened it, not when the pointer did", async () => {
		const user = userEvent.setup();
		const { unmount } = render(<Example />);
		await user.click(screen.getByRole("button", { name: "Filters" }));
		expect(await screen.findByRole("dialog")).not.toHaveAttribute(
			"data-instant",
		);
		unmount();

		render(<Example />);
		screen.getByRole("button", { name: "Filters" }).focus();
		await user.keyboard("{Enter}");
		expect(await screen.findByRole("dialog")).toHaveAttribute("data-instant");
		fireEvent.pointerDown(document.body);
	});

	it("controlled: announces the request but follows `open`", async () => {
		const user = userEvent.setup();
		const onOpenChange = mock((_o: boolean, _d: { reason: string }) => {});
		render(<Example open={false} onOpenChange={onOpenChange} />);

		await user.click(screen.getByRole("button", { name: "Filters" }));

		expect(onOpenChange.mock.calls[0]?.[0]).toBe(true);
		expect(queryPop()).toBeNull();
	});

	it("controlled open renders the content immediately", async () => {
		render(<Example open />);
		expect(
			await screen.findByRole("dialog", { name: "Filter builds" }),
		).toBeInTheDocument();
	});

	it("positions on the requested side/align, and defaults to bottom/center", async () => {
		const user = userEvent.setup();
		const { unmount } = render(<Example />);
		await user.click(screen.getByRole("button", { name: "Filters" }));
		const dialog = await screen.findByRole("dialog");
		expect(dialog.parentElement).toHaveAttribute("data-side", "bottom");
		expect(dialog.parentElement).toHaveAttribute("data-align", "center");
		unmount();

		render(
			<Example
				contentProps={{ side: "top", align: "end", className: "mine" }}
			/>,
		);
		await user.click(screen.getByRole("button", { name: "Filters" }));
		const placed = await screen.findByRole("dialog");
		expect(placed.parentElement).toHaveAttribute("data-side", "top");
		expect(placed.parentElement).toHaveAttribute("data-align", "end");
		expect(placed).toHaveClass("mine");
	});

	it("forwards a caller's ref to the popup element", async () => {
		const user = userEvent.setup();
		const ref = createRef<HTMLDivElement>();
		render(<Example contentProps={{ ref }} />);
		await user.click(screen.getByRole("button", { name: "Filters" }));
		const dialog = await screen.findByRole("dialog");
		expect(ref.current as HTMLElement | null).toBe(dialog);
	});

	it("exposes slots on trigger, header, title and description", async () => {
		const user = userEvent.setup();
		render(<Example />);
		const trigger = screen.getByRole("button", { name: "Filters" });
		expect(trigger).toHaveAttribute("data-slot", "popover-trigger");
		await user.click(trigger);
		await screen.findByRole("dialog");
		expect(screen.getByText("Filter builds")).toHaveAttribute(
			"data-slot",
			"popover-title",
		);
		expect(screen.getByText("Narrow the list.")).toHaveAttribute(
			"data-slot",
			"popover-description",
		);
		expect(screen.getByText("Filter builds").parentElement).toHaveAttribute(
			"data-slot",
			"popover-header",
		);
	});
});
