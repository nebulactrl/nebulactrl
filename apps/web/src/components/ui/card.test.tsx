import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import {
	Card,
	CardContent,
	CardDescription,
	CardFooter,
	CardHeader,
	CardTitle,
} from "./card";

describe("Card", () => {
	test("defaults to the default variant and size", () => {
		render(<Card data-testid="card">body</Card>);
		const card = screen.getByTestId("card");
		expect(card).toHaveAttribute("data-slot", "card");
		expect(card).toHaveAttribute("data-variant", "default");
		expect(card).toHaveAttribute("data-size", "default");
		expect(card).toHaveTextContent("body");
	});

	test.each(["default", "raised", "float", "pop", "well", "danger"] as const)(
		"variant %s is reflected in data-variant",
		(variant) => {
			render(
				<Card data-testid="card" variant={variant}>
					x
				</Card>,
			);
			expect(screen.getByTestId("card")).toHaveAttribute(
				"data-variant",
				variant,
			);
		},
	);

	test("each variant gets a distinct surface treatment", () => {
		const classes = (["raised", "float", "pop", "well", "danger"] as const).map(
			(variant) => {
				const { unmount } = render(
					<Card data-testid="card" variant={variant}>
						x
					</Card>,
				);
				const cls = screen.getByTestId("card").className;
				unmount();
				return cls;
			},
		);
		expect(new Set(classes).size).toBe(5);
	});

	test("size lg is reflected in data-size", () => {
		render(
			<Card data-testid="card" size="lg">
				x
			</Card>,
		);
		expect(screen.getByTestId("card")).toHaveAttribute("data-size", "lg");
	});

	test("className is merged and extra props pass through", () => {
		render(
			<Card data-testid="card" className="extra" role="region" aria-label="Box">
				x
			</Card>,
		);
		const card = screen.getByRole("region", { name: "Box" });
		expect(card).toHaveClass("extra");
		expect(card.className).not.toBe("extra");
	});

	test("composes header, title, description, content and footer in order", () => {
		render(
			<Card data-testid="card">
				<CardHeader>
					<CardTitle>Title</CardTitle>
					<CardDescription>Describes it</CardDescription>
				</CardHeader>
				<CardContent>Content</CardContent>
				<CardFooter>
					<button type="button">Save</button>
				</CardFooter>
			</Card>,
		);
		const card = screen.getByTestId("card");
		expect(
			Array.from(card.children).map((c) => c.getAttribute("data-slot")),
		).toEqual(["card-header", "card-content", "card-footer"]);
		expect(screen.getByText("Title")).toHaveAttribute(
			"data-slot",
			"card-title",
		);
		expect(screen.getByText("Describes it")).toHaveAttribute(
			"data-slot",
			"card-description",
		);
		expect(
			screen.getByRole("button", { name: "Save" }).parentElement,
		).toHaveAttribute("data-slot", "card-footer");
	});

	test("sub-parts accept className and props", () => {
		render(
			<Card>
				<CardHeader className="h-x" data-testid="h" />
				<CardTitle className="t-x">T</CardTitle>
				<CardDescription className="d-x">D</CardDescription>
				<CardContent className="c-x" data-testid="c" />
				<CardFooter className="f-x" data-testid="f" />
			</Card>,
		);
		expect(screen.getByTestId("h")).toHaveClass("h-x");
		expect(screen.getByText("T")).toHaveClass("t-x");
		expect(screen.getByText("D")).toHaveClass("d-x");
		expect(screen.getByTestId("c")).toHaveClass("c-x");
		expect(screen.getByTestId("f")).toHaveClass("f-x");
	});
});
