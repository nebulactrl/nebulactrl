import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import { DitherNotFound } from "./not-found";

const root = () =>
	document.querySelector("[data-slot=not-found]") as HTMLElement;

describe("DitherNotFound", () => {
	test("defaults to a 404 page with a level-1 title that speaks the code first", () => {
		render(<DitherNotFound />);
		const heading = screen.getByRole("heading", { level: 1 });
		expect(heading).toHaveTextContent("Error 404. Nothing at this address");
		expect(heading.querySelector(".sr-only")).toHaveTextContent("Error 404.");
		expect(
			screen.getByText(/It may have been moved or deleted/),
		).toHaveAttribute("data-slot", "empty-description");
	});

	test("code, title and body are replaceable", () => {
		render(
			<DitherNotFound
				code="500"
				title="Server error"
				body="Try again later."
			/>,
		);
		expect(screen.getByRole("heading")).toHaveTextContent(
			"Error 500. Server error",
		);
		expect(screen.getByText("Try again later.")).toBeInTheDocument();
		expect(screen.queryByText(/moved or deleted/)).toBeNull();
	});

	test("description wins over body", () => {
		render(<DitherNotFound body="old" description="new" />);
		expect(screen.getByText("new")).toBeInTheDocument();
		expect(screen.queryByText("old")).toBeNull();
	});

	test("titleAs h2 demotes the heading", () => {
		render(<DitherNotFound titleAs="h2" />);
		expect(screen.getByRole("heading", { level: 2 })).toBeInTheDocument();
		expect(screen.queryByRole("heading", { level: 1 })).toBeNull();
	});

	test("the block digits are decoration hidden from assistive tech", () => {
		render(<DitherNotFound code="418" />);
		const digits = root().querySelector(
			"[data-slot=dither-code]",
		) as HTMLElement;
		expect(digits).toHaveAttribute("aria-hidden", "true");
		// The only readable copy of the code is the sr-only heading text.
		expect(
			screen.getAllByText(/418/, { ignore: "[aria-hidden=true] *" }),
		).toHaveLength(1);
	});

	test("detail and actions render only when given", () => {
		const { unmount } = render(<DitherNotFound />);
		expect(root().querySelector("[data-slot=not-found-detail]")).toBeNull();
		expect(root().querySelector("[data-slot=empty-content]")).toBeNull();
		unmount();
		render(
			<DitherNotFound
				detail="/projects/xyz"
				actions={<a href="/projects">Go to projects</a>}
			/>,
		);
		expect(screen.getByText("/projects/xyz")).toHaveAttribute(
			"data-slot",
			"not-found-detail",
		);
		expect(
			screen.getByRole("link", { name: "Go to projects" }),
		).toHaveAttribute("href", "/projects");
	});

	test("the animated backdrop is opt-in", () => {
		const { unmount } = render(<DitherNotFound />);
		expect(root().querySelector("[data-slot=dither-shader]")).toBeNull();
		unmount();
		render(<DitherNotFound backdrop />);
		expect(root().querySelector("[data-slot=dither-shader]")).not.toBeNull();
	});

	test("className is merged onto the card", () => {
		render(<DitherNotFound className="max-w-xl" />);
		expect(root()).toHaveClass("surface-raised", "max-w-xl");
	});
});
