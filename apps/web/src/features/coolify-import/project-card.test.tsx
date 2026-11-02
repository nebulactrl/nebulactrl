import { describe, expect, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
	CoolifyPlan,
	CoolifyPlanProject,
	Project,
} from "#/api/gen/types.gen";
import { renderWithProviders } from "#/test/render";
import {
	type Baseline,
	buildChoices,
	EMPTY_DRAFT,
	type ImportDraft,
} from "./lib";
import { ProjectCard } from "./project-card";
import { plan, project, resource } from "./test-fixtures";
import { DraftHarness } from "./test-harness";

// Builds a literal `${NAME}` placeholder (the escaped `$` keeps it from interpolating).
const ref = (name: string) => `\${${name}}`;

const EXISTING = [
	{ id: "np-1", name: "Existing shop", slug: "existing" },
	{ id: "np-2", name: "Internal", slug: "internal" },
] as Project[];

async function renderProject(
	card: CoolifyPlanProject,
	{
		initial = EMPTY_DRAFT,
		baseline,
		thePlan = plan({ projects: [card] }),
		existingProjects = EXISTING,
	}: {
		initial?: ImportDraft;
		baseline?: Baseline;
		thePlan?: CoolifyPlan;
		existingProjects?: readonly Project[];
	} = {},
) {
	const seen = { current: initial };
	await renderWithProviders(
		<ul>
			<DraftHarness initial={initial} seen={seen}>
				{(draft, onDraft) => (
					<ProjectCard
						project={card}
						plan={thePlan}
						draft={draft}
						baseline={baseline}
						existingProjects={existingProjects}
						onDraft={onDraft}
					/>
				)}
			</DraftHarness>
		</ul>,
	);
	return { choices: () => buildChoices(seen.current) };
}

