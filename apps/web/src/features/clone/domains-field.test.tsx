import { describe, expect, it } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { DomainsField } from "./domains-field";
import type { DomainMode } from "./model";

function Harness({
	initialMode = "drop",
	example,
	onSuffixChange,
}: {
	initialMode?: DomainMode;
	example?: string;
	onSuffixChange?: (suffix: string) => void;
}) {
	const [mode, setMode] = useState<DomainMode>(initialMode);
	const [suffix, setSuffix] = useState("");
	return (
		<DomainsField
			mode={mode}
			onMode={setMode}
			suffix={suffix}
			onSuffix={(next) => {
				setSuffix(next);
				onSuffixChange?.(next);
			}}
			placeholder="-copy"
			example={example}
		/>
	);
}

describe("DomainsField", () => {
	it("starts without domains and offers no suffix input", () => {
		render(<Harness />);
		expect(
			screen.getByText(/The clone starts without domains/),
		).toBeInTheDocument();
		expect(screen.queryByLabelText("Domains")).not.toBeInTheDocument();
	});

	it("switches to suffixing and back", async () => {
		const user = userEvent.setup();
		render(<Harness />);
		await user.click(screen.getByRole("button", { name: "Suffix the names" }));
		expect(screen.getByLabelText("Domains")).toHaveAttribute(
			"placeholder",
			"-copy",
		);
		expect(
			screen.getByText(/A name that is already taken stops the clone/),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "No domains" }));
		expect(screen.queryByLabelText("Domains")).not.toBeInTheDocument();
	});

	it("previews the example with the placeholder, then with the typed suffix", async () => {
		const user = userEvent.setup();
		render(<Harness initialMode="suffix" example="app.example.com" />);
		expect(screen.getByText("app-copy.example.com")).toBeInTheDocument();
		await user.type(screen.getByLabelText("Domains"), "Beta");
		// The preview follows the suffix the server will read: lowercase, one hyphen.
		expect(screen.getByText("app-beta.example.com")).toBeInTheDocument();
		expect(screen.getByText("app.example.com")).toBeInTheDocument();
	});

	it("reports each edit of the suffix", async () => {
		const user = userEvent.setup();
		const seen: string[] = [];
		render(
			<Harness initialMode="suffix" onSuffixChange={(s) => seen.push(s)} />,
		);
		await user.type(screen.getByLabelText("Domains"), "-x");
		expect(seen).toEqual(["-", "-x"]);
	});

	it("shows no preview for an apex name, which cannot be suffixed", () => {
		render(<Harness initialMode="suffix" example="example.com" />);
		expect(screen.queryByText(/becomes/)).not.toBeInTheDocument();
	});

	it("shows no preview without an example hostname", () => {
		render(<Harness initialMode="suffix" />);
		expect(screen.queryByText(/becomes/)).not.toBeInTheDocument();
		expect(screen.getByText(/Added to the first label/)).toBeInTheDocument();
	});
});
