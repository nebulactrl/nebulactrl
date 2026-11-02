import { describe, expect, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { CoolifyPlanRewrite } from "#/api/gen/types.gen";
import { renderWithProviders } from "#/test/render";
import { EnvList } from "./env-list";
import {
	buildChoices,
	EMPTY_DRAFT,
	type ImportDraft,
	rewritesFor,
} from "./lib";
import { envRow } from "./test-fixtures";
import { DraftHarness } from "./test-harness";

// Builds a literal `${NAME}` placeholder (the escaped `$` keeps it from interpolating).
const ref = (name: string) => `\${${name}}`;

const NO_REWRITES = new Map<string, CoolifyPlanRewrite>();

async function renderList(
	env: Parameters<typeof EnvList>[0]["env"],
	{
		rewrites = NO_REWRITES,
		initial = EMPTY_DRAFT,
	}: { rewrites?: typeof NO_REWRITES; initial?: ImportDraft } = {},
) {
	const seen = { current: initial };
	await renderWithProviders(
		<DraftHarness initial={initial} seen={seen}>
			{(draft, onDraft) => (
				<EnvList
					resourceUuid="res-1"
					env={env}
					drafts={draft.resources["res-1"]?.env}
					rewrites={rewrites}
					onDraft={onDraft}
				/>
			)}
		</DraftHarness>,
	);
	return { choices: () => buildChoices(seen.current) };
}

describe("EnvList", () => {
	test("renders nothing for a resource without variables", async () => {
		await renderList([]);
		expect(screen.queryByRole("button", { name: /Variables/ })).toBeNull();
	});

	test("stays closed with a count when every value is known, and opens on click", async () => {
		const user = userEvent.setup();
		await renderList([envRow({ key: "A" }), envRow({ key: "B" })]);
		const toggle = screen.getByRole("button", { name: /Variables/ });
		expect(toggle).toHaveTextContent("(2)");
		expect(toggle).toHaveAttribute("aria-expanded", "false");
		expect(screen.queryByText("A")).toBeNull();

		await user.click(toggle);
		expect(toggle).toHaveAttribute("aria-expanded", "true");
		expect(screen.getByText("A")).toBeInTheDocument();
		expect(
			screen.getByText("Keys only: the values are never shown here."),
		).toBeInTheDocument();
	});

	test("opens by itself when a variable still needs a value, and says how many", async () => {
		await renderList([
			envRow({ key: "DB_PASSWORD", needsValue: true, secret: true }),
			envRow({ key: "OTHER_SECRET", needsValue: true, secret: true }),
			envRow({ key: "PLAIN" }),
		]);
		expect(screen.getByRole("button", { name: /Variables/ })).toHaveAttribute(
			"aria-expanded",
			"true",
		);
		expect(screen.getByText(/2 still need a value\./)).toBeInTheDocument();
		expect(screen.getAllByText("needs value")).toHaveLength(2);
		expect(screen.getByLabelText("Value for DB_PASSWORD")).toHaveAttribute(
			"type",
			"password",
		);
		expect(screen.queryByLabelText("Value for PLAIN")).toBeNull();
	});

	test("one missing value reads in the singular", async () => {
		await renderList([envRow({ key: "TOKEN", needsValue: true })]);
		expect(screen.getByText(/1 still needs a value\./)).toBeInTheDocument();
	});

	test("typing a value supplies it, clears 'needs value', and goes into the choices; emptying keeps the field", async () => {
		const user = userEvent.setup();
		const list = await renderList([
			envRow({ key: "DB_PASSWORD", needsValue: true, secret: true }),
		]);
		const field = screen.getByLabelText("Value for DB_PASSWORD");
		await user.type(field, "hunter2");

		expect(screen.queryByText("needs value")).toBeNull();
		expect(screen.queryByText(/still needs? a value/)).toBeNull();
		expect(list.choices()).toEqual({
			resources: { "res-1": { env: { DB_PASSWORD: { value: "hunter2" } } } },
		});

		await user.clear(field);
		expect(screen.getByLabelText("Value for DB_PASSWORD")).toHaveValue("");
		// An emptied value never blanks the variable in the request.
		expect(list.choices()).toEqual({});
	});

	test("shows scope, secret reason, and the plan's note", async () => {
		const user = userEvent.setup();
		await renderList([
			envRow({
				key: "BUILD_ONLY",
				scope: "build",
				note: "Only used while building.",
			}),
			envRow({ key: "BOTH", scope: "both" }),
			envRow({ key: "RUNTIME" }),
			envRow({ key: "KEY", secret: true, secretReason: "name" }),
			envRow({ key: "ODD", secret: true, secretReason: "custom reason" }),
			envRow({ key: "BARE", secret: true }),
		]);
		await user.click(screen.getByRole("button", { name: /Variables/ }));

		expect(screen.getByText("build")).toBeInTheDocument();
		expect(screen.getByText("build + runtime")).toBeInTheDocument();
		expect(screen.getByText("Only used while building.")).toBeInTheDocument();
		expect(screen.getByText("secret · by its name")).toBeInTheDocument();
		expect(screen.getByText("secret · custom reason")).toBeInTheDocument();
		expect(screen.getByText("secret")).toBeInTheDocument();
		// RUNTIME has no scope badge.
		const runtimeRow = screen.getByText("RUNTIME").closest("div")
			?.parentElement as HTMLElement;
		expect(within(runtimeRow).queryByText("build")).toBeNull();
	});

	test("the secret switch writes a secret choice, and switching back removes it", async () => {
		const user = userEvent.setup();
		const list = await renderList([envRow({ key: "API_URL" })]);
		await user.click(screen.getByRole("button", { name: /Variables/ }));
		const secret = screen.getByRole("switch", { name: "Secret: API_URL" });
		expect(secret).not.toBeChecked();

		await user.click(secret);
		expect(secret).toBeChecked();
		expect(screen.getByText("secret")).toBeInTheDocument();
		expect(list.choices()).toEqual({
			resources: { "res-1": { env: { API_URL: { secret: true } } } },
		});

		await user.click(secret);
		expect(secret).not.toBeChecked();
		expect(list.choices()).toEqual({
			resources: { "res-1": { env: { API_URL: { secret: false } } } },
		});
	});

	test("the skip switch marks the variable skipped; a variable the plan skips starts on", async () => {
		const user = userEvent.setup();
		const list = await renderList([
			envRow({ key: "KEPT" }),
			envRow({ key: "DROPPED", skipped: true }),
		]);
		await user.click(screen.getByRole("button", { name: /Variables/ }));
		expect(screen.getByRole("switch", { name: "Skip: DROPPED" })).toBeChecked();
		expect(
			screen.getByRole("switch", { name: "Skip: KEPT" }),
		).not.toBeChecked();
		expect(screen.getAllByText("skipped")).toHaveLength(1);

		await user.click(screen.getByRole("switch", { name: "Skip: KEPT" }));
		expect(screen.getAllByText("skipped")).toHaveLength(2);
		expect(list.choices()).toEqual({
			resources: { "res-1": { env: { KEPT: { skip: true } } } },
		});
	});

	test("a rewritten variable shows the reference it now uses and can keep the Coolify host", async () => {
		const user = userEvent.setup();
		const rewrites = rewritesFor(
			[
				{
					key: "DATABASE_URL",
					kind: "url",
					resource: "web",
					to: ref("{db.DATABASE_URL}"),
				},
			],
			"web",
		);
		const list = await renderList(
			[envRow({ key: "DATABASE_URL", rewritten: true }), envRow({ key: "X" })],
			{ rewrites },
		);
		await user.click(screen.getByRole("button", { name: /Variables/ }));

		expect(screen.getByText("rewritten")).toBeInTheDocument();
		expect(screen.getByText(ref("{db.DATABASE_URL}"))).toBeInTheDocument();
		expect(screen.getByLabelText("becomes")).toBeInTheDocument();
		const keep = screen.getByRole("checkbox", {
			name: "Keep the Coolify host and credentials (no reference)",
		});
		// Only the rewritten row offers it.
		expect(screen.getAllByRole("checkbox")).toHaveLength(1);
		expect(keep).not.toBeChecked();

		await user.click(keep);
		expect(keep).toBeChecked();
		expect(list.choices()).toEqual({
			resources: {
				"res-1": { env: { DATABASE_URL: { rewrite: false } } },
			},
		});
	});

	test("a variable the reader chose to keep stays offered even when the plan no longer rewrites it", async () => {
		await renderList([envRow({ key: "DATABASE_URL" })], {
			initial: {
				...EMPTY_DRAFT,
				resources: {
					"res-1": { env: { DATABASE_URL: { keepCoolify: true } } },
				},
			},
		});
		const toggle = screen.getByRole("button", { name: /Variables/ });
		await userEvent.setup().click(toggle);
		expect(
			screen.getByRole("checkbox", {
				name: "Keep the Coolify host and credentials (no reference)",
			}),
		).toBeChecked();
	});
});
