import { describe, expect, test } from "bun:test";
import { render, screen, within } from "@testing-library/react";
import { ReleaseNotes } from "./release-notes";

describe("ReleaseNotes", () => {
	test("renders the blocks of the notes under a labelled region", () => {
		render(
			<ReleaseNotes
				label="Release notes for v0.1.2"
				markdown={[
					"## What's new",
					"",
					"A **bold** claim, some *emphasis* and `code`.",
					"",
					"- first fix",
					"- second fix",
					"",
					"1. step one",
					"2. step two",
					"",
					"> be careful",
					"",
					"```",
					"nebulactrl update",
					"```",
				].join("\n")}
			/>,
		);
		const region = screen.getByRole("region", {
			name: "Release notes for v0.1.2",
		});
		expect(within(region).getByText("What's new")).toBeInTheDocument();
		expect(within(region).getByText("bold").tagName).toBe("STRONG");
		expect(within(region).getByText("emphasis").tagName).toBe("EM");
		expect(within(region).getByText("code").tagName).toBe("CODE");

		const lists = within(region).getAllByRole("list");
		expect(lists[0]?.tagName).toBe("UL");
		expect(
			within(lists[0] as HTMLElement)
				.getAllByRole("listitem")
				.map((li) => li.textContent),
		).toEqual(["first fix", "second fix"]);
		expect(lists[1]?.tagName).toBe("OL");
		expect(
			within(lists[1] as HTMLElement)
				.getAllByRole("listitem")
				.map((li) => li.textContent),
		).toEqual(["step one", "step two"]);

		expect(within(region).getByText("be careful").tagName).toBe("BLOCKQUOTE");
		expect(within(region).getByText("nebulactrl update").tagName).toBe("PRE");
	});

	test("an http(s) link opens in a new tab without leaking the opener", () => {
		render(
			<ReleaseNotes
				label="Notes"
				markdown="See [the changelog](https://example.com/changes)."
			/>,
		);
		const link = screen.getByRole("link", { name: "the changelog" });
		expect(link).toHaveAttribute("href", "https://example.com/changes");
		expect(link).toHaveAttribute("target", "_blank");
		expect(link).toHaveAttribute("rel", "noopener noreferrer");
	});

	test("an unsafe link is plain text and notes cannot inject HTML", () => {
		const { container } = render(
			<ReleaseNotes
				label="Notes"
				markdown={
					"[click](javascript:alert(1)) and <img src=x onerror=alert(1)> <script>boom()</script>"
				}
			/>,
		);
		expect(screen.queryByRole("link")).not.toBeInTheDocument();
		expect(container.querySelector("img")).toBeNull();
		expect(container.querySelector("script")).toBeNull();
		expect(screen.getByRole("region")).toHaveTextContent(
			"click) and <img src=x onerror=alert(1)> <script>boom()</script>",
		);
	});

	test("nested emphasis inside a link and a list item renders as nested elements", () => {
		render(
			<ReleaseNotes
				label="Notes"
				markdown="- fixed [**the crash**](https://example.com/1)"
			/>,
		);
		const link = screen.getByRole("link", { name: "the crash" });
		expect(link.querySelector("strong")).toHaveTextContent("the crash");
		expect(screen.getByRole("listitem")).toContainElement(link);
	});

	test("empty notes render an empty region", () => {
		render(<ReleaseNotes label="Notes" markdown="" />);
		expect(screen.getByRole("region", { name: "Notes" })).toBeEmptyDOMElement();
	});
});
