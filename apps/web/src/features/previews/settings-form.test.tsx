import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type { Environment, PreviewSettings } from "#/api/gen/types.gen";
import { type ApiStub, stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { PreviewSettingsForm } from "./settings-form";
import {
	wireEnv,
	wirePreviewEnv,
	wireService,
	wireSettings,
} from "./test-data";

const STAGING = wireEnv({
	id: "env-staging",
	name: "Staging",
	slug: "staging",
	isProduction: false,
});
const ENVS = [wireEnv(), STAGING, wirePreviewEnv()];

function stubServices() {
	return {
		"GET /environments/:id/services": (request: Request) => {
			const id = new URL(request.url).pathname.split("/")[4];
			return Response.json({
				items:
					id === "env-prod"
						? [
								wireService("web"),
								wireService("worker", { kind: "worker" }),
								wireService("gateway", { kind: "route" }),
							]
						: id === "env-staging"
							? [wireService("api")]
							: [],
			});
		},
	};
}

async function renderForm(
	settings: Partial<PreviewSettings> = {},
	environments: readonly Environment[] = ENVS,
	extra: Parameters<typeof stubApi>[0] = {},
) {
	const api = stubApi({ ...stubServices(), ...extra });
	await renderWithProviders(
		<PreviewSettingsForm
			projectId="proj-1"
			settings={wireSettings(settings)}
			environments={environments}
		/>,
	);
	return api;
}

const saveButton = () => screen.getByRole("button", { name: "Save" });
const discardButton = () =>
	screen.getByRole("button", { name: "Discard changes" });
const putCalls = (api: ApiStub) => api.calls.filter((c) => c.method === "PUT");

async function pick(
	user: ReturnType<typeof userEvent.setup>,
	trigger: HTMLElement,
	option: string,
) {
	await user.click(trigger);
	await user.click(await screen.findByRole("option", { name: option }));
}

describe("PreviewSettingsForm: reading", () => {
	test("shows the saved settings and nothing to save", async () => {
		await renderForm({
			enabled: true,
			maxConcurrent: 8,
			idleDays: 14,
			forkPrs: false,
		});

		expect(screen.getByRole("switch", { name: "Previews" })).toBeChecked();
		expect(
			screen.getByRole("switch", { name: "Previews for forks" }),
		).not.toBeChecked();
		expect(screen.getByLabelText("Running previews")).toHaveValue("8");
		expect(screen.getByLabelText("Delete after idle days")).toHaveValue("14");
		expect(
			screen.getByRole("combobox", { name: "Base environment" }),
		).toHaveTextContent("Production");
		expect(saveButton()).toBeDisabled();
		expect(discardButton()).toBeDisabled();
		expect(
			screen.queryByText("Fork previews never receive secrets"),
		).not.toBeInTheDocument();
	});

	test("the base environment choice leaves out preview environments", async () => {
		const user = userEvent.setup();
		await renderForm();

		await user.click(
			screen.getByRole("combobox", { name: "Base environment" }),
		);

		const options = await screen.findAllByRole("option");
		expect(options.map((o) => o.textContent)).toEqual([
			"Production",
			"Staging",
		]);
	});

	test("lists the base environment's services, without routes", async () => {
		await renderForm();

		expect(await screen.findByRole("checkbox", { name: /Web/ })).toBeChecked();
		expect(screen.getByRole("checkbox", { name: /Worker/ })).toBeChecked();
		expect(
			screen.queryByRole("checkbox", { name: /Gateway/ }),
		).not.toBeInTheDocument();
	});

	test("services already excluded show unchecked", async () => {
		await renderForm({ excludedServices: ["worker"] });

		expect(await screen.findByRole("checkbox", { name: /Web/ })).toBeChecked();
		expect(screen.getByRole("checkbox", { name: /Worker/ })).not.toBeChecked();
	});

	test("with no base environment it asks to choose one first", async () => {
		const api = await renderForm({ enabled: false, baseEnvironmentId: null });

		expect(
			screen.getByText("Choose a base environment first."),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Add seed command" }),
		).toBeDisabled();
		expect(api.calls).toEqual([]);
	});

	test("a base environment without services says so", async () => {
		await renderForm({ baseEnvironmentId: "env-empty" }, [
			...ENVS,
			wireEnv({
				id: "env-empty",
				name: "Sandbox",
				slug: "sandbox",
				isProduction: false,
			}),
		]);

		expect(
			await screen.findByText("Sandbox has no services yet."),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Add seed command" }),
		).toBeDisabled();
	});

	test("lists saved seed commands", async () => {
		await renderForm({
			seedCommands: [
				{ service: "web", command: "npm run seed", runOn: "push" },
			],
		});

		expect(screen.getByLabelText("Command")).toHaveValue("npm run seed");
		expect(
			screen.getByRole("combobox", { name: "When to run" }),
		).toHaveTextContent("Every push");
		expect(
			await screen.findByRole("combobox", {
				name: "Service that runs the command",
			}),
		).toHaveTextContent("Web");
	});
});

function Host() {
	const [max, setMax] = useState(5);
	return (
		<>
			<button type="button" onClick={() => setMax(9)}>
				server changes
			</button>
			<PreviewSettingsForm
				projectId="proj-1"
				settings={wireSettings({ maxConcurrent: max })}
				environments={ENVS}
			/>
		</>
	);
}

describe("PreviewSettingsForm: saving", () => {
	test("saving a change sends the whole settings and reports it", async () => {
		const api = await renderForm(
			{ maxConcurrent: 5, idleDays: 7, excludedServices: ["worker"] },
			ENVS,
			{
				"PUT /projects/:id/previews/settings": {
					body: wireSettings({ maxConcurrent: 3 }),
				},
				"GET /projects/:id/previews/settings": { body: wireSettings() },
			},
		);
		const user = userEvent.setup();

		const max = screen.getByLabelText("Running previews");
		await user.clear(max);
		await user.type(max, "3");
		expect(saveButton()).toBeEnabled();
		expect(discardButton()).toBeEnabled();
		await user.click(saveButton());

		expect(await screen.findByText("Previews are on")).toBeInTheDocument();
		expect(putCalls(api)).toHaveLength(1);
		expect(putCalls(api)[0]).toMatchObject({
			path: "/projects/proj-1/previews/settings",
			body: {
				enabled: true,
				baseEnvironmentId: "env-prod",
				forkPrs: false,
				maxConcurrent: 3,
				idleDays: 7,
				excludedServices: ["worker"],
				seedCommands: [],
			},
		});
	});

	test("turning previews off reports that existing ones keep running", async () => {
		const api = await renderForm({}, ENVS, {
			"PUT /projects/:id/previews/settings": {
				body: wireSettings({ enabled: false }),
			},
		});
		const user = userEvent.setup();

		await user.click(screen.getByRole("switch", { name: "Previews" }));
		expect(screen.getByRole("switch", { name: "Previews" })).not.toBeChecked();
		await user.click(saveButton());

		expect(await screen.findByText("Previews are off")).toBeInTheDocument();
		expect(
			screen.getByText(
				"Existing previews keep running until their pull requests close.",
			),
		).toBeInTheDocument();
		expect(putCalls(api)[0]?.body).toMatchObject({ enabled: false });
	});

	test("after a save the form is clean again", async () => {
		await renderForm({}, ENVS, {
			"PUT /projects/:id/previews/settings": {
				body: wireSettings({ forkPrs: true }),
			},
		});
		const user = userEvent.setup();

		await user.click(
			screen.getByRole("switch", { name: "Previews for forks" }),
		);
		await user.click(saveButton());

		await screen.findByText("Previews are on");
		await waitFor(() => expect(saveButton()).toBeDisabled());
		expect(discardButton()).toBeDisabled();
	});

	test("a refused save shows the reason and keeps the edits", async () => {
		await renderForm({}, ENVS, {
			"PUT /projects/:id/previews/settings": problem(422, {
				code: "invalid",
				detail: "Base environment not found.",
			}),
		});
		const user = userEvent.setup();

		await user.click(
			screen.getByRole("switch", { name: "Previews for forks" }),
		);
		await user.click(saveButton());

		expect(
			await screen.findByText("Base environment not found."),
		).toBeVisible();
		expect(
			screen.getByRole("switch", { name: "Previews for forks" }),
		).toBeChecked();
		expect(saveButton()).toBeEnabled();
		// Editing again clears the stale error.
		await user.click(
			screen.getByRole("switch", { name: "Previews for forks" }),
		);
		expect(
			screen.queryByText("Base environment not found."),
		).not.toBeInTheDocument();
	});

	test("Discard changes puts the saved values back", async () => {
		const user = userEvent.setup();
		const api = await renderForm();

		await user.click(
			screen.getByRole("switch", { name: "Previews for forks" }),
		);
		await user.clear(screen.getByLabelText("Delete after idle days"));
		await user.type(screen.getByLabelText("Delete after idle days"), "30");
		await user.click(discardButton());

		expect(
			screen.getByRole("switch", { name: "Previews for forks" }),
		).not.toBeChecked();
		expect(screen.getByLabelText("Delete after idle days")).toHaveValue("7");
		expect(saveButton()).toBeDisabled();
		expect(putCalls(api)).toEqual([]);
	});

	test("the form follows the server while untouched and keeps edits when it changes", async () => {
		stubApi({ ...stubServices() });
		const user = userEvent.setup();
		await renderWithProviders(<Host />);
		expect(screen.getByLabelText("Running previews")).toHaveValue("5");

		await user.click(screen.getByRole("button", { name: "server changes" }));
		expect(screen.getByLabelText("Running previews")).toHaveValue("9");

		await user.clear(screen.getByLabelText("Delete after idle days"));
		await user.type(screen.getByLabelText("Delete after idle days"), "30");
		await user.click(screen.getByRole("button", { name: "server changes" }));
		expect(screen.getByLabelText("Delete after idle days")).toHaveValue("30");
	});
});

describe("PreviewSettingsForm: validation", () => {
	test.each([
		["0", "Enter a number from 1 to 50."],
		["51", "Enter a number from 1 to 50."],
		["abc", "Enter a number from 1 to 50."],
		["", "Enter a number from 1 to 50."],
	])("running previews %p is refused", async (value, message) => {
		const api = await renderForm();
		const user = userEvent.setup();

		const max = screen.getByLabelText("Running previews");
		await user.clear(max);
		if (value) await user.type(max, value);

		expect(await screen.findByText(message)).toBeInTheDocument();
		expect(max).toHaveAttribute("aria-invalid", "true");
		expect(saveButton()).toBeDisabled();
		expect(putCalls(api)).toEqual([]);
	});

	test.each(["0", "366", "1.5"])("idle days %p is refused", async (value) => {
		await renderForm();
		const user = userEvent.setup();

		const idle = screen.getByLabelText("Delete after idle days");
		await user.clear(idle);
		await user.type(idle, value);

		expect(
			await screen.findByText("Enter a number of days from 1 to 365."),
		).toBeInTheDocument();
		expect(saveButton()).toBeDisabled();
	});

	test("the limits' edges are accepted", async () => {
		await renderForm();
		const user = userEvent.setup();

		const max = screen.getByLabelText("Running previews");
		await user.clear(max);
		await user.type(max, "50");
		const idle = screen.getByLabelText("Delete after idle days");
		await user.clear(idle);
		await user.type(idle, "365");

		expect(screen.queryByText(/Enter a number/)).not.toBeInTheDocument();
		expect(saveButton()).toBeEnabled();
	});

	test("enabling previews needs a base environment", async () => {
		await renderForm({ enabled: false, baseEnvironmentId: null });
		const user = userEvent.setup();

		expect(
			screen.queryByText("Choose the environment previews are cloned from."),
		).not.toBeInTheDocument();
		await user.click(screen.getByRole("switch", { name: "Previews" }));

		expect(
			await screen.findByText(
				"Choose the environment previews are cloned from.",
			),
		).toBeInTheDocument();
		expect(saveButton()).toBeDisabled();
	});

	test("Enter in a field does not submit an invalid form", async () => {
		const api = await renderForm();
		const user = userEvent.setup();

		const max = screen.getByLabelText("Running previews");
		await user.clear(max);
		await user.type(max, "0{Enter}");

		expect(putCalls(api)).toEqual([]);
	});
});

describe("PreviewSettingsForm: base environment and services", () => {
	test("changing the base environment reloads its services and clears excludes and seeds", async () => {
		const api = await renderForm(
			{
				excludedServices: ["worker"],
				seedCommands: [
					{ service: "web", command: "npm run seed", runOn: "create" },
				],
			},
			ENVS,
			{
				"PUT /projects/:id/previews/settings": { body: wireSettings() },
			},
		);
		const user = userEvent.setup();
		await screen.findByRole("checkbox", { name: /Web/ });

		await pick(
			user,
			screen.getByRole("combobox", { name: "Base environment" }),
			"Staging",
		);

		expect(await screen.findByRole("checkbox", { name: /Api/ })).toBeChecked();
		expect(
			screen.queryByRole("checkbox", { name: /Web/ }),
		).not.toBeInTheDocument();
		expect(screen.queryByLabelText("Command")).not.toBeInTheDocument();
		await user.click(saveButton());
		await waitFor(() => expect(putCalls(api)).toHaveLength(1));
		expect(putCalls(api)[0]?.body).toMatchObject({
			baseEnvironmentId: "env-staging",
			excludedServices: [],
			seedCommands: [],
		});
	});

	test("unchecking a service excludes it and drops its seed commands", async () => {
		const api = await renderForm(
			{
				seedCommands: [
					{ service: "web", command: "npm run seed", runOn: "create" },
					{ service: "worker", command: "node seed.js", runOn: "create" },
				],
			},
			ENVS,
			{ "PUT /projects/:id/previews/settings": { body: wireSettings() } },
		);
		const user = userEvent.setup();

		await user.click(await screen.findByRole("checkbox", { name: /Worker/ }));

		expect(screen.getByRole("checkbox", { name: /Worker/ })).not.toBeChecked();
		expect(screen.getAllByLabelText("Command")).toHaveLength(1);
		expect(screen.getByLabelText("Command")).toHaveValue("npm run seed");
		await user.click(saveButton());
		await waitFor(() => expect(putCalls(api)).toHaveLength(1));
		expect(putCalls(api)[0]?.body).toMatchObject({
			excludedServices: ["worker"],
			seedCommands: [
				{ service: "web", command: "npm run seed", runOn: "create" },
			],
		});
	});

	test("checking an excluded service includes it again", async () => {
		const api = await renderForm({ excludedServices: ["worker"] }, ENVS, {
			"PUT /projects/:id/previews/settings": { body: wireSettings() },
		});
		const user = userEvent.setup();

		await user.click(await screen.findByRole("checkbox", { name: /Worker/ }));
		await user.click(saveButton());

		await waitFor(() => expect(putCalls(api)).toHaveLength(1));
		expect(putCalls(api)[0]?.body).toMatchObject({ excludedServices: [] });
	});
});

describe("PreviewSettingsForm: forks", () => {
	test("allowing forks warns that they never get secrets, and saves the choice", async () => {
		const api = await renderForm({}, ENVS, {
			"PUT /projects/:id/previews/settings": {
				body: wireSettings({ forkPrs: true }),
			},
		});
		const user = userEvent.setup();

		await user.click(
			screen.getByRole("switch", { name: "Previews for forks" }),
		);

		expect(
			screen.getByText("Fork previews never receive secrets"),
		).toBeInTheDocument();
		await user.click(saveButton());
		await waitFor(() => expect(putCalls(api)).toHaveLength(1));
		expect(putCalls(api)[0]?.body).toMatchObject({ forkPrs: true });
	});

	test("switching forks back off hides the warning", async () => {
		await renderForm();
		const user = userEvent.setup();

		await user.click(
			screen.getByRole("switch", { name: "Previews for forks" }),
		);
		await user.click(
			screen.getByRole("switch", { name: "Previews for forks" }),
		);

		expect(
			screen.queryByText("Fork previews never receive secrets"),
		).not.toBeInTheDocument();
	});
});

describe("PreviewSettingsForm: seed commands", () => {
	test("adds a seed command, picks its service and when it runs, and saves it trimmed", async () => {
		const api = await renderForm({}, ENVS, {
			"PUT /projects/:id/previews/settings": { body: wireSettings() },
		});
		const user = userEvent.setup();
		await screen.findByRole("checkbox", { name: /Web/ });

		await user.click(screen.getByRole("button", { name: "Add seed command" }));
		await pick(
			user,
			screen.getByRole("combobox", { name: "Service that runs the command" }),
			"Worker",
		);
		await user.type(screen.getByLabelText("Command"), "  node seed.js  ");
		await pick(
			user,
			screen.getByRole("combobox", { name: "When to run" }),
			"Every push",
		);
		await user.click(saveButton());

		await waitFor(() => expect(putCalls(api)).toHaveLength(1));
		expect(putCalls(api)[0]?.body).toMatchObject({
			seedCommands: [
				{ service: "worker", command: "node seed.js", runOn: "push" },
			],
		});
	});

	test("an incomplete seed command names what is missing and blocks saving", async () => {
		const api = await renderForm();
		const user = userEvent.setup();
		await screen.findByRole("checkbox", { name: /Web/ });

		await user.click(screen.getByRole("button", { name: "Add seed command" }));
		expect(
			await screen.findByText("Choose the service that runs it."),
		).toBeInTheDocument();
		expect(saveButton()).toBeDisabled();

		await pick(
			user,
			screen.getByRole("combobox", { name: "Service that runs the command" }),
			"Web",
		);
		expect(
			await screen.findByText("Enter the command to run."),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Command")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
		expect(saveButton()).toBeDisabled();

		await user.type(screen.getByLabelText("Command"), "npm run seed");
		expect(
			screen.queryByText("Enter the command to run."),
		).not.toBeInTheDocument();
		expect(saveButton()).toBeEnabled();
		expect(putCalls(api)).toEqual([]);
	});

	test("a seed command's service choice leaves out excluded services", async () => {
		const user = userEvent.setup();
		await renderForm({ excludedServices: ["worker"] });
		await screen.findByRole("checkbox", { name: /Web/ });

		await user.click(screen.getByRole("button", { name: "Add seed command" }));
		await user.click(
			screen.getByRole("combobox", { name: "Service that runs the command" }),
		);

		const options = await screen.findAllByRole("option");
		expect(options.map((o) => o.textContent)).toEqual(["Web"]);
	});

	test("removing a seed command takes it out of the saved settings", async () => {
		const api = await renderForm(
			{
				seedCommands: [
					{ service: "web", command: "one", runOn: "create" },
					{ service: "web", command: "two", runOn: "push" },
				],
			},
			ENVS,
			{ "PUT /projects/:id/previews/settings": { body: wireSettings() } },
		);
		const user = userEvent.setup();
		await screen.findByRole("checkbox", { name: /Web/ });

		await user.click(
			screen.getAllByRole("button", {
				name: "Remove seed command",
			})[0] as HTMLElement,
		);

		expect(screen.getAllByLabelText("Command")).toHaveLength(1);
		await user.click(saveButton());
		await waitFor(() => expect(putCalls(api)).toHaveLength(1));
		expect(putCalls(api)[0]?.body).toMatchObject({
			seedCommands: [{ service: "web", command: "two", runOn: "push" }],
		});
	});

	test("a saved seed command is offered in its own row only", async () => {
		await renderForm({
			seedCommands: [{ service: "web", command: "one", runOn: "create" }],
		});
		await screen.findByRole("checkbox", { name: /Web/ });

		const rows = screen.getAllByLabelText("Command");
		expect(rows).toHaveLength(1);
		expect(
			within(rows[0]?.parentElement as HTMLElement).getByRole("button", {
				name: "Remove seed command",
			}),
		).toBeInTheDocument();
	});
});
