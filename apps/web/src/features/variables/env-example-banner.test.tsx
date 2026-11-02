import { describe, expect, it } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
	SuggestedVariable,
	SuggestedVariableList,
} from "#/api/gen/types.gen";
import type { Service } from "#/api/models";
import { stubApi } from "#/test/api";
import { make, type Overrides, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { EnvExampleBanner } from "./env-example-banner";

const OPEN = "$" + "{{";

function suggestion(
	over: Overrides<SuggestedVariable> = {},
): SuggestedVariable {
	return make("SuggestedVariable", {
		key: "LOG_LEVEL",
		value: "info",
		needsInput: false,
		proposedSecret: false,
		reason: "from .env.example",
		source: "env-example",
		file: ".env.example",
		warnings: null,
		...over,
	});
}

const GIT = { id: "svc1", sourceType: "git" } as Service;
const IMAGE = { id: "svc1", sourceType: "image" } as Service;

const EXAMPLE_ITEMS = [
	suggestion({ key: "LOG_LEVEL", value: "info" }),
	suggestion({
		key: "API_TOKEN",
		value: "",
		needsInput: true,
		proposedSecret: true,
	}),
	suggestion({
		key: "SESSION_SECRET",
		value: `${OPEN}secret(32)}}`,
		proposedSecret: true,
	}),
];

const suggestions = (
	items: SuggestedVariable[],
	extra: Overrides<SuggestedVariableList> = {},
) => ({
	"GET /services/svc1/suggested-variables": {
		body: make("SuggestedVariableList", {
			items,
			branch: "main",
			commit: "3f2a9c1d77aa",
			...extra,
		}),
	},
});

const OK_LIST = make("VariableList");

describe("EnvExampleBanner", () => {
	it("renders nothing and asks nothing for a service that is not built from git", async () => {
		const api = stubApi({});
		const { container } = await renderWithProviders(
			<EnvExampleBanner service={IMAGE} />,
		);
		expect(
			container.querySelector('[data-slot="env-example-banner"]'),
		).toBeNull();
		expect(api.calls).toEqual([]);
		expect(api.unmatched).toEqual([]);
	});

	it("renders nothing when only database suggestions exist", async () => {
		const api = stubApi(
			suggestions([suggestion({ key: "DATABASE_URL", source: "database" })]),
		);
		const { container, queryClient } = await renderWithProviders(
			<EnvExampleBanner service={GIT} />,
		);
		await waitFor(() => expect(api.calls).toHaveLength(1));
		await waitFor(() => expect(queryClient.isFetching()).toBe(0));
		expect(
			container.querySelector('[data-slot="env-example-banner"]'),
		).toBeNull();
	});

	it("announces how many keys are unset, from which file and commit", async () => {
		stubApi(suggestions(EXAMPLE_ITEMS));
		await renderWithProviders(<EnvExampleBanner service={GIT} />);
		expect(
			await screen.findByText("3 keys from .env.example aren't set"),
		).toBeInTheDocument();
		expect(
			screen.getByText(/Read from your repository \(main · 3f2a9c1\)\./),
		).toBeInTheDocument();
	});

	it("names the one file the keys come from, and uses the singular", async () => {
		stubApi(
			suggestions([suggestion({ file: ".env.sample" })], {
				branch: "",
				commit: "",
			}),
		);
		await renderWithProviders(<EnvExampleBanner service={GIT} />);
		expect(
			await screen.findByText("1 key from .env.sample isn't set"),
		).toBeInTheDocument();
		// No branch or commit: no origin in parentheses.
		expect(
			screen.getByText(/^Read from your repository\. Review/),
		).toBeInTheDocument();
	});

	it("Review keys opens the sheet with every key checked and the counts", async () => {
		const user = userEvent.setup();
		stubApi(suggestions(EXAMPLE_ITEMS));
		await renderWithProviders(<EnvExampleBanner service={GIT} />);
		await user.click(
			await screen.findByRole("button", { name: "Review keys" }),
		);
		expect(
			await screen.findByText("Keys from .env.example"),
		).toBeInTheDocument();
		expect(
			screen.getByText("3 of 3 selected · 1 without a value"),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Add 3 variables" }),
		).toBeEnabled();
		// The key that needs a value is focused first.
		expect(screen.getByLabelText("Value for API_TOKEN")).toHaveFocus();
	});

	it("stages the checked keys with the typed value and closes", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			...suggestions(EXAMPLE_ITEMS),
			"PUT /services/svc1/variables": { body: OK_LIST },
		});
		await renderWithProviders(<EnvExampleBanner service={GIT} />);
		await user.click(
			await screen.findByRole("button", { name: "Review keys" }),
		);
		await user.type(
			await screen.findByLabelText("Value for API_TOKEN"),
			"tok-1",
		);
		await user.click(screen.getByRole("checkbox", { name: "Add LOG_LEVEL" }));
		expect(screen.getByText("2 of 3 selected")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Add 2 variables" }));

		expect(await screen.findByText("Added 2 variables")).toBeInTheDocument();
		expect(
			screen.getByText("Staged until the next deploy."),
		).toBeInTheDocument();
		const put = api.calls.find((call) => call.method === "PUT");
		expect(put?.body).toEqual({
			variables: [
				{
					key: "API_TOKEN",
					value: "tok-1",
					source: "suggested",
					isSecret: true,
				},
				{
					key: "SESSION_SECRET",
					value: `${OPEN}secret(32)}}`,
					source: "suggested",
					isSecret: true,
				},
			],
		});
		await waitFor(() =>
			expect(screen.queryByText("2 of 3 selected")).toBeNull(),
		);
	});

	it("tells how many staged keys still have no value", async () => {
		const user = userEvent.setup();
		stubApi({
			...suggestions([
				suggestion({ key: "A", value: "", needsInput: true }),
				suggestion({ key: "B", value: "", needsInput: true }),
			]),
			"PUT /services/svc1/variables": { body: OK_LIST },
		});
		await renderWithProviders(<EnvExampleBanner service={GIT} />);
		await user.click(
			await screen.findByRole("button", { name: "Review keys" }),
		);
		await user.click(
			await screen.findByRole("button", { name: "Add 2 variables" }),
		);
		expect(await screen.findByText("Added 2 variables")).toBeInTheDocument();
		expect(
			screen.getByText("Staged until the next deploy. 2 still have no value."),
		).toBeInTheDocument();
	});

	it("disables Add when nothing is checked and Cancel closes without writing", async () => {
		const user = userEvent.setup();
		const api = stubApi(suggestions([suggestion()]));
		await renderWithProviders(<EnvExampleBanner service={GIT} />);
		await user.click(
			await screen.findByRole("button", { name: "Review keys" }),
		);
		await user.click(
			await screen.findByRole("checkbox", { name: "Add LOG_LEVEL" }),
		);
		expect(
			screen.getByRole("button", { name: "Add 0 variables" }),
		).toBeDisabled();
		expect(screen.getByText("0 of 1 selected")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByText("0 of 1 selected")).toBeNull(),
		);
		expect(api.calls.some((call) => call.method === "PUT")).toBe(false);
	});

	it("shows the failure and keeps the sheet open when staging fails", async () => {
		const user = userEvent.setup();
		stubApi({
			...suggestions([suggestion()]),
			"PUT /services/svc1/variables": problem(422, {
				code: "invalid",
				title: "Invalid",
				detail: "LOG_LEVEL is reserved",
			}),
		});
		await renderWithProviders(<EnvExampleBanner service={GIT} />);
		await user.click(
			await screen.findByRole("button", { name: "Review keys" }),
		);
		await user.click(
			await screen.findByRole("button", { name: "Add 1 variable" }),
		);
		const alert = await screen.findByRole("alert");
		expect(
			within(alert).getByText("Could not add the variables"),
		).toBeInTheDocument();
		expect(
			within(alert).getByText(/LOG_LEVEL is reserved/),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Add 1 variable" }),
		).toBeEnabled();
	});
});

