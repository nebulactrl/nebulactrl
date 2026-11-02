import { afterEach, describe, expect, jest, test } from "bun:test";
import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { CopyMarkdownButton } from "./copy-markdown";

const IDLE = "Copy page as Markdown";
const realFetch = globalThis.fetch;
const realClipboard = Object.getOwnPropertyDescriptor(navigator, "clipboard");
const realClipboardItem = globalThis.ClipboardItem;

// user-event's setup() would replace navigator.clipboard, so clicks use fireEvent.

/** Records the blobs handed to `clipboard.write` so a test can read what was copied. */
class FakeClipboardItem {
	constructor(readonly items: Record<string, Promise<Blob>>) {}
}

interface Clipboard {
	written: string[];
	api: {
		write?: (items: FakeClipboardItem[]) => Promise<void>;
		writeText?: (text: string) => Promise<void>;
	};
}

function useClipboard(
	options: { items?: boolean; text?: boolean } = {},
): Clipboard {
	const clipboard: Clipboard = { written: [], api: {} };
	if (options.items) {
		clipboard.api.write = async (items) => {
			const blob = await items[0]?.items["text/plain"];
			clipboard.written.push(await (blob as Blob).text());
		};
		globalThis.ClipboardItem =
			FakeClipboardItem as unknown as typeof ClipboardItem;
	} else {
		// @ts-expect-error: removing the global to model a browser without it
		delete globalThis.ClipboardItem;
	}
	if (options.text) {
		clipboard.api.writeText = async (text) => {
			clipboard.written.push(text);
		};
	}
	Object.defineProperty(navigator, "clipboard", {
		value: clipboard.api,
		configurable: true,
	});
	return clipboard;
}

function stubFetch(reply: () => Response | Promise<Response>) {
	const urls: string[] = [];
	globalThis.fetch = (async (input: RequestInfo | URL) => {
		urls.push(String(input));
		return reply();
	}) as typeof fetch;
	return urls;
}

afterEach(() => {
	jest.restoreAllMocks();
	globalThis.fetch = realFetch;
	globalThis.ClipboardItem = realClipboardItem;
	if (realClipboard)
		Object.defineProperty(navigator, "clipboard", realClipboard);
	else Reflect.deleteProperty(navigator, "clipboard");
});

describe("CopyMarkdownButton", () => {
	test("copies the page's markdown through a ClipboardItem promise", async () => {
		const clipboard = useClipboard({ items: true, text: true });
		const urls = stubFetch(() => new Response("# Backups\n\nBody"));
		render(<CopyMarkdownButton slug="operations/backups" />);
		fireEvent.click(screen.getByRole("button", { name: IDLE }));
		await waitFor(() =>
			expect(screen.getByRole("button")).toHaveTextContent(
				"Copied as Markdown",
			),
		);
		expect(urls).toEqual(["/docs/operations/backups.md"]);
		// Not writeText: Safari only accepts the write when it is handed a promise.
		expect(clipboard.written).toEqual(["# Backups\n\nBody"]);
		expect(screen.getByRole("status")).toHaveTextContent(
			"Page copied as Markdown",
		);
	});

	test("falls back to writeText where ClipboardItem is missing", async () => {
		const clipboard = useClipboard({ text: true });
		const urls = stubFetch(() => new Response("raw markdown"));
		render(<CopyMarkdownButton slug="getting-started/overview" />);
		fireEvent.click(screen.getByRole("button", { name: IDLE }));
		await waitFor(() =>
			expect(screen.getByRole("button")).toHaveTextContent(
				"Copied as Markdown",
			),
		);
		expect(urls).toEqual(["/docs/getting-started/overview.md"]);
		expect(clipboard.written).toEqual(["raw markdown"]);
	});

	test("says it could not copy when the page's markdown is not served", async () => {
		const clipboard = useClipboard({ text: true });
		stubFetch(() => new Response("nope", { status: 404 }));
		render(<CopyMarkdownButton slug="missing" />);
		fireEvent.click(screen.getByRole("button", { name: IDLE }));
		await waitFor(() =>
			expect(screen.getByRole("button")).toHaveTextContent("Could not copy"),
		);
		expect(clipboard.written).toEqual([]);
		expect(screen.getByRole("status")).toBeEmptyDOMElement();
	});

	test("says it could not copy when the browser has no clipboard", async () => {
		useClipboard();
		stubFetch(() => new Response("text"));
		render(<CopyMarkdownButton slug="a" />);
		fireEvent.click(screen.getByRole("button", { name: IDLE }));
		await waitFor(() =>
			expect(screen.getByRole("button")).toHaveTextContent("Could not copy"),
		);
	});

	test("says it could not copy when the clipboard write is refused", async () => {
		const clipboard = useClipboard({ items: true });
		clipboard.api.write = () => Promise.reject(new Error("NotAllowedError"));
		stubFetch(() => new Response("text"));
		render(<CopyMarkdownButton slug="a" />);
		fireEvent.click(screen.getByRole("button", { name: IDLE }));
		await waitFor(() =>
			expect(screen.getByRole("button")).toHaveTextContent("Could not copy"),
		);
	});

	test("is disabled while the markdown is being fetched", async () => {
		useClipboard({ text: true });
		let release: (response: Response) => void = () => {};
		stubFetch(() => new Promise<Response>((resolve) => (release = resolve)));
		render(<CopyMarkdownButton slug="a" />);
		fireEvent.click(screen.getByRole("button", { name: IDLE }));
		expect(screen.getByRole("button")).toBeDisabled();
		await act(async () => {
			release(new Response("done"));
		});
		await waitFor(() => expect(screen.getByRole("button")).toBeEnabled());
		expect(screen.getByRole("button")).toHaveTextContent("Copied as Markdown");
	});

	test("returns to its first label once the confirmation has been shown", async () => {
		useClipboard({ text: true });
		stubFetch(() => new Response("text"));
		render(<CopyMarkdownButton slug="a" />);
		jest.useFakeTimers();
		try {
			await act(async () => {
				fireEvent.click(screen.getByRole("button", { name: IDLE }));
			});
			expect(screen.getByRole("button")).toHaveTextContent(
				"Copied as Markdown",
			);
			act(() => {
				jest.advanceTimersByTime(2_000);
			});
			expect(screen.getByRole("button")).toHaveTextContent(
				"Copied as Markdown",
			);
			act(() => {
				jest.advanceTimersByTime(500);
			});
			expect(screen.getByRole("button")).toHaveTextContent(IDLE);
			expect(screen.getByRole("status")).toBeEmptyDOMElement();
		} finally {
			jest.useRealTimers();
		}
	});
});
