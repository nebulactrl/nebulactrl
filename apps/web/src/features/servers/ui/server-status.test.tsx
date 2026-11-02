import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import type { ServerStatus as Status } from "#/api/models";
import { ServerStatus } from "./server-status";

const WORDS: [Status, string][] = [
	["online", "Online"],
	["offline", "Offline"],
	["pending", "Waiting for agent"],
	["revoked", "Revoked"],
];

describe("ServerStatus", () => {
	test.each(WORDS)(
		"%s is written as '%s', never colour alone",
		(status, word) => {
			render(<ServerStatus status={status} />);
			expect(screen.getByText(word)).toBeInTheDocument();
		},
	);

	test("each state has its own icon", () => {
		const kinds = WORDS.map(([status]) => {
			const { container, unmount } = render(<ServerStatus status={status} />);
			const kind = container
				.querySelector("[data-kind]")
				?.getAttribute("data-kind");
			unmount();
			return kind;
		});
		expect(kinds).toEqual(["live", "offline", "pending", "failed"]);
	});

	test("the icon is silent so the word is read once", () => {
		const { container } = render(<ServerStatus status="online" />);
		expect(container.textContent).toBe("Online");
	});

	test("the pill variant carries the same word", () => {
		render(<ServerStatus status="pending" pill />);
		expect(screen.getByText("Waiting for agent")).toBeInTheDocument();
	});

	test("the pill and inline variants differ in layout only, not in content", () => {
		const inline = render(<ServerStatus status="revoked" />);
		const inlineText = inline.container.textContent;
		inline.unmount();
		const pill = render(<ServerStatus status="revoked" pill />);
		expect(pill.container.textContent).toBe(inlineText);
	});
});
