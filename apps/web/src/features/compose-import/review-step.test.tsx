import { describe, expect, test } from "bun:test";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ComposeChoices, ComposePlan } from "#/api/gen/types.gen";
import { ReviewStep } from "./review-step";
import { ChoicesHost, plan, service } from "./test-fixtures";

// Builds a literal `${NAME}` placeholder (the escaped `$` keeps it from interpolating).
const ref = (name: string) => `\${${name}}`;

function review(
	p: ComposePlan,
	over: {
		choices?: ComposeChoices;
		refreshing?: boolean;
		problem?: string | null;
		contents?: Record<string, string>;
	} = {},
) {
	const seen = { choices: over.choices ?? {} };
	const calls = {
		files: [] as Array<[string, string]>,
		editSource: 0,
	};
	const view = render(
		<ChoicesHost initial={over.choices ?? {}} seen={seen}>
			{(choices, onChoices) => (
				<ReviewStep
					plan={p}
					choices={choices}
					onChoices={onChoices}
					contents={over.contents ?? {}}
					onFile={(path, content) => calls.files.push([path, content])}
					onEditSource={() => {
						calls.editSource++;
					}}
					refreshing={over.refreshing ?? false}
					problem={over.problem ?? null}
				/>
			)}
		</ChoicesHost>,
	);
	return { ...view, seen, calls };
}

const web = service({ key: "web", port: 80, portHttp: true });
const db = service({ key: "db", kind: "database" });

describe("summary", () => {
	test("counts included services, rewrites and warnings", () => {
		review(
			plan({
				name: "shop",
				services: [web, db],
				rewrites: [
					{
						from: "db",
						key: "DATABASE_URL",
						kind: "host",
						reason: "Points at the db service.",
						service: "web",
						target: "db",
						to: "shop-db",
					},
				],
				warnings: ["One thing."],
			}),
		);
		expect(screen.getByRole("status")).toHaveTextContent(
			"2 services, 1 rewritten reference, 1 warning",
		);
		expect(screen.getByRole("region", { name: "Services" })).toHaveTextContent(
			"2 of 2",
		);
		expect(screen.getByText("shop")).toBeInTheDocument();
	});

	test("unchecking a service updates the count and the section header", async () => {
		const user = userEvent.setup();
		review(plan({ services: [web, db] }));
		await user.click(screen.getByRole("checkbox", { name: "Include db" }));
		expect(screen.getByRole("status")).toHaveTextContent("1 service,");
		expect(screen.getByRole("region", { name: "Services" })).toHaveTextContent(
			"1 of 2",
		);
	});

	test("a repository source shows the short commit", () => {
		review(
			plan({
				sourceKind: "repo",
				commit: "0123456789abcdef",
				services: [web],
			}),
		);
		expect(screen.getByText("shop · 0123456")).toBeInTheDocument();
	});

	test("a pasted source shows no commit", () => {
		review(plan({ commit: "0123456789abcdef", services: [web] }));
		expect(screen.getByText("shop")).toBeInTheDocument();
		expect(screen.queryByText(/0123456/)).not.toBeInTheDocument();
	});

	test("Updating is announced only while refreshing", () => {
		const { container, rerender } = review(plan({ services: [web] }), {
			refreshing: true,
		});
		expect(container.querySelector("[aria-busy]")).toHaveAttribute(
			"aria-busy",
			"true",
		);
		expect(screen.getByText("Updating")).not.toHaveAttribute(
			"aria-hidden",
			"true",
		);
		rerender(<div />);
		const idle = review(plan({ services: [web] }));
		expect(idle.container.querySelector("[aria-busy]")).toHaveAttribute(
			"aria-busy",
			"false",
		);
		expect(within(idle.container).getByText("Updating")).toHaveAttribute(
			"aria-hidden",
			"true",
		);
	});
});

describe("alerts", () => {
	test("a failed refresh says the cards show the last plan that worked", () => {
		review(plan({ services: [web] }), { problem: "boom" });
		expect(
			screen.getByText("The plan could not be updated"),
		).toBeInTheDocument();
		expect(
			screen.getByText("boom The cards show the last plan that worked."),
		).toBeInTheDocument();
	});

	test("blockers list every reason and Edit source goes back", async () => {
		const user = userEvent.setup();
		const { calls } = review(
			plan({ blockers: ["No services.", "Bad port."], services: [web] }),
		);
		const alert = screen
			.getByText("This import is blocked")
			.closest("[role=alert]") as HTMLElement;
		expect(
			within(alert)
				.getAllByRole("listitem")
				.map((li) => li.textContent),
		).toEqual(["No services.", "Bad port."]);
		await user.click(
			within(alert).getByRole("button", { name: "Edit source" }),
		);
		expect(calls.editSource).toBe(1);
	});

	test("missing variables name each one; the title is singular for one", async () => {
		const user = userEvent.setup();
		const one = review(plan({ missing: ["TAG"], services: [web] }));
		expect(screen.getByText("A variable has no value")).toBeInTheDocument();
		expect(screen.getByText(ref("TAG"))).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Edit .env" }));
		expect(one.calls.editSource).toBe(1);
		one.unmount();

		review(plan({ missing: ["TAG", "PASSWORD"], services: [web] }));
		expect(screen.getByText("2 variables have no value")).toBeInTheDocument();
		expect(screen.getByText(ref("PASSWORD"))).toBeInTheDocument();
	});

	test("no blockers, missing variables or problem means no alerts", () => {
		review(plan({ services: [web] }));
		expect(screen.queryByRole("alert")).not.toBeInTheDocument();
		expect(screen.queryByRole("note")).not.toBeInTheDocument();
	});
});

