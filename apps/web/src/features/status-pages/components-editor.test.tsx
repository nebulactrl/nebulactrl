import { describe, expect, it } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { ComponentsEditor } from "./components-editor";
import type { ComponentRow } from "./model";

function row(id: string, over: Partial<ComponentRow> = {}): ComponentRow {
	return {
		serviceId: id,
		displayName: id,
		groupName: "",
		url: "",
		serviceName: id,
		projectName: "Acme",
		environmentName: "production",
		...over,
	};
}

/** The editor over real state, so an edit shows up on screen as it would in the page. */
function Harness({
	initial,
	seen,
}: {
	initial: ComponentRow[];
	seen?: (rows: ComponentRow[]) => void;
}) {
	const [rows, setRows] = useState(initial);
	return (
		<ComponentsEditor
			pageId="page-1"
			rows={rows}
			onChange={(next) => {
				seen?.(next);
				setRows(next);
			}}
		/>
	);
}

/** The service ids in the order the editor shows them. */
function shownOrder(): string[] {
	return screen
		.getAllByRole("textbox", { name: "Public name" })
		.map((input) => (input as HTMLInputElement).placeholder);
}

function view(id: string, name: string, environmentId: string) {
	return make("ServiceView", {
		id,
		name,
		slug: name,
		environmentId,
		projectId: "proj-1",
		config: { healthcheck: { type: "http" }, processes: null },
	});
}

describe("ComponentsEditor: list", () => {
	it("explains an empty page and says nothing is shown", async () => {
		await renderWithProviders(<Harness initial={[]} />);
		expect(screen.getByText("Nothing is shown yet.")).toBeInTheDocument();
		expect(
			screen.getByText(/A page needs at least one before it can be published/),
		).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Add services" })).toBeEnabled();
		expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
	});

	it("shows each service with where it lives, its public name and its link", async () => {
		await renderWithProviders(
			<Harness
				initial={[
					row("web", {
						displayName: "Website",
						serviceName: "web",
						url: "https://example.com/docs",
					}),
					row("api", { projectName: "Beta", environmentName: "staging" }),
				]}
			/>,
		);
		expect(
			screen.getByText(
				"2 components of 100. Use the arrows, or Alt and ↑ ↓ in a field, to reorder.",
			),
		).toBeInTheDocument();
		expect(screen.getByText("web · Acme · production")).toBeInTheDocument();
		expect(screen.getByText("api · Beta · staging")).toBeInTheDocument();
		const names = screen.getAllByRole("textbox", { name: "Public name" });
		expect((names[0] as HTMLInputElement).value).toBe("Website");
		expect((names[0] as HTMLInputElement).placeholder).toBe("web");
		expect(screen.getAllByLabelText("Link (optional)")[0]).toHaveValue(
			"https://example.com/docs",
		);
	});

	it("shows just the service name for a row without project or environment", async () => {
		await renderWithProviders(
			<Harness
				initial={[row("web", { projectName: "", environmentName: "" })]}
			/>,
		);
		expect(screen.getByText("web", { selector: "span" })).toBeInTheDocument();
		expect(screen.queryByText(/·/)).not.toBeInTheDocument();
	});

	it("uses the singular for a single component", async () => {
		await renderWithProviders(<Harness initial={[row("web")]} />);
		expect(screen.getByText(/^1 component of 100\./)).toBeInTheDocument();
	});

	it("does not show group headings while no row is in a group", async () => {
		await renderWithProviders(<Harness initial={[row("web"), row("api")]} />);
		expect(
			screen.queryByText("Not in a group, shown first"),
		).not.toBeInTheDocument();
		expect(
			screen.getByRole("list", { name: "Components" }),
		).toBeInTheDocument();
	});

	it("puts ungrouped rows first under their own heading, then each group", async () => {
		await renderWithProviders(
			<Harness
				initial={[
					row("a", { groupName: "Core" }),
					row("b"),
					row("c", { groupName: "Edge" }),
				]}
			/>,
		);
		expect(screen.getByText("Not in a group, shown first")).toBeInTheDocument();
		expect(shownOrder()).toEqual(["b", "a", "c"]);
		expect(
			within(screen.getByRole("region", { name: "Group Core" })).getByText(
				"1 component",
			),
		).toBeInTheDocument();
		expect(
			within(screen.getByRole("list", { name: "Core" })).getAllByRole(
				"listitem",
			),
		).toHaveLength(1);
		expect(
			screen.getByRole("region", { name: "Not in a group" }),
		).toBeInTheDocument();
	});

	it("offers the existing group names as suggestions for a row's group", async () => {
		await renderWithProviders(
			<Harness
				initial={[
					row("a", { groupName: "Core" }),
					row("b", { groupName: "Edge" }),
				]}
			/>,
		);
		const group = screen.getAllByLabelText("Group")[0] as HTMLInputElement;
		const list = document.getElementById(group.getAttribute("list") ?? "");
		expect(
			[...(list?.querySelectorAll("option") ?? [])].map((o) => o.value),
		).toEqual(["Core", "Edge"]);
	});

	it("disables Add services when the page is full", async () => {
		const rows = Array.from({ length: 100 }, (_, i) => row(`svc-${i}`));
		await renderWithProviders(<Harness initial={rows} />);
		expect(screen.getByRole("button", { name: "Add services" })).toBeDisabled();
		expect(screen.getByText(/^100 components of 100\./)).toBeInTheDocument();
	}, 30_000);
});

