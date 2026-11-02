import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import { JsonView } from "./json-view";

describe("JsonView", () => {
	test("pretty-prints the value into a read-only, labelled text area", () => {
		render(<JsonView label="Payload as JSON" value={{ a: 1, b: ["x"] }} />);
		const box = screen.getByRole("textbox", { name: "Payload as JSON" });
		expect(box).toHaveAttribute("readonly");
		expect((box as HTMLTextAreaElement).value).toBe(
			JSON.stringify({ a: 1, b: ["x"] }, null, 2),
		);
	});

	test("shows null for a value JSON cannot represent", () => {
		render(<JsonView label="Result as JSON" value={undefined} />);
		expect(
			(
				screen.getByRole("textbox", {
					name: "Result as JSON",
				}) as HTMLTextAreaElement
			).value,
		).toBe("null");
	});

	test("sizes to its content for a short value", () => {
		render(<JsonView label="small" value={{ a: 1 }} />);
		// { / "a": 1 / }
		expect(screen.getByRole("textbox", { name: "small" })).toHaveAttribute(
			"rows",
			"3",
		);
	});

	test("stops growing at 18 rows and scrolls beyond", () => {
		const big = Object.fromEntries(
			Array.from({ length: 40 }, (_, i) => [`key${i}`, i]),
		);
		render(<JsonView label="big" value={big} />);
		expect(screen.getByRole("textbox", { name: "big" })).toHaveAttribute(
			"rows",
			"18",
		);
	});
});
