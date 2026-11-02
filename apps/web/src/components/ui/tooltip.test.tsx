import { describe, expect, it } from "bun:test";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
	Tooltip,
	TooltipContent,
	TooltipProvider,
	TooltipTrigger,
} from "#/components/ui/tooltip";

// Base UI gives the tooltip popup no ARIA role, so it is found by its slot.
const queryTip = () =>
	document.querySelector<HTMLElement>('[data-slot="tooltip-content"]');
const findTip = () =>
	waitFor(
		() => {
			const tip = queryTip();
			if (!tip) throw new Error("tooltip not shown");
			return tip;
		},
		{ timeout: 3000 },
	);

function Example({
	disabled,
	contentProps,
	open,
	onOpenChange,
}: {
	disabled?: boolean;
	contentProps?: Partial<React.ComponentProps<typeof TooltipContent>>;
	open?: boolean;
	onOpenChange?: (open: boolean) => void;
}) {
	return (
		<TooltipProvider delay={0}>
			<Tooltip
				disabled={disabled}
				{...(open === undefined ? {} : { open })}
				onOpenChange={onOpenChange}
			>
				<TooltipTrigger>Hover me</TooltipTrigger>
				<TooltipContent {...contentProps}>Copy to clipboard</TooltipContent>
			</Tooltip>
		</TooltipProvider>
	);
}

describe("Tooltip", () => {
	it("is hidden until the trigger is hovered, then shows its text", async () => {
		const user = userEvent.setup();
		render(<Example />);
		expect(queryTip()).toBeNull();

		await user.hover(screen.getByRole("button", { name: "Hover me" }));

		const tip = await findTip();
		expect(tip).toHaveTextContent("Copy to clipboard");
		expect(tip).toBeVisible();
	});

	it("hides again when the pointer leaves", async () => {
		const user = userEvent.setup();
		render(<Example />);
		const trigger = screen.getByRole("button", { name: "Hover me" });
		await user.hover(trigger);
		await findTip();

		await user.unhover(trigger);

		await waitFor(() => expect(queryTip()).toBeNull());
	});

	it("opens on keyboard focus and Escape closes it", async () => {
		const user = userEvent.setup();
		render(<Example />);

		await user.tab();
		expect(screen.getByRole("button", { name: "Hover me" })).toHaveFocus();
		await findTip();

		await user.keyboard("{Escape}");
		await waitFor(() => expect(queryTip()).toBeNull());
	});

	it("a disabled tooltip never opens", async () => {
		const user = userEvent.setup();
		render(<Example disabled />);

		await user.hover(screen.getByRole("button", { name: "Hover me" }));
		await user.tab();

		expect(queryTip()).toBeNull();
	});

	it("controlled: reports the request but follows `open`", async () => {
		const user = userEvent.setup();
		const requests: boolean[] = [];
		render(
			<Example open={false} onOpenChange={(next) => requests.push(next)} />,
		);

		await user.hover(screen.getByRole("button", { name: "Hover me" }));

		await waitFor(() => expect(requests).toContain(true));
		expect(queryTip()).toBeNull();
	});

	it("controlled open shows the tooltip without any interaction", async () => {
		render(<Example open />);
		expect(await findTip()).toHaveTextContent("Copy to clipboard");
	});

	it("positions on the requested side and merges className", async () => {
		const user = userEvent.setup();
		render(<Example contentProps={{ side: "bottom", className: "mine" }} />);
		await user.hover(screen.getByRole("button", { name: "Hover me" }));
		const tip = await findTip();
		expect(tip).toHaveClass("mine");
		expect(tip.parentElement).toHaveAttribute("data-side", "bottom");
	});

	it("defaults to the top side", async () => {
		const user = userEvent.setup();
		render(<Example />);
		await user.hover(screen.getByRole("button", { name: "Hover me" }));
		const tip = await findTip();
		expect(tip.parentElement).toHaveAttribute("data-side", "top");
	});

	it("the provider's default delay keeps the tooltip closed straight after the pointer arrives", async () => {
		const user = userEvent.setup();
		render(
			<TooltipProvider>
				<Tooltip>
					<TooltipTrigger>Slow</TooltipTrigger>
					<TooltipContent>later</TooltipContent>
				</Tooltip>
			</TooltipProvider>,
		);
		await user.hover(screen.getByRole("button", { name: "Slow" }));
		expect(queryTip()).toBeNull();
		expect(await findTip()).toHaveTextContent("later");
	});
});
