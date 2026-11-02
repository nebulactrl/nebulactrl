import { afterEach, beforeEach, describe, expect, it, mock } from "bun:test";
import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import {
	Tabs,
	TabsContent,
	TabsList,
	TabsTrigger,
	tabsListVariants,
	tabsTriggerVariants,
} from "#/components/ui/tabs";
import { installLayoutEnv, type LayoutEnv } from "./layout-env.testkit";

/** The panel the selected tab controls; a panel that is playing its exit stays in the DOM, inert. */
function activePanel(): HTMLElement {
	const selected = screen.getByRole("tab", { selected: true });
	const panel = document.getElementById(
		selected.getAttribute("aria-controls") ?? "",
	);
	if (!panel) throw new Error("selected tab controls no panel");
	return panel;
}

function Demo({
	variant,
	onValueChange,
	...root
}: {
	variant?: "default" | "underline" | "sheet" | "pressed";
	onValueChange?: (v: unknown) => void;
} & Partial<React.ComponentProps<typeof Tabs>>) {
	return (
		<Tabs defaultValue="a" onValueChange={onValueChange} {...root}>
			<TabsList {...(variant ? { variant } : {})} aria-label="Sections">
				<TabsTrigger value="a">Alpha</TabsTrigger>
				<TabsTrigger value="b">Beta</TabsTrigger>
				<TabsTrigger value="c" disabled>
					Gamma
				</TabsTrigger>
			</TabsList>
			<TabsContent value="a">Alpha panel</TabsContent>
			<TabsContent value="b">Beta panel</TabsContent>
		</Tabs>
	);
}

function Harness() {
	const [value, setValue] = useState<unknown>("a");
	return <Demo value={value as string} onValueChange={setValue} />;
}

