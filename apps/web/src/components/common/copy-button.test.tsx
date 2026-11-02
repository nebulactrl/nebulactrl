import {
	afterEach,
	beforeEach,
	describe,
	expect,
	it,
	jest,
	spyOn,
} from "bun:test";
import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithProviders } from "#/test/render";
import { CodeBlock } from "./code-block";
import {
	COPIED_MS,
	CopyButton,
	CopyChipFaces,
	copyWithToast,
	writeClipboard,
} from "./copy-button";

let writeText: ReturnType<typeof spyOn>;

function stubClipboard(impl: (text: string) => Promise<void>) {
	writeText.mockImplementation(impl);
}

// userEvent.setup() swaps in its own clipboard, so the spy goes on afterwards.
function setupUser() {
	const user = userEvent.setup();
	writeText = spyOn(navigator.clipboard, "writeText");
	stubClipboard(async () => {});
	return user;
}

beforeEach(() => {
	writeText = spyOn(navigator.clipboard, "writeText");
	stubClipboard(async () => {});
});

afterEach(() => {
	jest.useRealTimers();
	jest.restoreAllMocks();
});

describe("writeClipboard", () => {
	it("resolves true once the text is written", async () => {
		expect(await writeClipboard("hello")).toBe(true);
		expect(writeText).toHaveBeenCalledWith("hello");
	});

	it("resolves false when the browser rejects the write", async () => {
		stubClipboard(async () => {
			throw new DOMException("denied", "NotAllowedError");
		});
		expect(await writeClipboard("hello")).toBe(false);
	});

	it("resolves false, without throwing, when the Clipboard API is absent (plain http)", async () => {
		const descriptor = Object.getOwnPropertyDescriptor(navigator, "clipboard");
		Object.defineProperty(navigator, "clipboard", {
			value: undefined,
			configurable: true,
		});
		try {
			expect(await writeClipboard("hello")).toBe(false);
		} finally {
			if (descriptor) Object.defineProperty(navigator, "clipboard", descriptor);
			else Reflect.deleteProperty(navigator, "clipboard");
		}
	});
});

describe("copyWithToast", () => {
	it("raises a success toast naming what was copied", async () => {
		await renderWithProviders(<p>page</p>);
		await act(async () => {
			await copyWithToast("ssh root@10.0.0.1");
		});
		expect(await screen.findByText("Copied")).toBeInTheDocument();
		expect(screen.getByText("ssh root@10.0.0.1")).toBeInTheDocument();
	});

	it("tells the reader to copy by hand when the clipboard is blocked", async () => {
		stubClipboard(async () => {
			throw new Error("blocked");
		});
		await renderWithProviders(<p>page</p>);
		await act(async () => {
			await copyWithToast("secret-token");
		});
		expect(await screen.findByText("Nothing was copied")).toBeInTheDocument();
		expect(
			screen.getByText(/Select it and copy by hand: secret-token/),
		).toBeInTheDocument();
	});
});

describe("CopyChipFaces", () => {
	it.each([
		["idle", "Copy"],
		["copied", "Copied"],
		["failed", "Select and copy"],
	] as const)("reads %s as %s", async (state, text) => {
		const { container } = await renderWithProviders(
			<CopyChipFaces state={state} />,
		);
		expect(container).toHaveTextContent(text);
	});

	it("uses the given idle label", async () => {
		const { container } = await renderWithProviders(
			<CopyChipFaces state="idle">Copy key</CopyChipFaces>,
		);
		expect(container).toHaveTextContent("Copy key");
	});
});

