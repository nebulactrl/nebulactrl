import { describe, expect, it } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type { ServiceView } from "#/api/gen/types.gen";
import type { Variable } from "#/api/models";
import { type ApiStub, stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { makeVariable } from "./testing";
import { VariableDialog, type VariableDialogProps } from "./variable-dialog";

// Literal `${NAME}` placeholder: the escaped `$` keeps it from being a template placeholder.
const ref = (name: string) => `\${${name}}`;

const PUT = "PUT /services/:id/variables";
const VALIDATE = "POST /services/:id/variables/validate";
const emptyList = make("VariableList");

const serviceWire = (
	id: string,
	slug: string,
	kind: ServiceView["kind"] = "app",
) =>
	make("ServiceView", {
		id,
		environmentId: "env-1",
		projectId: "p1",
		name: slug,
		slug,
		kind,
		sourceType: "image",
	});

/** Everything the Name field and the reference list may read, empty unless overridden. */
function routes(overrides: Parameters<typeof stubApi>[0] = {}) {
	return {
		"GET /environments/:id/services": {
			body: make("PageServiceView", { items: [serviceWire("svc-1", "api")] }),
		},
		"GET /environments/:id/shared-variables": {
			body: make("SharedVariableList"),
		},
		"GET /team/shared-variables": { body: make("SharedVariableList") },
		"GET /secret-managers": { body: make("ListOutputBody") },
		"GET /services/:id/variables": { body: emptyList },
		"GET /services/:id/suggested-variables": {
			body: make("SuggestedVariableList"),
		},
		[PUT]: { body: emptyList },
		[VALIDATE]: {
			body: make("ValidateVariableResult", {
				ok: true,
				containsSecret: false,
				generators: [],
				references: [],
				warnings: [],
			}),
		},
		...overrides,
	};
}

interface Opts {
	target?: Variable | "new" | null;
	existingKeys?: string[];
	canBuild?: boolean;
	draft?: VariableDialogProps["draft"];
}

async function renderDialog(opts: Opts = {}) {
	let closed = 0;
	const props: VariableDialogProps = {
		serviceId: "svc-1",
		environmentId: "env-1",
		target: opts.target === undefined ? "new" : opts.target,
		existingKeys: opts.existingKeys ?? [],
		canBuild: opts.canBuild ?? false,
		draft: opts.draft ?? null,
		onClose: () => {
			closed += 1;
		},
	};
	await renderWithProviders(<VariableDialog {...props} />);
	return { closed: () => closed };
}

const nameField = () => screen.getByLabelText("Name");
const valueField = () => screen.getByLabelText("Value");
const secretBox = () => screen.getByRole("checkbox", { name: "Secret" });
const submitButton = (name: RegExp | string) =>
	screen.getByRole("button", { name });
/** An option by its name; the typed text is split into spans, so match the text. */
async function option(name: string) {
	return waitFor(() => {
		const found = screen
			.getAllByRole("option")
			.find((o) => o.textContent?.startsWith(name));
		if (!found) throw new Error(`no option ${name}`);
		return found;
	});
}
const putCalls = (api: ApiStub) =>
	api.calls.filter((call) => call.method === "PUT");

describe("VariableDialog: adding", () => {
	it("renders nothing while there is no target", async () => {
		stubApi(routes());
		await renderDialog({ target: null });
		expect(screen.queryByRole("dialog")).toBeNull();
	});

	it("is titled New variable and cannot be added without a name", async () => {
		stubApi(routes());
		await renderDialog();
		expect(await screen.findByText("New variable")).toBeInTheDocument();
		expect(submitButton("Add variable")).toBeDisabled();
		const user = userEvent.setup();
		await user.type(nameField(), "  ");
		expect(submitButton("Add variable")).toBeDisabled();
		await user.type(nameField(), "LEVEL");
		expect(submitButton("Add variable")).toBeEnabled();
	});

	it("stages the variable, toasts and closes", async () => {
		const user = userEvent.setup();
		const api = stubApi(routes());
		const dialog = await renderDialog();
		await user.type(await screen.findByLabelText("Name"), "LEVEL");
		await user.type(valueField(), "info");
		await user.type(screen.getByLabelText("Note"), "  log verbosity ");
		await user.click(submitButton("Add variable"));

		expect(await screen.findByText("Added LEVEL")).toBeInTheDocument();
		expect(
			screen.getByText("Staged until the next deploy."),
		).toBeInTheDocument();
		await waitFor(() => expect(dialog.closed()).toBe(1));
		const put = putCalls(api);
		expect(put).toHaveLength(1);
		expect(put[0]?.path).toBe("/services/svc-1/variables");
		// A plain variable sends no isSecret and no rotation reminder.
		expect(put[0]?.body).toEqual({
			variables: [
				{
					key: "LEVEL",
					scope: "runtime",
					comment: "log verbosity",
					value: "info",
				},
			],
		});
	});

	it("trims the name and sends an empty value as such", async () => {
		const user = userEvent.setup();
		const api = stubApi(routes());
		await renderDialog();
		await user.type(await screen.findByLabelText("Name"), "  EMPTY  ");
		await user.click(submitButton("Add variable"));
		await screen.findByText("Added EMPTY");
		expect(putCalls(api)[0]?.body).toEqual({
			variables: [{ key: "EMPTY", scope: "runtime", comment: "", value: "" }],
		});
	});

	it("submits with Enter in the name field", async () => {
		const user = userEvent.setup();
		const api = stubApi(routes());
		await renderDialog();
		await user.type(await screen.findByLabelText("Name"), "K{Enter}");
		await screen.findByText("Added K");
		expect(putCalls(api)).toHaveLength(1);
	});

	it("sends a secret with its rotation reminder", async () => {
		const user = userEvent.setup();
		const api = stubApi(routes());
		await renderDialog();
		await user.type(await screen.findByLabelText("Name"), "TOKEN");
		expect(screen.queryByLabelText("Rotation reminder (days)")).toBeNull();
		await user.click(secretBox());
		expect(secretBox()).toBeChecked();
		await user.type(screen.getByLabelText("Rotation reminder (days)"), "30");
		await user.type(valueField(), "s3cret");
		await user.click(submitButton("Add variable"));
		await screen.findByText("Added TOKEN");
		expect(putCalls(api)[0]?.body).toEqual({
			variables: [
				{
					key: "TOKEN",
					isSecret: true,
					scope: "runtime",
					comment: "",
					rotateEveryDays: 30,
					value: "s3cret",
				},
			],
		});
	});

	it("rejects a rotation reminder that is not a whole number", async () => {
		const user = userEvent.setup();
		const api = stubApi(routes());
		await renderDialog();
		await user.type(await screen.findByLabelText("Name"), "TOKEN");
		await user.click(secretBox());
		const days = screen.getByLabelText("Rotation reminder (days)");
		await user.type(days, "soon");
		expect(days).toHaveAttribute("aria-invalid", "true");
		expect(submitButton("Add variable")).toBeDisabled();
		expect(putCalls(api)).toEqual([]);
		await user.clear(days);
		await user.type(days, "7");
		expect(days).toHaveAttribute("aria-invalid", "false");
		expect(submitButton("Add variable")).toBeEnabled();
	});

	it("explains an invalid name once the field is left, not while typing", async () => {
		const user = userEvent.setup();
		stubApi(routes());
		await renderDialog();
		await user.type(await screen.findByLabelText("Name"), "1bad-name");
		expect(
			screen.queryByText(/Use letters, digits and underscores/),
		).toBeNull();
		await user.tab();
		expect(
			await screen.findByText(
				"Use letters, digits and underscores, not starting with a digit.",
			),
		).toBeInTheDocument();
		expect(nameField()).toHaveAttribute("aria-invalid", "true");
		// Typing again hides it until the next blur.
		await user.type(nameField(), "x");
		expect(screen.queryByText(/Use letters, digits/)).toBeNull();
	});

	it("refuses the platform's names", async () => {
		const user = userEvent.setup();
		const api = stubApi(routes());
		await renderDialog();
		await user.type(await screen.findByLabelText("Name"), "NEBULA_TOKEN");
		await user.click(submitButton("Add variable"));
		expect(
			await screen.findByText(
				"PORT and names starting with NEBULA_ are provided by the platform.",
			),
		).toBeInTheDocument();
		expect(putCalls(api)).toEqual([]);
		await user.clear(nameField());
		await user.type(nameField(), "PORT");
		await user.click(submitButton("Add variable"));
		expect(screen.getByText(/provided by the platform/)).toBeInTheDocument();
		expect(putCalls(api)).toEqual([]);
	});

	it("refuses a name that already exists and points to editing it", async () => {
		const user = userEvent.setup();
		const api = stubApi(routes());
		await renderDialog({ existingKeys: ["LEVEL"] });
		await user.type(await screen.findByLabelText("Name"), "LEVEL");
		await user.click(submitButton("Add variable"));
		expect(
			await screen.findByText("LEVEL already exists; edit it instead."),
		).toBeInTheDocument();
		expect(putCalls(api)).toEqual([]);
		expect(nameField()).toHaveFocus();
	});

	it("shows the server's reason and stays open when saving fails", async () => {
		const user = userEvent.setup();
		const api = stubApi(
			routes({
				[PUT]: problem(409, {
					code: "conflict",
					title: "Conflict",
					detail: "limit of 500 variables reached",
				}),
			}),
		);
		const dialog = await renderDialog();
		await user.type(await screen.findByLabelText("Name"), "K");
		await user.click(submitButton("Add variable"));
		expect(
			await screen.findByText("limit of 500 variables reached"),
		).toBeInTheDocument();
		expect(dialog.closed()).toBe(0);
		expect(screen.queryByText("Added K")).toBeNull();
		expect(putCalls(api)).toHaveLength(1);
		expect(submitButton("Add variable")).toBeEnabled();
	});

	it("names the services restaged by the save and toasts the server's warnings", async () => {
		const user = userEvent.setup();
		stubApi(
			routes({
				[PUT]: {
					body: make("VariableList", {
						restaged: [
							{ serviceId: "s2", slug: "web" },
							{ serviceId: "s3", slug: "worker" },
						],
						warnings: [
							{ code: "w", message: "LEVEL shadows a shared variable" },
						],
					}),
				},
			}),
		);
		await renderDialog();
		await user.type(await screen.findByLabelText("Name"), "LEVEL");
		await user.click(submitButton("Add variable"));
		expect(
			await screen.findByText(
				"Staged until the next deploy. Also restaged: web, worker.",
			),
		).toBeInTheDocument();
		expect(
			await screen.findByText("LEVEL shadows a shared variable"),
		).toBeInTheDocument();
	});

	it("Cancel closes without saving", async () => {
		const user = userEvent.setup();
		const api = stubApi(routes());
		const dialog = await renderDialog();
		await user.type(await screen.findByLabelText("Name"), "K");
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		expect(dialog.closed()).toBe(1);
		expect(putCalls(api)).toEqual([]);
	});

	it("prefills a suggestion that needs a value and keeps its secret flag", async () => {
		const user = userEvent.setup();
		const api = stubApi(routes());
		await renderDialog({
			draft: { key: "STRIPE_KEY", value: "", isSecret: true },
		});
		expect(await screen.findByLabelText("Name")).toHaveValue("STRIPE_KEY");
		expect(secretBox()).toBeChecked();
		await user.type(valueField(), "sk_live");
		await user.click(submitButton("Add variable"));
		await screen.findByText("Added STRIPE_KEY");
		expect(putCalls(api)[0]?.body).toEqual({
			variables: [
				{
					key: "STRIPE_KEY",
					isSecret: true,
					scope: "runtime",
					comment: "",
					value: "sk_live",
				},
			],
		});
	});
});

describe("VariableDialog: the value field", () => {
	it("Enter saves, Shift+Enter adds a line", async () => {
		const user = userEvent.setup();
		const api = stubApi(routes());
		await renderDialog();
		await user.type(await screen.findByLabelText("Name"), "NOTE");
		await user.type(valueField(), "one{Shift>}{Enter}{/Shift}two");
		expect(valueField()).toHaveValue("one\ntwo");
		expect(putCalls(api)).toEqual([]);
		await user.type(valueField(), "{Enter}");
		await screen.findByText("Added NOTE");
		expect(putCalls(api)[0]?.body).toEqual({
			variables: [
				{ key: "NOTE", scope: "runtime", comment: "", value: "one\ntwo" },
			],
		});
	});

	it("Ctrl+Enter inserts a line break at the caret instead of saving", async () => {
		const user = userEvent.setup();
		const api = stubApi(routes());
		await renderDialog();
		await user.type(await screen.findByLabelText("Name"), "NOTE");
		await user.type(valueField(), "ab");
		await user.type(valueField(), "{Control>}{Enter}{/Control}");
		expect(valueField()).toHaveValue("ab\n");
		expect(putCalls(api)).toEqual([]);
	});

	it("Generate inserts an expression and makes the variable a secret", async () => {
		const user = userEvent.setup();
		const api = stubApi(routes());
		await renderDialog();
		await user.type(await screen.findByLabelText("Name"), "SESSION");
		await user.click(screen.getByRole("button", { name: "Generate" }));
		await user.click(await screen.findByRole("button", { name: "Insert" }));
		expect(valueField()).toHaveValue(ref("{secret(32)}"));
		expect(secretBox()).toBeChecked();
		// A second insert appends after what is there.
		await user.click(screen.getByRole("button", { name: "Generate" }));
		await user.click(await screen.findByRole("button", { name: "Insert" }));
		expect(valueField()).toHaveValue(ref("{secret(32)}") + ref("{secret(32)}"));
		await user.click(submitButton("Add variable"));
		await screen.findByText("Added SESSION");
		expect(putCalls(api)[0]?.body).toMatchObject({
			variables: [
				{
					key: "SESSION",
					isSecret: true,
					value: ref("{secret(32)}") + ref("{secret(32)}"),
				},
			],
		});
	});

	it("a UUID from Generate does not turn the secret flag on", async () => {
		const user = userEvent.setup();
		stubApi(routes());
		await renderDialog();
		await screen.findByLabelText("Name");
		await user.click(screen.getByRole("button", { name: "Generate" }));
		await user.click(await screen.findByRole("button", { name: "UUID" }));
		await user.click(screen.getByRole("button", { name: "Insert" }));
		expect(valueField()).toHaveValue(ref("{uuid()}"));
		expect(secretBox()).not.toBeChecked();
	});

	it("shows the server's check of an expression and blocks saving a broken one", async () => {
		const user = userEvent.setup();
		const api = stubApi(
			routes({
				[VALIDATE]: {
					body: {
						ok: false,
						containsSecret: false,
						generators: [],
						references: [],
						warnings: [],
						error: { message: "unknown function nope", offset: 3 },
					},
				},
			}),
		);
		await renderDialog();
		await user.type(await screen.findByLabelText("Name"), "K");
		await user.click(valueField());
		await user.paste(ref("{nope()}"));
		expect(
			await screen.findByText("unknown function nope (character 4)"),
		).toBeInTheDocument();
		expect(submitButton("Add variable")).toBeDisabled();
		expect(putCalls(api)).toEqual([]);
		const check = api.calls.find((c) => c.path.endsWith("/validate"));
		expect(check?.body).toEqual({
			key: "K",
			value: ref("{nope()}"),
			isSecret: false,
		});
	});

	it("warns about references that do not resolve but still allows saving", async () => {
		const user = userEvent.setup();
		stubApi(
			routes({
				[VALIDATE]: {
					body: {
						ok: true,
						containsSecret: false,
						generators: [],
						warnings: [],
						references: [
							{ ns: "redis", key: "URL", resolved: false, secret: false },
						],
					},
				},
			}),
		);
		await renderDialog();
		await user.type(await screen.findByLabelText("Name"), "K");
		await user.click(valueField());
		await user.paste(ref("{redis.URL}"));
		expect(
			await screen.findByText(
				"redis.URL does not resolve yet. A deploy is blocked until it does.",
			),
		).toBeInTheDocument();
		expect(submitButton("Add variable")).toBeEnabled();
	});

	it("moves a reference typed into the name to the value and says why", async () => {
		const user = userEvent.setup();
		stubApi(routes());
		await renderDialog();
		await user.click(await screen.findByLabelText("Name"));
		await user.paste(ref("{postgres.HOST}"));
		expect(nameField()).toHaveValue("");
		expect(valueField()).toHaveValue(ref("{postgres.HOST}"));
		expect(
			screen.getByText(
				"References go in the value; the name is the variable your app reads.",
			),
		).toBeInTheDocument();
		// Typing a plain name clears the hint.
		await user.type(nameField(), "DB");
		expect(screen.queryByText(/References go in the value/)).toBeNull();
	});
});

describe("VariableDialog: name suggestions", () => {
	const sourced = () =>
		routes({
			"GET /environments/:id/services": {
				body: make("PageServiceView", {
					items: [
						serviceWire("svc-1", "api"),
						serviceWire("svc-db", "postgres", "database"),
					],
				}),
			},
			"GET /services/:id/variables": (request) =>
				Response.json(
					new URL(request.url).pathname.includes("svc-db")
						? make("VariableList", {
								exports: [
									{ key: "DATABASE_URL", description: "URL", secret: true },
								],
							})
						: emptyList,
				),
			"GET /services/:id/suggested-variables": {
				body: make("SuggestedVariableList", {
					items: [
						make("SuggestedVariable", {
							key: "STRIPE_KEY",
							value: "",
							needsInput: true,
							proposedSecret: true,
							source: "env-example",
						}),
						make("SuggestedVariable", {
							key: "LOG_FORMAT",
							value: "json",
							needsInput: false,
							proposedSecret: false,
							source: "env-example",
						}),
					],
				}),
			},
			"GET /environments/:id/shared-variables": {
				body: make("SharedVariableList", {
					items: [
						make("SharedVariableView", {
							key: "REGION",
							layer: "project",
							computedSecret: false,
						}),
					],
				}),
			},
		});

	it("picking another service's export fills the name and a reference to it", async () => {
		const user = userEvent.setup();
		const api = stubApi(sourced());
		await renderDialog();
		await user.click(await screen.findByLabelText("Name"));
		await user.type(nameField(), "DATABASE");
		await user.click(await option("DATABASE_URL"));
		expect(nameField()).toHaveValue("DATABASE_URL");
		expect(valueField()).toHaveValue(ref("{postgres.DATABASE_URL}"));
		expect(
			screen.getByText(
				"Filled from the postgres service. Edit it, or press Enter to add.",
			),
		).toBeInTheDocument();
		await user.click(submitButton("Add variable"));
		await screen.findByText("Added DATABASE_URL");
		expect(putCalls(api)[0]?.body).toMatchObject({
			variables: [
				{ key: "DATABASE_URL", value: ref("{postgres.DATABASE_URL}") },
			],
		});
	});

	it("picking a shared variable says where it was filled from", async () => {
		const user = userEvent.setup();
		stubApi(sourced());
		await renderDialog();
		await user.click(await screen.findByLabelText("Name"));
		await user.type(nameField(), "REGION");
		await user.click(await option("REGION"));
		expect(valueField()).toHaveValue(ref("{shared.REGION}"));
		expect(
			screen.getByText(
				"Filled from the project shared variables. Edit it, or press Enter to add.",
			),
		).toBeInTheDocument();
	});

	it("picking a .env.example key that needs input proposes a secret and leaves the value empty", async () => {
		const user = userEvent.setup();
		stubApi(sourced());
		await renderDialog();
		await user.click(await screen.findByLabelText("Name"));
		await user.type(nameField(), "STRIPE");
		await user.click(await option("STRIPE_KEY"));
		expect(nameField()).toHaveValue("STRIPE_KEY");
		expect(valueField()).toHaveValue("");
		expect(secretBox()).toBeChecked();
	});

	it("picking a .env.example key with a proposed value fills it and says so", async () => {
		const user = userEvent.setup();
		stubApi(sourced());
		await renderDialog();
		await user.click(await screen.findByLabelText("Name"));
		await user.type(nameField(), "LOG_F");
		await user.click(await option("LOG_FORMAT"));
		expect(valueField()).toHaveValue("json");
		expect(
			screen.getByText(
				"Proposed by .env.example. Edit it, or press Enter to add.",
			),
		).toBeInTheDocument();
		expect(secretBox()).not.toBeChecked();
	});

	it("the note about a filled value goes away once the value is edited", async () => {
		const user = userEvent.setup();
		stubApi(sourced());
		await renderDialog();
		await user.click(await screen.findByLabelText("Name"));
		await user.type(nameField(), "LOG_F");
		await user.click(await option("LOG_FORMAT"));
		await user.type(valueField(), "x");
		expect(screen.queryByText(/Proposed by \.env\.example/)).toBeNull();
	});
});

describe("VariableDialog: editing", () => {
	it("opens a plain variable with its name locked and its value filled in", async () => {
		const api = stubApi(routes());
		await renderDialog({
			target: makeVariable({
				key: "LEVEL",
				value: "info",
				comment: "verbosity",
			}),
		});
		expect(await screen.findByText("Edit LEVEL")).toBeInTheDocument();
		expect(nameField()).toHaveValue("LEVEL");
		expect(nameField()).toHaveAttribute("readonly");
		expect(nameField()).not.toHaveAttribute("role", "combobox");
		expect(valueField()).toHaveValue("info");
		expect(screen.getByLabelText("Note")).toHaveValue("verbosity");
		expect(submitButton("Save variable")).toBeEnabled();
		// An existing key is not "new": its name is never checked against the others, and
		// nothing is fetched to suggest names.
		expect(
			api.calls.filter((c) => c.path.includes("shared-variables")),
		).toEqual([]);
	});

	it("saves an edit with the changed value and the unchanged settings", async () => {
		const user = userEvent.setup();
		const api = stubApi(routes());
		const dialog = await renderDialog({
			target: makeVariable({ key: "LEVEL", value: "info", scope: "build" }),
			existingKeys: ["LEVEL"],
			canBuild: true,
		});
		const value = await screen.findByLabelText("Value");
		await user.clear(value);
		await user.type(value, "debug");
		await user.click(submitButton("Save variable"));
		expect(await screen.findByText("Updated LEVEL")).toBeInTheDocument();
		await waitFor(() => expect(dialog.closed()).toBe(1));
		expect(putCalls(api)[0]?.body).toEqual({
			variables: [
				{ key: "LEVEL", scope: "build", comment: "", value: "debug" },
			],
		});
	});

	it("keeps a stored secret when its value is left empty", async () => {
		const user = userEvent.setup();
		const api = stubApi(routes());
		await renderDialog({
			target: makeVariable({
				key: "TOKEN",
				isSecret: true,
				value: undefined,
				comment: "api",
			}),
		});
		expect(await screen.findByLabelText("Value")).toHaveValue("");
		expect(valueField()).toHaveAttribute(
			"placeholder",
			"Leave empty to keep the current secret",
		);
		expect(secretBox()).toBeChecked();
		// It cannot be made plain without a new value.
		expect(secretBox()).toHaveAttribute("aria-disabled", "true");
		await user.click(submitButton("Save variable"));
		await screen.findByText("Updated TOKEN");
		const body = putCalls(api)[0]?.body;
		expect(body).toEqual({
			variables: [
				{ key: "TOKEN", isSecret: true, scope: "runtime", comment: "api" },
			],
		});
	});

	it("says a generated secret keeps its generated value, and what it was generated as", async () => {
		stubApi(routes());
		await renderDialog({
			target: makeVariable({
				key: "SESSION",
				isSecret: true,
				value: undefined,
				source: "generated",
				generated: [
					{ fn: "secret", args: ["32"] },
					{ fn: "uuid", args: null },
				],
			}),
		});
		expect(await screen.findByLabelText("Value")).toHaveAttribute(
			"placeholder",
			"Leave empty to keep the generated value",
		);
		expect(
			screen.getByText(/Generated once as secret\(32\) \+ uuid\(\)\./),
		).toBeInTheDocument();
	});

	it("a secret gets a new value, and can be made plain with one", async () => {
		const user = userEvent.setup();
		const api = stubApi(routes());
		await renderDialog({
			target: makeVariable({ key: "TOKEN", isSecret: true, value: undefined }),
		});
		await user.type(await screen.findByLabelText("Value"), "visible");
		expect(secretBox()).not.toHaveAttribute("aria-disabled", "true");
		expect(
			screen.getByText(/To make it plain again, enter a new value\./),
		).toBeInTheDocument();
		await user.click(secretBox());
		await user.click(submitButton("Save variable"));
		await screen.findByText("Updated TOKEN");
		expect(putCalls(api)[0]?.body).toEqual({
			variables: [
				{
					key: "TOKEN",
					isSecret: false,
					scope: "runtime",
					comment: "",
					value: "visible",
				},
			],
		});
	});

	it("shows the stored rotation reminder, flags it when due, and clears it with 0", async () => {
		const user = userEvent.setup();
		const api = stubApi(routes());
		await renderDialog({
			target: makeVariable({
				key: "TOKEN",
				isSecret: true,
				value: undefined,
				rotateEveryDays: 90,
				rotationDue: true,
			}),
		});
		const days = await screen.findByLabelText("Rotation reminder (days)");
		expect(days).toHaveValue("90");
		expect(screen.getByText("due")).toBeInTheDocument();
		await user.clear(days);
		await user.click(submitButton("Save variable"));
		await screen.findByText("Updated TOKEN");
		expect(putCalls(api)[0]?.body).toMatchObject({
			variables: [{ key: "TOKEN", rotateEveryDays: 0 }],
		});
	});

	it("does not flag a reminder that is not due", async () => {
		stubApi(routes());
		await renderDialog({
			target: makeVariable({
				key: "TOKEN",
				isSecret: true,
				value: undefined,
				rotateEveryDays: 90,
				rotationDue: false,
			}),
		});
		await screen.findByLabelText("Rotation reminder (days)");
		expect(screen.queryByText("due")).toBeNull();
	});

	it("saves a changed rotation reminder as a number", async () => {
		const user = userEvent.setup();
		const api = stubApi(routes());
		await renderDialog({
			target: makeVariable({
				key: "TOKEN",
				isSecret: true,
				value: undefined,
				rotateEveryDays: 90,
			}),
		});
		const days = await screen.findByLabelText("Rotation reminder (days)");
		await user.clear(days);
		await user.type(days, "14");
		await user.click(submitButton("Save variable"));
		await screen.findByText("Updated TOKEN");
		expect(putCalls(api)[0]?.body).toMatchObject({
			variables: [{ key: "TOKEN", rotateEveryDays: 14 }],
		});
	});
});

describe("VariableDialog: where it is available", () => {
	it("offers no scope choice for a service that is not built here", async () => {
		stubApi(routes());
		await renderDialog({ canBuild: false });
		await screen.findByLabelText("Name");
		expect(screen.queryByRole("button", { name: "Build" })).toBeNull();
		expect(screen.queryByText("Available at")).toBeNull();
	});

	it("shows how a plain build variable is read in a Dockerfile and sends the scope", async () => {
		const user = userEvent.setup();
		const api = stubApi(routes());
		await renderDialog({ canBuild: true });
		await user.type(await screen.findByLabelText("Name"), "API_URL");
		expect(screen.queryByText("ARG API_URL")).toBeNull();
		await user.click(screen.getByRole("button", { name: "Build" }));
		expect(screen.getByText("ARG API_URL")).toBeInTheDocument();
		await user.click(submitButton("Add variable"));
		await screen.findByText("Added API_URL");
		expect(putCalls(api)[0]?.body).toMatchObject({
			variables: [{ key: "API_URL", scope: "build" }],
		});
	});

	it("shows the BuildKit secret mount for a secret build variable, never an ARG", async () => {
		const user = userEvent.setup();
		stubApi(routes());
		await renderDialog({ canBuild: true });
		await user.type(await screen.findByLabelText("Name"), "NPM_TOKEN");
		await user.click(secretBox());
		await user.click(screen.getByRole("button", { name: "Both" }));
		expect(
			screen.getByText(
				"RUN --mount=type=secret,id=NPM_TOKEN,env=NPM_TOKEN <your command>",
			),
		).toBeInTheDocument();
		expect(screen.queryByText("ARG NPM_TOKEN")).toBeNull();
		expect(
			screen.getByText(/A secret is never offered as a build argument/),
		).toBeInTheDocument();
	});

	it("uses a placeholder name in the snippet until a name is typed", async () => {
		const user = userEvent.setup();
		stubApi(routes());
		await renderDialog({ canBuild: true });
		await screen.findByLabelText("Name");
		await user.click(screen.getByRole("button", { name: "Build" }));
		expect(screen.getByText("ARG KEY")).toBeInTheDocument();
	});
});

function Reopen() {
	const [target, setTarget] = useState<Variable | "new" | null>("new");
	return (
		<>
			<button type="button" onClick={() => setTarget("new")}>
				reopen
			</button>
			<VariableDialog
				serviceId="svc-1"
				environmentId="env-1"
				target={target}
				existingKeys={[]}
				canBuild={false}
				onClose={() => setTarget(null)}
			/>
		</>
	);
}

describe("VariableDialog: closing", () => {
	it("closes on Escape when the name list is not open", async () => {
		const user = userEvent.setup();
		stubApi(routes());
		const dialog = await renderDialog({
			target: makeVariable({ key: "LEVEL", value: "info" }),
		});
		await screen.findByText("Edit LEVEL");
		await user.keyboard("{Escape}");
		await waitFor(() => expect(dialog.closed()).toBe(1));
	});

	it("Escape closes the name list first and leaves the dialog open", async () => {
		const user = userEvent.setup();
		stubApi(routes());
		const dialog = await renderDialog();
		await user.click(await screen.findByLabelText("Name"));
		await screen.findByRole("listbox");
		await user.keyboard("{Escape}");
		expect(screen.queryByRole("listbox")).toBeNull();
		expect(dialog.closed()).toBe(0);
		expect(screen.getByText("New variable")).toBeInTheDocument();
	});

	it("starts every opening from its own state", async () => {
		const user = userEvent.setup();
		stubApi(routes());
		await renderWithProviders(<Reopen />);
		await user.type(await screen.findByLabelText("Name"), "STALE");
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		await user.click(screen.getByRole("button", { name: "reopen" }));
		expect(await screen.findByLabelText("Name")).toHaveValue("");
	});
});
