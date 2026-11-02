import { describe, expect, it } from "bun:test";
import { render, screen, within } from "@testing-library/react";
import { Block, Note } from "./block";

describe("Block", () => {
	it("is a landmark named by its title with the content inside", () => {
		render(
			<Block title="Incidents">
				<p>Nothing posted.</p>
			</Block>,
		);
		const section = screen.getByRole("region", { name: "Incidents" });
		expect(
			within(section).getByRole("heading", { level: 2 }),
		).toHaveTextContent("Incidents");
		expect(within(section).getByText("Nothing posted.")).toBeInTheDocument();
	});

	it("shows the aside in the heading only when one is given", () => {
		const { rerender } = render(
			<Block title="Maintenance" aside={<a href="/all">View all</a>}>
				<span>body</span>
			</Block>,
		);
		expect(
			within(screen.getByRole("heading", { level: 2 })).getByRole("link", {
				name: "View all",
			}),
		).toBeInTheDocument();
		rerender(
			<Block title="Maintenance">
				<span>body</span>
			</Block>,
		);
		expect(screen.queryByRole("link")).not.toBeInTheDocument();
	});
});

describe("Note", () => {
	it("renders its guidance as text", () => {
		render(<Note>Posting is public.</Note>);
		expect(screen.getByText("Posting is public.")).toBeInTheDocument();
	});
});
