import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type { ConnectionView, Repo } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import {
	RepoPicker,
	type RepoSelection,
	usableConnections,
} from "./repo-picker";
import { connection, repo } from "./test-fixtures";

function Harness({
	connections,
	initial = null,
	onChange,
}: {
	connections: ConnectionView[];
	initial?: RepoSelection | null;
	onChange?: (next: RepoSelection | null) => void;
}) {
	const [value, setValue] = useState<RepoSelection | null>(initial);
	return (
		<RepoPicker
			idPrefix="t"
			connections={connections}
			value={value}
			onChange={(next) => {
				onChange?.(next);
				setValue(next);
			}}
		/>
	);
}

const acme = repo({ description: "The API", defaultBranch: "trunk" });
const infra = repo({
	fullName: "acme/infra",
	cloneUrl: "https://github.com/acme/infra.git",
	private: true,
	defaultBranch: "main",
});

/** A 200 reply listing `items` as the repositories of a connection. */
const repos = (...items: Repo[]) => ({
	body: make("ReposOutputBody", { items }),
});

/** A 200 reply listing `names` as the branches of a repository. */
const branches = (...names: string[]) => ({
	body: make("BranchesOutputBody", { items: names }),
});

describe("usableConnections", () => {
	test("keeps only finished connections", () => {
		const list = [
			connection({ id: "a", status: "active" }),
			connection({ id: "b", status: "pending" }),
			connection({ id: "c", status: "error" }),
		];
		expect(usableConnections(list).map((c) => c.id)).toEqual(["a"]);
	});
});

describe("RepoPicker with a listable connection", () => {
	test("renders nothing when no connection is usable", async () => {
		const api = stubApi({});
		const { container } = await renderWithProviders(
			<Harness connections={[connection({ status: "pending" })]} />,
		);
		expect(container.querySelector("input")).toBeNull();
		expect(api.calls).toEqual([]);
	});

	test("lists repositories with description, privacy and default branch", async () => {
		const api = stubApi({
			"GET /git/connections/:id/repos": repos(acme, infra),
		});
		await renderWithProviders(<Harness connections={[connection()]} />);
		expect(await screen.findByText("acme/api")).toBeInTheDocument();
		expect(screen.getByText("The API")).toBeInTheDocument();
		expect(screen.getByText("trunk")).toBeInTheDocument();
		const infraRow = screen.getByText("acme/infra").closest("button");
		expect(infraRow).not.toBeNull();
		expect(
			within(infraRow as HTMLElement).getByLabelText("Private"),
		).toBeInTheDocument();
		expect(
			within(
				screen.getByText("acme/api").closest("button") as HTMLElement,
			).queryByLabelText("Private"),
		).toBeNull();
		expect(api.calls[0]).toMatchObject({
			method: "GET",
			path: "/git/connections/c1/repos",
		});
		expect(api.calls[0]?.query.get("limit")).toBe("50");
		expect(api.unmatched).toEqual([]);
	});

	test("shows who the connection signs in as when there is one account", async () => {
		stubApi({ "GET /git/connections/:id/repos": repos() });
		await renderWithProviders(<Harness connections={[connection()]} />);
		expect(screen.getByText("Acme GitHub")).toBeInTheDocument();
		expect(screen.getByText("as acme")).toBeInTheDocument();
	});

	test("picking a repository selects it with its default branch and clone URL", async () => {
		stubApi({
			"GET /git/connections/:id/repos": repos(acme, infra),
			"GET /git/connections/:id/branches": branches("trunk", "dev"),
		});
		const picks: (RepoSelection | null)[] = [];
		await renderWithProviders(
			<Harness connections={[connection()]} onChange={(n) => picks.push(n)} />,
		);
		await userEvent.setup().click(await screen.findByText("acme/api"));
		expect(picks).toEqual([
			{
				connectionId: "c1",
				repo: "acme/api",
				branch: "trunk",
				cloneUrl: "https://github.com/acme/api.git",
			},
		]);
		const row = screen.getByText("acme/api").closest("button");
		expect(row).toHaveAttribute("aria-pressed", "true");
		expect(screen.getByText("acme/infra").closest("button")).toHaveAttribute(
			"aria-pressed",
			"false",
		);
	});

	test("a repository without a default branch falls back to main", async () => {
		stubApi({
			"GET /git/connections/:id/repos": repos(repo({ defaultBranch: "" })),
			"GET /git/connections/:id/branches": branches("main"),
		});
		const picks: (RepoSelection | null)[] = [];
		await renderWithProviders(
			<Harness connections={[connection()]} onChange={(n) => picks.push(n)} />,
		);
		await userEvent.setup().click(await screen.findByText("acme/api"));
		expect(picks[0]?.branch).toBe("main");
	});

	test("typing searches after a pause, sending the trimmed query", async () => {
		const api = stubApi({
			"GET /git/connections/:id/repos": (req) => {
				const q = new URL(req.url).searchParams.get("q");
				return Response.json({ items: q === "inf" ? [infra] : [acme, infra] });
			},
		});
		await renderWithProviders(<Harness connections={[connection()]} />);
		await screen.findByText("acme/api");
		await userEvent
			.setup()
			.type(screen.getByRole("textbox", { name: "Repository" }), " inf ");
		await waitFor(() =>
			expect(api.calls.some((c) => c.query.get("q") === "inf")).toBe(true),
		);
		await waitFor(() =>
			expect(screen.queryByText("acme/api")).not.toBeInTheDocument(),
		);
		expect(screen.getByText("acme/infra")).toBeInTheDocument();
		// Nothing was asked for each keystroke: only "" and the settled "inf".
		const qs = new Set(api.calls.map((c) => c.query.get("q")));
		expect([...qs].sort()).toEqual(["", "inf"]);
	});

	test("an empty list explains how to get repositories visible", async () => {
		stubApi({
			"GET /git/connections/:id/repos": {
				body: make("ReposOutputBody", { items: null }),
			},
		});
		await renderWithProviders(<Harness connections={[connection()]} />);
		expect(
			await screen.findByText(/can not see any repository yet/),
		).toBeInTheDocument();
	});

	test("a search with no hits names the query", async () => {
		stubApi({
			"GET /git/connections/:id/repos": (req) =>
				Response.json({
					items: new URL(req.url).searchParams.get("q") ? [] : [acme],
				}),
		});
		await renderWithProviders(<Harness connections={[connection()]} />);
		await screen.findByText("acme/api");
		await userEvent
			.setup()
			.type(screen.getByRole("textbox", { name: "Repository" }), "zzz");
		expect(
			await screen.findByText('No repository matches "zzz".'),
		).toBeInTheDocument();
	});

	test("a failing list shows the server's reason", async () => {
		stubApi({
			"GET /git/connections/:id/repos": problem(502, {
				title: "Bad Gateway",
				detail: "provider said no",
			}),
		});
		await renderWithProviders(<Harness connections={[connection()]} />);
		expect(
			await screen.findByText(/Repositories did not load: provider said no/),
		).toBeInTheDocument();
	});
});

