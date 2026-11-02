import { describe, expect, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import type { Grant, IntegrationScope } from "./model";
import { ScopeField } from "./scope-field";

const project = (id: string, name: string, system = false) => ({
	id,
	name,
	slug: name.toLowerCase(),
	description: "",
	system,
	buildLocation: "inherit",
	buildServerId: "",
	registryId: "",
	createdAt: "2026-01-01T00:00:00Z",
	updatedAt: "2026-01-01T00:00:00Z",
});

const environment = (
	id: string,
	projectId: string,
	name: string,
	kind: "standard" | "preview" = "standard",
) => ({
	id,
	projectId,
	name,
	slug: name.toLowerCase(),
	kind,
	isProduction: false,
	parentEnvironmentId: null,
	prNumber: null,
	prTitle: "",
	createdAt: "2026-01-01T00:00:00Z",
});

/** The field driven by real state, with the latest value exposed to assertions. */
function Harness({
	initialScope = "team",
	initialGrants = [],
	error,
	latest,
}: {
	initialScope?: IntegrationScope;
	initialGrants?: Grant[];
	error?: string;
	latest: { scope: IntegrationScope; grants: Grant[] };
}) {
	const [state, setState] = useState({
		scope: initialScope,
		grants: initialGrants,
	});
	latest.scope = state.scope;
	latest.grants = state.grants;
	return <ScopeField {...state} error={error} onChange={setState} />;
}

const projectsReply = {
	body: {
		items: [
			project("p1", "Shop"),
			project("p2", "Blog"),
			project("sys", "System", true),
		],
	},
};

describe("ScopeField", () => {
	test("whole team explains the reach and never loads projects", async () => {
		const api = stubApi({});
		const latest = { scope: "team" as IntegrationScope, grants: [] as Grant[] };
		await renderWithProviders(<Harness latest={latest} />);
		expect(
			screen.getByText(/Any variable of any project may reference it/),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("group", { name: "Available to" }),
		).not.toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});

	test("switching to selected projects lists non-system projects", async () => {
		const api = stubApi({ "GET /projects": projectsReply });
		const user = userEvent.setup();
		const latest = { scope: "team" as IntegrationScope, grants: [] as Grant[] };
		await renderWithProviders(<Harness latest={latest} />);
		await user.click(screen.getByRole("button", { name: "Selected projects" }));
		expect(latest.scope).toBe("selected");
		expect(
			await screen.findByRole("checkbox", { name: "Shop" }),
		).toBeInTheDocument();
		expect(screen.getByRole("checkbox", { name: "Blog" })).toBeInTheDocument();
		expect(
			screen.queryByRole("checkbox", { name: "System" }),
		).not.toBeInTheDocument();
		expect(
			screen.getByText(/also covers environments created later/),
		).toBeInTheDocument();
		expect(api.unmatched).toEqual([]);
	});

	test("shows loading, then an empty state when there are no projects", async () => {
		stubApi({
			"GET /projects": { body: { items: [project("sys", "S", true)] } },
		});
		const latest = {
			scope: "selected" as IntegrationScope,
			grants: [] as Grant[],
		};
		await renderWithProviders(
			<Harness initialScope="selected" latest={latest} />,
		);
		expect(screen.getByText("Loading projects…")).toBeInTheDocument();
		expect(await screen.findByText("No projects yet.")).toBeInTheDocument();
	});

	test("shows the validation error in place of the hint", async () => {
		stubApi({ "GET /projects": projectsReply });
		const latest = {
			scope: "selected" as IntegrationScope,
			grants: [] as Grant[],
		};
		await renderWithProviders(
			<Harness
				initialScope="selected"
				error="Choose at least one project."
				latest={latest}
			/>,
		);
		expect(
			screen.getByText("Choose at least one project."),
		).toBeInTheDocument();
		expect(
			screen.queryByText(/also covers environments created later/),
		).not.toBeInTheDocument();
	});

	test("ticking a project grants all its environments, unticking removes it", async () => {
		stubApi({ "GET /projects": projectsReply });
		const user = userEvent.setup();
		const latest = {
			scope: "selected" as IntegrationScope,
			grants: [] as Grant[],
		};
		await renderWithProviders(
			<Harness initialScope="selected" latest={latest} />,
		);
		const shop = await screen.findByRole("checkbox", { name: "Shop" });
		await user.click(shop);
		expect(latest.grants).toEqual([{ projectId: "p1", environmentId: null }]);
		expect(shop).toBeChecked();
		await user.click(shop);
		expect(latest.grants).toEqual([]);
	});

	test("opens a project's environments, listing standard ones only", async () => {
		const api = stubApi({
			"GET /projects": projectsReply,
			"GET /projects/:projectId/environments": {
				body: {
					items: [
						environment("e1", "p1", "Production"),
						environment("e2", "p1", "Staging"),
						environment("e3", "p1", "pr-7", "preview"),
					],
				},
			},
		});
		const user = userEvent.setup();
		const latest = {
			scope: "selected" as IntegrationScope,
			grants: [] as Grant[],
		};
		await renderWithProviders(
			<Harness initialScope="selected" latest={latest} />,
		);
		await screen.findByRole("checkbox", { name: "Shop" });
		const [open] = screen.getAllByRole("button", { name: "Environments" });
		expect(open).toHaveAttribute("aria-expanded", "false");
		await user.click(open as HTMLElement);
		expect(
			await screen.findByRole("checkbox", { name: "Production" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("checkbox", { name: "Staging" }),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("checkbox", { name: "pr-7" }),
		).not.toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Hide environments" }),
		).toHaveAttribute("aria-expanded", "true");
		expect(api.calls.map((c) => c.path)).toContain("/projects/p1/environments");

		await user.click(screen.getByRole("button", { name: "Hide environments" }));
		expect(
			screen.queryByRole("checkbox", { name: "Production" }),
		).not.toBeInTheDocument();
	});

	test("ticking one environment grants just it; both fold into the whole project", async () => {
		stubApi({
			"GET /projects": projectsReply,
			"GET /projects/:projectId/environments": {
				body: {
					items: [
						environment("e1", "p1", "Production"),
						environment("e2", "p1", "Staging"),
					],
				},
			},
		});
		const user = userEvent.setup();
		const latest = {
			scope: "selected" as IntegrationScope,
			grants: [] as Grant[],
		};
		await renderWithProviders(
			<Harness initialScope="selected" latest={latest} />,
		);
		await screen.findByRole("checkbox", { name: "Shop" });
		const [open] = screen.getAllByRole("button", { name: "Environments" });
		await user.click(open as HTMLElement);
		await user.click(
			await screen.findByRole("checkbox", { name: "Production" }),
		);
		expect(latest.grants).toEqual([{ projectId: "p1", environmentId: "e1" }]);
		expect(screen.getByText("some environments")).toBeInTheDocument();
		expect(screen.getByRole("checkbox", { name: /^Shop/ })).not.toBeChecked();

		await user.click(screen.getByRole("checkbox", { name: "Staging" }));
		expect(latest.grants).toEqual([{ projectId: "p1", environmentId: null }]);
		expect(screen.getByRole("checkbox", { name: /^Shop/ })).toBeChecked();
	});

	test("a project with only some environments granted opens expanded", async () => {
		stubApi({
			"GET /projects": projectsReply,
			"GET /projects/:projectId/environments": {
				body: { items: [environment("e1", "p1", "Production")] },
			},
		});
		const latest = {
			scope: "selected" as IntegrationScope,
			grants: [] as Grant[],
		};
		await renderWithProviders(
			<Harness
				initialScope="selected"
				initialGrants={[{ projectId: "p1", environmentId: "e1" }]}
				latest={latest}
			/>,
		);
		expect(
			await screen.findByRole("button", { name: "Hide environments" }),
		).toBeInTheDocument();
		expect(
			await screen.findByRole("checkbox", { name: "Production" }),
		).toBeChecked();
		// The project that is not granted stays collapsed.
		const blog = (
			await screen.findByRole("checkbox", { name: "Blog" })
		).closest("div.flex.flex-col") as HTMLElement;
		expect(
			within(blog).getByRole("button", { name: "Environments" }),
		).toBeInTheDocument();
	});

	test("says when a project has no standard environments", async () => {
		stubApi({
			"GET /projects": projectsReply,
			"GET /projects/:projectId/environments": {
				body: { items: [environment("e3", "p1", "pr-7", "preview")] },
			},
		});
		const user = userEvent.setup();
		const latest = {
			scope: "selected" as IntegrationScope,
			grants: [] as Grant[],
		};
		await renderWithProviders(
			<Harness initialScope="selected" latest={latest} />,
		);
		await screen.findByRole("checkbox", { name: "Shop" });
		const [open] = screen.getAllByRole("button", { name: "Environments" });
		await user.click(open as HTMLElement);
		expect(await screen.findByText("No environments.")).toBeInTheDocument();
	});

	test("switching back to the whole team keeps the grants for the form", async () => {
		stubApi({ "GET /projects": projectsReply });
		const user = userEvent.setup();
		const latest = {
			scope: "selected" as IntegrationScope,
			grants: [] as Grant[],
		};
		await renderWithProviders(
			<Harness
				initialScope="selected"
				initialGrants={[{ projectId: "p1", environmentId: null }]}
				latest={latest}
			/>,
		);
		await user.click(screen.getByRole("button", { name: "Whole team" }));
		expect(latest.scope).toBe("team");
		expect(latest.grants).toEqual([{ projectId: "p1", environmentId: null }]);
	});
});
