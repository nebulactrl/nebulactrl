import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DitherButton } from "./button";

const finishLayers = (button: HTMLElement) =>
	button.querySelector("[data-slot=button-finish]");

/** The class of the finish layer on top of a button; fails when it has none. */
const lastLayerClass = (name: string) => {
	const layer = finishLayers(screen.getByText(name))?.lastElementChild;
	if (!layer) throw new Error(`no finish layers on ${name}`);
	return layer.className;
};

describe("DitherButton behaviour", () => {
	test("is a button element that defaults to type=button so it never submits a form", () => {
		render(<DitherButton>Save</DitherButton>);
		const button = screen.getByRole("button", { name: "Save" });
		expect(button.tagName).toBe("BUTTON");
		expect(button).toHaveAttribute("type", "button");
		expect(button).toHaveAttribute("data-finish", "dither");
	});

	test("keeps an explicit type", () => {
		render(<DitherButton type="submit">Send</DitherButton>);
		expect(screen.getByRole("button", { name: "Send" })).toHaveAttribute(
			"type",
			"submit",
		);
	});

	test("click handlers run", async () => {
		let clicks = 0;
		render(<DitherButton onClick={() => clicks++}>Go</DitherButton>);
		await userEvent.setup().click(screen.getByRole("button", { name: "Go" }));
		expect(clicks).toBe(1);
	});

	test("a disabled button does not fire clicks", async () => {
		let clicks = 0;
		render(
			<DitherButton disabled onClick={() => clicks++}>
				Go
			</DitherButton>,
		);
		const button = screen.getByRole("button", { name: "Go" });
		expect(button).toBeDisabled();
		await userEvent.setup().click(button);
		expect(clicks).toBe(0);
	});

	test("the label is the accessible name; the decoration is aria-hidden", () => {
		render(
			<DitherButton bloom="low" loading>
				Deploy
			</DitherButton>,
		);
		const button = screen.getByRole("button", { name: "Deploy" });
		for (const slot of ["button-finish", "button-bloom", "button-loading"]) {
			expect(button.querySelector(`[data-slot=${slot}]`)).toHaveAttribute(
				"aria-hidden",
				"true",
			);
		}
	});
});

describe("DitherButton loading", () => {
	test("sets aria-busy and data-loading and shows three busy cells at the cell size", () => {
		render(
			<DitherButton loading cell={3}>
				Deploy
			</DitherButton>,
		);
		const button = screen.getByRole("button", { name: "Deploy" });
		expect(button).toHaveAttribute("aria-busy", "true");
		expect(button).toHaveAttribute("data-loading");
		const cells = button.querySelectorAll<HTMLElement>(
			"[data-slot=button-loading] i",
		);
		expect(cells).toHaveLength(3);
		expect(Array.from(cells, (c) => c.style.width)).toEqual([
			"3px",
			"3px",
			"3px",
		]);
		// The cells step one after the other.
		expect(Array.from(cells, (c) => c.style.animationDelay)).toEqual([
			"0ms",
			"160ms",
			"320ms",
		]);
	});

	test("swallows clicks while loading and does not bubble them", async () => {
		let clicks = 0;
		let bubbled = 0;
		render(
			// biome-ignore lint/a11y/noStaticElementInteractions: a stand-in parent that listens for clicks
			// biome-ignore lint/a11y/useKeyWithClickEvents: only click bubbling is under test
			<div onClick={() => bubbled++}>
				<DitherButton loading onClick={() => clicks++}>
					Deploy
				</DitherButton>
			</div>,
		);
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Deploy" }));
		expect(clicks).toBe(0);
		expect(bubbled).toBe(0);
	});

	test("an idle button has no busy markers and passes clicks through", async () => {
		let clicks = 0;
		render(<DitherButton onClick={() => clicks++}>Deploy</DitherButton>);
		const button = screen.getByRole("button", { name: "Deploy" });
		expect(button).not.toHaveAttribute("aria-busy");
		expect(button).not.toHaveAttribute("data-loading");
		expect(button.querySelector("[data-slot=button-loading]")).toBeNull();
		await userEvent.setup().click(button);
		expect(clicks).toBe(1);
	});
});

describe("DitherButton asChild", () => {
	test("lends the look to a link without nesting a second element", () => {
		render(
			<DitherButton asChild>
				<a href="/projects">Projects</a>
			</DitherButton>,
		);
		const link = screen.getByRole("button", { name: "Projects" });
		expect(link.tagName).toBe("A");
		expect(link).toHaveAttribute("href", "/projects");
		expect(link).toHaveAttribute("data-finish", "dither");
		expect(link.querySelector("a")).toBeNull();
		expect(link).not.toHaveAttribute("type");
	});

	test("with a non-element child it is an ordinary button", () => {
		render(<DitherButton asChild>plain text</DitherButton>);
		expect(screen.getByRole("button", { name: "plain text" }).tagName).toBe(
			"BUTTON",
		);
	});

	test("render prop swaps the element and drops the default type", () => {
		render(
			<DitherButton render={<a href="/docs" />} nativeButton={false}>
				Docs
			</DitherButton>,
		);
		const link = screen.getByRole("button", { name: "Docs" });
		expect(link.tagName).toBe("A");
		expect(link).not.toHaveAttribute("type");
	});
});

