import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { KvRow } from "./kv-row";

describe("KvRow", () => {
	test("renders label and value", () => {
		render(
			<KvRow label="Region">
				<span>eu-west</span>
			</KvRow>,
		);
		expect(screen.getByText("Region")).toBeInTheDocument();
		expect(screen.getByText("eu-west")).toBeInTheDocument();
	});

	test("without htmlFor the label is plain text, not a <label>", () => {
		render(
			<KvRow label="Region">
				<input aria-label="field" />
			</KvRow>,
		);
		expect(screen.getByText("Region").tagName).toBe("SPAN");
		expect(document.querySelector("label")).toBeNull();
	});

	test("htmlFor turns the label into a <label> that focuses the control", async () => {
		const user = userEvent.setup();
		render(
			<KvRow label="Hostname" htmlFor="host">
				<input id="host" />
			</KvRow>,
		);
		const input = screen.getByLabelText("Hostname");
		expect(input.id).toBe("host");
		await user.click(screen.getByText("Hostname"));
		expect(input).toHaveFocus();
	});

	test("shows the description only when given", () => {
		const { rerender } = render(
			<KvRow label="Replicas" description="How many copies run">
				<span>3</span>
			</KvRow>,
		);
		expect(screen.getByText("How many copies run")).toBeInTheDocument();
		rerender(
			<KvRow label="Replicas">
				<span>3</span>
			</KvRow>,
		);
		expect(screen.queryByText("How many copies run")).toBeNull();
	});

	test("an empty-string description renders nothing", () => {
		const { container } = render(
			<KvRow label="Replicas" description="">
				<span>3</span>
			</KvRow>,
		);
		// label span + value div only; no description span inside the label column
		expect(
			container.querySelector("[data-slot=kv-row] > span")?.children,
		).toHaveLength(1);
	});

	test("rule picks the side of the hairline, bottom by default", () => {
		const { rerender } = render(
			<KvRow label="A">
				<span>v</span>
			</KvRow>,
		);
		const row = () =>
			document.querySelector("[data-slot=kv-row]") as HTMLElement;
		expect(row()).toHaveClass("border-b");
		expect(row()).not.toHaveClass("border-t");
		rerender(
			<KvRow label="A" rule="top">
				<span>v</span>
			</KvRow>,
		);
		expect(row()).toHaveClass("border-t");
		expect(row()).not.toHaveClass("border-b");
	});

	test("className is merged onto the row", () => {
		render(
			<KvRow label="A" className="custom">
				<span>v</span>
			</KvRow>,
		);
		expect(document.querySelector("[data-slot=kv-row]")).toHaveClass("custom");
	});
});