describe("CopyButton", () => {
	it("copies its value, says Copied, and reverts after the confirmation window", async () => {
		await renderWithProviders(
			<CopyButton value="abc-123">Copy token</CopyButton>,
		);
		jest.useFakeTimers();
		const button = screen.getByRole("button", { name: /Copy token/ });
		await act(async () => {
			fireEvent.click(button);
		});
		expect(writeText).toHaveBeenCalledWith("abc-123");
		expect(button).toHaveTextContent("Copied");
		expect(screen.getByRole("status")).toHaveTextContent("Copied");

		act(() => {
			jest.advanceTimersByTime(COPIED_MS - 1);
		});
		expect(button).toHaveTextContent("Copied");
		act(() => {
			jest.advanceTimersByTime(1);
		});
		expect(button).toHaveTextContent("Copy token");
		expect(screen.getByRole("status")).toBeEmptyDOMElement();
	});

	it("offers manual copy when the clipboard write fails", async () => {
		const user = setupUser();
		stubClipboard(async () => {
			throw new Error("denied");
		});
		await renderWithProviders(<CopyButton value="abc">Copy</CopyButton>);
		await user.click(screen.getByRole("button", { name: /Copy/ }));
		await waitFor(() =>
			expect(screen.getByRole("button")).toHaveTextContent("Select and copy"),
		);
		expect(screen.getByRole("status")).toBeEmptyDOMElement();
	});

	it("keeps the button named by its label when icon-only", async () => {
		const user = setupUser();
		await renderWithProviders(
			<CopyButton value="v" label="Copy the deploy key" />,
		);
		const button = screen.getByRole("button", { name: "Copy the deploy key" });
		await user.click(button);
		expect(writeText).toHaveBeenCalledWith("v");
		await waitFor(() =>
			expect(screen.getByRole("status")).toHaveTextContent("Copied"),
		);
	});

	it("defaults an icon-only button's name to Copy", async () => {
		await renderWithProviders(<CopyButton value="v" />);
		expect(screen.getByRole("button", { name: "Copy" })).toBeInTheDocument();
	});

	it("copies from the chip variant and announces it", async () => {
		const user = setupUser();
		await renderWithProviders(
			<CopyButton variant="chip" value="chip-value">
				Copy
			</CopyButton>,
		);
		await user.click(screen.getByRole("button"));
		expect(writeText).toHaveBeenCalledWith("chip-value");
		await waitFor(() =>
			expect(screen.getByRole("status")).toHaveTextContent("Copied"),
		);
	});

	it("copies on keyboard activation", async () => {
		const user = setupUser();
		await renderWithProviders(<CopyButton value="kbd">Copy</CopyButton>);
		screen.getByRole("button").focus();
		await user.keyboard("{Enter}");
		expect(writeText).toHaveBeenCalledWith("kbd");
	});
});

describe("CodeBlock", () => {
	it("is plain text without copyValue: no button, no status", async () => {
		await renderWithProviders(<CodeBlock>docker compose up</CodeBlock>);
		expect(screen.getByText("docker compose up")).toBeInTheDocument();
		expect(screen.queryByRole("button")).toBeNull();
		expect(screen.queryByRole("status")).toBeNull();
	});

	it("copies the exact value when the block is clicked", async () => {
		const user = setupUser();
		await renderWithProviders(
			<CodeBlock copyValue="npm i -g nebula">npm i -g nebula</CodeBlock>,
		);
		const block = screen.getByRole("button", {
			name: "Copy to clipboard: npm i -g nebula",
		});
		await user.click(block);
		expect(writeText).toHaveBeenCalledWith("npm i -g nebula");
		await waitFor(() =>
			expect(screen.getByRole("status")).toHaveTextContent("Copied"),
		);
	});

	it.each(["{Enter}", " "])("copies on the %j key", async (key) => {
		const user = setupUser();
		await renderWithProviders(<CodeBlock copyValue="x">x</CodeBlock>);
		screen.getByRole("button").focus();
		await user.keyboard(key);
		expect(writeText).toHaveBeenCalledWith("x");
	});

	it("ignores other keys", async () => {
		const user = setupUser();
		await renderWithProviders(<CodeBlock copyValue="x">x</CodeBlock>);
		screen.getByRole("button").focus();
		await user.keyboard("a");
		expect(writeText).not.toHaveBeenCalled();
	});

	it("leaves the clipboard alone when the click ends a text selection", async () => {
		const user = setupUser();
		await renderWithProviders(
			<CodeBlock copyValue="whole command">whole command</CodeBlock>,
		);
		const selection = spyOn(window, "getSelection").mockReturnValue({
			isCollapsed: false,
		} as Selection);
		await user.click(screen.getByRole("button"));
		expect(selection).toHaveBeenCalled();
		expect(writeText).not.toHaveBeenCalled();
	});

	it("reverts the confirmation after the window", async () => {
		await renderWithProviders(<CodeBlock copyValue="x">x</CodeBlock>);
		jest.useFakeTimers();
		await act(async () => {
			fireEvent.click(screen.getByRole("button"));
		});
		expect(screen.getByRole("status")).toHaveTextContent("Copied");
		act(() => {
			jest.advanceTimersByTime(COPIED_MS);
		});
		expect(screen.getByRole("status")).toBeEmptyDOMElement();
	});
});
