import { describe, expect, it } from "bun:test";
import { render, screen, within } from "@testing-library/react";
import { NEW_NAME_NOTE } from "#/lib/dns-new-name";
import { Fact, FactGrid } from "./key-value";
import { MetricTile } from "./metric-tile";
import { NewNameNote } from "./new-name-note";
import { SaveBarStatus } from "./save-bar-status";
import { StatePanel } from "./state-panel";
import { ToneIcon } from "./tone-icon";

describe("FactGrid and Fact", () => {
	it("pairs each label with its value as a definition list", () => {
		const { container } = render(
			<FactGrid>
				<Fact label="Region">fra1</Fact>
				<Fact label="Owner" mono={false}>
					Robin
				</Fact>
			</FactGrid>,
		);
		const terms = Array.from(container.querySelectorAll("dt")).map(
			(el) => el.textContent,
		);
		const values = Array.from(container.querySelectorAll("dd")).map(
			(el) => el.textContent,
		);
		expect(terms).toEqual(["Region", "Owner"]);
		expect(values).toEqual(["fra1", "Robin"]);
		expect(container.querySelector("dl")).toHaveAttribute(
			"data-slot",
			"fact-grid",
		);
	});

	it("shows a value in mono by default and in the body face with mono off", () => {
		render(
			<FactGrid>
				<Fact label="Mono">a</Fact>
				<Fact label="Plain" mono={false}>
					b
				</Fact>
			</FactGrid>,
		);
		expect(screen.getByText("a").className).toContain("font-mono");
		expect(screen.getByText("b").className).not.toContain("font-mono");
	});
});

describe("MetricTile", () => {
	it("shows the label, the value and the hint", () => {
		render(<MetricTile label="Uptime" value="99.9%" hint="last 30 days" />);
		expect(screen.getByText("Uptime")).toBeInTheDocument();
		expect(screen.getByText("99.9%")).toBeInTheDocument();
		expect(screen.getByText("last 30 days")).toBeInTheDocument();
	});

	it("omits the hint line when none is given", () => {
		const { container } = render(<MetricTile label="Uptime" value="–" />);
		expect(container.querySelectorAll("span")).toHaveLength(2);
	});
});

describe("StatePanel", () => {
	it("renders the title as the page heading with body, detail and actions", () => {
		render(
			<StatePanel
				tone="danger"
				title="No access"
				body="Ask an owner to invite you."
				detail="robin@example.com"
				actions={<button type="button">Sign out</button>}
			/>,
		);
		expect(
			screen.getByRole("heading", { level: 1, name: "No access" }),
		).toBeInTheDocument();
		expect(screen.getByText("Ask an owner to invite you.")).toBeInTheDocument();
		expect(screen.getByText("robin@example.com")).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Sign out" }),
		).toBeInTheDocument();
	});

	it("carries its tone for styling and leaves out absent detail and actions", () => {
		const { container } = render(
			<StatePanel tone="warning" title="Careful" body="Heads up" />,
		);
		expect(container.firstElementChild).toHaveAttribute("data-tone", "warning");
		expect(screen.queryByRole("button")).toBeNull();
		expect(container.querySelector(".surface-well")).toBeNull();
	});
});

describe("ToneIcon", () => {
	it("is decorative for every tone", () => {
		const { container } = render(
			<>
				<ToneIcon tone="danger" />
				<ToneIcon tone="warning" />
				<ToneIcon tone="info" />
				<ToneIcon tone="success" />
			</>,
		);
		const icons = container.querySelectorAll("svg");
		expect(icons).toHaveLength(4);
		for (const icon of icons)
			expect(icon).toHaveAttribute("aria-hidden", "true");
		// Each tone draws a different glyph.
		const paths = Array.from(icons).map((i) => i.innerHTML);
		expect(new Set(paths).size).toBe(4);
	});
});

describe("SaveBarStatus", () => {
	it("marks a saved state with a decorative check", () => {
		const { container } = render(
			<SaveBarStatus state="saved">All changes saved</SaveBarStatus>,
		);
		expect(screen.getByText("All changes saved")).toBeInTheDocument();
		expect(container.querySelectorAll("svg")).toHaveLength(1);
	});

	it("shows no icon for unsaved changes", () => {
		const { container } = render(
			<SaveBarStatus state="dirty">Unsaved changes</SaveBarStatus>,
		);
		expect(container.querySelector("svg")).toBeNull();
	});

	it("shows an icon and the error colour for a failed save", () => {
		const { container } = render(
			<SaveBarStatus state="failed">Could not save</SaveBarStatus>,
		);
		expect(container.querySelector("svg")).not.toBeNull();
		expect(container.firstElementChild?.className).toContain(
			"text-status-failed-on",
		);
	});

	it("shows an icon for an invalid form without the error colour", () => {
		const { container } = render(
			<SaveBarStatus state="invalid">Name is required</SaveBarStatus>,
		);
		expect(container.querySelector("svg")).not.toBeNull();
		expect(container.firstElementChild?.className).not.toContain(
			"text-status-failed-on",
		);
	});
});

describe("NewNameNote", () => {
	it("explains that a new DNS name can take a while to resolve", () => {
		const { container } = render(<NewNameNote />);
		expect(within(container).getByText(NEW_NAME_NOTE)).toBeInTheDocument();
	});
});
