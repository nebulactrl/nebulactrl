import { describe, expect, test } from "bun:test";
import { render } from "@testing-library/react";
import { Highlighted } from "./highlight";

function view(
	props: Partial<Parameters<typeof Highlighted>[0]> & { text: string },
) {
	const { container } = render(
		<div data-testid="out">
			<Highlighted lower={props.text.toLowerCase()} terms={[]} {...props} />
		</div>,
	);
	return container.firstElementChild as HTMLElement;
}

const marks = (el: HTMLElement) =>
	[...el.querySelectorAll("mark")].map((m) => m.textContent);

describe("Highlighted", () => {
	test("a line without terms or level is the plain text", () => {
		const el = view({ text: "server started" });
		expect(el.textContent).toBe("server started");
		expect(el.querySelector("mark")).toBeNull();
		expect(el.querySelector("span")).toBeNull();
	});

	test("marks each match, case-insensitively, keeping the original text", () => {
		const el = view({
			text: "Timeout after 5s, TIMEOUT again",
			terms: [{ parts: ["timeout"] }],
		});
		expect(marks(el)).toEqual(["Timeout", "TIMEOUT"]);
		expect(el.textContent).toBe("Timeout after 5s, TIMEOUT again");
	});

	test("terms that match nothing leave the text unmarked", () => {
		const el = view({ text: "all fine", terms: [{ parts: ["error"] }] });
		expect(marks(el)).toEqual([]);
		expect(el.textContent).toBe("all fine");
	});

	test("overlapping matches are marked as one", () => {
		const el = view({
			text: "abcdef",
			terms: [{ parts: ["abcd"] }, { parts: ["cdef"] }],
		});
		expect(marks(el)).toEqual(["abcdef"]);
	});

	test("the level word gets the level class", () => {
		const el = view({
			text: "ERROR disk full",
			levelSpan: { start: 0, end: 5 },
			levelClass: "text-danger",
		});
		const span = el.querySelector("span");
		expect(span).toHaveTextContent("ERROR");
		expect(span).toHaveClass("text-danger");
		expect(el.textContent).toBe("ERROR disk full");
	});

	test("a match inside the level word is marked within the coloured span", () => {
		const el = view({
			text: "ERROR disk full",
			terms: [{ parts: ["err"] }, { parts: ["disk"] }],
			levelSpan: { start: 0, end: 5 },
			levelClass: "text-danger",
		});
		expect(marks(el)).toEqual(["ERR", "disk"]);
		const spans = [...el.querySelectorAll("span")];
		expect(spans.map((s) => s.textContent).join("")).toBe("ERROR");
		for (const span of spans) expect(span).toHaveClass("text-danger");
		expect(spans[0]?.querySelector("mark")).toHaveTextContent("ERR");
		expect(el.textContent).toBe("ERROR disk full");
	});
});
