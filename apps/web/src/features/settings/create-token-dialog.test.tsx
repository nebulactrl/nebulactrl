import { describe, expect, mock, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { listTeamsQueryKey } from "#/api/gen/@tanstack/react-query.gen";
import type { Team } from "#/api/gen/types.gen";
import { type ApiReply, type ApiStub, stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { CreateTokenDialog } from "./create-token-dialog";
import type { PresetId } from "./token-presets";

const team = (role: Team["role"]) =>
	make("Team", {
		id: "team1",
		name: "Platform",
		slug: "platform",
		current: true,
		role,
		memberCount: 2,
		createdAt: "2026-01-01T00:00:00Z",
	});

const me = (instanceAdmin: boolean) =>
	make("MeBody", {
		id: "u1",
		name: "Ada",
		email: "ada@example.com",
		instanceAdmin,
		workspaceId: "w1",
		createdAt: "2026-01-01T00:00:00Z",
		principal: { type: "user", label: "Ada", scopes: [] },
	});

interface Setup {
	role?: Team["role"];
	instanceAdmin?: boolean;
	projects?: { id: string; name: string }[];
	create?: ApiReply;
}

const created = make("CreatedToken", {
	id: "tok1",
	token: "nct_secret-value-123",
	name: "read-only",
	kind: "rest",
	prefix: "secret-v",
	scopes: ["projects:read"],
	allowProductionWithoutApproval: false,
	createdAt: "2026-01-01T00:00:00Z",
});

async function open(
	{ role = "developer", instanceAdmin = false, projects, create }: Setup = {},
	props: { initialPreset?: PresetId; onOpenChange?: (o: boolean) => void } = {},
) {
	const api = stubApi({
		"GET /teams": {
			body: make("ListTeamsOutputBody", { items: [team(role)] }),
		},
		"GET /me": { body: me(instanceAdmin) },
		"GET /projects": {
			body: make("PageProject", {
				items: (projects ?? []).map((p) =>
					make("Project", { ...p, slug: p.name }),
				),
			}),
		},
		"GET /tokens": { body: { items: [] } },
		"POST /tokens": create ?? { status: 201, body: created },
	});
	const onOpenChange = props.onOpenChange ?? mock(() => {});
	const user = userEvent.setup();
	await renderWithProviders(
		<CreateTokenDialog
			open
			onOpenChange={onOpenChange}
			{...(props.initialPreset ? { initialPreset: props.initialPreset } : {})}
		/>,
	);
	await screen.findByLabelText("Name");
	return { api, user, onOpenChange };
}

const post = (api: ApiStub) => api.calls.find((call) => call.method === "POST");

/** The body of the POST that was sent; fails the test when none was. */
function postBody(api: ApiStub): Record<string, unknown> {
	const call = post(api);
	if (!call) throw new Error("expected a POST request to have been sent");
	return call.body as Record<string, unknown>;
}

describe("CreateTokenDialog", () => {
	test("opens on the read-only preset with its suggested name and scopes", async () => {
		await open();
		expect(screen.getByLabelText("Name")).toHaveValue("read-only");
		expect(screen.getByRole("radio", { name: /^Read-only/ })).toBeChecked();
		for (const scope of ["projects:read", "logs:read", "servers:read"]) {
			expect(screen.getByText(scope)).toBeInTheDocument();
		}
		expect(
			screen.getByText(
				"No project selected: the token can reach every project.",
			),
		).toBeInTheDocument();
	});

	test("creating sends the preset's scopes, REST kind and a 90 day expiry, then shows the secret once", async () => {
		const before = Date.now();
		const { api, user } = await open();
		await user.click(screen.getByRole("button", { name: "Create token" }));
		expect(await screen.findByText("Copy your token now")).toBeInTheDocument();
		expect(screen.getByTestId("created-token")).toHaveTextContent(
			"nct_secret-value-123",
		);
		expect(screen.getByText("Shown once")).toBeInTheDocument();
		const call = post(api);
		expect(call?.path).toBe("/tokens");
		const body = call?.body as {
			name: string;
			scopes: string[];
			projectIds: string[] | null;
			kind: string;
			expiresAt: string;
		};
		expect(body.name).toBe("read-only");
		expect(body.scopes).toEqual(["projects:read", "logs:read", "servers:read"]);
		expect(body.projectIds).toBeNull();
		expect(body.kind).toBe("rest");
		expect(body).not.toHaveProperty("allowProductionWithoutApproval");
		const days = (Date.parse(body.expiresAt) - before) / 86_400_000;
		expect(days).toBeGreaterThan(89.99);
		expect(days).toBeLessThan(90.01);
	});

	test("I have copied it closes the dialog", async () => {
		const { user, onOpenChange } = await open();
		await user.click(screen.getByRole("button", { name: "Create token" }));
		await user.click(
			await screen.findByRole("button", { name: "I have copied it" }),
		);
		expect(onOpenChange).toHaveBeenCalledWith(false);
	});

	test("No expiry omits expiresAt; other expiries move it", async () => {
		const { api, user } = await open();
		await user.click(screen.getByRole("button", { name: "No expiry" }));
		await user.click(screen.getByRole("button", { name: "Create token" }));
		await screen.findByText("Copy your token now");
		expect(post(api)?.body).not.toHaveProperty("expiresAt");
	});

	test("choosing 30 days sends an expiry about a month out", async () => {
		const before = Date.now();
		const { api, user } = await open();
		await user.click(screen.getByRole("button", { name: "30 days" }));
		await user.click(screen.getByRole("button", { name: "Create token" }));
		await screen.findByText("Copy your token now");
		const { expiresAt } = postBody(api) as { expiresAt: string };
		const days = (Date.parse(expiresAt) - before) / 86_400_000;
		expect(days).toBeGreaterThan(29.99);
		expect(days).toBeLessThan(30.01);
	});

	test("an MCP preset creates an mcp token and follows the suggested name", async () => {
		const { api, user } = await open();
		await user.click(screen.getByRole("radio", { name: /^MCP deploy/ }));
		expect(screen.getByLabelText("Name")).toHaveValue("mcp-deploy");
		await user.click(screen.getByRole("button", { name: "Create token" }));
		await screen.findByText("Copy your token now");
		expect(post(api)?.body).toMatchObject({
			kind: "mcp",
			name: "mcp-deploy",
			scopes: [
				"projects:read",
				"logs:read",
				"servers:read",
				"deployments:write",
			],
		});
	});

	test("a name the person typed is kept when the preset changes", async () => {
		const { api, user } = await open();
		const name = screen.getByLabelText("Name");
		await user.clear(name);
		await user.type(name, "  my-ci  ");
		await user.click(screen.getByRole("radio", { name: /^Full access/ }));
		expect(name).toHaveValue("  my-ci  ");
		await user.click(screen.getByRole("button", { name: "Create token" }));
		await screen.findByText("Copy your token now");
		const body = post(api)?.body as { name: string; scopes: string[] };
		expect(body.name).toBe("my-ci");
		expect(body.scopes).toContain("admin");
		expect(body.scopes).toHaveLength(9);
	});

	test("initialPreset picks the preset the dialog opens on", async () => {
		await open({}, { initialPreset: "mcp-configure" });
		expect(screen.getByLabelText("Name")).toHaveValue("mcp-configure");
		expect(screen.getByRole("radio", { name: /^MCP configure/ })).toBeChecked();
	});

	test("custom lets the person pick scopes and the kind", async () => {
		const { api, user } = await open();
		await user.click(screen.getByRole("radio", { name: /^Custom/ }));
		// Custom starts from projects:read only, with no suggested name.
		expect(screen.getByLabelText("Name")).toHaveValue("");
		expect(
			screen.getByRole("checkbox", { name: /projects:read/ }),
		).toBeChecked();
		expect(
			screen.getByRole("checkbox", { name: /^logs:read/ }),
		).not.toBeChecked();
		await user.click(
			screen.getByRole("checkbox", { name: /^deployments:write/ }),
		);
		await user.click(screen.getByRole("button", { name: "MCP agent" }));
		await user.type(screen.getByLabelText("Name"), "agent-x");
		await user.click(screen.getByRole("button", { name: "Create token" }));
		await screen.findByText("Copy your token now");
		expect(post(api)?.body).toMatchObject({
			name: "agent-x",
			kind: "mcp",
			scopes: ["projects:read", "deployments:write"],
		});
	});

	test("custom with no scope, or no name, cannot be created", async () => {
		const { api, user } = await open();
		await user.click(screen.getByRole("radio", { name: /^Custom/ }));
		const create = screen.getByRole("button", { name: "Create token" });
		expect(create).toBeDisabled(); // no name yet
		await user.type(screen.getByLabelText("Name"), "x");
		expect(create).toBeEnabled();
		await user.click(screen.getByRole("checkbox", { name: /projects:read/ }));
		expect(create).toBeDisabled(); // no scope left
		expect(post(api)).toBeUndefined();
	});

	test("a blank name cannot be submitted", async () => {
		const { api, user } = await open();
		const name = screen.getByLabelText("Name");
		await user.clear(name);
		await user.type(name, "   ");
		expect(screen.getByRole("button", { name: "Create token" })).toBeDisabled();
		await user.type(name, "{Enter}");
		expect(post(api)).toBeUndefined();
	});

	test("restricting to projects sends their ids and explains the effect", async () => {
		const { api, user } = await open({
			projects: [
				{ id: "p1", name: "Storefront" },
				{ id: "p2", name: "Blog" },
			],
		});
		await user.click(
			await screen.findByRole("checkbox", { name: "Storefront" }),
		);
		expect(
			screen.getByText("Restricted to 1 project; others answer 404."),
		).toBeInTheDocument();
		await user.click(screen.getByRole("checkbox", { name: "Blog" }));
		expect(
			screen.getByText("Restricted to 2 projects; others answer 404."),
		).toBeInTheDocument();
		await user.click(screen.getByRole("checkbox", { name: "Blog" }));
		await user.click(screen.getByRole("button", { name: "Create token" }));
		await screen.findByText("Copy your token now");
		expect(postBody(api).projectIds).toEqual(["p1"]);
	});

	test("a team without projects says so", async () => {
		await open({ projects: [] });
		expect(await screen.findByText("No projects yet.")).toBeInTheDocument();
	});

	test("only owners, admins and the instance admin can skip production approval", async () => {
		for (const [role, instanceAdmin, offered] of [
			["developer", false, false],
			["viewer", false, false],
			["owner", false, true],
			["admin", false, true],
			["developer", true, true],
		] as const) {
			const { unmount, queryClient } = await openUnmountable({
				role,
				instanceAdmin,
			});
			if (offered) {
				expect(
					await screen.findByRole("checkbox", {
						name: /Skip approval for production changes/,
					}),
				).toBeInTheDocument();
			} else {
				// Wait for the team and profile to load, then check no box appears.
				await waitFor(() =>
					expect(queryClient.getQueryState(listTeamsQueryKey())?.status).toBe(
						"success",
					),
				);
				await waitFor(() => expect(queryClient.isFetching()).toBe(0));
				expect(
					screen.queryByRole("checkbox", {
						name: /Skip approval for production changes/,
					}),
				).toBeNull();
			}
			unmount();
		}
	});

	test("an admin who skips approval is warned and the flag is sent", async () => {
		const { api, user } = await open({ role: "admin" });
		await user.click(
			await screen.findByRole("checkbox", {
				name: /Skip approval for production changes/,
			}),
		);
		expect(
			screen.getByText("Production approvals are off"),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Create token" }));
		await screen.findByText("Copy your token now");
		expect(post(api)?.body).toMatchObject({
			allowProductionWithoutApproval: true,
		});
	});

	test("a server refusal shows its reason and keeps the form open", async () => {
		const { user } = await open({
			create: {
				status: 422,
				body: {
					status: 422,
					title: "Unprocessable",
					detail: "A token with this name already exists",
					code: "validation",
				},
			},
		});
		await user.click(screen.getByRole("button", { name: "Create token" }));
		expect(
			await screen.findByText("A token with this name already exists"),
		).toBeInTheDocument();
		expect(screen.queryByText("Copy your token now")).toBeNull();
		expect(screen.getByLabelText("Name")).toBeInTheDocument();
	});

	test("Cancel closes without creating", async () => {
		const { api, user, onOpenChange } = await open();
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		expect(onOpenChange).toHaveBeenCalledWith(false);
		expect(post(api)).toBeUndefined();
	});
});

/** `open` for tests that mount the dialog more than once. */
async function openUnmountable(setup: Setup) {
	stubApi({
		"GET /teams": {
			body: make("ListTeamsOutputBody", {
				items: [team(setup.role ?? "developer")],
			}),
		},
		"GET /me": { body: me(setup.instanceAdmin ?? false) },
		"GET /projects": { body: make("PageProject", { items: [] }) },
	});
	const result = await renderWithProviders(
		<CreateTokenDialog open onOpenChange={() => {}} />,
	);
	await screen.findByLabelText("Name");
	return result;
}
