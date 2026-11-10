import { describe, expect, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithProviders } from "#/test/render";
import { ResultStep } from "./result-step";
import { applied } from "./test-fixtures";

describe("ResultStep", () => {
	test("says what was adopted, with the counts of the rest", async () => {
		await renderWithProviders(
			<ResultStep result={applied({ left: 2, removed: 1, reattached: 1 })} />,
		);
		expect(screen.getByRole("status")).toHaveTextContent(
			"Adopted 1 service, reattached 1, left 2 running unmanaged, removed 1.",
		);
		expect(
			screen.getByText(/The adopted services were not restarted/),
		).toBeInTheDocument();
		expect(
			screen.getByText(/The services you left keep running, unmanaged/),
		).toBeInTheDocument();
		expect(
			screen.getByText(/their volumes stay on the host/),
		).toBeInTheDocument();
	});

	test("links each adopted service to its canvas", async () => {
		await renderWithProviders(<ResultStep result={applied()} />);
		const section = screen.getByRole("region", { name: "Services" });
		const link = within(section).getByRole("link", { name: "web" });
		expect(link).toHaveAttribute(
			"href",
			"/p/p-1/production?service=11111111-1111-4111-8111-111111111111",
		);
		expect(
			within(section).getByText("adopted-from-alpha/production/web"),
		).toBeInTheDocument();
	});

	test("following a link tells the dialog", async () => {
		let followed = 0;
		const user = userEvent.setup();
		await renderWithProviders(
			<ResultStep result={applied()} onNavigate={() => (followed += 1)} />,
		);
		await user.click(screen.getByRole("link", { name: "web" }));
		expect(followed).toBe(1);
	});

	test("lists the projects the adoption created", async () => {
		await renderWithProviders(<ResultStep result={applied()} />);
		const section = screen.getByRole("region", { name: "New projects" });
		expect(within(section).getByText("Adopted from alpha")).toBeInTheDocument();
		expect(within(section).getByText("adopted-from-alpha")).toBeInTheDocument();
	});

	test("no new project, no section; no service adopted, says so", async () => {
		await renderWithProviders(
			<ResultStep
				result={applied({ projects: null, services: null, left: 3 })}
			/>,
		);
		expect(
			screen.queryByRole("region", { name: "New projects" }),
		).not.toBeInTheDocument();
		expect(screen.getByText("No service was adopted.")).toBeInTheDocument();
		expect(screen.getByText(/Nothing was adopted/)).toBeInTheDocument();
	});

	test("shows the server's warnings, singular and plural", async () => {
		const { unmount } = await renderWithProviders(
			<ResultStep result={applied({ warnings: ["One thing"] })} />,
		);
		expect(screen.getByText("A note about this adoption")).toBeInTheDocument();
		expect(screen.getByText("One thing")).toBeInTheDocument();
		unmount();
		await renderWithProviders(
			<ResultStep result={applied({ warnings: ["a", "b"] })} />,
		);
		expect(screen.getByText("2 notes about this adoption")).toBeInTheDocument();
	});
});
