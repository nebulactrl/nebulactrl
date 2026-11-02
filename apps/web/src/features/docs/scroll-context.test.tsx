import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import { DocsScrollContext, useDocsScroll } from "./scroll-context";

function Probe() {
	const element = useDocsScroll();
	return <p>{element ? `scroll root: ${element.id}` : "no scroll root"}</p>;
}

describe("useDocsScroll", () => {
	test("is null outside a docs layout", () => {
		render(<Probe />);
		expect(screen.getByText("no scroll root")).toBeInTheDocument();
	});

	test("gives pages the layout's scrolling element", () => {
		const root = document.createElement("div");
		root.id = "docs-main";
		render(
			<DocsScrollContext value={root}>
				<Probe />
			</DocsScrollContext>,
		);
		expect(screen.getByText("scroll root: docs-main")).toBeInTheDocument();
	});
});
