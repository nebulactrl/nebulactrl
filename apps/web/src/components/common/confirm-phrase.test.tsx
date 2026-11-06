import { afterEach, describe, expect, it, jest, spyOn } from "bun:test";
import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithProviders } from "#/test/render";
import { ConfirmPhrase, ConfirmPrompt } from "./confirm-phrase";
import { COPIED_MS } from "./copy-button";

afterEach(() => {
	jest.useRealTimers();
	jest.restoreAllMocks();
});

// userEvent.setup() swaps in its own clipboard, so the spy goes on afterwards.
function setupUser(impl: (text: string) => Promise<void> = async () => {}) {
	const user = userEvent.setup();
	const writeText = spyOn(navigator.clipboard, "writeText");
	writeText.mockImplementation(impl);
	return { user, writeText };
}

describe("ConfirmPhrase", () => {
	it("shows the phrase with an icon-only Copy button that never submits", async () => {
		await renderWithProviders(<ConfirmPhrase value="homelab-1" />);
		expect(screen.getByText("homelab-1")).toBeInTheDocument();
		const button = screen.getByRole("button", { name: "Copy homelab-1" });
		expect(button).toHaveAttribute("type", "button");
		expect(button).toHaveTextContent("");
	});

	it("keeps the site's own phrase style", async () => {
		await renderWithProviders(
			<ConfirmPhrase value="db" className="type-mono" />,
		);
		expect(screen.getByText("db")).toHaveClass("type-mono");
	});

	it("copies the exact value and says so for COPIED_MS", async () => {
		const { user, writeText } = setupUser();
		await renderWithProviders(<ConfirmPhrase value="homelab-1" />);
		await user.click(screen.getByRole("button", { name: "Copy homelab-1" }));
		expect(writeText).toHaveBeenCalledTimes(1);
		expect(writeText).toHaveBeenCalledWith("homelab-1");
		expect(await screen.findByRole("status")).toHaveTextContent("Copied");
		await waitFor(
			() => expect(screen.getByRole("status")).toHaveTextContent(""),
			{ timeout: COPIED_MS + 1_000 },
		);
	});

	it("tells the reader to copy by hand when the clipboard is blocked", async () => {
		const { user } = setupUser(async () => {
			throw new Error("blocked");
		});
		await renderWithProviders(<ConfirmPhrase value="homelab-1" />);
		await user.click(screen.getByRole("button", { name: "Copy homelab-1" }));
		expect(await screen.findByText("Nothing was copied")).toBeInTheDocument();
		expect(
			screen.getByText(/Select it and copy by hand: homelab-1/),
		).toBeInTheDocument();
		expect(screen.getByRole("status")).toHaveTextContent("");
	});

	it("falls back the same way when the Clipboard API is absent (plain http)", async () => {
		const user = userEvent.setup();
		const descriptor = Object.getOwnPropertyDescriptor(navigator, "clipboard");
		Object.defineProperty(navigator, "clipboard", {
			value: undefined,
			configurable: true,
		});
		try {
			await renderWithProviders(<ConfirmPhrase value="homelab-1" />);
			await act(async () => {
				await user.click(
					screen.getByRole("button", { name: "Copy homelab-1" }),
				);
			});
			expect(await screen.findByText("Nothing was copied")).toBeInTheDocument();
		} finally {
			if (descriptor) Object.defineProperty(navigator, "clipboard", descriptor);
			else Reflect.deleteProperty(navigator, "clipboard");
		}
	});
});

describe("ConfirmPrompt", () => {
	it("labels the field 'Type <phrase> to confirm' with the copy button outside both labels", async () => {
		await renderWithProviders(
			<>
				<ConfirmPrompt htmlFor="c" value="homelab-1" />
				<input id="c" />
			</>,
		);
		const input = screen.getByRole("textbox", {
			name: "Type homelab-1 to confirm",
		});
		expect(screen.getByLabelText(/Type homelab-1 to confirm/)).toBe(input);
		const button = screen.getByRole("button", { name: "Copy homelab-1" });
		expect(button.closest("label")).toBeNull();
	});

	it("takes another tail", async () => {
		await renderWithProviders(
			<>
				<ConfirmPrompt htmlFor="c" value="db" tail="to continue" />
				<input id="c" />
			</>,
		);
		expect(
			screen.getByRole("textbox", { name: "Type db to continue" }),
		).toBeInTheDocument();
	});

	it("copies without focusing the field or submitting the form", async () => {
		const { user, writeText } = setupUser();
		const submit = jest.fn((event: { preventDefault(): void }) =>
			event.preventDefault(),
		);
		await renderWithProviders(
			<form onSubmit={submit}>
				<ConfirmPrompt htmlFor="c" value="homelab-1" />
				<input id="c" />
			</form>,
		);
		await user.click(screen.getByRole("button", { name: "Copy homelab-1" }));
		expect(writeText).toHaveBeenCalledWith("homelab-1");
		expect(submit).not.toHaveBeenCalled();
		expect(screen.getByRole("textbox")).not.toHaveFocus();
	});
});
