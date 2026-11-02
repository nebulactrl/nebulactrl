import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { EnvironmentConfigBanner } from "./environment-banner";
import { onOpenProjectConfig, type ProjectConfigDialog } from "./open";
import {
	connection,
	connectionsReply,
	environment,
	envSource,
} from "./test-support";

const INVALID =
	'nebula.toml:19:1: error[unknown_key]: unknown key "prot"\n  --> services.web\nhelp: did you mean port?';

async function show(env = environment()) {
	return renderWithProviders(<EnvironmentConfigBanner environment={env} />);
}

describe("EnvironmentConfigBanner", () => {
	test("a file that differs says how many services and offers Review", async () => {
		stubApi({
			"GET /environments/:id/config-source": {
				body: envSource({
					status: "differs",
					services: [
						{ slug: "web", state: "file_changed", changes: 2 },
						{ slug: "api", state: "drift", changes: 1 },
						{ slug: "db", state: "in_sync", changes: 0 },
					],
				}),
			},
		});
		const opened: ProjectConfigDialog[] = [];
		const off = onOpenProjectConfig((dialog) => opened.push(dialog));
		await show();
		const banner = await screen.findByRole("alert", { name: "nebula.toml" });
		expect(
			within(banner).getByText("nebula.toml @3f9c2a1 differs — 2 services"),
		).toBeInTheDocument();
		expect(
			within(banner).getByText(/nothing is applied until you stage it/),
		).toBeInTheDocument();
		await userEvent
			.setup()
			.click(within(banner).getByRole("button", { name: "Review" }));
		off();
		expect(opened).toEqual(["review"]);
	});

	test.each([
		["in sync", envSource({ status: "in_sync" })],
		["not linked", envSource({ mode: "none", status: "unknown" })],
		[
			"unreachable",
			envSource({ status: "unreachable", statusDetail: "timeout" }),
		],
	])("a %s environment shows no banner", async (_name, body) => {
		const api = stubApi({ "GET /environments/:id/config-source": { body } });
		const { queryClient } = await show();
		await waitFor(() => expect(api.calls).toHaveLength(1));
		await waitFor(() => expect(queryClient.isFetching()).toBe(0));
		expect(screen.queryByRole("alert")).toBeNull();
	});

	test("a preview environment is never asked about and shows nothing", async () => {
		const api = stubApi({});
		await show(environment({ kind: "preview" }));
		expect(screen.queryByRole("alert")).toBeNull();
		expect(api.calls).toEqual([]);
		expect(api.unmatched).toEqual([]);
	});

	test("an invalid file shows its first error and links to the line in the provider", async () => {
		stubApi({
			"GET /environments/:id/config-source": {
				body: envSource({ status: "invalid", statusDetail: INVALID }),
			},
			"GET /git/connections": connectionsReply(),
		});
		await show();
		const banner = await screen.findByRole("alert", { name: "nebula.toml" });
		expect(
			within(banner).getByText("nebula.toml @3f9c2a1 is invalid"),
		).toBeInTheDocument();
		expect(
			within(banner).getByText(
				'nebula.toml:19:1: error[unknown_key]: unknown key "prot"',
			),
		).toBeInTheDocument();
		const link = await within(banner).findByRole("link", {
			name: /View line 19/,
		});
		expect(link).toHaveAttribute(
			"href",
			"https://github.com/acme/shop/blob/3f9c2a1deadbeef/nebula.toml#L19",
		);
		expect(link).toHaveAttribute("target", "_blank");
		expect(link).toHaveAttribute("rel", "noopener noreferrer");
		expect(within(banner).queryByRole("button", { name: "Review" })).toBeNull();
	});

	test("a file read at no commit cannot be linked to", async () => {
		stubApi({
			"GET /environments/:id/config-source": {
				body: envSource({
					status: "invalid",
					statusDetail: "the file is not valid TOML",
					commit: "",
				}),
			},
			"GET /git/connections": connectionsReply([
				connection({ provider: "gitlab", baseUrl: "https://git.acme.dev/" }),
			]),
		});
		await show();
		const banner = await screen.findByRole("alert", { name: "nebula.toml" });
		expect(
			within(banner).getByText("nebula.toml is invalid"),
		).toBeInTheDocument();
		expect(
			within(banner).getByText("the file is not valid TOML"),
		).toBeInTheDocument();
		expect(within(banner).queryByRole("link")).toBeNull();
	});

	test("an error without a line opens the file itself", async () => {
		stubApi({
			"GET /environments/:id/config-source": {
				body: envSource({
					status: "invalid",
					statusDetail: "the file is not valid TOML",
				}),
			},
			"GET /git/connections": connectionsReply([
				connection({ provider: "gitlab", baseUrl: "https://git.acme.dev/" }),
			]),
		});
		await show();
		const link = await screen.findByRole("link", { name: /View file/ });
		expect(link).toHaveAttribute(
			"href",
			"https://git.acme.dev/acme/shop/-/blob/3f9c2a1deadbeef/nebula.toml",
		);
	});

	test("no link when the binding's connection is not in the list", async () => {
		const api = stubApi({
			"GET /environments/:id/config-source": {
				body: envSource({ status: "invalid", statusDetail: INVALID }),
			},
			"GET /git/connections": connectionsReply([
				connection({ id: "someone_else" }),
			]),
		});
		const { queryClient } = await show();
		const banner = await screen.findByRole("alert", { name: "nebula.toml" });
		await waitFor(() =>
			expect(api.calls.map((c) => c.path)).toContain("/git/connections"),
		);
		await waitFor(() => expect(queryClient.isFetching()).toBe(0));
		expect(within(banner).queryByRole("link")).toBeNull();
		expect(
			within(banner).getByRole("button", { name: "Check again" }),
		).toBeEnabled();
	});

	test("Check again re-reads the file and shows the new answer", async () => {
		let checked = false;
		const api = stubApi({
			"GET /environments/:id/config-source": () =>
				Response.json(
					checked
						? envSource({
								status: "differs",
								services: [{ slug: "web", state: "file_changed", changes: 1 }],
							})
						: envSource({ status: "invalid", statusDetail: INVALID }),
				),
			"POST /environments/:id/config-source/check": () => {
				checked = true;
				return Response.json(envSource({ status: "differs" }));
			},
			"GET /git/connections": connectionsReply(),
		});
		await show();
		await userEvent
			.setup()
			.click(await screen.findByRole("button", { name: "Check again" }));
		expect(
			await screen.findByText("nebula.toml @3f9c2a1 differs — 1 service"),
		).toBeInTheDocument();
		expect(
			api.calls.filter((c) => c.method === "POST").map((c) => c.path),
		).toEqual(["/environments/env_1/config-source/check"]);
	});

	test("a failed check says why and keeps the banner", async () => {
		stubApi({
			"GET /environments/:id/config-source": {
				body: envSource({ status: "invalid", statusDetail: INVALID }),
			},
			"GET /git/connections": connectionsReply(),
			"POST /environments/:id/config-source/check": problem(502, {
				code: "git_unreachable",
				detail: "GitHub did not answer.",
			}),
		});
		await show();
		await userEvent
			.setup()
			.click(await screen.findByRole("button", { name: "Check again" }));
		expect(
			await screen.findByText("Could not check nebula.toml"),
		).toBeInTheDocument();
		expect(screen.getByText("GitHub did not answer.")).toBeInTheDocument();
		expect(
			screen.getByRole("alert", { name: "nebula.toml" }),
		).toBeInTheDocument();
	});

	test("Check again is disabled while the check runs", async () => {
		let finish: ((response: Response) => void) | undefined;
		stubApi({
			"GET /environments/:id/config-source": {
				body: envSource({ status: "invalid", statusDetail: INVALID }),
			},
			"GET /git/connections": connectionsReply(),
			"POST /environments/:id/config-source/check": () =>
				new Promise<Response>((resolve) => {
					finish = resolve;
				}),
		});
		await show();
		await userEvent
			.setup()
			.click(await screen.findByRole("button", { name: "Check again" }));
		await waitFor(() => expect(finish).toBeDefined());
		expect(screen.getByRole("button", { name: /Check again/ })).toBeDisabled();
		finish?.(
			Response.json(envSource({ status: "invalid", statusDetail: INVALID })),
		);
		await waitFor(
			() =>
				expect(
					screen.getByRole("button", { name: /Check again/ }),
				).toBeEnabled(),
			{ timeout: 5000 },
		);
	});
});
