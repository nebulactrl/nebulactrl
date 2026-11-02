import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import { PaneHeader } from "./pane-header";
import { RuledSection } from "./ruled-section";

describe("PaneHeader", () => {
	test("renders the title as a heading with the subtitle under it", () => {
		render(<PaneHeader title="Map" subtitle="World · 3 places located" />);
		expect(screen.getByRole("heading", { name: "Map" })).toBeInTheDocument();
		expect(screen.getByText("World · 3 places located")).toBeInTheDocument();
	});

	test("leaves out the subtitle line when there is none", () => {
		const { container } = render(<PaneHeader title="Map" />);
		expect(container.querySelector("p")).toBeNull();
	});
});

describe("RuledSection", () => {
	test("renders title, subtitle, actions and the rows", () => {
		render(
			<RuledSection
				title="Top pages"
				subtitle="Paths that returned 404."
				actions={<button type="button">Pages</button>}
			>
				<p>row content</p>
			</RuledSection>,
		);
		expect(
			screen.getByRole("heading", { name: "Top pages" }),
		).toBeInTheDocument();
		expect(screen.getByText("Paths that returned 404.")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Pages" })).toBeInTheDocument();
		expect(screen.getByText("row content")).toBeInTheDocument();
	});

	test("omits subtitle and actions when not given", () => {
		const { container } = render(
			<RuledSection title="Status codes">
				<p>x</p>
			</RuledSection>,
		);
		expect(container.querySelector("header p")).toBeNull();
		expect(screen.queryByRole("button")).not.toBeInTheDocument();
	});
});
