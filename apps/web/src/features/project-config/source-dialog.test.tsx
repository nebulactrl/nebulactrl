import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { SourceDialog } from "./source-dialog";
import {
	connection,
	connectionsReply,
	environment,
	envSource,
} from "./test-support";

const SOURCE = "GET /environments/:id/config-source";
const CONNECTIONS = {
	"GET /git/connections": connectionsReply([
		connection(),
		connection({ id: "conn_2", name: "Acme GitLab", provider: "gitlab" }),
	]),
};

async function open(
	routes: Record<
		string,
		never | object | ((r: Request) => Response | Promise<Response>)
	>,
	env = environment(),
	isOpen = true,
) {
	const api = stubApi({ ...CONNECTIONS, ...routes });
	const view = await renderWithProviders(
		<SourceDialog environment={env} open={isOpen} onOpenChange={() => {}} />,
	);
	return { api, view, user: userEvent.setup() };
}

describe("SourceDialog: what is stored", () => {
	test("shows the link, the file and the last check", async () => {
		await open({
			[SOURCE]: {
				body: envSource({
					path: "deploy/nebula.toml",
					resolvedPath: "deploy/nebula.toml",
				}),
			},
		});
		expect(await screen.findByText("Set here")).toBeInTheDocument();
		expect(
			screen.getByText(/acme\/shop@main · deploy\/nebula\.toml/),
		).toBeInTheDocument();
		expect(screen.getByText("In sync")).toBeInTheDocument();
		expect(screen.getByText("3f9c2a1")).toBeInTheDocument();
		expect(screen.queryByText("Detail")).toBeNull();
	});

	test("seeds the form from the stored binding", async () => {
		await open({
			[SOURCE]: { body: envSource({ path: "deploy/nebula.toml" }) },
		});
		expect(await screen.findByLabelText("Repository")).toHaveValue("acme/shop");
		expect(screen.getByLabelText("Branch")).toHaveValue("main");
		expect(screen.getByLabelText(/Path/)).toHaveValue("deploy/nebula.toml");
		expect(
			await screen.findByRole("combobox", { name: "Git connection" }),
		).toHaveTextContent("Acme GitHub");
	});

	test("a derived link does not seed the path: it is not the reader's to edit", async () => {
		await open({
			[SOURCE]: {
				body: envSource({
					mode: "derived",
					path: "",
					resolvedPath: "nebula.toml",
				}),
			},
		});
		expect(
			await screen.findByText("From the environment's git services"),
		).toBeInTheDocument();
		expect(screen.getByLabelText(/Path/)).toHaveValue("");
		expect(screen.queryByRole("button", { name: "Remove link" })).toBeNull();
	});

	test("a bad file shows the status and why", async () => {
		await open({
			[SOURCE]: {
				body: envSource({
					status: "invalid",
					statusDetail: "nebula.toml:3:1: unknown key",
				}),
			},
		});
		expect(await screen.findByText("Invalid")).toBeInTheDocument();
		expect(screen.getByText("Detail")).toBeInTheDocument();
		expect(
			screen.getByText("nebula.toml:3:1: unknown key"),
		).toBeInTheDocument();
	});

	test("an environment that is not linked has no file to show and cannot check", async () => {
		await open({
			[SOURCE]: {
				body: envSource({
					mode: "none",
					status: "unknown",
					repo: "",
					branch: "",
					connectionId: undefined,
					checkedAt: undefined,
					commit: "",
				}),
			},
		});
		expect(await screen.findByText("Not linked")).toBeInTheDocument();
		expect(screen.queryByText("Last check")).toBeNull();
		expect(screen.getByRole("button", { name: /Check now/ })).toBeDisabled();
		expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
		expect(screen.getByRole("switch", { name: "Auto-apply" })).toHaveAttribute(
			"aria-disabled",
			"true",
		);
		expect(
			screen.getAllByText("Link a repository first: there is no file to apply.")
				.length,
		).toBeGreaterThan(0);
		expect(screen.queryByRole("button", { name: "Remove link" })).toBeNull();
	});

	test("asks nothing while closed", async () => {
		const { api } = await open({}, environment(), false);
		expect(api.calls).toEqual([]);
		expect(api.unmatched).toEqual([]);
	});
});

