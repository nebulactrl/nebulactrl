import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import type { QueryClient } from "@tanstack/react-query";
import { useRouterState } from "@tanstack/react-router";
import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { meQuery } from "#/api/hooks";
import { teamsQuery } from "#/features/teams/queries";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders, testQueryClient } from "#/test/render";
import type { BufferLine } from "./buffer";
import { useLogSearch } from "./use-log-search";

const LEGACY_KEY = "nebulactrl.logs.recent-queries";
const recentKey = (team: string, user: string) =>
	`${LEGACY_KEY}:${team}:${user}`;
const RECENT_KEY = recentKey("t1", "u1");

/** A client that already knows who is signed in and which team they act in. */
function signedIn(user = "u1", team = "t1"): QueryClient {
	const queryClient = testQueryClient();
	queryClient.setQueryData(meQuery().queryKey, make("MeBody", { id: user }));
	queryClient.setQueryData(teamsQuery().queryKey, {
		items: [make("Team", { id: team, current: true })],
	});
	return queryClient;
}

const seed: BufferLine[] = [
	{ id: 1, line: "INFO server started" },
	{ id: 2, line: "ERROR disk full" },
	{ id: 3, line: "ERROR timeout talking to db" },
];

function Probe({
	initial = seed,
	epoch = 0,
	skipBlank = false,
}: {
	initial?: BufferLine[];
	epoch?: number;
	skipBlank?: boolean;
}) {
	const [lines, setLines] = useState(initial);
	const [ep, setEp] = useState(epoch);
	const [shift, setShift] = useState(0);
	const search = useLogSearch({ lines, epoch: ep, reorder: shift, skipBlank });
	const urlSearch = useRouterState({ select: (s) => s.location.searchStr });
	return (
		<div>
			<input
				aria-label="query"
				value={search.text}
				onChange={(e) => search.setText(e.target.value)}
			/>
			<output aria-label="visible">
				{search.visible.map((l) => l.line).join("|")}
			</output>
			<output aria-label="total">{search.total}</output>
			<output aria-label="settled">{search.settled.input}</output>
			<output aria-label="announcement">{search.announcement}</output>
			<output aria-label="recent">{search.recent.join("|")}</output>
			<output aria-label="url">{urlSearch}</output>
			<output aria-label="terms">
				{search.terms.map((t) => t.parts.join("+")).join("|")}
			</output>
			<button type="button" onClick={search.commit}>
				commit
			</button>
			<button
				type="button"
				onClick={() => {
					setLines((l) => [
						l[0] as BufferLine,
						{ id: 1.5, line: "ERROR slipped in" },
						...l.slice(1),
					]);
					setShift((n) => n + 1);
				}}
			>
				splice
			</button>
			<button type="button" onClick={search.clear}>
				clear
			</button>
			<button
				type="button"
				onClick={() =>
					setLines((l) => [...l, { id: 4, line: "ERROR new failure" }])
				}
			>
				append
			</button>
			<button
				type="button"
				onClick={() => {
					setLines([{ id: 1, line: "WARN replaced" }]);
					setEp((e) => e + 1);
				}}
			>
				replace
			</button>
		</div>
	);
}

const out = (name: string) => screen.getByLabelText(name);
const visible = () => {
	const text = out("visible").textContent ?? "";
	return text === "" ? [] : text.split("|");
};
const type = async (user: ReturnType<typeof userEvent.setup>, text: string) => {
	await user.clear(screen.getByLabelText("query"));
	await user.type(screen.getByLabelText("query"), text);
};

// Renders without a seeded client ask who is signed in; the answer never comes.
beforeEach(() => {
	stubApi({
		"GET /me": () => new Promise<Response>(() => {}),
		"GET /teams": () => new Promise<Response>(() => {}),
	});
});
afterEach(() => localStorage.clear());