describe("ComponentsEditor: editing a row", () => {
	it("changes the public name as it is typed", async () => {
		const user = userEvent.setup();
		const seen: ComponentRow[][] = [];
		await renderWithProviders(
			<Harness initial={[row("web")]} seen={(rows) => seen.push(rows)} />,
		);
		const input = screen.getByRole("textbox", { name: "Public name" });
		await user.clear(input);
		await user.type(input, "Site");
		expect(input).toHaveValue("Site");
		expect(seen.at(-1)?.[0]?.displayName).toBe("Site");
		expect(seen.at(-1)?.[0]?.serviceId).toBe("web");
	});

	it("changes the link as it is typed", async () => {
		const user = userEvent.setup();
		const seen: ComponentRow[][] = [];
		await renderWithProviders(
			<Harness initial={[row("web")]} seen={(rows) => seen.push(rows)} />,
		);
		await user.type(
			screen.getByLabelText("Link (optional)"),
			"https://a.dev/x",
		);
		expect(seen.at(-1)?.[0]?.url).toBe("https://a.dev/x");
	});

	it("flags a public name that is too long", async () => {
		await renderWithProviders(
			<Harness initial={[row("web", { displayName: "x".repeat(61) })]} />,
		);
		const input = screen.getByRole("textbox", { name: "Public name" });
		expect(input).toBeInvalid();
		expect(
			screen.getByText("A name is at most 60 characters."),
		).toBeInTheDocument();
		expect(input).toHaveAccessibleDescription(
			"A name is at most 60 characters.",
		);
	});

	it("flags a link that is not https", async () => {
		await renderWithProviders(
			<Harness initial={[row("web", { url: "http://example.com" })]} />,
		);
		expect(screen.getByLabelText("Link (optional)")).toBeInvalid();
		expect(
			screen.getByText(
				"Use an absolute https link, like https://example.com/docs.",
			),
		).toBeInTheDocument();
	});

	it("flags a group name that is too long", async () => {
		await renderWithProviders(
			<Harness initial={[row("web", { groupName: "g".repeat(61) })]} />,
		);
		expect(
			screen.getByText("A group name is at most 60 characters."),
		).toBeInTheDocument();
	});

	it("shows no error for a valid row", async () => {
		await renderWithProviders(
			<Harness initial={[row("web", { url: "https://example.com" })]} />,
		);
		expect(screen.getByRole("textbox", { name: "Public name" })).toBeValid();
		expect(screen.getByLabelText("Link (optional)")).toBeValid();
	});

	it("removes a row and announces it", async () => {
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness
				initial={[row("web", { displayName: "Website" }), row("api")]}
			/>,
		);
		await user.click(
			screen.getByRole("button", { name: "Remove Website from the page" }),
		);
		expect(shownOrder()).toEqual(["api"]);
		expect(screen.getByRole("status")).toHaveTextContent(
			"Website removed from the page.",
		);
	});

	it("names a row by its service when its public name is blank", async () => {
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness initial={[row("web", { displayName: "  " })]} />,
		);
		await user.click(
			screen.getByRole("button", { name: "Remove web from the page" }),
		);
		expect(screen.getByRole("status")).toHaveTextContent(
			"web removed from the page.",
		);
		expect(screen.getByText("Nothing is shown yet.")).toBeInTheDocument();
	});
});