describe("SourceDialog: Auto-apply", () => {
	test("is never available in production, and says so", async () => {
		await open(
			{ [SOURCE]: { body: envSource() } },
			environment({
				isProduction: true,
				name: "Production",
				slug: "production",
			}),
		);
		const toggle = await screen.findByRole("switch", { name: "Auto-apply" });
		expect(toggle).toHaveAttribute("aria-disabled", "true");
		expect(
			screen.getAllByText(
				"Not available in production: a person reviews and applies changes there.",
			).length,
		).toBeGreaterThan(0);
	});

	test("turning it on sends only that setting and confirms", async () => {
		const { api, user } = await open({
			[SOURCE]: { body: envSource() },
			"PUT /environments/:id/config-source": {
				body: envSource({ autoApply: true }),
			},
		});
		await screen.findByRole("switch", { name: "Auto-apply" });
		await user.click(screen.getByText("Auto-apply", { selector: "label" }));
		expect(await screen.findByText("Auto-apply on")).toBeInTheDocument();
		expect(
			screen.getByText("Pushes stage the file's changes. Nothing is deployed."),
		).toBeInTheDocument();
		const put = api.calls.find((c) => c.method === "PUT");
		expect(put?.path).toBe("/environments/env_1/config-source");
		expect(put?.body).toEqual({ autoApply: true });
	});

	test("turning it off says pushes only mark the environment", async () => {
		const { api, user } = await open({
			[SOURCE]: { body: envSource({ autoApply: true }) },
			"PUT /environments/:id/config-source": {
				body: envSource({ autoApply: false }),
			},
		});
		await waitFor(() =>
			expect(
				screen.getByRole("switch", { name: "Auto-apply" }),
			).toHaveAttribute("aria-checked", "true"),
		);
		await user.click(screen.getByText("Auto-apply", { selector: "label" }));
		expect(await screen.findByText("Auto-apply off")).toBeInTheDocument();
		expect(
			screen.getByText("Pushes only mark the environment as differing."),
		).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
			autoApply: false,
		});
	});

	test("a refusal from the control plane is shown under the form", async () => {
		const { user } = await open({
			[SOURCE]: { body: envSource() },
			"PUT /environments/:id/config-source": {
				status: 403,
				body: {
					code: "forbidden",
					detail: "Auto-apply needs the admin scope.",
					status: 403,
				},
			},
		});
		await screen.findByRole("switch", { name: "Auto-apply" });
		await user.click(screen.getByText("Auto-apply", { selector: "label" }));
		expect(
			await screen.findByText("Auto-apply needs the admin scope."),
		).toBeInTheDocument();
		expect(screen.queryByText("Auto-apply on")).toBeNull();
	});
});

