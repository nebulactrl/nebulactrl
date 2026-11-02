import {
	afterEach,
	beforeEach,
	describe,
	expect,
	jest,
	spyOn,
	test,
} from "bun:test";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { DocContent } from "./doc-content";
import { CODE_HTML } from "./docs-fixtures";

const HTML = `<h2 id="setup">Setup</h2><p>
<a href="#setup">to setup</a>
<a href="#caf%C3%A9">to cafe</a>
<a href="/docs/operations/backups#x">backups</a>
<a href="/docs">docs home</a>
<a href="/docsfoo">not docs</a>
<a href="https://example.com/out">outside</a>
<a href="/docs/other" target="_blank">new tab</a>
<a>no href</a>
</p><h2 id="café">Café</h2>${CODE_HTML}`;

let scrolled: string[];

beforeEach(() => {
	scrolled = [];
	spyOn(Element.prototype, "scrollIntoView").mockImplementation(function (
		this: HTMLElement,
	) {
		scrolled.push(this.id);
	});
});

afterEach(() => {
	jest.useRealTimers();
	jest.restoreAllMocks();
});

function click(name: string, init: MouseEventInit = {}) {
	const event = new MouseEvent("click", {
		bubbles: true,
		cancelable: true,
		button: 0,
		...init,
	});
	fireEvent(screen.getByText(name), event);
	return event;
}

describe("DocContent links", () => {
	test("an in-page anchor scrolls to its heading and reports the anchor", () => {
		const visited: string[] = [];
		render(
			<DocContent html={HTML} onNavigate={(href) => visited.push(href)} />,
		);
		const event = click("to setup");
		expect(event.defaultPrevented).toBe(true);
		expect(scrolled).toEqual(["setup"]);
		expect(visited).toEqual(["#setup"]);
	});

	test("decodes a percent-encoded anchor to find the heading", () => {
		const visited: string[] = [];
		render(
			<DocContent html={HTML} onNavigate={(href) => visited.push(href)} />,
		);
		click("to cafe");
		expect(scrolled).toEqual(["café"]);
		expect(visited).toEqual(["#caf%C3%A9"]);
	});

	test.each([
		["backups", "/docs/operations/backups#x"],
		["docs home", "/docs"],
	])(
		"a docs link (%s) is handed to the app instead of reloading",
		(name, href) => {
			const visited: string[] = [];
			render(
				<DocContent html={HTML} onNavigate={(next) => visited.push(next)} />,
			);
			const event = click(name);
			expect(event.defaultPrevented).toBe(true);
			expect(visited).toEqual([href]);
		},
	);

	test.each(["outside", "not docs", "new tab", "no href"])(
		"leaves %s to the browser",
		(name) => {
			const visited: string[] = [];
			render(
				<DocContent html={HTML} onNavigate={(href) => visited.push(href)} />,
			);
			const event = click(name);
			expect(event.defaultPrevented).toBe(false);
			expect(visited).toEqual([]);
		},
	);

	test.each([
		["meta", { metaKey: true }],
		["ctrl", { ctrlKey: true }],
		["shift", { shiftKey: true }],
		["alt", { altKey: true }],
		["middle button", { button: 1 }],
	])("a %s click keeps the browser's own behaviour", (_name, init) => {
		const visited: string[] = [];
		render(
			<DocContent html={HTML} onNavigate={(href) => visited.push(href)} />,
		);
		const event = click("backups", init);
		expect(event.defaultPrevented).toBe(false);
		expect(visited).toEqual([]);
	});

	test("a click on text that is not a link does nothing", () => {
		const visited: string[] = [];
		render(
			<DocContent html={HTML} onNavigate={(href) => visited.push(href)} />,
		);
		click("Setup");
		expect(visited).toEqual([]);
		expect(scrolled).toEqual([]);
	});
});