describe("Tabs", () => {
	let env: LayoutEnv;
	beforeEach(() => {
		env = installLayoutEnv();
	});
	afterEach(() => env.restore());

	it("exposes a tablist of tabs with the default one selected and its panel shown", () => {
		render(<Demo />);
		expect(
			screen.getByRole("tablist", { name: "Sections" }),
		).toBeInTheDocument();
		expect(screen.getByRole("tab", { name: "Alpha" })).toHaveAttribute(
			"aria-selected",
			"true",
		);
		expect(screen.getByRole("tab", { name: "Beta" })).toHaveAttribute(
			"aria-selected",
			"false",
		);
		expect(screen.getByRole("tabpanel")).toHaveTextContent("Alpha panel");
		expect(screen.getByRole("tab", { name: "Alpha" })).toHaveAttribute(
			"aria-controls",
			screen.getByRole("tabpanel").id,
		);
		expect(screen.queryByText("Beta panel")).not.toBeInTheDocument();
	});

	it("selects a tab on click, swaps the panel and reports the value", async () => {
		const onValueChange = mock();
		render(<Demo onValueChange={onValueChange} />);
		await userEvent.setup().click(screen.getByRole("tab", { name: "Beta" }));
		expect(screen.getByRole("tab", { name: "Beta" })).toHaveAttribute(
			"aria-selected",
			"true",
		);
		expect(activePanel()).toHaveTextContent("Beta panel");
		expect(activePanel()).not.toHaveAttribute("inert");
		expect(onValueChange.mock.calls[0]?.[0]).toBe("b");
	});

	it("arrow keys move focus between tabs and Enter or Space selects the focused one", async () => {
		const onValueChange = mock();
		const user = userEvent.setup();
		render(<Demo onValueChange={onValueChange} />);
		await user.tab();
		expect(screen.getByRole("tab", { name: "Alpha" })).toHaveFocus();
		await user.keyboard("{ArrowRight}");
		expect(screen.getByRole("tab", { name: "Beta" })).toHaveFocus();
		// Focus alone does not switch the panel.
		expect(screen.getByRole("tab", { name: "Alpha" })).toHaveAttribute(
			"aria-selected",
			"true",
		);
		expect(onValueChange).not.toHaveBeenCalled();
		await user.keyboard("{Enter}");
		expect(screen.getByRole("tab", { name: "Beta" })).toHaveAttribute(
			"aria-selected",
			"true",
		);
		await user.keyboard("{ArrowLeft}");
		await user.keyboard(" ");
		expect(screen.getByRole("tab", { name: "Alpha" })).toHaveAttribute(
			"aria-selected",
			"true",
		);
		expect(onValueChange.mock.calls.map((c) => c[0])).toEqual(["b", "a"]);
	});

	it("Home and End jump to the first and last tab", async () => {
		const user = userEvent.setup();
		render(<Demo />);
		await user.tab();
		await user.keyboard("{End}");
		expect(screen.getByRole("tab", { name: "Gamma" })).toHaveFocus();
		await user.keyboard("{Home}");
		expect(screen.getByRole("tab", { name: "Alpha" })).toHaveFocus();
	});

	it("only the selected tab is in the tab order", () => {
		render(<Demo />);
		expect(screen.getByRole("tab", { name: "Alpha" })).toHaveAttribute(
			"tabindex",
			"0",
		);
		expect(screen.getByRole("tab", { name: "Beta" })).toHaveAttribute(
			"tabindex",
			"-1",
		);
	});

	it("activateOnFocus selects as the arrow keys move", async () => {
		const onValueChange = mock();
		const user = userEvent.setup();
		render(
			<Tabs defaultValue="a" onValueChange={onValueChange}>
				<TabsList aria-label="s" activateOnFocus>
					<TabsTrigger value="a">Alpha</TabsTrigger>
					<TabsTrigger value="b">Beta</TabsTrigger>
				</TabsList>
			</Tabs>,
		);
		await user.tab();
		await user.keyboard("{ArrowRight}");
		expect(screen.getByRole("tab", { name: "Beta" })).toHaveAttribute(
			"aria-selected",
			"true",
		);
		expect(onValueChange.mock.calls[0]?.[0]).toBe("b");
	});

	it("does not select a disabled tab", async () => {
		const onValueChange = mock();
		render(<Demo onValueChange={onValueChange} />);
		const user = userEvent.setup();
		await user.click(screen.getByRole("tab", { name: "Gamma" }));
		await user.keyboard("{Enter}");
		expect(onValueChange).not.toHaveBeenCalled();
		expect(screen.getByRole("tab", { name: "Alpha" })).toHaveAttribute(
			"aria-selected",
			"true",
		);
	});

	it("is controlled: the selection only changes when the parent updates it", async () => {
		render(<Demo value="a" />);
		await userEvent.setup().click(screen.getByRole("tab", { name: "Beta" }));
		expect(screen.getByRole("tab", { name: "Alpha" })).toHaveAttribute(
			"aria-selected",
			"true",
		);
	});

	it("follows parent state", async () => {
		render(<Harness />);
		await userEvent.setup().click(screen.getByRole("tab", { name: "Beta" }));
		expect(activePanel()).toHaveTextContent("Beta panel");
	});

	it("reports orientation and tags its slots", () => {
		const { rerender } = render(<Demo />);
		expect(document.querySelector("[data-slot=tabs]")).toHaveAttribute(
			"data-orientation",
			"horizontal",
		);
		expect(screen.getByRole("tab", { name: "Alpha" })).toHaveAttribute(
			"data-slot",
			"tabs-trigger",
		);
		expect(screen.getByRole("tabpanel")).toHaveAttribute(
			"data-slot",
			"tabs-content",
		);
		rerender(<Demo orientation="vertical" />);
		expect(document.querySelector("[data-slot=tabs]")).toHaveAttribute(
			"data-orientation",
			"vertical",
		);
	});

	it("default variant draws no glide indicator", () => {
		render(<Demo />);
		const list = screen.getByRole("tablist");
		expect(list).toHaveAttribute("data-variant", "default");
		expect(list).not.toHaveAttribute("data-glide");
		expect(list.querySelector("[data-ind]")).toBeNull();
	});

	it.each(["underline", "sheet", "pressed"] as const)(
		"%s variant draws a hidden decorative indicator",
		(variant) => {
			render(<Demo variant={variant} />);
			const list = screen.getByRole("tablist");
			expect(list).toHaveAttribute("data-variant", variant);
			expect(list).toHaveAttribute("data-glide", "u");
			expect(list.querySelector("[data-ind]")).toHaveAttribute(
				"aria-hidden",
				"true",
			);
		},
	);

	it("underline variant glides a 2px bar under the selected tab", async () => {
		env.layout = (el) =>
			el.textContent === "Alpha"
				? { offsetLeft: 5, offsetWidth: 60 }
				: el.textContent === "Beta"
					? { offsetLeft: 90, offsetWidth: 40 }
					: undefined;
		render(<Demo variant="underline" />);
		await env.flush();
		const ind = document.querySelector("[data-ind]") as HTMLElement;
		expect(ind.style.width).toBe("60px");
		expect(ind.style.transform).toBe("translateX(5px)");
		expect(ind.style.height).toBe("2px");
		await userEvent.setup().click(screen.getByRole("tab", { name: "Beta" }));
		await env.flush();
		expect(ind.style.width).toBe("40px");
		expect(ind.style.transform).toBe("translateX(90px)");
		expect(ind.style.transition).toBe("");
	});

	it("pressed variant sizes a raised thumb to the selected tab", async () => {
		env.layout = (el) =>
			el.textContent === "Alpha"
				? { offsetLeft: 2, offsetTop: 2, offsetWidth: 60, offsetHeight: 28 }
				: undefined;
		render(<Demo variant="pressed" />);
		await env.flush();
		const ind = document.querySelector("[data-ind]") as HTMLElement;
		expect(ind.style.height).toBe("28px");
		expect(ind.style.transform).toBe("translate(2px, 2px)");
	});

	it("fades the continuing end of a scrolling underline strip, but not a sheet strip", async () => {
		env.layout = (el) =>
			el.getAttribute("role") === "tablist"
				? { clientWidth: 100, scrollWidth: 400 }
				: undefined;
		const { unmount } = render(<Demo variant="underline" />);
		const list = screen.getByRole("tablist");
		await act(async () => {
			list.dispatchEvent(new Event("scroll"));
		});
		expect(list.style.maskImage).toContain("linear-gradient");
		unmount();
		render(<Demo variant="sheet" />);
		const sheet = screen.getByRole("tablist");
		await act(async () => {
			sheet.dispatchEvent(new Event("scroll"));
		});
		expect(sheet.style.maskImage).toBeFalsy();
	});

	it("forwards the list ref as object and function", () => {
		const objectRef: { current: HTMLDivElement | null } = { current: null };
		let fnNode = null as HTMLDivElement | null;
		const { unmount } = render(
			<Tabs defaultValue="a">
				<TabsList ref={objectRef} aria-label="o">
					<TabsTrigger value="a">A</TabsTrigger>
				</TabsList>
				<TabsList
					ref={(n) => {
						fnNode = n;
					}}
					aria-label="f"
				>
					<TabsTrigger value="b">B</TabsTrigger>
				</TabsList>
			</Tabs>,
		);
		expect(objectRef.current).toBe(screen.getByRole("tablist", { name: "o" }));
		expect(fnNode).toBe(screen.getByRole("tablist", { name: "f" }));
		unmount();
	});

	it("merges classNames on list, trigger and content", () => {
		render(
			<Tabs defaultValue="a" className="root-x">
				<TabsList className="list-x" aria-label="s">
					<TabsTrigger value="a" className="trig-x">
						A
					</TabsTrigger>
				</TabsList>
				<TabsContent value="a" className="body-x">
					body
				</TabsContent>
			</Tabs>,
		);
		expect(document.querySelector("[data-slot=tabs]")).toHaveClass("root-x");
		expect(screen.getByRole("tablist")).toHaveClass("list-x");
		expect(screen.getByRole("tab")).toHaveClass("trig-x");
		expect(screen.getByRole("tabpanel")).toHaveClass("body-x");
	});
});

describe("tab variants", () => {
	it("list variants differ per look", () => {
		expect(tabsListVariants({ variant: "sheet" })).toContain("border-b-0");
		expect(tabsListVariants({ variant: "pressed" })).toContain(
			"bg-surface-well",
		);
		expect(tabsListVariants({ variant: "underline" })).toContain("gap-[22px]");
		expect(tabsListVariants({})).toContain("gap-0.5");
	});

	it("trigger variants mark the active tab", () => {
		expect(tabsTriggerVariants({ variant: "default", active: true })).toContain(
			"font-semibold",
		);
		expect(
			tabsTriggerVariants({ variant: "underline", active: true }),
		).toContain("border-accent-ink");
		expect(tabsTriggerVariants({ variant: "pressed", active: true })).toContain(
			"thumb-raised",
		);
		expect(
			tabsTriggerVariants({ variant: "pressed", active: false }),
		).not.toContain("thumb-raised");
	});
});
