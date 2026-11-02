import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import type { TocEntry } from "@nebulactrl/docs/types";
import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DocsToc } from "./docs-toc";

const entries: TocEntry[] = [
	{ id: "setup", text: "Setup", level: 2 },
	{ id: "install", text: "Install", level: 3 },
	{ id: "usage", text: "Usage", level: 2 },
];

class FakeObserver {
	static all: FakeObserver[] = [];
	observed: Element[] = [];
	disconnected = false;
	constructor(
		readonly callback: IntersectionObserverCallback,
		readonly options: IntersectionObserverInit | undefined,
	) {
		FakeObserver.all.push(this);
	}
	observe(element: Element) {
		this.observed.push(element);
	}
	disconnect() {
		this.disconnected = true;
	}
	/** Reports each element's visibility as the browser would. */
	report(visibility: Record<string, boolean>) {
		const changes = Object.entries(visibility).map(([id, isIntersecting]) => ({
			isIntersecting,
			target: document.getElementById(id),
		}));
		act(() => {
			this.callback(
				changes as unknown as IntersectionObserverEntry[],
				this as unknown as IntersectionObserver,
			);
		});
	}
}

const realObserver = globalThis.IntersectionObserver;
beforeEach(() => {
	FakeObserver.all = [];
	globalThis.IntersectionObserver =
		FakeObserver as unknown as typeof IntersectionObserver;
	for (const entry of entries) {
		const heading = document.createElement("h2");
		heading.id = entry.id;
		heading.scrollIntoView = () => {};
		document.body.append(heading);
	}
});
afterEach(() => {
	globalThis.IntersectionObserver = realObserver;
	window.history.replaceState(null, "", "/");
});

function current() {
	return screen
		.getAllByRole("link")
		.filter((link) => link.getAttribute("aria-current") === "location")
		.map((link) => link.textContent);
}

describe("DocsToc", () => {
	test("lists the outline as anchors to each heading", () => {
		render(<DocsToc entries={entries} scrollRoot={document.body} />);
		const nav = screen.getByRole("navigation", { name: "On this page" });
		expect(nav).toBeInTheDocument();
		expect(
			screen
				.getAllByRole("link")
				.map((link) => [link.textContent, link.getAttribute("href")]),
		).toEqual([
			["Setup", "#setup"],
			["Install", "#install"],
			["Usage", "#usage"],
		]);
	});

	test.each([[[]], [[entries[0]]]])(
		"is left out when the page has fewer than two headings (%#)",
		(few) => {
			const { container } = render(
				<DocsToc entries={few as TocEntry[]} scrollRoot={document.body} />,
			);
			expect(container).toBeEmptyDOMElement();
		},
	);

	test("clicking an entry scrolls to its heading, puts it in the URL and marks it", async () => {
		const scrolled: string[] = [];
		for (const entry of entries) {
			const heading = document.getElementById(entry.id) as HTMLElement;
			heading.scrollIntoView = () => {
				scrolled.push(entry.id);
			};
		}
		render(<DocsToc entries={entries} scrollRoot={document.body} />);
		await userEvent.setup().click(screen.getByRole("link", { name: "Usage" }));
		expect(scrolled).toEqual(["usage"]);
		expect(window.location.hash).toBe("#usage");
		expect(current()).toEqual(["Usage"]);
	});

	test("marks the first visible heading in outline order as the one being read", () => {
		render(<DocsToc entries={entries} scrollRoot={document.body} />);
		const observer = FakeObserver.all[0] as FakeObserver;
		expect(current()).toEqual([]);
		observer.report({ usage: true, install: true });
		expect(current()).toEqual(["Install"]);
		observer.report({ install: false });
		expect(current()).toEqual(["Usage"]);
	});

	test("keeps the last mark when nothing is visible", () => {
		render(<DocsToc entries={entries} scrollRoot={document.body} />);
		const observer = FakeObserver.all[0] as FakeObserver;
		observer.report({ setup: true });
		observer.report({ setup: false });
		expect(current()).toEqual(["Setup"]);
	});

	test("watches the headings inside the given scroll root and stops on unmount", () => {
		const { unmount } = render(
			<DocsToc entries={entries} scrollRoot={document.body} />,
		);
		const observer = FakeObserver.all[0] as FakeObserver;
		expect(observer.options?.root).toBe(document.body);
		expect(observer.observed.map((element) => element.id)).toEqual([
			"setup",
			"install",
			"usage",
		]);
		unmount();
		expect(observer.disconnected).toBe(true);
	});

	test("does not observe anything without a scroll root", () => {
		render(<DocsToc entries={entries} scrollRoot={null} />);
		expect(FakeObserver.all).toHaveLength(0);
		expect(screen.getByRole("link", { name: "Setup" })).toBeInTheDocument();
	});

	test("skips headings that are not in the page", () => {
		document.getElementById("install")?.remove();
		render(<DocsToc entries={entries} scrollRoot={document.body} />);
		expect(FakeObserver.all[0]?.observed.map((element) => element.id)).toEqual([
			"setup",
			"usage",
		]);
	});
});