describe("ComponentsEditor: grouping", () => {
	it("moves a row into a group only when the group field is committed with Enter", async () => {
		const user = userEvent.setup();
		const seen: ComponentRow[][] = [];
		await renderWithProviders(
			<Harness
				initial={[row("web"), row("api")]}
				seen={(rows) => seen.push(rows)}
			/>,
		);
		const group = screen.getAllByLabelText("Group")[0] as HTMLInputElement;
		await user.type(group, "Core");
		// Typing alone must not re-parent the row under the typist's fingers.
		expect(seen).toEqual([]);
		expect(
			screen.queryByRole("region", { name: "Group Core" }),
		).not.toBeInTheDocument();

		await user.keyboard("{Enter}");
		expect(seen.at(-1)?.find((r) => r.serviceId === "web")?.groupName).toBe(
			"Core",
		);
		const core = screen.getByRole("region", { name: "Group Core" });
		expect(within(core).getByText("1 component")).toBeInTheDocument();
		expect(screen.getByText("Not in a group, shown first")).toBeInTheDocument();
	});

	it("commits the group field when it loses focus", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Harness initial={[row("web"), row("api")]} />);
		await user.type(
			screen.getAllByLabelText("Group")[1] as HTMLElement,
			"Edge",
		);
		await user.tab();
		expect(
			screen.getByRole("region", { name: "Group Edge" }),
		).toBeInTheDocument();
	});

	it("Escape abandons a half-typed group name", async () => {
		const user = userEvent.setup();
		const seen: ComponentRow[][] = [];
		await renderWithProviders(
			<Harness initial={[row("web")]} seen={(rows) => seen.push(rows)} />,
		);
		const group = screen.getByLabelText("Group");
		await user.type(group, "Cor");
		await user.keyboard("{Escape}");
		expect(group).toHaveValue("");
		await user.tab();
		expect(seen).toEqual([]);
	});

	it("renames a group from its heading and keeps its members", async () => {
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness
				initial={[
					row("a", { groupName: "Core" }),
					row("b", { groupName: "Core" }),
				]}
			/>,
		);
		const name = screen.getByRole("textbox", { name: "Name of group Core" });
		await user.clear(name);
		await user.type(name, "Platform{Enter}");
		const region = screen.getByRole("region", { name: "Group Platform" });
		expect(within(region).getByText("2 components")).toBeInTheDocument();
		expect(
			screen.queryByRole("region", { name: "Group Core" }),
		).not.toBeInTheDocument();
	});

	it("ignores a blank group name instead of dropping the group", async () => {
		const user = userEvent.setup();
		const seen: ComponentRow[][] = [];
		await renderWithProviders(
			<Harness
				initial={[row("a", { groupName: "Core" })]}
				seen={(rows) => seen.push(rows)}
			/>,
		);
		const name = screen.getByRole("textbox", { name: "Name of group Core" });
		await user.clear(name);
		await user.type(name, "  {Enter}");
		expect(seen).toEqual([]);
		expect(
			screen.getByRole("region", { name: "Group Core" }),
		).toBeInTheDocument();
	});

	it("merges a group renamed onto another", async () => {
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness
				initial={[
					row("a", { groupName: "Core" }),
					row("b", { groupName: "Edge" }),
				]}
			/>,
		);
		const name = screen.getByRole("textbox", { name: "Name of group Edge" });
		await user.clear(name);
		await user.type(name, "Core{Enter}");
		expect(
			within(screen.getByRole("region", { name: "Group Core" })).getByText(
				"2 components",
			),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("region", { name: "Group Edge" }),
		).not.toBeInTheDocument();
	});

	it("moves a whole group and announces where it landed", async () => {
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness
				initial={[
					row("a", { groupName: "Core" }),
					row("b", { groupName: "Edge" }),
				]}
			/>,
		);
		expect(
			screen.getByRole("button", { name: "Move group Core up" }),
		).toBeDisabled();
		expect(
			screen.getByRole("button", { name: "Move group Edge down" }),
		).toBeDisabled();

		await user.click(
			screen.getByRole("button", { name: "Move group Core down" }),
		);
		expect(shownOrder()).toEqual(["b", "a"]);
		expect(screen.getByRole("status")).toHaveTextContent(
			"Group Core moved down, group 2 of 2.",
		);
		// The reader's place is kept: focus lands on the button they used, or its twin at the end.
		expect(document.activeElement).toBe(
			screen.getByRole("button", { name: "Move group Core up" }),
		);
	});

	it("moves a group with Alt + arrow from its name field", async () => {
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness
				initial={[
					row("a", { groupName: "Core" }),
					row("b", { groupName: "Edge" }),
				]}
			/>,
		);
		await user.click(
			screen.getByRole("textbox", { name: "Name of group Edge" }),
		);
		await user.keyboard("{Alt>}{ArrowUp}{/Alt}");
		expect(shownOrder()).toEqual(["b", "a"]);
		expect(screen.getByRole("status")).toHaveTextContent(
			"Group Edge moved up, group 1 of 2.",
		);
	});

	it("ignores a plain arrow key in a group name field", async () => {
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness
				initial={[
					row("a", { groupName: "Core" }),
					row("b", { groupName: "Edge" }),
				]}
			/>,
		);
		await user.click(
			screen.getByRole("textbox", { name: "Name of group Edge" }),
		);
		await user.keyboard("{ArrowUp}");
		expect(shownOrder()).toEqual(["a", "b"]);
	});
});