describe("SourceDialog: saving", () => {
	test("Save stays off until a connection, repository and branch are given", async () => {
		const { user } = await open({
			[SOURCE]: {
				body: envSource({
					mode: "none",
					status: "unknown",
					repo: "",
					branch: "",
					connectionId: undefined,
				}),
			},
		});
		const save = await screen.findByRole("button", { name: "Save" });
		expect(save).toBeDisabled();
		await user.type(screen.getByLabelText("Repository"), "acme/shop");
		await user.type(screen.getByLabelText("Branch"), "main");
		expect(save).toBeDisabled();
		await user.click(screen.getByRole("combobox", { name: "Git connection" }));
		await user.click(
			await screen.findByRole("option", { name: "Acme GitLab" }),
		);
		expect(save).toBeEnabled();
	});

	test("Save sends the trimmed binding, leaving a blank path out", async () => {
		const { api, user } = await open({
			[SOURCE]: {
				body: envSource({
					mode: "none",
					status: "unknown",
					repo: "",
					branch: "",
					connectionId: undefined,
				}),
			},
			"PUT /environments/:id/config-source": {
				body: envSource({ connectionId: "conn_2" }),
			},
		});
		await user.click(
			await screen.findByRole("combobox", { name: "Git connection" }),
		);
		await user.click(
			await screen.findByRole("option", { name: "Acme GitLab" }),
		);
		await user.type(screen.getByLabelText("Repository"), "  acme/shop ");
		await user.type(screen.getByLabelText("Branch"), " release ");
		await user.click(screen.getByRole("button", { name: "Save" }));
		expect(await screen.findByText("Saved")).toBeInTheDocument();
		expect(
			screen.getByText("nebula.toml is read from this repository."),
		).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
			connectionId: "conn_2",
			repo: "acme/shop",
			branch: "release",
		});
	});

	test("a path is sent when one is given", async () => {
		const { api, user } = await open({
			[SOURCE]: { body: envSource({ path: "" }) },
			"PUT /environments/:id/config-source": { body: envSource() },
		});
		await user.type(
			await screen.findByLabelText(/Path/),
			" deploy/nebula.toml ",
		);
		await user.click(screen.getByRole("button", { name: "Save" }));
		await screen.findByText("Saved");
		expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
			connectionId: "conn_1",
			repo: "acme/shop",
			branch: "main",
			path: "deploy/nebula.toml",
		});
	});

	test("a rejected save shows the reason and no confirmation", async () => {
		const { user } = await open({
			[SOURCE]: { body: envSource() },
			"PUT /environments/:id/config-source": {
				status: 422,
				body: {
					code: "invalid_repo",
					detail: "That repository does not exist.",
					status: 422,
				},
			},
		});
		await user.click(await screen.findByRole("button", { name: "Save" }));
		expect(
			await screen.findByText("That repository does not exist."),
		).toBeInTheDocument();
		expect(screen.queryByText("Saved")).toBeNull();
	});
});

describe("SourceDialog: Check now and Remove link", () => {
	test("Check now reads the file and reports its status", async () => {
		const { api, user } = await open({
			[SOURCE]: { body: envSource() },
			"POST /environments/:id/config-source/check": {
				body: envSource({ status: "differs" }),
			},
		});
		await user.click(await screen.findByRole("button", { name: /Check now/ }));
		expect(await screen.findByText("Checked nebula.toml")).toBeInTheDocument();
		expect(screen.getAllByText("Differs").length).toBeGreaterThan(0);
		expect(api.calls.find((c) => c.method === "POST")?.path).toBe(
			"/environments/env_1/config-source/check",
		);
	});

	test("a failed check shows why", async () => {
		const { user } = await open({
			[SOURCE]: { body: envSource() },
			"POST /environments/:id/config-source/check": problem(502, {
				code: "git_unreachable",
				detail: "GitHub did not answer.",
			}),
		});
		await user.click(await screen.findByRole("button", { name: /Check now/ }));
		expect(
			await screen.findByText("GitHub did not answer."),
		).toBeInTheDocument();
		expect(screen.queryByText("Checked nebula.toml")).toBeNull();
	});

	test("Remove link deletes the binding and confirms", async () => {
		const { api, user } = await open({
			[SOURCE]: { body: envSource() },
			"DELETE /environments/:id/config-source": { status: 204 },
		});
		await user.click(
			await screen.findByRole("button", { name: "Remove link" }),
		);
		expect(await screen.findByText("Link removed")).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "DELETE")?.path).toBe(
			"/environments/env_1/config-source",
		);
	});

	test("a failed removal shows why and keeps the link", async () => {
		const { user } = await open({
			[SOURCE]: { body: envSource() },
			"DELETE /environments/:id/config-source": {
				status: 403,
				body: {
					code: "forbidden",
					detail: "You cannot unlink this.",
					status: 403,
				},
			},
		});
		await user.click(
			await screen.findByRole("button", { name: "Remove link" }),
		);
		expect(
			await screen.findByText("You cannot unlink this."),
		).toBeInTheDocument();
		expect(screen.queryByText("Link removed")).toBeNull();
		expect(
			screen.getByRole("button", { name: "Remove link" }),
		).toBeInTheDocument();
	});

	test("links to how nebula.toml works", async () => {
		await open({ [SOURCE]: { body: envSource() } });
		const dialog = await screen.findByRole("dialog");
		expect(
			within(dialog).getByText("How nebula.toml works"),
		).toBeInTheDocument();
	});
});