describe("ProjectCard", () => {
	test("a new project shows its name, slug and action, its environment and resources", async () => {
		await renderProject(project());
		const card = document.querySelector(
			"[data-slot=coolify-project-card]",
		) as HTMLElement;
		expect(card).toHaveAttribute("data-action", "create");
		expect(within(card).getByText("Shop")).toBeInTheDocument();
		expect(within(card).getByText("slug shop")).toBeInTheDocument();
		expect(within(card).getByText("new project")).toBeInTheDocument();
		expect(screen.getByLabelText("New project name")).toHaveValue("Shop");

		const env = screen.getByRole("region", { name: "Environment production" });
		expect(within(env).getByText("new")).toBeInTheDocument();
		expect(
			within(env).getByRole("checkbox", { name: "Include web" }),
		).toBeInTheDocument();
	});

	test("a project the plan merges shows that, and a reused environment says reused", async () => {
		await renderProject(
			project({
				action: "existing",
				environments: [
					{
						name: "staging",
						slug: "staging",
						action: "existing",
						production: false,
						resources: [],
					},
				],
			}),
		);
		expect(
			screen.getByText("merged into an existing project"),
		).toBeInTheDocument();
		const env = screen.getByRole("region", { name: "Environment staging" });
		expect(within(env).getByText("reused")).toBeInTheDocument();
		expect(within(env).queryByText("production")).toBeNull();
		expect(
			within(env).getByText("No application or database in this environment."),
		).toBeInTheDocument();
	});

	test("only the production environment is badged production", async () => {
		await renderProject(
			project({
				environments: [
					{
						name: "live",
						slug: "live",
						action: "create",
						production: true,
						resources: [],
					},
					{
						name: "dev",
						slug: "dev",
						action: "create",
						production: false,
						resources: [],
					},
				],
			}),
		);
		expect(
			within(
				screen.getByRole("region", { name: "Environment live" }),
			).getByText("production"),
		).toBeInTheDocument();
		expect(
			within(
				screen.getByRole("region", { name: "Environment dev" }),
			).queryByText("production"),
		).toBeNull();
	});

	test("renaming the new project records the name", async () => {
		const user = userEvent.setup();
		const card = await renderProject(project());
		const name = screen.getByLabelText("New project name");
		await user.clear(name);
		await user.type(name, "Storefront");
		expect(name).toHaveValue("Storefront");
		expect(card.choices()).toEqual({
			projects: { "proj-1": { name: "Storefront" } },
		});
	});

	test("a name that is taken says so, before and after the reader names it", async () => {
		const user = userEvent.setup();
		await renderProject(
			project({
				name: "Shop (Coolify)",
				slug: "shop-coolify",
				collision: true,
			}),
			{
				baseline: { projectNames: { "proj-1": "Shop" }, domainHosts: {} },
			},
		);
		// The baseline name is what the card is titled with.
		expect(
			screen.getByText("Shop", { selector: "span.font-medium" }),
		).toBeInTheDocument();
		expect(screen.getByRole("note")).toHaveTextContent(
			"Name taken → will be created as Shop (Coolify).",
		);

		await user.type(screen.getByLabelText("New project name"), "x");
		expect(screen.getByRole("note")).toHaveTextContent(
			"That name is taken. The project is created as Shop (Coolify), slug shop-coolify.",
		);
	});

	test("no collision note for a free name", async () => {
		await renderProject(project());
		expect(screen.queryByRole("note")).toBeNull();
	});

	test("choosing an existing project swaps the name field for an explanation and records the project", async () => {
		const user = userEvent.setup();
		const card = await renderProject(project());
		await user.click(screen.getByRole("combobox", { name: "Import into" }));
		await user.click(screen.getByRole("option", { name: "Existing shop" }));

		expect(screen.queryByLabelText("New project name")).toBeNull();
		expect(
			screen.getByText(/Environments with the same name are reused/),
		).toBeInTheDocument();
		expect(
			screen.getByRole("combobox", { name: "Import into" }),
		).toHaveTextContent("Existing shop");
		expect(card.choices()).toEqual({
			projects: { "proj-1": { existingProjectId: "np-1" } },
		});

		await user.click(screen.getByRole("combobox", { name: "Import into" }));
		await user.click(screen.getByRole("option", { name: "A new project" }));
		expect(screen.getByLabelText("New project name")).toBeInTheDocument();
		expect(card.choices()).toEqual({});
	});

	test("choosing an existing project drops a name typed before", async () => {
		const user = userEvent.setup();
		const card = await renderProject(project());
		await user.type(screen.getByLabelText("New project name"), "Typed");
		await user.click(screen.getByRole("combobox", { name: "Import into" }));
		await user.click(screen.getByRole("option", { name: "Internal" }));
		expect(card.choices()).toEqual({
			projects: { "proj-1": { existingProjectId: "np-2" } },
		});
	});

	test("unticking a project collapses it to a skipped line, and ticking brings it back", async () => {
		const user = userEvent.setup();
		const card = await renderProject(project());
		await user.click(screen.getByRole("checkbox", { name: /Shop/ }));

		const li = screen.getByText("skipped").closest("li") as HTMLElement;
		expect(li).toHaveAttribute("data-action", "skip");
		expect(screen.getByText("not imported")).toBeInTheDocument();
		expect(
			screen.getByText(/Nothing from this project is imported/),
		).toBeInTheDocument();
		expect(screen.queryByLabelText("New project name")).toBeNull();
		expect(screen.queryByRole("region", { name: /Environment/ })).toBeNull();
		expect(card.choices()).toEqual({ projects: { "proj-1": { skip: true } } });

		await user.click(screen.getByRole("checkbox", { name: /Shop/ }));
		expect(screen.getByLabelText("New project name")).toBeInTheDocument();
		expect(screen.queryByText("skipped")).toBeNull();
		expect(card.choices()).toEqual({});
	});

	test("a project the plan skips starts unticked", async () => {
		await renderProject(project({ action: "skip" }));
		expect(screen.getByRole("checkbox", { name: /Shop/ })).not.toBeChecked();
		expect(screen.getByText("skipped")).toBeInTheDocument();
	});

	test("resources get the rewrites that name them", async () => {
		const thePlan = plan({
			rewrites: [
				{
					key: "DATABASE_URL",
					kind: "url",
					resource: "web",
					to: ref("{db.URL}"),
				},
			],
		});
		await renderProject(
			project({}, [
				resource({
					env: [
						{
							key: "DATABASE_URL",
							scope: "runtime",
							secret: false,
							rewritten: true,
						},
					],
				}),
			]),
			{ thePlan },
		);
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: /Details/ }));
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: /Variables/ }));
		expect(screen.getByText(ref("{db.URL}"))).toBeInTheDocument();
	});
});