describe("files to add", () => {
	const needed = plan({
		services: [web],
		neededFiles: [
			{
				path: "./app.env",
				purpose: "env_file",
				required: true,
				service: "web",
			},
			{
				path: "./nginx.conf",
				purpose: "bind",
				required: false,
				service: "web",
			},
		],
	});

	test("each needed file gets a box with its purpose and whether it is optional", () => {
		review(needed, { contents: { "./app.env": "A=1" } });
		const section = screen.getByRole("region", { name: "Files to add" });
		expect(section).toHaveTextContent("2");
		expect(
			within(section).getByRole("textbox", { name: "Contents of ./app.env" }),
		).toHaveValue("A=1");
		expect(
			within(section).getByRole("textbox", {
				name: "Contents of ./nginx.conf",
			}),
		).toHaveValue("");
		expect(within(section).getByText("env file")).toBeInTheDocument();
		expect(within(section).getByText("mounted file")).toBeInTheDocument();
		expect(within(section).getAllByText("optional")).toHaveLength(1);
	});

	test("typing reports the path and the text", async () => {
		const user = userEvent.setup();
		const { calls } = review(needed);
		await user.type(
			screen.getByRole("textbox", { name: "Contents of ./nginx.conf" }),
			"x",
		);
		expect(calls.files).toEqual([["./nginx.conf", "x"]]);
	});

	test("no section when nothing is needed", () => {
		review(plan({ services: [web] }));
		expect(
			screen.queryByRole("region", { name: "Files to add" }),
		).not.toBeInTheDocument();
	});
});

describe("rewritten references and project notes", () => {
	test("rewrites are grouped under the service's display name", () => {
		review(
			plan({
				services: [service({ key: "web", name: "Frontend" }), db],
				rewrites: [
					{
						from: "postgres://db",
						key: "DATABASE_URL",
						kind: "host",
						reason: "db is now its own service.",
						service: "web",
						target: "db",
						to: "postgres://shop-db",
					},
					{
						from: "x",
						key: "OTHER",
						kind: "host",
						reason: "r",
						service: "gone",
						target: "db",
						to: "y",
					},
				],
			}),
		);
		const section = screen.getByRole("region", {
			name: "Rewritten references",
		});
		expect(section).toHaveTextContent("2");
		expect(
			within(section).getByRole("heading", { name: "Frontend" }),
		).toBeInTheDocument();
		// A service the plan no longer lists falls back to its Compose key.
		expect(
			within(section).getByRole("heading", { name: "gone" }),
		).toBeInTheDocument();
		expect(within(section).getByText("web.DATABASE_URL")).toBeInTheDocument();
		expect(within(section).getByText("postgres://db")).toBeInTheDocument();
		expect(within(section).getByText("postgres://shop-db")).toBeInTheDocument();
		expect(within(section).getAllByLabelText("becomes")).toHaveLength(2);
		expect(
			within(section).getByText("db is now its own service."),
		).toBeInTheDocument();
	});

	test("project warnings, ignored settings and networks with a note", async () => {
		const user = userEvent.setup();
		review(
			plan({
				services: [web],
				warnings: ["The file uses extension fields."],
				notes: [{ key: "x-env", message: "Ignored.", severity: "info" }],
				networks: [
					{
						external: true,
						name: "proxy",
						note: "Joined by name.",
						services: [],
					},
					{ external: false, name: "quiet", note: "", services: [] },
				],
			}),
		);
		const project = screen.getByRole("region", { name: "Project" });
		expect(
			within(project).getByText("The file uses extension fields."),
		).toBeInTheDocument();
		expect(
			within(project).getByRole("button", {
				name: "Ignored project settings (1)",
			}),
		).toBeInTheDocument();
		// Networks without a note are not listed.
		await user.click(
			within(project).getByRole("button", { name: "Networks (1)" }),
		);
		const item = within(project)
			.getByText("proxy")
			.closest("li") as HTMLElement;
		expect(item).toHaveTextContent("proxy (external): Joined by name.");
		expect(within(project).queryByText("quiet")).not.toBeInTheDocument();
	});

	test("without project-level content there is no Project section", () => {
		review(plan({ services: [web] }));
		expect(
			screen.queryByRole("region", { name: "Project" }),
		).not.toBeInTheDocument();
	});
});

describe("preflight", () => {
	test("checks show with failures first", () => {
		review(
			plan({
				services: [web],
				checks: [
					{ id: "a", message: "Docker is installed.", status: "ok" },
					{ id: "b", message: "Port 80 is taken.", status: "fail" },
				],
			}),
		);
		const preflight = screen.getByRole("region", { name: "Preflight" });
		expect(
			within(preflight)
				.getAllByRole("listitem")
				.map((li) => li.textContent),
		).toEqual(["Failed: Port 80 is taken.", "Passed: Docker is installed."]);
	});

	test("no checks, no preflight section", () => {
		review(plan({ services: [web] }));
		expect(
			screen.queryByRole("region", { name: "Preflight" }),
		).not.toBeInTheDocument();
	});
});
