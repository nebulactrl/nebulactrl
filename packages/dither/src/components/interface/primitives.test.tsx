import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
	Avatar,
	AvatarFallback,
	AvatarGroup,
	AvatarGroupCount,
	AvatarImage,
	Button,
	Card,
	Empty,
	EmptyContent,
	EmptyDescription,
	EmptyHeader,
	EmptyMedia,
} from "./primitives";

describe("Button", () => {
	test("renders a <button> labelled by its children that fires onClick", async () => {
		const clicks: string[] = [];
		render(<Button onClick={() => clicks.push("x")}>Save</Button>);
		const button = screen.getByRole("button", { name: "Save" });
		expect(button).toHaveAttribute("data-slot", "button");
		await userEvent.setup().click(button);
		expect(clicks).toEqual(["x"]);
	});

	test("a disabled button never fires onClick", async () => {
		const clicks: string[] = [];
		render(
			<Button disabled onClick={() => clicks.push("x")}>
				Save
			</Button>,
		);
		const button = screen.getByRole("button", { name: "Save" });
		expect(button).toBeDisabled();
		await userEvent.setup().click(button);
		expect(clicks).toEqual([]);
	});

	test("the default variant is primary and keeps the extra padding of accent keys", () => {
		render(
			<>
				<Button>implicit</Button>
				<Button variant="primary">explicit</Button>
			</>,
		);
		const implicit = screen.getByText("implicit");
		expect(implicit).toHaveClass("px-3.5");
		expect(implicit.className).toBe(screen.getByText("explicit").className);
	});

	test("accent keys are padded at default and lg only; danger-solid only at lg", () => {
		render(
			<>
				<Button variant="accent" size="lg">
					accent-lg
				</Button>
				<Button variant="accent" size="sm">
					accent-sm
				</Button>
				<Button variant="danger-solid" size="lg">
					danger-lg
				</Button>
				<Button variant="destructive-solid">danger-default</Button>
				<Button variant="outline">outline-default</Button>
			</>,
		);
		expect(screen.getByText("accent-lg")).toHaveClass("px-3.5");
		expect(screen.getByText("accent-sm")).not.toHaveClass("px-3.5");
		expect(screen.getByText("danger-lg")).toHaveClass("px-3.5");
		expect(screen.getByText("danger-default")).not.toHaveClass("px-3.5");
		expect(screen.getByText("outline-default")).not.toHaveClass("px-3.5");
	});

	test("a link button at the default size drops its height and padding", () => {
		render(
			<>
				<Button variant="link">link-default</Button>
				<Button variant="link" size="lg">
					link-lg
				</Button>
			</>,
		);
		expect(screen.getByText("link-default")).toHaveClass("h-auto", "px-0");
		expect(screen.getByText("link-lg")).not.toHaveClass("px-0");
	});

	test("every variant yields a distinct class list, and className wins last", () => {
		const variants = [
			"primary",
			"accent",
			"secondary",
			"outline",
			"ghost",
			"danger",
			"danger-solid",
			"link",
		] as const;
		render(
			<>
				{variants.map((variant) => (
					<Button key={variant} variant={variant} className="custom-x">
						{variant}
					</Button>
				))}
			</>,
		);
		const classes = variants.map((variant) => {
			const el = screen.getByText(variant);
			expect(el).toHaveClass("custom-x");
			return el.className.replace("custom-x", "").replace(/px-3\.5/, "");
		});
		expect(new Set(classes).size).toBeGreaterThan(5);
	});
});

describe("Card", () => {
	test("exposes its variant and size as data attributes", () => {
		render(
			<Card variant="raised" size="lg" data-testid="c">
				body
			</Card>,
		);
		const card = screen.getByTestId("c");
		expect(card).toHaveAttribute("data-slot", "card");
		expect(card).toHaveAttribute("data-variant", "raised");
		expect(card).toHaveAttribute("data-size", "lg");
		expect(card).toHaveTextContent("body");
	});

	test("defaults to the default variant and size and merges className", () => {
		render(<Card data-testid="c" className="mine" />);
		const card = screen.getByTestId("c");
		expect(card).toHaveAttribute("data-variant", "default");
		expect(card).toHaveAttribute("data-size", "default");
		expect(card).toHaveClass("mine");
	});

	test("the danger variant gets the destructive border", () => {
		render(<Card variant="danger" data-testid="c" />);
		expect(screen.getByTestId("c")).toHaveClass("border-destructive-border");
	});
});

describe("Avatar primitives", () => {
	test("Avatar carries its size and shows the fallback when no image loads", async () => {
		render(
			<Avatar size="lg" data-testid="a">
				<AvatarImage src="" alt="" />
				<AvatarFallback>AB</AvatarFallback>
			</Avatar>,
		);
		const avatar = screen.getByTestId("a");
		expect(avatar).toHaveAttribute("data-slot", "avatar");
		expect(avatar).toHaveAttribute("data-size", "lg");
		expect(await screen.findByText("AB")).toHaveAttribute(
			"data-slot",
			"avatar-fallback",
		);
	});

	test("Avatar defaults to the default size", () => {
		render(<Avatar data-testid="a" />);
		expect(screen.getByTestId("a")).toHaveAttribute("data-size", "default");
	});

	test("AvatarGroup and AvatarGroupCount mark their slots and pass children", () => {
		render(
			<AvatarGroup data-testid="g">
				<AvatarGroupCount>+3</AvatarGroupCount>
			</AvatarGroup>,
		);
		expect(screen.getByTestId("g")).toHaveAttribute(
			"data-slot",
			"avatar-group",
		);
		expect(screen.getByText("+3")).toHaveAttribute(
			"data-slot",
			"avatar-group-count",
		);
	});
});

describe("Empty primitives", () => {
	test("compose into the empty-state slots", () => {
		render(
			<Empty data-testid="e">
				<EmptyHeader>
					<EmptyMedia>media</EmptyMedia>
					<EmptyDescription>why</EmptyDescription>
				</EmptyHeader>
				<EmptyContent>actions</EmptyContent>
			</Empty>,
		);
		expect(screen.getByTestId("e")).toHaveAttribute("data-slot", "empty");
		expect(screen.getByText("why")).toHaveAttribute(
			"data-slot",
			"empty-description",
		);
		expect(screen.getByText("actions")).toHaveAttribute(
			"data-slot",
			"empty-content",
		);
		expect(screen.getByText("media").parentElement).toHaveAttribute(
			"data-slot",
			"empty-header",
		);
	});

	test("EmptyMedia is the plain variant by default and the tile with variant=icon", () => {
		render(
			<>
				<EmptyMedia>plain</EmptyMedia>
				<EmptyMedia variant="icon">tile</EmptyMedia>
			</>,
		);
		expect(screen.getByText("plain")).toHaveAttribute(
			"data-variant",
			"default",
		);
		const tile = screen.getByText("tile");
		expect(tile).toHaveAttribute("data-variant", "icon");
		expect(tile).toHaveClass("surface-well");
		expect(screen.getByText("plain")).not.toHaveClass("surface-well");
	});
});