describe("RepoPicker branches", () => {
	const selected: RepoSelection = {
		connectionId: "c1",
		repo: "acme/api",
		branch: "dev",
		cloneUrl: "https://github.com/acme/api.git",
	};

	test("lists the repository's branches in a select showing the current one", async () => {
		const api = stubApi({
			"GET /git/connections/:id/repos": repos(acme),
			"GET /git/connections/:id/branches": branches("main", "dev"),
		});
		await renderWithProviders(
			<Harness connections={[connection()]} initial={selected} />,
		);
		const trigger = await screen.findByRole("combobox", { name: "Branch" });
		expect(trigger).toHaveTextContent("dev");
		const call = api.calls.find((c) => c.path.endsWith("/branches"));
		expect(call?.query.get("repo")).toBe("acme/api");
	});

	test("choosing another branch reports it and keeps the repository", async () => {
		stubApi({
			"GET /git/connections/:id/repos": repos(acme),
			"GET /git/connections/:id/branches": branches("main", "dev"),
		});
		const picks: (RepoSelection | null)[] = [];
		await renderWithProviders(
			<Harness
				connections={[connection()]}
				initial={selected}
				onChange={(n) => picks.push(n)}
			/>,
		);
		const user = userEvent.setup();
		await user.click(await screen.findByRole("combobox", { name: "Branch" }));
		await user.click(await screen.findByRole("option", { name: "main" }));
		expect(picks.at(-1)).toEqual({ ...selected, branch: "main" });
	});

	test("a current branch missing from the list is still offered", async () => {
		stubApi({
			"GET /git/connections/:id/repos": repos(acme),
			"GET /git/connections/:id/branches": branches("main"),
		});
		await renderWithProviders(
			<Harness
				connections={[connection()]}
				initial={{ ...selected, branch: "legacy" }}
			/>,
		);
		const user = userEvent.setup();
		await user.click(await screen.findByRole("combobox", { name: "Branch" }));
		expect(await screen.findByRole("option", { name: "legacy" })).toBeVisible();
		expect(screen.getByRole("option", { name: "main" })).toBeVisible();
	});

	test("when branches fail to load the branch can be typed", async () => {
		stubApi({
			"GET /git/connections/:id/repos": repos(acme),
			"GET /git/connections/:id/branches": problem(500, {
				title: "boom",
				detail: "rate limited",
			}),
		});
		const picks: (RepoSelection | null)[] = [];
		await renderWithProviders(
			<Harness
				connections={[connection()]}
				initial={selected}
				onChange={(n) => picks.push(n)}
			/>,
		);
		expect(
			await screen.findByText(/Branches did not load \(rate limited\)/),
		).toBeInTheDocument();
		const input = screen.getByRole("textbox", { name: "Branch" });
		expect(input).toHaveValue("dev");
		await userEvent.setup().type(input, "x");
		expect(picks.at(-1)).toEqual({ ...selected, branch: "devx" });
	});

	test("no branch field until a repository is chosen", async () => {
		stubApi({ "GET /git/connections/:id/repos": repos(acme) });
		await renderWithProviders(<Harness connections={[connection()]} />);
		await screen.findByText("acme/api");
		expect(screen.queryByText("Branch")).not.toBeInTheDocument();
	});
});

