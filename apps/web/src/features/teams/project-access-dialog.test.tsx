import { describe, expect, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type { TeamMember } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, type Overrides, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { ProjectAccessDialog } from "./project-access-dialog";

function project(id: string, name: string) {
	return make("Project", {
		id,
		name,
		slug: name.toLowerCase(),
		description: "",
		system: false,
		createdAt: "2026-01-01T00:00:00Z",
		updatedAt: "2026-01-01T00:00:00Z",
	});
}

const PROJECTS = make("PageProject", {
	items: [project("p-1", "Storefront"), project("p-2", "Billing")],
});

function member(over: Overrides<TeamMember> = {}): TeamMember {
	return make("TeamMember", {
		userId: "u-7",
		name: "Grace Hopper",
		email: "grace@example.com",
		role: "developer",
		joinedAt: "2026-01-01T00:00:00Z",
		you: false,
		...over,
	});
}

function Harness({ initial }: { initial: TeamMember | null }) {
	const [current, setCurrent] = useState<TeamMember | null>(initial);
	return (
		<>
			<span data-testid="state">{current ? "open" : "closed"}</span>
			<ProjectAccessDialog
				teamId="t-1"
				member={current}
				onOpenChange={(open) => {
					if (!open) setCurrent(null);
				}}
			/>
		</>
	);
}

const scopeButton = (name: string) => screen.getByRole("button", { name });

describe("ProjectAccessDialog", () => {
	test("renders nothing and fetches nothing without a member", async () => {
		const api = stubApi({});
		await renderWithProviders(<Harness initial={null} />);
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});

	test("an unrestricted member starts on All projects and saves allProjects", async () => {
		const api = stubApi({
			"GET /projects": { body: PROJECTS },
			"PATCH /teams/:teamId/members/:userId": { body: member() },
			"GET /teams/:teamId/members": { body: { items: [] } },
		});
		const user = userEvent.setup();
		await renderWithProviders(<Harness initial={member()} />);

		expect(
			await screen.findByText("Project access for Grace Hopper"),
		).toBeInTheDocument();
		expect(scopeButton("All projects")).toHaveAttribute("aria-pressed", "true");
		expect(
			screen.queryByRole("group", { name: "Projects this member can see" }),
		).not.toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: "Save access" }));

		expect(
			await screen.findByText("Grace Hopper can see every project."),
		).toBeInTheDocument();
		const patch = api.calls.find((c) => c.method === "PATCH");
		expect(patch).toMatchObject({
			path: "/teams/t-1/members/u-7",
			body: { allProjects: true },
		});
		expect(screen.getByTestId("state")).toHaveTextContent("closed");
		expect(api.unmatched).toEqual([]);
	});

	test("a restricted member starts with their projects ticked and saves the edited list", async () => {
		const api = stubApi({
			"GET /projects": { body: PROJECTS },
			"PATCH /teams/:teamId/members/:userId": { body: member() },
			"GET /teams/:teamId/members": { body: { items: [] } },
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness initial={member({ projectIds: ["p-1"] })} />,
		);

		const list = await screen.findByRole("group", {
			name: "Projects this member can see",
		});
		expect(scopeButton("Selected projects")).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		const storefront = await within(list).findByRole("checkbox", {
			name: "Storefront",
		});
		const billing = within(list).getByRole("checkbox", { name: "Billing" });
		expect(storefront).toBeChecked();
		expect(billing).not.toBeChecked();
		expect(screen.getByText("1 selected.")).toBeInTheDocument();

		await user.click(billing);
		expect(screen.getByText("2 selected.")).toBeInTheDocument();
		await user.click(storefront);
		expect(screen.getByText("1 selected.")).toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: "Save access" }));

		expect(
			await screen.findByText("Grace Hopper can see 1 project."),
		).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "PATCH")?.body).toEqual({
			projectIds: ["p-2"],
		});
	});

	test("selecting projects for an unrestricted member sends the chosen ids; none selected warns", async () => {
		const api = stubApi({
			"GET /projects": { body: PROJECTS },
			"PATCH /teams/:teamId/members/:userId": { body: member() },
			"GET /teams/:teamId/members": { body: { items: [] } },
		});
		const user = userEvent.setup();
		await renderWithProviders(<Harness initial={member({ role: "viewer" })} />);

		await user.click(
			await screen.findByRole("button", { name: "Selected projects" }),
		);
		expect(
			screen.getByText("Nothing selected: this member sees no project at all."),
		).toBeInTheDocument();

		await user.click(await screen.findByRole("checkbox", { name: "Billing" }));
		await user.click(screen.getByRole("checkbox", { name: "Storefront" }));
		await user.click(screen.getByRole("button", { name: "Save access" }));

		expect(
			await screen.findByText("Grace Hopper can see 2 projects."),
		).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "PATCH")?.body).toEqual({
			projectIds: ["p-2", "p-1"],
		});
	});

	test("says so when the team has no projects", async () => {
		stubApi({ "GET /projects": { body: { items: [] } } });
		const user = userEvent.setup();
		await renderWithProviders(<Harness initial={member()} />);

		await user.click(
			await screen.findByRole("button", { name: "Selected projects" }),
		);
		expect(
			await screen.findByText("This team has no projects yet."),
		).toBeInTheDocument();
	});

	test("shows the server's reason when saving fails and keeps the dialog open", async () => {
		stubApi({
			"GET /projects": { body: PROJECTS },
			"PATCH /teams/:teamId/members/:userId": problem(403, {
				code: "forbidden",
				title: "Forbidden",
				detail: "admins cannot restrict owners",
			}),
		});
		const user = userEvent.setup();
		await renderWithProviders(<Harness initial={member()} />);

		await user.click(
			await screen.findByRole("button", { name: "Save access" }),
		);

		expect(
			await screen.findByText("admins cannot restrict owners"),
		).toBeInTheDocument();
		expect(screen.getByTestId("state")).toHaveTextContent("open");
	});

	test("Cancel closes without a request", async () => {
		const api = stubApi({ "GET /projects": { body: PROJECTS } });
		const user = userEvent.setup();
		await renderWithProviders(<Harness initial={member()} />);

		await user.click(await screen.findByRole("button", { name: "Cancel" }));

		expect(screen.getByTestId("state")).toHaveTextContent("closed");
		expect(api.calls.filter((c) => c.method !== "GET")).toEqual([]);
	});

	test("names the member's role in the description", async () => {
		stubApi({ "GET /projects": { body: PROJECTS } });
		await renderWithProviders(<Harness initial={member({ role: "viewer" })} />);
		expect(
			await screen.findByText(/^Viewers\s+can be limited to some projects/),
		).toBeInTheDocument();
	});
});
