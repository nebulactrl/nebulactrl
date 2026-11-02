import { describe, expect, test } from "bun:test";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { EnvTable } from "./env-table";
import { ChoicesHost, envRow, service } from "./test-fixtures";

function table(
	svc = service({
		key: "api",
		env: [
			envRow({ key: "LOG_LEVEL", value: "info" }),
			envRow({ key: "DB_PASSWORD", value: "hunter2", secret: true }),
			envRow({ key: "TOKEN", value: "abc", secretGuess: true }),
			envRow({ key: "EMPTY", value: "" }),
		],
	}),
	initial = {},
) {
	const seen = { choices: initial as Record<string, unknown> };
	const view = render(
		<ChoicesHost initial={initial} seen={seen as never}>
			{(choices, onChoices) => (
				<EnvTable service={svc} choices={choices} onChoices={onChoices} />
			)}
		</ChoicesHost>,
	);
	return { ...view, seen };
}

async function open(user: ReturnType<typeof userEvent.setup>) {
	await user.click(
		screen.getByRole("button", { name: /Environment variables/ }),
	);
}

describe("EnvTable", () => {
	test("renders nothing for a service without variables", () => {
		const { container } = table(service({ key: "api" }));
		expect(container).toBeEmptyDOMElement();
	});

	test("is collapsed with a count until opened", async () => {
		const user = userEvent.setup();
		table();
		const toggle = screen.getByRole("button", {
			name: "Environment variables (4)",
		});
		expect(toggle).toHaveAttribute("aria-expanded", "false");
		expect(screen.queryByText("LOG_LEVEL")).not.toBeInTheDocument();
		await open(user);
		expect(screen.getByText("LOG_LEVEL")).toBeInTheDocument();
	});

	test("shows plain values, hides secrets and labels empty values", async () => {
		const user = userEvent.setup();
		table();
		await open(user);
		expect(screen.getAllByText("info").length).toBeGreaterThan(0);
		expect(screen.queryByText("hunter2")).not.toBeInTheDocument();
		expect(screen.getAllByText(/\(secret, hidden\)/)).toHaveLength(1);
		expect(screen.getAllByText("empty").length).toBeGreaterThan(0);
	});

	test("marks a guessed secret flag until the reader sets it", async () => {
		const user = userEvent.setup();
		table();
		await open(user);
		expect(screen.getAllByText("guessed")).toHaveLength(1);
		const token = screen.getByRole("switch", { name: "Secret: TOKEN" });
		expect(token).not.toBeChecked();
		await user.click(token);
		expect(screen.getByRole("switch", { name: "Secret: TOKEN" })).toBeChecked();
		expect(screen.queryByText("guessed")).not.toBeInTheDocument();
	});

	test("the secret and skip switches write the choice for that variable", async () => {
		const user = userEvent.setup();
		const { seen } = table();
		await open(user);

		await user.click(screen.getByRole("switch", { name: "Secret: LOG_LEVEL" }));
		expect(seen.choices).toEqual({
			services: { api: { env: { LOG_LEVEL: { secret: true } } } },
		});

		await user.click(screen.getByRole("switch", { name: "Skip: EMPTY" }));
		expect(seen.choices).toEqual({
			services: {
				api: { env: { LOG_LEVEL: { secret: true }, EMPTY: { skip: true } } },
			},
		});
		expect(screen.getByRole("switch", { name: "Skip: EMPTY" })).toBeChecked();

		// Turning a skip off drops the choice, so the plan's default applies again.
		await user.click(screen.getByRole("switch", { name: "Skip: EMPTY" }));
		expect(seen.choices).toEqual({
			services: { api: { env: { LOG_LEVEL: { secret: true } } } },
		});
	});

	test("a variable the plan skipped cannot be switched back on", async () => {
		const user = userEvent.setup();
		const { seen } = table(
			service({
				key: "api",
				env: [
					envRow({ key: "DOCKER_HOST", skipped: true, note: "Not passed." }),
				],
			}),
		);
		await open(user);
		const skip = screen.getByRole("switch", { name: "Skip: DOCKER_HOST" });
		expect(skip).toBeChecked();
		expect(skip).toHaveAttribute("data-disabled");
		expect(screen.getByText("Not passed.")).toBeInTheDocument();
		await user.click(skip);
		expect(seen.choices).toEqual({});
	});

	test("a rewritten reference is badged and can keep its original host names", async () => {
		const user = userEvent.setup();
		const { seen } = table(
			service({
				key: "api",
				env: [
					envRow({
						key: "DATABASE_URL",
						value: "postgres://db",
						rewritten: true,
					}),
					envRow({ key: "PLAIN", value: "x" }),
				],
			}),
		);
		await open(user);
		expect(screen.getAllByText("rewritten")).toHaveLength(1);
		// Only the rewritten row offers the switch.
		expect(
			screen.getAllByRole("checkbox", { name: "Keep original host names" }),
		).toHaveLength(1);

		await user.click(
			screen.getByRole("checkbox", { name: "Keep original host names" }),
		);
		expect(seen.choices).toEqual({
			services: { api: { env: { DATABASE_URL: { rewrite: false } } } },
		});
		expect(
			screen.getByRole("checkbox", { name: "Keep original host names" }),
		).toBeChecked();

		await user.click(
			screen.getByRole("checkbox", { name: "Keep original host names" }),
		);
		expect(seen.choices).toEqual({});
	});

	test("a row the reader kept hosts for still offers the switch after the plan stops rewriting it", async () => {
		const user = userEvent.setup();
		table(
			service({
				key: "api",
				env: [envRow({ key: "DATABASE_URL", value: "x", rewritten: false })],
			}),
			{ services: { api: { env: { DATABASE_URL: { rewrite: false } } } } },
		);
		await open(user);
		expect(
			screen.getByRole("checkbox", { name: "Keep original host names" }),
		).toBeChecked();
		expect(screen.queryByText("rewritten")).not.toBeInTheDocument();
	});

	test("row notes are shown under the variable", async () => {
		const user = userEvent.setup();
		table(
			service({
				key: "api",
				env: [envRow({ key: "A", value: "1", note: "Came from .env." })],
			}),
		);
		await open(user);
		const row = screen.getByText("Came from .env.")
			.parentElement as HTMLElement;
		expect(within(row).getByText("A")).toBeInTheDocument();
	});
});
