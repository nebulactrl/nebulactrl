import { describe, expect, it } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { SuggestedVariable } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, type Overrides, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import {
	SuggestedVariables,
	type SuggestionDraft,
} from "./suggested-variables";

const OPEN = "$" + "{{";

function suggestion(
	over: Overrides<SuggestedVariable> = {},
): SuggestedVariable {
	return make("SuggestedVariable", {
		key: "DATABASE_URL",
		value: `${OPEN}postgres.DATABASE_URL}}`,
		needsInput: false,
		proposedSecret: false,
		reason: "postgres is not referenced here yet",
		source: "database",
		warnings: null,
		...over,
	});
}

const list = (items: SuggestedVariable[]) => ({
	"GET /services/svc1/suggested-variables": {
		body: make("SuggestedVariableList", { items }),
	},
});

const OK_LIST = make("VariableList");

function renderIt(onNeedsInput: (draft: SuggestionDraft) => void = () => {}) {
	return renderWithProviders(
		<SuggestedVariables serviceId="svc1" onNeedsInput={onNeedsInput} />,
	);
}

describe("SuggestedVariables", () => {
	it("renders nothing without database suggestions (env-example keys have their own banner)", async () => {
		const api = stubApi(
			list([suggestion({ key: "LOG_LEVEL", source: "env-example" })]),
		);
		const { container, queryClient } = await renderIt();
		await waitFor(() => expect(api.calls).toHaveLength(1));
		await waitFor(() => expect(queryClient.isFetching()).toBe(0));
		expect(container.querySelector("ul")).toBeNull();
		expect(screen.queryByText(/suggested variable/)).toBeNull();
	});

	it("lists database suggestions with reason, value and badges, counting only them", async () => {
		stubApi(
			list([
				suggestion({ warnings: ["points at a stopped database"] }),
				suggestion({
					key: "DB_PASSWORD",
					value: "",
					needsInput: true,
					proposedSecret: true,
					reason: "postgres needs a password",
				}),
				suggestion({ key: "LOG_LEVEL", source: "env-example" }),
			]),
		);
		await renderIt();
		expect(
			await screen.findByText("2 suggested variables"),
		).toBeInTheDocument();
		const rows = within(
			screen.getByRole("list", { name: "Suggested variables" }),
		).getAllByRole("listitem");
		expect(rows).toHaveLength(2);

		const url = within(rows[0] as HTMLElement);
		expect(url.getByText("DATABASE_URL")).toBeInTheDocument();
		expect(url.getByText(`${OPEN}postgres.DATABASE_URL}}`)).toBeInTheDocument();
		expect(
			url.getByText("postgres is not referenced here yet"),
		).toBeInTheDocument();
		expect(url.getByText("points at a stopped database")).toBeInTheDocument();
		expect(url.getByRole("button", { name: "Add" })).toBeInTheDocument();

		const password = within(rows[1] as HTMLElement);
		expect(password.getByText("secret")).toBeInTheDocument();
		expect(password.getByText("needs a value")).toBeInTheDocument();
		expect(password.getByText("—")).toBeInTheDocument();
		expect(password.getByRole("button", { name: "Set…" })).toBeInTheDocument();
	});

	it("uses the singular for one suggestion", async () => {
		stubApi(list([suggestion()]));
		await renderIt();
		expect(await screen.findByText("1 suggested variable")).toBeInTheDocument();
	});

	it("collapses and expands the list", async () => {
		const user = userEvent.setup();
		stubApi(list([suggestion()]));
		await renderIt();
		const toggle = await screen.findByRole("button", {
			name: /1 suggested variable/,
		});
		expect(toggle).toHaveAttribute("aria-expanded", "true");
		expect(toggle).toHaveTextContent("Hide");
		await user.click(toggle);
		expect(toggle).toHaveAttribute("aria-expanded", "false");
		expect(toggle).toHaveTextContent("Show");
		await user.click(toggle);
		expect(toggle).toHaveAttribute("aria-expanded", "true");
	});

	it("Add stages the variable as-is and confirms", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			...list([suggestion()]),
			"PUT /services/svc1/variables": { body: OK_LIST },
		});
		let drafts = 0;
		await renderIt(() => drafts++);
		await user.click(await screen.findByRole("button", { name: "Add" }));
		expect(await screen.findByText("Added DATABASE_URL")).toBeInTheDocument();
		expect(
			screen.getByText("Staged until the next deploy."),
		).toBeInTheDocument();
		const put = api.calls.find((call) => call.method === "PUT");
		expect(put?.body).toEqual({
			variables: [
				{ key: "DATABASE_URL", value: `${OPEN}postgres.DATABASE_URL}}` },
			],
		});
		expect(drafts).toBe(0);
	});

	it("Add writes a secret-looking suggestion as a secret", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			...list([suggestion({ key: "DB_URL", proposedSecret: true })]),
			"PUT /services/svc1/variables": { body: OK_LIST },
		});
		await renderIt();
		await user.click(await screen.findByRole("button", { name: "Add" }));
		await screen.findByText("Added DB_URL");
		expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
			variables: [
				{
					key: "DB_URL",
					value: `${OPEN}postgres.DATABASE_URL}}`,
					isSecret: true,
				},
			],
		});
	});

	it("Set… hands a key that needs a value to the caller instead of writing", async () => {
		const user = userEvent.setup();
		const api = stubApi(
			list([
				suggestion({
					key: "DB_PASSWORD",
					value: "",
					needsInput: true,
					proposedSecret: true,
				}),
			]),
		);
		const drafts: SuggestionDraft[] = [];
		await renderIt((draft) => drafts.push(draft));
		await user.click(await screen.findByRole("button", { name: "Set…" }));
		expect(drafts).toEqual([{ key: "DB_PASSWORD", value: "", isSecret: true }]);
		expect(api.calls.some((call) => call.method === "PUT")).toBe(false);
	});

	it("keeps the list when staging fails and does not confirm", async () => {
		const user = userEvent.setup();
		stubApi({
			...list([suggestion()]),
			"PUT /services/svc1/variables": problem(500, {
				code: "internal_error",
				title: "Boom",
				detail: "write failed",
			}),
		});
		await renderIt();
		await user.click(await screen.findByRole("button", { name: "Add" }));
		await waitFor(() =>
			expect(screen.getByRole("button", { name: "Add" })).toBeEnabled(),
		);
		expect(screen.queryByText("Added DATABASE_URL")).toBeNull();
		expect(screen.getByText("DATABASE_URL")).toBeInTheDocument();
	});
});
