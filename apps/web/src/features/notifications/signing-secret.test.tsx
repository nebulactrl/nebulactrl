import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { VERIFY_SNIPPETS } from "./model";
import { SigningSecretPanel } from "./signing-secret";

describe("SigningSecretPanel", () => {
	test("shows the secret once, with the shown-once warning", () => {
		render(<SigningSecretPanel secret="whsec_0123456789abcdef" />);
		expect(screen.getByTestId("signing-secret")).toHaveTextContent(
			"whsec_0123456789abcdef",
		);
		expect(screen.getByText("Shown once")).toBeInTheDocument();
		expect(screen.getByText(/never shows it again/)).toBeInTheDocument();
	});

	test("starts on the Node.js verification snippet", () => {
		render(<SigningSecretPanel secret="s" />);
		expect(screen.getByText(/createHmac/, { selector: "pre" })).toBeTruthy();
		expect(document.querySelector("pre")?.textContent).toBe(
			VERIFY_SNIPPETS.node,
		);
	});

	test("picking a language swaps the snippet", async () => {
		const user = userEvent.setup();
		render(<SigningSecretPanel secret="s" />);
		await user.click(screen.getByRole("button", { name: "Python" }));
		expect(document.querySelector("pre")?.textContent).toBe(
			VERIFY_SNIPPETS.python,
		);
		await user.click(screen.getByRole("button", { name: "Go" }));
		expect(document.querySelector("pre")?.textContent).toBe(VERIFY_SNIPPETS.go);
	});

	test("names the signature headers the receiver must check", () => {
		render(<SigningSecretPanel secret="s" />);
		expect(screen.getByText("X-NebulaCtrl-Signature")).toBeInTheDocument();
		expect(screen.getByText("X-NebulaCtrl-Event")).toBeInTheDocument();
		expect(screen.getByText("X-NebulaCtrl-Delivery")).toBeInTheDocument();
	});
});