describe("RepoPicker for a connection that cannot list repositories", () => {
	const bitbucket = connection({
		id: "b1",
		name: "Bitbucket team",
		provider: "bitbucket",
		authType: "webhook_only",
		canListRepos: false,
		accountLogin: "",
	});

	test("asks for owner/repository and never calls the list endpoints", async () => {
		const api = stubApi({});
		await renderWithProviders(<Harness connections={[bitbucket]} />);
		expect(screen.getByPlaceholderText("owner/repository")).toBeInTheDocument();
		expect(
			screen.getByText(/can not list repositories: type its full name/),
		).toBeInTheDocument();
		expect(screen.queryByText("as acme")).not.toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});

	test("typing a name selects it with main and no clone URL", async () => {
		stubApi({});
		const picks: (RepoSelection | null)[] = [];
		await renderWithProviders(
			<Harness connections={[bitbucket]} onChange={(n) => picks.push(n)} />,
		);
		await userEvent
			.setup()
			.type(screen.getByRole("textbox", { name: "Repository" }), "team/web");
		expect(picks.at(-1)).toEqual({
			connectionId: "b1",
			repo: "team/web",
			branch: "main",
			cloneUrl: null,
		});
		// The branch is a typed field too, prefilled with the default.
		expect(screen.getByRole("textbox", { name: "Branch" })).toHaveValue("main");
	});

	test("an existing branch is kept when the name changes", async () => {
		stubApi({});
		const picks: (RepoSelection | null)[] = [];
		await renderWithProviders(
			<Harness
				connections={[bitbucket]}
				initial={{
					connectionId: "b1",
					repo: "team/w",
					branch: "release",
					cloneUrl: null,
				}}
				onChange={(n) => picks.push(n)}
			/>,
		);
		await userEvent
			.setup()
			.type(screen.getByRole("textbox", { name: "Repository" }), "e");
		expect(picks.at(-1)).toMatchObject({ repo: "team/we", branch: "release" });
	});
});

describe("RepoPicker with several connections", () => {
	const gitea = connection({
		id: "g1",
		name: "Home Gitea",
		provider: "gitea",
		authType: "token",
		accountLogin: "me",
	});

	test("offers an account select and switching resets the repository", async () => {
		stubApi({
			"GET /git/connections/:id/repos": repos(acme),
			"GET /git/connections/:id/branches": branches("dev"),
		});
		const picks: (RepoSelection | null)[] = [];
		await renderWithProviders(
			<Harness
				connections={[connection(), gitea]}
				initial={{
					connectionId: "c1",
					repo: "acme/api",
					branch: "dev",
					cloneUrl: "https://github.com/acme/api.git",
				}}
				onChange={(n) => picks.push(n)}
			/>,
		);
		const user = userEvent.setup();
		const trigger = await screen.findByRole("combobox", { name: "Account" });
		expect(trigger).toHaveTextContent("Acme GitHub");
		await user.click(trigger);
		await user.click(await screen.findByRole("option", { name: /Home Gitea/ }));
		expect(picks.at(-1)).toEqual({
			connectionId: "g1",
			repo: "",
			branch: "",
			cloneUrl: null,
		});
	});

	test("skips connections that are not usable", async () => {
		stubApi({ "GET /git/connections/:id/repos": repos() });
		await renderWithProviders(
			<Harness connections={[connection(), { ...gitea, status: "error" }]} />,
		);
		expect(
			screen.queryByRole("combobox", { name: "Account" }),
		).not.toBeInTheDocument();
	});

	test("a selection pointing at a missing connection falls back to the first usable one", async () => {
		const api = stubApi({
			"GET /git/connections/:id/repos": repos(acme),
		});
		await renderWithProviders(
			<Harness
				connections={[connection()]}
				initial={{
					connectionId: "gone",
					repo: "x/y",
					branch: "z",
					cloneUrl: null,
				}}
			/>,
		);
		await screen.findByText("acme/api");
		expect(api.calls[0]?.path).toBe("/git/connections/c1/repos");
		// The stale repo of another connection is not carried over.
		expect(screen.queryByText("Branch")).not.toBeInTheDocument();
	});
});