describe("DocContent scrolling", () => {
	test("scrolls to the deep-linked heading once the page is in the DOM", () => {
		render(<DocContent html={HTML} hash="café" onNavigate={() => {}} />);
		expect(scrolled).toEqual(["café"]);
	});

	test("ignores a deep link to a heading the page does not have", () => {
		render(<DocContent html={HTML} hash="missing" onNavigate={() => {}} />);
		expect(scrolled).toEqual([]);
	});

	test("without a deep link returns the scrolling ancestor to the top", () => {
		const scrollTo = spyOn(Element.prototype, "scrollTo").mockImplementation(
			() => {},
		);
		render(
			<div data-docs-scroll>
				<DocContent html={HTML} onNavigate={() => {}} />
			</div>,
		);
		expect(scrollTo).toHaveBeenCalledWith({ top: 0 });
		expect(scrolled).toEqual([]);
	});

	test("an explicit scroll root wins over the nearest scrolling ancestor", () => {
		const scrollTo = spyOn(Element.prototype, "scrollTo").mockImplementation(
			() => {},
		);
		const root = document.createElement("section");
		render(
			<div data-docs-scroll>
				<DocContent html={HTML} scrollRoot={root} onNavigate={() => {}} />
			</div>,
		);
		expect(scrollTo).toHaveBeenCalledTimes(1);
		expect(scrollTo.mock.contexts[0]).toBe(root);
	});

	test("scrolls again when a different page's html arrives", () => {
		const scrollTo = spyOn(Element.prototype, "scrollTo").mockImplementation(
			() => {},
		);
		const { rerender } = render(
			<div data-docs-scroll>
				<DocContent html="<p>one</p>" onNavigate={() => {}} />
			</div>,
		);
		rerender(
			<div data-docs-scroll>
				<DocContent html="<p>two</p>" onNavigate={() => {}} />
			</div>,
		);
		expect(scrollTo).toHaveBeenCalledTimes(2);
		expect(screen.getByText("two")).toBeInTheDocument();
	});
});

describe("DocContent code copy", () => {
	function copyButton() {
		const button =
			document.querySelector<HTMLButtonElement>("[data-docs-copy]");
		if (!button) throw new Error("no [data-docs-copy] button is rendered");
		return button;
	}

	test("copies the code without its trailing newline and confirms, then reverts", async () => {
		const writeText = spyOn(
			navigator.clipboard,
			"writeText",
		).mockResolvedValue();
		render(<DocContent html={HTML} onNavigate={() => {}} />);
		jest.useFakeTimers();
		await act(async () => {
			fireEvent.click(screen.getByText("Copy"));
		});
		expect(writeText).toHaveBeenCalledWith("bun install");
		expect(screen.getByText("Copied")).toBeInTheDocument();
		expect(copyButton()).toHaveAttribute("data-copied");
		act(() => {
			jest.advanceTimersByTime(1_500);
		});
		expect(screen.getByText("Copy")).toBeInTheDocument();
		expect(copyButton()).not.toHaveAttribute("data-copied");
	});

	test("a copy click is not treated as a link click", async () => {
		spyOn(navigator.clipboard, "writeText").mockResolvedValue();
		const visited: string[] = [];
		render(
			<DocContent
				html={`<div class="docs-code"><a href="/docs/x"><button data-docs-copy><span class="docs-code-copy-text">Copy</span></button></a><pre>code</pre></div>`}
				onNavigate={(href) => visited.push(href)}
			/>,
		);
		await act(async () => {
			fireEvent.click(screen.getByText("Copy"));
		});
		expect(visited).toEqual([]);
	});

	test("asks for the keys when the browser refuses the clipboard", async () => {
		spyOn(navigator.clipboard, "writeText").mockRejectedValue(
			new DOMException("denied", "NotAllowedError"),
		);
		render(<DocContent html={HTML} onNavigate={() => {}} />);
		await act(async () => {
			fireEvent.click(screen.getByText("Copy"));
		});
		expect(screen.getByText("Press ⌘C")).toBeInTheDocument();
		expect(copyButton()).not.toHaveAttribute("data-copied");
	});
});