describe("EnvExampleBanner: Read again", () => {
	const scan = (status: string, message: string) => ({
		body: {
			status,
			message,
			method: "provider",
			files: [],
			items: [],
			skipped: [],
		},
	});

	it("rescans the repository and refetches the suggestions", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			...suggestions(EXAMPLE_ITEMS),
			"POST /services/svc1/env-template-scan": scan("found", ""),
		});
		await renderWithProviders(<EnvExampleBanner service={GIT} />);
		await user.click(await screen.findByRole("button", { name: "Read again" }));
		await waitFor(() =>
			expect(
				api.calls.filter((call) => call.path.endsWith("/suggested-variables")),
			).toHaveLength(2),
		);
		expect(
			api.calls.find((call) => call.path === "/services/svc1/env-template-scan")
				?.method,
		).toBe("POST");
		expect(screen.queryByText("The repository could not be read")).toBeNull();
		expect(screen.queryByText("No example file found")).toBeNull();
	});

	it("warns when the repository could not be read", async () => {
		const user = userEvent.setup();
		stubApi({
			...suggestions(EXAMPLE_ITEMS),
			"POST /services/svc1/env-template-scan": scan(
				"unavailable",
				"token expired",
			),
		});
		await renderWithProviders(<EnvExampleBanner service={GIT} />);
		await user.click(await screen.findByRole("button", { name: "Read again" }));
		expect(
			await screen.findByText("The repository could not be read"),
		).toBeInTheDocument();
		expect(screen.getByText("token expired")).toBeInTheDocument();
	});

	it("says when the repository has no example file", async () => {
		const user = userEvent.setup();
		stubApi({
			...suggestions(EXAMPLE_ITEMS),
			"POST /services/svc1/env-template-scan": scan(
				"none",
				"no .env.example in main",
			),
		});
		await renderWithProviders(<EnvExampleBanner service={GIT} />);
		await user.click(await screen.findByRole("button", { name: "Read again" }));
		expect(
			await screen.findByText("No example file found"),
		).toBeInTheDocument();
		expect(screen.getByText("no .env.example in main")).toBeInTheDocument();
	});

	it("reports a failed request", async () => {
		const user = userEvent.setup();
		stubApi({
			...suggestions(EXAMPLE_ITEMS),
			"POST /services/svc1/env-template-scan": {
				status: 502,
				body: {
					status: 502,
					code: "bad_gateway",
					title: "Bad gateway",
					detail: "git provider unreachable",
				},
			},
		});
		await renderWithProviders(<EnvExampleBanner service={GIT} />);
		await user.click(await screen.findByRole("button", { name: "Read again" }));
		expect(
			await screen.findByText("Could not read the repository"),
		).toBeInTheDocument();
		expect(screen.getByText(/git provider unreachable/)).toBeInTheDocument();
	});
});