describe("DitherButton colour", () => {
	test("without a colour the variant keeps its own look (no inline colours)", () => {
		render(<DitherButton>Plain</DitherButton>);
		const button = screen.getByRole("button", { name: "Plain" });
		expect(button.style.color).toBe("");
		expect(button.style.boxShadow).toBe("");
	});

	test("a filled key with a colour gets a contrasting label and a tile shadow", () => {
		render(<DitherButton color="blue">Go</DitherButton>);
		const button = screen.getByRole("button", { name: "Go" });
		expect(button.style.color).toBe("var(--color-surface-base)");
		expect(button.style.boxShadow).toContain("var(--shadow-tile)");
	});

	test("the filled variant is the primary key", () => {
		render(
			<>
				<DitherButton variant="filled">filled</DitherButton>
				<DitherButton variant="primary">primary</DitherButton>
			</>,
		);
		expect(screen.getByText("filled").className).toBe(
			screen.getByText("primary").className,
		);
	});

	test("an outline key prints its label in the colour and drops the shadow", () => {
		render(
			<DitherButton variant="outline" color="--chart-1">
				Outlined
			</DitherButton>,
		);
		const button = screen.getByRole("button", { name: "Outlined" });
		expect(button.style.color).toBe("var(--chart-1)");
		expect(button.style.boxShadow).toBe("none");
	});

	test("secondary counts as an outline family and ghost as text", () => {
		render(
			<>
				<DitherButton variant="secondary" color="red">
					secondary
				</DitherButton>
				<DitherButton variant="ghost" color="red">
					ghost
				</DitherButton>
			</>,
		);
		expect(screen.getByText("secondary").style.boxShadow).toBe("none");
		const ghost = screen.getByText("ghost");
		expect(ghost.style.color).toBe("var(--dither-red)");
		// Text keys change only the label colour.
		expect(ghost.style.boxShadow).toBe("");
	});

	test("danger-solid is a filled family", () => {
		render(
			<DitherButton variant="danger-solid" color="red">
				delete
			</DitherButton>,
		);
		expect(screen.getByText("delete").style.color).toBe(
			"var(--color-surface-base)",
		);
	});

	test("caller style overrides the colour's own and survives; function styles are merged", () => {
		render(
			<>
				<DitherButton color="blue" variant="ghost" style={{ color: "red" }}>
					object
				</DitherButton>
				<DitherButton
					color="blue"
					variant="ghost"
					style={() => ({ opacity: 0.5 })}
				>
					fn
				</DitherButton>
			</>,
		);
		expect(screen.getByText("object").style.color).toBe("red");
		const fn = screen.getByText("fn");
		expect(fn.style.color).toBe("var(--dither-blue)");
		expect(fn.style.opacity).toBe("0.5");
	});
});

describe("DitherButton bloom and finish", () => {
	test("bloom low and high draw a glow layer, off draws none", () => {
		render(
			<>
				<DitherButton bloom="low">low</DitherButton>
				<DitherButton bloom="high">high</DitherButton>
				<DitherButton bloom="off">off</DitherButton>
				<DitherButton>none</DitherButton>
			</>,
		);
		const glow = (name: string) =>
			screen
				.getByText(name)
				.querySelector<HTMLElement>("[data-slot=button-bloom]");
		expect(glow("low")?.style.boxShadow).toContain("12px");
		expect(glow("high")?.style.boxShadow).toContain("22px");
		expect(glow("off")).toBeNull();
		expect(glow("none")).toBeNull();
	});

	test("a filled key has highlight and shade layers; a coloured outline key only the shade", () => {
		render(
			<>
				<DitherButton>filled</DitherButton>
				<DitherButton variant="outline" color="blue">
					outlined
				</DitherButton>
			</>,
		);
		const filled = finishLayers(screen.getByText("filled"));
		const outlined = finishLayers(screen.getByText("outlined"));
		expect(filled?.children).toHaveLength(2);
		expect(outlined?.children).toHaveLength(1);
	});

	test("an outline key with a colour tints its shade layer with that colour", () => {
		render(
			<DitherButton variant="outline" color="green">
				tint
			</DitherButton>,
		);
		const shade = finishLayers(screen.getByText("tint"))
			?.firstElementChild as HTMLElement;
		expect(shade.style.backgroundColor).toBe("var(--dither-green)");
	});

	test("fill, cell and matrix change the mask that cuts the finish", () => {
		render(
			<>
				<DitherButton>base</DitherButton>
				<DitherButton fill="solid">solid</DitherButton>
				<DitherButton fill="dotted">dotted</DitherButton>
				<DitherButton cell={4}>cell</DitherButton>
				<DitherButton matrix="blue-noise">noise</DitherButton>
			</>,
		);
		const masks = ["base", "solid", "dotted", "cell", "noise"].map(
			(name) => lastLayerClass(name).match(/nc-dm-\d+/)?.[0],
		);
		expect(masks.every(Boolean)).toBe(true);
		expect(new Set(masks).size).toBe(5);
	});

	test("equal props share one mask class", () => {
		render(
			<>
				<DitherButton>one</DitherButton>
				<DitherButton>two</DitherButton>
			</>,
		);
		const mask = (name: string) => lastLayerClass(name).match(/nc-dm-\d+/)?.[0];
		expect(mask("one")).toBe(mask("two"));
	});

	test("className is added after the button's own classes", () => {
		render(<DitherButton className="w-full">wide</DitherButton>);
		expect(screen.getByText("wide")).toHaveClass("w-full", "relative");
	});
});
