import { describe, expect, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import { renderWithProviders } from "#/test/render";
import { ResultStep } from "./result-step";
import { applied } from "./test-fixtures";

describe("ResultStep", () => {
	test("says what was staged when nothing was deployed", async () => {
		await renderWithProviders(
			<ResultStep result={applied()} deployed={false} />,
		);
		expect(screen.getByRole("status")).toHaveTextContent(
			"Imported: 1 project, 1 service; nothing deployed.",
		);
		expect(
			screen.getByText(/Everything is staged and nothing runs yet/),
		).toBeInTheDocument();
		expect(screen.queryByText(/The services are deploying/)).toBeNull();
	});

	test("says the services are deploying when they were", async () => {
		await renderWithProviders(
			<ResultStep result={applied({ deployments: ["d1", "d2"] })} deployed />,
		);
		expect(screen.getByRole("status")).toHaveTextContent("; 2 deploying.");
		expect(screen.getByText(/The services are deploying/)).toBeInTheDocument();
	});

	test("lists created and merged projects with their environments", async () => {
		await renderWithProviders(
			<ResultStep
				deployed={false}
				result={applied({
					projects: [
						{
							id: "p1",
							name: "Shop",
							slug: "shop",
							created: true,
							environments: [
								{
									id: "e1",
									name: "production",
									slug: "production",
									created: true,
								},
								{ id: "e2", name: "staging", slug: "staging", created: false },
							],
						},
						{
							id: "p2",
							name: "Blog",
							slug: "blog",
							created: false,
							environments: null,
						},
					],
				})}
			/>,
		);
		const section = screen.getByRole("region", { name: "Projects" });
		expect(within(section).getByText("2")).toBeInTheDocument();
		const shop = within(section).getByText("Shop").closest("li") as HTMLElement;
		expect(within(shop).getByText("shop")).toBeInTheDocument();
		expect(within(shop).getByText("created")).toBeInTheDocument();
		expect(within(shop).getByText("production · new")).toBeInTheDocument();
		expect(within(shop).getByText("staging · reused")).toBeInTheDocument();
		const blog = within(section).getByText("Blog").closest("li") as HTMLElement;
		expect(within(blog).getByText("merged into")).toBeInTheDocument();
	});

	test("lists services by project, environment and slug with their kind", async () => {
		await renderWithProviders(
			<ResultStep result={applied()} deployed={false} />,
		);
		const section = screen.getByRole("region", { name: "Services" });
		expect(within(section).getByText("web")).toBeInTheDocument();
		expect(
			within(section).getByText("shop/production/web"),
		).toBeInTheDocument();
		expect(within(section).getByText("app")).toBeInTheDocument();
	});

	test("says so when no service was created", async () => {
		await renderWithProviders(
			<ResultStep result={applied({ services: null })} deployed={false} />,
		);
		expect(screen.getByText("No service was created.")).toBeInTheDocument();
	});

	test("shows the server's warnings, singular and plural", async () => {
		const { unmount } = await renderWithProviders(
			<ResultStep
				result={applied({ warnings: ["One thing"] })}
				deployed={false}
			/>,
		);
		expect(screen.getByText("A note about this import")).toBeInTheDocument();
		expect(screen.getByText("One thing")).toBeInTheDocument();
		unmount();

		await renderWithProviders(
			<ResultStep
				result={applied({ warnings: ["a", "b"] })}
				deployed={false}
			/>,
		);
		expect(screen.getByText("2 notes about this import")).toBeInTheDocument();
	});

	test("lists the remaining manual steps with copyable commands", async () => {
		await renderWithProviders(
			<ResultStep
				deployed={false}
				result={applied({
					followups: [
						"db: the data is not copied. Dump it on the Coolify host (docker exec abc pg_dump -U postgres -Fc app > db.dump), then run: nebulactrl databases restore db --file db.dump",
						"Point DNS at the new server.",
					],
				})}
			/>,
		);
		const section = screen.getByRole("region", { name: "Still to do" });
		expect(within(section).getByText("2")).toBeInTheDocument();
		expect(
			within(section).getByText(
				"docker exec abc pg_dump -U postgres -Fc app > db.dump",
			),
		).toBeInTheDocument();
		expect(
			within(section).getByText(
				"nebulactrl databases restore db --file db.dump",
			),
		).toBeInTheDocument();
		expect(
			within(section).getByText("Point DNS at the new server."),
		).toBeInTheDocument();
		expect(
			within(section).getAllByRole("button", { name: "Copy command" }),
		).toHaveLength(2);
	});

	test("no follow-up or warning sections when there are none", async () => {
		await renderWithProviders(
			<ResultStep result={applied()} deployed={false} />,
		);
		expect(screen.queryByRole("region", { name: "Still to do" })).toBeNull();
		expect(screen.queryByRole("note")).toBeNull();
	});
});