describe("ComponentsEditor: ordering rows", () => {
	it("disables the arrows that would leave the group", async () => {
		await renderWithProviders(<Harness initial={[row("web"), row("api")]} />);
		expect(screen.getByRole("button", { name: "Move web up" })).toBeDisabled();
		expect(screen.getByRole("button", { name: "Move web down" })).toBeEnabled();
		expect(screen.getByRole("button", { name: "Move api up" })).toBeEnabled();
		expect(
			screen.getByRole("button", { name: "Move api down" }),
		).toBeDisabled();
	});

	it("does not let a row cross into a neighbouring group", async () => {
		await renderWithProviders(
			<Harness
				initial={[
					row("a", { groupName: "Core" }),
					row("b", { groupName: "Edge" }),
				]}
			/>,
		);
		expect(screen.getByRole("button", { name: "Move a down" })).toBeDisabled();
		expect(screen.getByRole("button", { name: "Move b up" })).toBeDisabled();
	});

	it("moves a row with its buttons and announces the new position", async () => {
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness initial={[row("web"), row("api"), row("db")]} />,
		);
		await user.click(screen.getByRole("button", { name: "Move web down" }));
		expect(shownOrder()).toEqual(["api", "web", "db"]);
		expect(screen.getByRole("status")).toHaveTextContent(
			"web moved down, position 2 of 3.",
		);
		expect(document.activeElement).toBe(
			screen.getByRole("button", { name: "Move web down" }),
		);

		await user.click(screen.getByRole("button", { name: "Move db up" }));
		expect(shownOrder()).toEqual(["api", "db", "web"]);
		expect(screen.getByRole("status")).toHaveTextContent(
			"db moved up, position 2 of 3.",
		);
	});

	it("hands focus to the opposite arrow when the row reaches the end", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Harness initial={[row("web"), row("api")]} />);
		await user.click(screen.getByRole("button", { name: "Move web down" }));
		expect(shownOrder()).toEqual(["api", "web"]);
		expect(document.activeElement).toBe(
			screen.getByRole("button", { name: "Move web up" }),
		);
	});

	it.each([
		[
			"public name",
			() => screen.getAllByRole("textbox", { name: "Public name" })[1],
		],
		["group", () => screen.getAllByLabelText("Group")[1]],
		["link", () => screen.getAllByLabelText("Link (optional)")[1]],
	])(
		"moves a row up and down with Alt + arrow from its %s field",
		async (_name, field) => {
			const user = userEvent.setup();
			await renderWithProviders(<Harness initial={[row("web"), row("api")]} />);
			await user.click(field() as HTMLElement);
			await user.keyboard("{Alt>}{ArrowUp}{/Alt}");
			expect(shownOrder()).toEqual(["api", "web"]);

			await user.click(
				screen.getAllByRole("textbox", {
					name: "Public name",
				})[0] as HTMLElement,
			);
			await user.keyboard("{Alt>}{ArrowDown}{/Alt}");
			expect(shownOrder()).toEqual(["web", "api"]);
			expect(screen.getByRole("status")).toHaveTextContent(
				"api moved down, position 2 of 2.",
			);
		},
	);

	it("leaves the order alone for an arrow without Alt", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Harness initial={[row("web"), row("api")]} />);
		await user.click(
			screen.getAllByRole("textbox", { name: "Public name" })[1] as HTMLElement,
		);
		await user.keyboard("{ArrowUp}");
		expect(shownOrder()).toEqual(["web", "api"]);
	});

	it("leaves the order alone for Alt with another key", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Harness initial={[row("web"), row("api")]} />);
		await user.click(
			screen.getAllByRole("textbox", { name: "Public name" })[1] as HTMLElement,
		);
		await user.keyboard("{Alt>}{ArrowLeft}{/Alt}");
		expect(shownOrder()).toEqual(["web", "api"]);
	});
});