describe("useLogSearch", () => {
	test("with no query every line is visible", async () => {
		await renderWithProviders(<Probe />);
		expect(visible()).toEqual([
			"INFO server started",
			"ERROR disk full",
			"ERROR timeout talking to db",
		]);
		expect(out("total")).toHaveTextContent("3");
	});

	test("the box starts from ?q= and filters at once", async () => {
		await renderWithProviders(<Probe />, { path: "/?q=disk" });
		expect(screen.getByLabelText("query")).toHaveValue("disk");
		expect(visible()).toEqual(["ERROR disk full"]);
		expect(out("total")).toHaveTextContent("3");
		expect(out("terms")).toHaveTextContent("disk");
	});

	test("typing filters after the debounce, announces the count and writes ?q=", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Probe />);
		await type(user, "error");
		await waitFor(() =>
			expect(visible()).toEqual([
				"ERROR disk full",
				"ERROR timeout talking to db",
			]),
		);
		expect(out("settled")).toHaveTextContent("error");
		await waitFor(() => expect(out("url").textContent).toContain("q=error"));
		await waitFor(
			() => expect(out("announcement")).toHaveTextContent("2 of 3 lines match"),
			{ timeout: 3000 },
		);
	});

	test("emptying the box removes ?q= and the announcement", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Probe />, { path: "/?q=error" });
		await waitFor(
			() => expect(out("announcement")).toHaveTextContent("2 of 3 lines match"),
			{ timeout: 3000 },
		);
		await user.click(screen.getByRole("button", { name: "clear" }));
		await waitFor(() => expect(out("url").textContent).not.toContain("q="));
		expect(screen.getByLabelText("query")).toHaveValue("");
		expect(out("announcement")).toHaveTextContent("");
		expect(visible()).toHaveLength(3);
	});

	test("new lines are matched as they arrive", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Probe />, { path: "/?q=error" });
		await user.click(screen.getByRole("button", { name: "append" }));
		expect(visible()).toEqual([
			"ERROR disk full",
			"ERROR timeout talking to db",
			"ERROR new failure",
		]);
		expect(out("total")).toHaveTextContent("4");
	});

	test("a new epoch takes the lines afresh", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Probe />);
		await user.click(screen.getByRole("button", { name: "replace" }));
		expect(visible()).toEqual(["WARN replaced"]);
		expect(out("total")).toHaveTextContent("1");
	});

	test("a line placed among the shown ones is taken without starting over", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Probe />, { path: "/?q=error" });
		await user.click(screen.getByRole("button", { name: "splice" }));
		expect(visible()).toEqual([
			"ERROR slipped in",
			"ERROR disk full",
			"ERROR timeout talking to db",
		]);
		expect(out("total")).toHaveTextContent("4");
	});

	test("committing remembers a query once, newest first, and stores it", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Probe />, { queryClient: signedIn() });
		await type(user, "timeout");
		await user.click(screen.getByRole("button", { name: "commit" }));
		await type(user, "disk");
		await user.click(screen.getByRole("button", { name: "commit" }));
		await type(user, "timeout");
		await user.click(screen.getByRole("button", { name: "commit" }));
		expect(out("recent")).toHaveTextContent("timeout|disk");
		expect(JSON.parse(localStorage.getItem(RECENT_KEY) ?? "[]")).toEqual([
			"timeout",
			"disk",
		]);
	});

	test("committing a blank or invalid query remembers nothing", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Probe />, { queryClient: signedIn() });
		await user.click(screen.getByRole("button", { name: "commit" }));
		await type(user, "status:>");
		await user.click(screen.getByRole("button", { name: "commit" }));
		expect(out("recent")).toHaveTextContent("");
		expect(localStorage.getItem(RECENT_KEY)).toBeNull();
	});

	test("a query that ends mid-word is not remembered, a whole one is", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Probe />, { queryClient: signedIn() });
		for (const half of ["@", "@method:"]) {
			await type(user, half);
			await user.click(screen.getByRole("button", { name: "commit" }));
		}
		expect(out("recent")).toHaveTextContent("");
		expect(localStorage.getItem(RECENT_KEY)).toBeNull();
		await type(user, "@method:GET");
		await user.click(screen.getByRole("button", { name: "commit" }));
		expect(out("recent")).toHaveTextContent("@method:GET");
	});

	test("recent queries from an earlier visit are loaded", async () => {
		localStorage.setItem(RECENT_KEY, JSON.stringify(["a b", "level:error"]));
		await renderWithProviders(<Probe />, { queryClient: signedIn() });
		expect(out("recent")).toHaveTextContent("a b|level:error");
	});

	test("two users on one browser do not see each other's recent queries", async () => {
		const user = userEvent.setup();
		const first = await renderWithProviders(<Probe />, {
			queryClient: signedIn("u1"),
		});
		await type(user, "customer@example.com");
		await user.click(screen.getByRole("button", { name: "commit" }));
		expect(out("recent")).toHaveTextContent("customer@example.com");
		first.unmount();

		await renderWithProviders(<Probe />, { queryClient: signedIn("u2") });
		expect(out("recent")).toHaveTextContent("");
		await type(user, "disk");
		await user.click(screen.getByRole("button", { name: "commit" }));
		expect(out("recent")).toHaveTextContent(/^disk$/);
		expect(
			JSON.parse(localStorage.getItem(recentKey("t1", "u1")) ?? "[]"),
		).toEqual(["customer@example.com"]);
		expect(
			JSON.parse(localStorage.getItem(recentKey("t1", "u2")) ?? "[]"),
		).toEqual(["disk"]);
	});

	test("the same user in another team starts from that team's own list", async () => {
		localStorage.setItem(recentKey("t1", "u1"), JSON.stringify(["t1 query"]));
		await renderWithProviders(<Probe />, {
			queryClient: signedIn("u1", "t2"),
		});
		expect(out("recent")).toHaveTextContent("");
	});

	test("the global list of older builds is ignored and removed", async () => {
		localStorage.setItem(LEGACY_KEY, JSON.stringify(["old secret"]));
		await renderWithProviders(<Probe />, { queryClient: signedIn() });
		expect(out("recent")).toHaveTextContent("");
		expect(localStorage.getItem(LEGACY_KEY)).toBeNull();
	});

	test("a switch of team inside the page shows the new team's list", async () => {
		localStorage.setItem(recentKey("t1", "u1"), JSON.stringify(["in t1"]));
		localStorage.setItem(recentKey("t2", "u1"), JSON.stringify(["in t2"]));
		const queryClient = signedIn("u1", "t1");
		await renderWithProviders(<Probe />, { queryClient });
		expect(out("recent")).toHaveTextContent("in t1");
		act(() =>
			queryClient.setQueryData(teamsQuery().queryKey, {
				items: [make("Team", { id: "t2", current: true })],
			}),
		);
		await waitFor(() => expect(out("recent")).toHaveTextContent("in t2"));
	});

	test("blank lines are left out of the total when skipBlank is on", async () => {
		await renderWithProviders(
			<Probe
				skipBlank
				initial={[
					{ id: 1, line: "hello" },
					{ id: 2, line: "   " },
				]}
			/>,
		);
		expect(out("total")).toHaveTextContent("1");
	});
});
