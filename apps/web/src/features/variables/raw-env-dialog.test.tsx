import { describe, expect, it } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { RawEnvDialog } from "./raw-env-dialog";
import { makeVariable } from "./testing";

const PUT = "PUT /services/:id/variables";
const DELETE = "DELETE /services/:id/variables/:key";
const emptyList = make("VariableList");

const VARIABLES = [
	makeVariable({ key: "LEVEL", value: "info" }),
	makeVariable({ key: "GREETING", value: "hello world" }),
	makeVariable({ key: "API_TOKEN", isSecret: true, value: undefined }),
];

function Harness({
	variables = VARIABLES,
	onChange,
}: {
	variables?: typeof VARIABLES;
	onChange?: (open: boolean) => void;
}) {
	const [open, setOpen] = useState(true);
	return (
		<RawEnvDialog
			serviceId="s1"
			environmentId="env-1"
			open={open}
			variables={variables}
			onOpenChange={(next) => {
				setOpen(next);
				onChange?.(next);
			}}
		/>
	);
}

const editor = () => screen.findByLabelText("Variables in .env format");
const saveButton = () => screen.getByRole("button", { name: /Save variables/ });

/** Replaces the whole text (typing `${{` would be read as a key chord). */
async function setText(user: ReturnType<typeof userEvent.setup>, text: string) {
	const field = await editor();
	await user.clear(field);
	if (text !== "") await user.click(field).then(() => user.paste(text));
}

function Reopen() {
	const [open, setOpen] = useState(true);
	return (
		<>
			<button type="button" onClick={() => setOpen(true)}>
				reopen
			</button>
			<RawEnvDialog
				serviceId="s1"
				environmentId="env-1"
				open={open}
				variables={VARIABLES}
				onOpenChange={setOpen}
			/>
		</>
	);
}