describe("ComponentsEditor: adding services", () => {
	function stubTeam() {
		return stubApi({
			"GET /projects": {
				body: make("PageProject", {
					items: [make("Project", { id: "proj-1", name: "Acme" })],
				}),
			},
			"GET /projects/proj-1/environments": {
				body: make("PageEnvironment", {
					items: [
						make("Environment", {
							id: "env-1",
							projectId: "proj-1",
							name: "production",
							slug: "production",
							isProduction: false,
						}),
					],
				}),
			},
			"GET /environments/env-1/services": {
				body: make("PageServiceView", {
					items: [
						view("web", "web", "env-1"),
						view("api", "api", "env-1"),
						view("db", "db", "env-1"),
					],
				}),
			},
		});
	}

	it("appends the picked services under their own names and announces it", async () => {
		stubTeam();
		const user = userEvent.setup();
		const seen: ComponentRow[][] = [];
		await renderWithProviders(
			<Harness initial={[row("web")]} seen={(rows) => seen.push(rows)} />,
		);
		await user.click(screen.getByRole("button", { name: "Add services" }));
		await screen.findByRole("dialog", { name: "Add services to the page" });
		// A service already on the page is not offered again.
		await screen.findByRole("checkbox", { name: "api" });
		expect(
			screen.queryByRole("checkbox", { name: "web" }),
		).not.toBeInTheDocument();

		await user.click(screen.getByRole("checkbox", { name: "db" }));
		await user.click(screen.getByRole("checkbox", { name: "api" }));
		await user.click(screen.getByRole("button", { name: "Add 2 services" }));

		await waitFor(() => expect(shownOrder()).toEqual(["web", "api", "db"]));
		expect(seen.at(-1)?.[1]).toEqual({
			serviceId: "api",
			displayName: "api",
			groupName: "",
			url: "",
			serviceName: "api",
			projectName: "Acme",
			environmentName: "production",
		});
		expect(screen.getAllByRole("status").map((s) => s.textContent)).toContain(
			"2 services added to the page.",
		);
	});

	it("tells the picker how much room is left on the page", async () => {
		stubTeam();
		const user = userEvent.setup();
		const rows = Array.from({ length: 99 }, (_, i) => row(`svc-${i}`));
		await renderWithProviders(<Harness initial={rows} />);
		await user.click(screen.getByRole("button", { name: "Add services" }));
		await user.click(await screen.findByRole("checkbox", { name: "api" }));
		expect(
			screen.getByText(
				"The page has room for 1 more component; that is all of them.",
			),
		).toBeInTheDocument();
	}, 30_000);
});
