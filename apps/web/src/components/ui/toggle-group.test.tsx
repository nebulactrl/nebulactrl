import { afterEach, beforeEach, describe, expect, it, mock } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createRef, useState } from "react";
import { ToggleGroup, ToggleGroupItem } from "#/components/ui/toggle-group";
import { installLayoutEnv, type LayoutEnv } from "./layout-env.testkit";

function Harness() {
	const [value, setValue] = useState<string[]>(["a"]);
	return (
		<ToggleGroup value={value} onValueChange={setValue}>
			<ToggleGroupItem value="a">A</ToggleGroupItem>
			<ToggleGroupItem value="b">B</ToggleGroupItem>
		</ToggleGroup>
	);
}

function Seg({ indicator }: { indicator?: "glide" | "instant" }) {
	const [value, setValue] = useState(["A"]);
	return (
		<ToggleGroup
			variant="segmented"
			{...(indicator ? { indicator } : {})}
			value={value}
			onValueChange={(v) => v.length && setValue(v)}
			aria-label="seg"
		>
			<ToggleGroupItem value="A">A</ToggleGroupItem>
			<ToggleGroupItem value="B">B</ToggleGroupItem>
		</ToggleGroup>
	);
}

describe("ToggleGroup selection", () => {
	it("single-select (default): picking another item moves the selection and reports it", async () => {
		const onValueChange = mock();
		const user = userEvent.setup();
		render(
			<ToggleGroup defaultValue={["a"]} onValueChange={onValueChange}>
				<ToggleGroupItem value="a">A</ToggleGroupItem>
				<ToggleGroupItem value="b">B</ToggleGroupItem>
			</ToggleGroup>,
		);
		const a = screen.getByRole("button", { name: "A" });
		const b = screen.getByRole("button", { name: "B" });
		expect(a).toHaveAttribute("aria-pressed", "true");
		expect(b).toHaveAttribute("aria-pressed", "false");
		await user.click(b);
		expect(b).toHaveAttribute("aria-pressed", "true");
		expect(a).toHaveAttribute("aria-pressed", "false");
		expect(onValueChange.mock.calls[0]?.[0]).toEqual(["b"]);
	});

	it("single-select: clicking the pressed item clears the group", async () => {
		const onValueChange = mock();
		render(
			<ToggleGroup defaultValue={["a"]} onValueChange={onValueChange}>
				<ToggleGroupItem value="a">A</ToggleGroupItem>
			</ToggleGroup>,
		);
		await userEvent.setup().click(screen.getByRole("button", { name: "A" }));
		expect(onValueChange.mock.calls[0]?.[0]).toEqual([]);
		expect(screen.getByRole("button")).toHaveAttribute("aria-pressed", "false");
	});

	it("multiple: items press independently and the payload lists every pressed value", async () => {
		const onValueChange = mock();
		const user = userEvent.setup();
		render(
			<ToggleGroup multiple onValueChange={onValueChange}>
				<ToggleGroupItem value="a">A</ToggleGroupItem>
				<ToggleGroupItem value="b">B</ToggleGroupItem>
			</ToggleGroup>,
		);
		await user.click(screen.getByRole("button", { name: "A" }));
		await user.click(screen.getByRole("button", { name: "B" }));
		expect(onValueChange.mock.calls.map((c) => c[0])).toEqual([
			["a"],
			["a", "b"],
		]);
	});

	it("controlled: does not move until the parent updates value", async () => {
		const user = userEvent.setup();
		render(
			<ToggleGroup value={["a"]} onValueChange={() => {}}>
				<ToggleGroupItem value="a">A</ToggleGroupItem>
				<ToggleGroupItem value="b">B</ToggleGroupItem>
			</ToggleGroup>,
		);
		await user.click(screen.getByRole("button", { name: "B" }));
		expect(screen.getByRole("button", { name: "A" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		expect(screen.getByRole("button", { name: "B" })).toHaveAttribute(
			"aria-pressed",
			"false",
		);
	});

	it("controlled by parent state", async () => {
		render(<Harness />);
		await userEvent.setup().click(screen.getByRole("button", { name: "B" }));
		expect(screen.getByRole("button", { name: "B" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
	});

	it("a disabled group ignores clicks and a disabled item is skipped", async () => {
		const onValueChange = mock();
		const user = userEvent.setup();
		render(
			<>
				<ToggleGroup disabled onValueChange={onValueChange} aria-label="g1">
					<ToggleGroupItem value="a">A</ToggleGroupItem>
				</ToggleGroup>
				<ToggleGroup onValueChange={onValueChange} aria-label="g2">
					<ToggleGroupItem value="b" disabled>
						B
					</ToggleGroupItem>
				</ToggleGroup>
			</>,
		);
		await user.click(screen.getByRole("button", { name: "A" }));
		await user.click(screen.getByRole("button", { name: "B" }));
		expect(onValueChange).not.toHaveBeenCalled();
	});

	it("an item toggles from the keyboard with Space and Enter", async () => {
		const onValueChange = mock();
		const user = userEvent.setup();
		render(
			<ToggleGroup onValueChange={onValueChange}>
				<ToggleGroupItem value="a">A</ToggleGroupItem>
			</ToggleGroup>,
		);
		await user.tab();
		await user.keyboard(" ");
		await user.keyboard("{Enter}");
		expect(onValueChange.mock.calls.map((c) => c[0])).toEqual([["a"], []]);
	});
});

describe("ToggleGroup attributes", () => {
	it("reports variant, size, spacing and orientation on the group and its items", () => {
		render(
			<ToggleGroup
				variant="outline"
				size="sm"
				spacing={0}
				orientation="vertical"
				aria-label="g"
			>
				<ToggleGroupItem value="a">A</ToggleGroupItem>
			</ToggleGroup>,
		);
		const group = screen.getByRole("group", { name: "g" });
		expect(group).toHaveAttribute("data-slot", "toggle-group");
		expect(group).toHaveAttribute("data-variant", "outline");
		expect(group).toHaveAttribute("data-size", "sm");
		expect(group).toHaveAttribute("data-spacing", "0");
		expect(group).toHaveAttribute("data-orientation", "vertical");
		expect(group).not.toHaveAttribute("data-indicator");
		expect(group).not.toHaveAttribute("data-glide");
		const item = screen.getByRole("button", { name: "A" });
		expect(item).toHaveAttribute("data-slot", "toggle-group-item");
		expect(item).toHaveAttribute("data-variant", "outline");
		expect(item).toHaveAttribute("data-size", "sm");
		expect(item).toHaveAttribute("data-spacing", "0");
	});

	it("defaults to horizontal with a 2 spacing and exposes it as the --gap variable", () => {
		render(
			<ToggleGroup aria-label="g">
				<ToggleGroupItem value="a">A</ToggleGroupItem>
			</ToggleGroup>,
		);
		const group = screen.getByRole("group");
		expect(group).toHaveAttribute("data-orientation", "horizontal");
		expect(group).toHaveAttribute("data-spacing", "2");
		expect(group.style.getPropertyValue("--gap")).toBe("2");
	});

	it("merges a style object with --gap", () => {
		render(
			<ToggleGroup aria-label="g" spacing={1} style={{ color: "red" }}>
				<ToggleGroupItem value="a">A</ToggleGroupItem>
			</ToggleGroup>,
		);
		const group = screen.getByRole("group");
		expect(group.style.getPropertyValue("--gap")).toBe("1");
		expect(group.style.color).toBe("red");
	});

	it("merges a style function with --gap", () => {
		render(
			<ToggleGroup aria-label="g" spacing={3} style={() => ({ color: "blue" })}>
				<ToggleGroupItem value="a">A</ToggleGroupItem>
			</ToggleGroup>,
		);
		const group = screen.getByRole("group");
		expect(group.style.getPropertyValue("--gap")).toBe("3");
		expect(group.style.color).toBe("blue");
	});

	it("an item takes the group's variant over its own", () => {
		render(
			<ToggleGroup variant="outline" aria-label="g">
				<ToggleGroupItem value="a" variant="default">
					A
				</ToggleGroupItem>
			</ToggleGroup>,
		);
		expect(screen.getByRole("button", { name: "A" })).toHaveAttribute(
			"data-variant",
			"outline",
		);
	});

	it("an item outside a group uses its own variant/size fallback from the default context", () => {
		render(
			<ToggleGroupItem value="a" size="lg">
				A
			</ToggleGroupItem>,
		);
		const item = screen.getByRole("button", { name: "A" });
		// The default context supplies size "default"/variant "default".
		expect(item).toHaveAttribute("data-variant", "default");
		expect(item).toHaveAttribute("data-size", "default");
	});

	it("forwards ref as a function and as an object", () => {
		const objectRef = createRef<HTMLDivElement>();
		let node = null as HTMLDivElement | null;
		const { unmount } = render(
			<>
				<ToggleGroup aria-label="o" ref={objectRef}>
					<ToggleGroupItem value="a">A</ToggleGroupItem>
				</ToggleGroup>
				<ToggleGroup
					aria-label="f"
					ref={(n) => {
						node = n;
					}}
				>
					<ToggleGroupItem value="b">B</ToggleGroupItem>
				</ToggleGroup>
			</>,
		);
		expect(objectRef.current).toBe(screen.getByRole("group", { name: "o" }));
		expect(node).toBe(screen.getByRole("group", { name: "f" }));
		unmount();
		expect(objectRef.current).toBeNull();
		expect(node).toBeNull();
	});
});

describe("ToggleGroup segmented glide indicator", () => {
	let env: LayoutEnv;
	beforeEach(() => {
		env = installLayoutEnv();
	});
	afterEach(() => env.restore());

	const boxes: Record<
		string,
		{
			offsetLeft: number;
			offsetTop: number;
			offsetWidth: number;
			offsetHeight: number;
		}
	> = {
		A: { offsetLeft: 3, offsetTop: 2, offsetWidth: 40, offsetHeight: 24 },
		B: { offsetLeft: 45, offsetTop: 2, offsetWidth: 60, offsetHeight: 24 },
	};
	const placeBoxes = () => {
		env.layout = (el) => boxes[el.textContent ?? ""];
	};

	const thumb = () =>
		document.querySelector<HTMLElement>("[data-ind]") as HTMLElement;

	it("draws a hidden, decorative thumb with glide markers", () => {
		render(<Seg />);
		const group = screen.getByRole("group", { name: "seg" });
		expect(group).toHaveAttribute("data-indicator", "glide");
		expect(group).toHaveAttribute("data-glide", "p");
		expect(thumb()).toHaveAttribute("aria-hidden", "true");
	});

	it("has no thumb for the instant indicator", () => {
		render(<Seg indicator="instant" />);
		expect(screen.getByRole("group")).toHaveAttribute(
			"data-indicator",
			"instant",
		);
		expect(screen.getByRole("group")).not.toHaveAttribute("data-glide");
		expect(document.querySelector("[data-ind]")).toBeNull();
	});

	it("places the thumb on the pressed item without animating the first placement", async () => {
		placeBoxes();
		render(<Seg />);
		await env.flush();
		const t = thumb();
		expect(t.style.width).toBe("40px");
		expect(t.style.height).toBe("24px");
		expect(t.style.transform).toBe("translate(3px, 2px)");
		expect(t.style.opacity).toBe("1");
		expect(t.style.transition).toBe("none");
	});

	it("glides to the newly pressed item on a pointer click", async () => {
		placeBoxes();
		const user = userEvent.setup();
		render(<Seg />);
		await env.flush();
		await user.click(screen.getByRole("button", { name: "B" }));
		await env.flush();
		const t = thumb();
		expect(t.style.width).toBe("60px");
		expect(t.style.transform).toBe("translate(45px, 2px)");
		// An empty value hands the clock back to the CSS glide class.
		expect(t.style.transition).toBe("");
	});

	it("lands without gliding when the move came from the keyboard", async () => {
		placeBoxes();
		const user = userEvent.setup();
		render(<Seg />);
		await env.flush();
		await user.tab();
		await user.keyboard("{ArrowRight}");
		await user.keyboard(" ");
		await env.flush();
		const t = thumb();
		expect(t.style.transform).toBe("translate(45px, 2px)");
		expect(t.style.transition).toBe("none");
	});

	it("never glides under reduced motion", async () => {
		placeBoxes();
		env.reducedMotion = true;
		const user = userEvent.setup();
		render(<Seg />);
		await env.flush();
		await user.click(screen.getByRole("button", { name: "B" }));
		await env.flush();
		expect(thumb().style.transition).toBe("none");
	});

	it("hides the thumb when nothing is pressed", async () => {
		placeBoxes();
		render(
			<ToggleGroup variant="segmented" aria-label="seg">
				<ToggleGroupItem value="A">A</ToggleGroupItem>
			</ToggleGroup>,
		);
		await env.flush();
		expect(thumb().style.opacity).toBe("0");
	});

	it("re-places the thumb when a watched element is resized", async () => {
		placeBoxes();
		render(<Seg />);
		await env.flush();
		boxes.A = {
			offsetLeft: 3,
			offsetTop: 2,
			offsetWidth: 80,
			offsetHeight: 24,
		};
		try {
			for (const ro of env.resizeObservers()) ro.fire();
			await env.flush();
			expect(thumb().style.width).toBe("80px");
		} finally {
			boxes.A = {
				offsetLeft: 3,
				offsetTop: 2,
				offsetWidth: 40,
				offsetHeight: 24,
			};
		}
	});

	it("stops observing when unmounted", async () => {
		placeBoxes();
		const { unmount } = render(<Seg />);
		await env.flush();
		const observers = env.resizeObservers();
		expect(observers.length).toBeGreaterThan(0);
		unmount();
		expect(observers.every((o) => o.disconnected)).toBe(true);
	});
});
