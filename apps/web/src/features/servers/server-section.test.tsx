import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import { ServerSection } from "./server-section";

describe("ServerSection", () => {
	test("is a region named by its title, with its content inside", () => {
		render(
			<ServerSection id="usage" title="Usage">
				<p>body text</p>
			</ServerSection>,
		);
		const region = screen.getByRole("region", { name: "Usage" });
		expect(region).toHaveAttribute("id", "usage");
		expect(region).toHaveTextContent("body text");
		expect(
			screen.getByRole("heading", { level: 2, name: "Usage" }),
		).toHaveAttribute("id", "usage-title");
	});

	test("the optional note sits in the heading", () => {
		render(
			<ServerSection id="usage" title="Usage" description="last 24 hours">
				<p>x</p>
			</ServerSection>,
		);
		expect(screen.getByRole("heading", { level: 2 })).toHaveTextContent(
			"Usagelast 24 hours",
		);
		expect(screen.getByText("last 24 hours")).toBeVisible();
	});

	test("without a note the heading is the title alone", () => {
		render(
			<ServerSection id="usage" title="Usage">
				<p>x</p>
			</ServerSection>,
		);
		expect(screen.getByRole("heading", { level: 2 })).toHaveTextContent(
			/^Usage$/,
		);
	});
});