describe("RawEnvDialog", () => {
	it("shows the plain variables as KEY=value, quoting what needs it, and no secrets", async () => {
		stubApi({});
		await renderWithProviders(<Harness />);
		const field = await editor();
		expect(field).toHaveValue('LEVEL=info\nGREETING="hello world"');
		expect(field.textContent).not.toContain("API_TOKEN");
	});

	it("starts with Save disabled and says there are no changes", async () => {
		stubApi({});
		await renderWithProviders(<Harness />);
		await editor();
		expect(saveButton()).toBeDisabled();
		expect(screen.getByText("No changes.")).toBeInTheDocument();
	});

	it("tells how many secrets stay untouched", async () => {
		stubApi({});
		await renderWithProviders(<Harness />);
		expect(
			await screen.findByText(/1 secret is not shown and stays as it is\./),
		).toBeInTheDocument();
	});

	it("pluralises the secret count and says secrets are never shown when there are none", async () => {
		stubApi({});
		const two = [
			makeVariable({ key: "A", isSecret: true, value: undefined }),
			makeVariable({ key: "B", isSecret: true, value: undefined }),
		];
		const view = await renderWithProviders(<Harness variables={two} />);
		expect(
			await screen.findByText(/2 secrets are not shown and stay as they are\./),
		).toBeInTheDocument();
		view.unmount();
		await renderWithProviders(<Harness variables={[makeVariable()]} />);
		expect(
			await screen.findByText(/Secrets are never shown here\./),
		).toBeInTheDocument();
	});

	it("counts additions and deletions while typing and saves them as upserts then deletes", async () => {
		const user = userEvent.setup();
		const changes: boolean[] = [];
		const api = stubApi({
			[PUT]: { body: emptyList },
			[DELETE]: { status: 204 },
			"GET /services/:id/variables": { body: emptyList },
		});
		await renderWithProviders(<Harness onChange={(o) => changes.push(o)} />);
		// LEVEL changes, GREETING is dropped, NEW_ONE is added, API_TOKEN (secret) replaced.
		await setText(user, "LEVEL=debug\nNEW_ONE=1\nAPI_TOKEN=s3cret");
		expect(
			await screen.findByText("3 to add or update, 1 to delete."),
		).toBeInTheDocument();
		await user.click(saveButton());

		expect(
			await screen.findByText("Saved 4 variable changes"),
		).toBeInTheDocument();
		expect(
			screen.getByText("Staged until the next deploy."),
		).toBeInTheDocument();
		await waitFor(() => expect(changes).toEqual([false]));
		const writes = api.calls.filter((c) => c.method !== "GET");
		expect(writes.map((c) => `${c.method} ${c.path}`)).toEqual([
			"PUT /services/s1/variables",
			"DELETE /services/s1/variables/GREETING",
		]);
		expect(writes[0]?.body).toEqual({
			variables: [
				{ key: "LEVEL", value: "debug" },
				{ key: "NEW_ONE", value: "1" },
				{ key: "API_TOKEN", value: "s3cret", isSecret: true },
			],
		});
	});

	it("uses the singular for one change", async () => {
		const user = userEvent.setup();
		stubApi({
			[PUT]: { body: emptyList },
			"GET /services/:id/variables": { body: emptyList },
		});
		await renderWithProviders(<Harness />);
		await setText(user, 'LEVEL=warn\nGREETING="hello world"');
		await user.click(saveButton());
		expect(
			await screen.findByText("Saved 1 variable change"),
		).toBeInTheDocument();
	});

	it("deleting every plain variable leaves the secrets alone", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			[DELETE]: { status: 204 },
			"GET /services/:id/variables": { body: emptyList },
		});
		await renderWithProviders(<Harness />);
		await setText(user, "");
		expect(
			await screen.findByText("0 to add or update, 2 to delete."),
		).toBeInTheDocument();
		await user.click(saveButton());
		await screen.findByText("Saved 2 variable changes");
		expect(
			api.calls.filter((c) => c.method === "DELETE").map((c) => c.path),
		).toEqual([
			"/services/s1/variables/LEVEL",
			"/services/s1/variables/GREETING",
		]);
		expect(api.calls.some((c) => c.method === "PUT")).toBe(false);
	});

	it("lists problems with their line numbers and blocks Save", async () => {
		const user = userEvent.setup();
		const api = stubApi({});
		await renderWithProviders(<Harness />);
		await setText(user, "LEVEL=info\nnot a pair\n1BAD=x");
		const problems = await screen.findByRole("list", {
			name: "Problems in the text",
		});
		const items = within(problems).getAllByRole("listitem");
		expect(items[0]).toHaveTextContent("Line 2: Expected KEY=value.");
		expect(items[1]).toHaveTextContent("Line 3: “1BAD” is not a valid name");
		expect(saveButton()).toBeDisabled();
		expect(screen.queryByText(/to add or update/)).toBeNull();
		expect(api.calls).toEqual([]);
	});

	it("shows at most five problems", async () => {
		const user = userEvent.setup();
		stubApi({});
		await renderWithProviders(<Harness />);
		await setText(user, "a\nb\nc\nd\ne\nf\ng");
		const problems = await screen.findByRole("list", {
			name: "Problems in the text",
		});
		expect(within(problems).getAllByRole("listitem")).toHaveLength(5);
	});

	it("flags a duplicate key instead of dropping one silently", async () => {
		const user = userEvent.setup();
		stubApi({});
		await renderWithProviders(<Harness />);
		await setText(user, "LEVEL=a\nLEVEL=b");
		expect(
			await screen.findByText("Line 2: LEVEL appears more than once."),
		).toBeInTheDocument();
		expect(saveButton()).toBeDisabled();
	});

	it("shows the server's reason and stays open when saving fails", async () => {
		const user = userEvent.setup();
		const changes: boolean[] = [];
		stubApi({
			[PUT]: problem(422, {
				code: "invalid",
				title: "Invalid",
				detail: "PORT is provided by the platform",
			}),
		});
		await renderWithProviders(<Harness onChange={(o) => changes.push(o)} />);
		await setText(user, 'LEVEL=info\nGREETING="hello world"\nPORT=80');
		await user.click(saveButton());
		expect(
			await screen.findByText("PORT is provided by the platform"),
		).toBeInTheDocument();
		expect(changes).toEqual([]);
		expect(screen.queryByText(/Saved/)).toBeNull();
		expect(screen.getByLabelText("Variables in .env format")).toHaveValue(
			'LEVEL=info\nGREETING="hello world"\nPORT=80',
		);
	});

	it("Cancel closes without writing", async () => {
		const user = userEvent.setup();
		const changes: boolean[] = [];
		const api = stubApi({});
		await renderWithProviders(<Harness onChange={(o) => changes.push(o)} />);
		await setText(user, "LEVEL=changed");
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		expect(changes).toEqual([false]);
		expect(api.calls).toEqual([]);
	});

	it("starts from the stored variables every time it opens", async () => {
		const user = userEvent.setup();
		stubApi({});
		await renderWithProviders(<Reopen />);
		await setText(user, "EDITED=1");
		await user.keyboard("{Escape}");
		await waitFor(() =>
			expect(screen.queryByLabelText("Variables in .env format")).toBeNull(),
		);
		await user.click(screen.getByRole("button", { name: "reopen" }));
		expect(await editor()).toHaveValue('LEVEL=info\nGREETING="hello world"');
	});

	it("fetches reference candidates only once a reference is typed", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"GET /environments/:id/services": { body: make("PageServiceView") },
			"GET /environments/:id/shared-variables": {
				body: make("SharedVariableList"),
			},
			"GET /team/shared-variables": { body: make("SharedVariableList") },
			"GET /secret-managers": { body: make("ListOutputBody") },
			"GET /services/:id/variables": { body: emptyList },
		});
		await renderWithProviders(<Harness />);
		await editor();
		expect(api.calls).toEqual([]);
		await setText(user, "LEVEL=${{");
		await waitFor(() => expect(api.calls.length).toBeGreaterThan(0));
		expect(api.calls.some((c) => c.path.endsWith("/services"))).toBe(true);
		expect(api.unmatched).toEqual([]);
	});
});
