import { test as bunTest, describe, expect } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { parseQuery } from "../logs/search/query";
import type { Draft } from "./model";
import { ScopeFields } from "./scope-fields";
import { project } from "./test-fixtures";
import { DraftHarness, newHost } from "./test-harness";
import type { TeamService } from "./use-team-services";

/** Form tests type into a large dialog; allow for a loaded CI machine. */
const test = (
	name: string,
	fn: (() => void) | (() => Promise<unknown>),
): void => {
	bunTest(name, fn, 30_000);
};

const PROJECTS = [project("p-web", "Web"), project("p-ops", "Ops")];

function svc(
	id: string,
	name: string,
	projectId: string,
	projectName: string,
): TeamService {
	return { id, name, projectId, projectName, environmentName: "Production" };
}

const SERVICES = [
	svc("s-api", "api", "p-web", "Web"),
	svc("s-cron", "cron", "p-ops", "Ops"),
];

function renderScope(
	initial: Partial<Draft> = {},
	props: Partial<{
		projects: typeof PROJECTS;
		projectsPending: boolean;
		services: TeamService[];
		servicesPending: boolean;
	}> = {},
) {
	const host = newHost();
	render(
		<DraftHarness initial={initial} host={host}>
			{(draft, set) => (
				<ScopeFields
					draft={draft}
					set={set}
					projects={props.projects ?? PROJECTS}
					projectsPending={props.projectsPending ?? false}
					services={props.services ?? SERVICES}
					servicesPending={props.servicesPending ?? false}
				/>
			)}
		</DraftHarness>,
	);
	return host;
}

describe("ScopeFields", () => {
	test("all projects is the default and hides the project list", () => {
		renderScope();
		expect(
			screen.getByText(
				"Every project of the team, including ones created later.",
			),
		).toBeVisible();
		expect(screen.queryByRole("checkbox", { name: "Web" })).toBeNull();
		expect(
			screen.getByRole("button", { name: "All projects" }),
		).toHaveAttribute("aria-pressed", "true");
	});

	test("choosing selected projects lists them and lets the person tick and untick", async () => {
		const user = userEvent.setup();
		const host = renderScope();
		await user.click(screen.getByRole("button", { name: "Selected projects" }));
		expect(host.draft().allProjects).toBe(false);
		await user.click(screen.getByRole("checkbox", { name: "Ops" }));
		await user.click(screen.getByRole("checkbox", { name: "Web" }));
		expect(host.draft().projectIds).toEqual(["p-ops", "p-web"]);
		await user.click(screen.getByRole("checkbox", { name: "Ops" }));
		expect(host.draft().projectIds).toEqual(["p-web"]);
		await user.click(screen.getByRole("button", { name: "All projects" }));
		expect(host.draft().allProjects).toBe(true);
	});

	test("says projects are loading while they load", () => {
		renderScope(
			{ allProjects: false },
			{ projects: [], projectsPending: true },
		);
		expect(screen.getByText("Loading projects…")).toBeVisible();
	});

	test("says there are no projects once loaded", () => {
		renderScope({ allProjects: false }, { projects: [] });
		expect(screen.getByText("No projects yet.")).toBeVisible();
	});

	test("lists services of every project with where they live, and toggles them", async () => {
		const user = userEvent.setup();
		const host = renderScope();
		const api = screen.getByRole("checkbox", { name: /^api/ });
		expect(screen.getByText("Web · Production")).toBeVisible();
		await user.click(api);
		await user.click(screen.getByRole("checkbox", { name: /^cron/ }));
		expect(host.draft().serviceIds).toEqual(["s-api", "s-cron"]);
		await user.click(api);
		expect(host.draft().serviceIds).toEqual(["s-cron"]);
	});

	test("only offers services of the selected projects", () => {
		renderScope({ allProjects: false, projectIds: ["p-ops"] });
		expect(screen.getByRole("checkbox", { name: /^cron/ })).toBeVisible();
		expect(screen.queryByRole("checkbox", { name: /^api/ })).toBeNull();
	});

	test("says services are loading while they load", () => {
		renderScope({}, { services: [], servicesPending: true });
		expect(screen.getByText("Loading services…")).toBeVisible();
	});

	test("says no services in scope when the scope has none", () => {
		renderScope(
			{ allProjects: false, projectIds: ["p-ops"] },
			{ services: [SERVICES[0] as TeamService] },
		);
		expect(screen.getByText("No services in this scope yet.")).toBeVisible();
	});

	test("a long service list gets a filter that narrows by name, project or environment", async () => {
		const user = userEvent.setup();
		const many = Array.from({ length: 8 }, (_, i) =>
			svc(`s${i}`, `svc-${i}`, "p-web", "Web"),
		);
		many.push({
			...svc("s-x", "special", "p-web", "Web"),
			environmentName: "Staging",
		});
		renderScope({}, { services: many });
		const filter = screen.getByRole("searchbox", { name: "Filter services" });
		await user.type(filter, "staging");
		expect(screen.getAllByRole("checkbox", { name: /^special/ })).toHaveLength(
			1,
		);
		expect(screen.queryByRole("checkbox", { name: /^svc-1/ })).toBeNull();
		await user.clear(filter);
		await user.type(filter, "nothing-like-this");
		expect(screen.getByText("No service matches the filter.")).toBeVisible();
	});

	test("a short service list has no filter box", () => {
		renderScope();
		expect(
			screen.queryByRole("searchbox", { name: "Filter services" }),
		).toBeNull();
	});

	test("typing a query updates the draft and a valid query shows no error", async () => {
		const user = userEvent.setup();
		const host = renderScope();
		await user.type(screen.getByLabelText("Query (optional)"), "level:error");
		expect(host.draft().query).toBe("level:error");
		expect(screen.getByLabelText("Query (optional)")).toHaveAttribute(
			"aria-invalid",
			"false",
		);
	});

	test("an invalid query is flagged with the parser's message", () => {
		renderScope({ query: 'level:error "open' });
		const input = screen.getByLabelText("Query (optional)");
		expect(input).toHaveAttribute("aria-invalid", "true");
		const expected = parseQuery('level:error "open').errors[0]?.message;
		expect(expected).toBeTruthy();
		expect(screen.getByRole("alert")).toHaveTextContent(expected as string);
	});
});
