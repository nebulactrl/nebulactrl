import { describe, expect, mock, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type { SecretManager } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { SecretManagerDialog } from "./secret-manager-dialog";

// Literal `${NAME}` placeholder: the escaped `$` keeps it from being a template placeholder.
const ref = (name: string) => `\${${name}}`;

/** Keeps the dialog's open state, with a button to open it again. */
function ReopenHost({ target }: { target: SecretManager }) {
	const [isOpen, setOpen] = useState(true);
	return (
		<>
			<button type="button" onClick={() => setOpen(true)}>
				reopen
			</button>
			<SecretManagerDialog
				open={isOpen}
				onOpenChange={setOpen}
				manager={target}
			/>
		</>
	);
}

/** Types a value in one step: the field's content, not its keystrokes, is under test. */
async function fill(
	user: ReturnType<typeof userEvent.setup>,
	field: HTMLElement,
	text: string,
) {
	await user.click(field);
	await user.paste(text);
}

const problem = (status: number, detail: string) => ({
	status,
	body: { status, title: "Error", detail, code: "bad_request" },
});

function manager(overrides: Partial<SecretManager> = {}): SecretManager {
	return {
		id: "sm1",
		name: "bao",
		kind: "vault",
		config: {
			address: "https://bao.example.com:8200",
			mount: "kv",
			namespace: "team-a",
			authMethod: "token",
		},
		createdAt: "2026-01-01T00:00:00Z",
		updatedAt: "2026-01-01T00:00:00Z",
		example: ref("{vault:bao/apps/shop#password}"),
		grants: null,
		lastTest: { at: null, ok: null },
		scope: "team",
		secretSet: true,
		...overrides,
	};
}

const projectsReply = {
	body: {
		items: [
			{
				id: "p1",
				name: "Shop",
				slug: "shop",
				description: "",
				system: false,
				buildLocation: "inherit",
				buildServerId: "",
				registryId: "",
				createdAt: "2026-01-01T00:00:00Z",
				updatedAt: "2026-01-01T00:00:00Z",
			},
		],
	},
};

async function open(
	target: SecretManager | null = null,
	onOpenChange: (open: boolean) => void = () => {},
) {
	return renderWithProviders(
		<SecretManagerDialog open onOpenChange={onOpenChange} manager={target} />,
	);
}

const submitButton = (name: string | RegExp) =>
	screen.getByRole("button", { name });

describe("SecretManagerDialog: adding", () => {
	test("an empty submit names every missing field and sends nothing", async () => {
		const api = stubApi({});
		const user = userEvent.setup();
		await open();
		expect(
			screen.getByRole("heading", { name: "Add a secret manager" }),
		).toBeInTheDocument();
		// Nothing is flagged before the first submit.
		expect(screen.queryByText("Paste the credential.")).not.toBeInTheDocument();

		await user.click(submitButton("Add secret manager"));

		expect(
			screen.getByText(/Use 1 to 63 lowercase letters, digits and dashes/),
		).toBeInTheDocument();
		expect(
			screen.getByText(/Enter a full URL, like https:\/\/vault\.example\.com/),
		).toBeInTheDocument();
		expect(screen.getByText("Paste the credential.")).toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});

	test("creates a Vault integration with the typed values", async () => {
		const api = stubApi({
			"POST /secret-managers": { status: 201, body: manager() },
			"GET /secret-managers": { body: { items: [] } },
		});
		const onOpenChange = mock((_: boolean) => {});
		const user = userEvent.setup();
		await open(null, onOpenChange);

		await fill(user, screen.getByLabelText("Name"), "  bao");
		await fill(
			user,
			screen.getByLabelText("Address"),
			"https://bao.example.com:8200",
		);
		await fill(user, screen.getByLabelText("KV v2 mount"), "kv");
		await fill(user, screen.getByLabelText(/^Namespace/), "team-a");
		await fill(user, screen.getByLabelText("Token"), "s.abc123");
		await user.click(submitButton("Add secret manager"));

		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
		expect(api.calls.filter((c) => c.method === "POST")).toEqual([
			{
				method: "POST",
				path: "/secret-managers",
				query: expect.anything(),
				body: {
					name: "bao",
					kind: "vault",
					address: "https://bao.example.com:8200",
					mount: "kv",
					namespace: "team-a",
					authMethod: "token",
					caCert: "",
					scope: "team",
					grants: [],
					secret: "s.abc123",
				},
			},
		]);
		expect(await screen.findByText("Secret manager added")).toBeInTheDocument();
		expect(api.unmatched).toEqual([]);
	});

	test("AppRole asks for role id and sends it with the secret id", async () => {
		const api = stubApi({
			"POST /secret-managers": { status: 201, body: manager() },
			"GET /secret-managers": { body: { items: [] } },
		});
		const onOpenChange = mock((_: boolean) => {});
		const user = userEvent.setup();
		await open(null, onOpenChange);
		expect(screen.queryByLabelText("Role id")).not.toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: "AppRole" }));
		expect(screen.getByLabelText("Secret id")).toBeInTheDocument();
		await fill(user, screen.getByLabelText("Name"), "bao");
		await fill(
			user,
			screen.getByLabelText("Address"),
			"https://bao.example.com",
		);
		await user.click(submitButton("Add secret manager"));
		expect(screen.getByText("Enter the role id.")).toBeInTheDocument();
		expect(api.calls).toEqual([]);

		await fill(user, screen.getByLabelText("Role id"), "role-1");
		await fill(user, screen.getByLabelText("AppRole mount"), "approle-x");
		await fill(user, screen.getByLabelText("Secret id"), "sid-9");
		await user.click(submitButton("Add secret manager"));
		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
		const body = api.calls.find((c) => c.method === "POST")?.body;
		expect(body).toMatchObject({
			authMethod: "approle",
			roleId: "role-1",
			approleMount: "approle-x",
			secret: "sid-9",
		});
	});

	test("warns about plain HTTP to a remote host, not to localhost", async () => {
		stubApi({});
		const user = userEvent.setup();
		await open();
		const address = screen.getByLabelText("Address");
		await fill(user, address, "http://vault.internal:8200");
		expect(screen.getByText("Plain HTTP")).toBeInTheDocument();
		await user.clear(address);
		await fill(user, address, "http://localhost:8200");
		expect(screen.queryByText("Plain HTTP")).not.toBeInTheDocument();
		await user.clear(address);
		await fill(user, address, "https://vault.internal:8200");
		expect(screen.queryByText("Plain HTTP")).not.toBeInTheDocument();
	});

	test("rejects a CA that is not a PEM certificate", async () => {
		const api = stubApi({});
		const user = userEvent.setup();
		await open();
		await fill(user, screen.getByLabelText("Name"), "bao");
		await fill(
			user,
			screen.getByLabelText("Address"),
			"https://bao.example.com",
		);
		await fill(
			user,
			screen.getByLabelText(/^CA certificate/),
			"not a certificate",
		);
		await fill(user, screen.getByLabelText("Token"), "line1");
		await user.click(submitButton("Add secret manager"));
		expect(
			screen.getByText(/Paste a PEM certificate: -----BEGIN CERTIFICATE-----/),
		).toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});

	test("creates an Infisical integration with its own fields only", async () => {
		const api = stubApi({
			"POST /secret-managers": {
				status: 201,
				body: manager({ kind: "infisical", name: "inf" }),
			},
			"GET /secret-managers": { body: { items: [] } },
		});
		const onOpenChange = mock((_: boolean) => {});
		const user = userEvent.setup();
		await open(null, onOpenChange);

		await user.click(screen.getByRole("button", { name: "Infisical" }));
		expect(screen.queryByLabelText("Address")).not.toBeInTheDocument();
		expect(screen.getByLabelText("Client secret")).toBeInTheDocument();

		await fill(user, screen.getByLabelText("Name"), "inf");
		await user.click(submitButton("Add secret manager"));
		expect(
			screen.getByText("Enter the machine identity's client id."),
		).toBeInTheDocument();
		expect(
			screen.getByText(/Enter the project id \(letters, digits and dashes\)/),
		).toBeInTheDocument();
		expect(api.calls).toEqual([]);

		await fill(user, screen.getByLabelText("Client id"), "cid-1");
		await fill(user, screen.getByLabelText("Project id"), "proj-1");
		await fill(user, screen.getByLabelText(/^Organization slug/), "acme");
		await fill(user, screen.getByLabelText("Client secret"), "shh");
		await user.click(submitButton("Add secret manager"));
		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
		expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
			name: "inf",
			kind: "infisical",
			siteUrl: "",
			clientId: "cid-1",
			projectId: "proj-1",
			organizationSlug: "acme",
			scope: "team",
			grants: [],
			secret: "shh",
		});
	});

	test("Doppler needs the default project and config together", async () => {
		const api = stubApi({
			"POST /secret-managers": {
				status: 201,
				body: manager({ kind: "doppler", name: "dop" }),
			},
			"GET /secret-managers": { body: { items: [] } },
		});
		const onOpenChange = mock((_: boolean) => {});
		const user = userEvent.setup();
		await open(null, onOpenChange);

		await user.click(screen.getByRole("button", { name: "Doppler" }));
		await fill(user, screen.getByLabelText("Name"), "dop");
		await fill(user, screen.getByLabelText("Token"), "dp.st.abc");
		await fill(user, screen.getByLabelText(/^Default project/), "backend");
		await user.click(submitButton("Add secret manager"));
		expect(
			screen.getByText("Set both the default project and config, or neither."),
		).toBeInTheDocument();
		expect(api.calls).toEqual([]);

		await fill(user, screen.getByLabelText(/^Default config/), "prd");
		await user.click(submitButton("Add secret manager"));
		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
		expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
			name: "dop",
			kind: "doppler",
			siteUrl: "",
			defaultProject: "backend",
			defaultConfig: "prd",
			scope: "team",
			grants: [],
			secret: "dp.st.abc",
		});
	});

	test("Doppler flags a plain-HTTP API URL", async () => {
		stubApi({});
		const user = userEvent.setup();
		await open();
		await user.click(screen.getByRole("button", { name: "Doppler" }));
		await fill(
			user,
			screen.getByLabelText(/^Doppler API URL/),
			"http://doppler.corp",
		);
		const alert = screen.getByText("Plain HTTP").closest("[role=alert]");
		expect(alert).not.toBeNull();
		expect(
			within(alert as HTMLElement).getByText(/The token and every secret/),
		).toBeInTheDocument();
	});

	test("selected projects without a grant blocks the save; a grant is sent", async () => {
		const api = stubApi({
			"GET /projects": projectsReply,
			"POST /secret-managers": { status: 201, body: manager() },
			"GET /secret-managers": { body: { items: [] } },
		});
		const onOpenChange = mock((_: boolean) => {});
		const user = userEvent.setup();
		await open(null, onOpenChange);
		await fill(user, screen.getByLabelText("Name"), "bao");
		await fill(
			user,
			screen.getByLabelText("Address"),
			"https://bao.example.com",
		);
		await fill(user, screen.getByLabelText("Token"), "tok");
		await user.click(screen.getByRole("button", { name: "Selected projects" }));
		await user.click(submitButton("Add secret manager"));
		expect(
			screen.getByText(/Choose at least one project or environment/),
		).toBeInTheDocument();
		expect(api.calls.filter((c) => c.method === "POST")).toEqual([]);

		await user.click(await screen.findByRole("checkbox", { name: "Shop" }));
		await user.click(submitButton("Add secret manager"));
		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
		expect(api.calls.find((c) => c.method === "POST")?.body).toMatchObject({
			scope: "selected",
			grants: [{ projectId: "p1" }],
		});
	});

	test("a failed save shows the server's reason and keeps the dialog open", async () => {
		const api = stubApi({
			"POST /secret-managers": problem(
				409,
				"A secret manager named bao exists",
			),
		});
		const onOpenChange = mock((_: boolean) => {});
		const user = userEvent.setup();
		await open(null, onOpenChange);
		await fill(user, screen.getByLabelText("Name"), "bao");
		await fill(
			user,
			screen.getByLabelText("Address"),
			"https://bao.example.com",
		);
		await fill(user, screen.getByLabelText("Token"), "tok");
		await user.click(submitButton("Add secret manager"));
		expect(
			await screen.findByText("A secret manager named bao exists"),
		).toBeInTheDocument();
		expect(onOpenChange).not.toHaveBeenCalled();
		expect(screen.queryByText("Secret manager added")).not.toBeInTheDocument();
		expect(api.calls).toHaveLength(1);
		// The button is usable again for a retry.
		expect(submitButton("Add secret manager")).toBeEnabled();
	});

	test("Cancel closes without a request", async () => {
		const api = stubApi({});
		const onOpenChange = mock((_: boolean) => {});
		const user = userEvent.setup();
		await open(null, onOpenChange);
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		expect(onOpenChange).toHaveBeenCalledWith(false);
		expect(api.calls).toEqual([]);
	});

	test("the reference example follows the chosen type and name", async () => {
		stubApi({});
		const user = userEvent.setup();
		await open();
		await fill(user, screen.getByLabelText("Name"), "bao");
		expect(screen.getByText(/\$\{\{vault:bao\//)).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Doppler" }));
		expect(screen.getByText(/\$\{\{doppler:bao/)).toBeInTheDocument();
	});
});

describe("SecretManagerDialog: editing", () => {
	test("shows the stored values, locks the name and keeps the credential", async () => {
		stubApi({});
		await open(manager());
		expect(
			screen.getByRole("heading", { name: "Edit bao" }),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Name")).toBeDisabled();
		expect(screen.getByLabelText("Name")).toHaveValue("bao");
		expect(screen.getByLabelText("Address")).toHaveValue(
			"https://bao.example.com:8200",
		);
		expect(screen.getByLabelText("KV v2 mount")).toHaveValue("kv");
		// The type cannot change on an existing integration.
		expect(
			screen.queryByRole("button", { name: "Infisical" }),
		).not.toBeInTheDocument();
		const secret = screen.getByLabelText("Token (leave empty to keep)");
		expect(secret).toHaveValue("");
		expect(secret).toHaveAttribute("placeholder", "••••••••");
		expect(
			screen.getByText(/The name is part of every reference/),
		).toBeInTheDocument();
	});

	test("saving without touching the credential PUTs no secret", async () => {
		const api = stubApi({
			"PUT /secret-managers/:id": { body: manager() },
			"GET /secret-managers": { body: { items: [] } },
		});
		const onOpenChange = mock((_: boolean) => {});
		const user = userEvent.setup();
		await open(manager(), onOpenChange);
		await user.clear(screen.getByLabelText("KV v2 mount"));
		await fill(user, screen.getByLabelText("KV v2 mount"), "kv2");
		await user.click(submitButton("Save"));
		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
		const put = api.calls.find((c) => c.method === "PUT");
		expect(put?.path).toBe("/secret-managers/sm1");
		expect(put?.body).toEqual({
			address: "https://bao.example.com:8200",
			mount: "kv2",
			namespace: "team-a",
			authMethod: "token",
			caCert: "",
			scope: "team",
			grants: [],
		});
		expect(await screen.findByText("Secret manager saved")).toBeInTheDocument();
	});

	test("changing the address demands the credential again", async () => {
		const api = stubApi({
			"PUT /secret-managers/:id": { body: manager() },
			"GET /secret-managers": { body: { items: [] } },
		});
		const onOpenChange = mock((_: boolean) => {});
		const user = userEvent.setup();
		await open(manager(), onOpenChange);
		const address = screen.getByLabelText("Address");
		await user.clear(address);
		await fill(user, address, "https://other.example.com");
		expect(screen.getByLabelText("Token")).toBeInTheDocument();
		expect(
			screen.queryByLabelText("Token (leave empty to keep)"),
		).not.toBeInTheDocument();

		await user.click(submitButton("Save"));
		expect(
			screen.getByText(
				"Paste the credential again: the address or identity changed.",
			),
		).toBeInTheDocument();
		expect(api.calls).toEqual([]);

		await fill(user, screen.getByLabelText("Token"), "new-token");
		await user.click(submitButton("Save"));
		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
		expect(api.calls.find((c) => c.method === "PUT")?.body).toMatchObject({
			address: "https://other.example.com",
			secret: "new-token",
		});
	});

	test("an Infisical integration is edited with client id and project id", async () => {
		const api = stubApi({
			"PUT /secret-managers/:id": { body: manager() },
			"GET /secret-managers": { body: { items: [] } },
		});
		const onOpenChange = mock((_: boolean) => {});
		const user = userEvent.setup();
		await open(
			manager({
				kind: "infisical",
				name: "inf",
				config: { clientId: "cid", projectId: "proj", siteUrl: "" },
			}),
			onOpenChange,
		);
		expect(screen.getByLabelText("Client id")).toHaveValue("cid");
		expect(screen.getByLabelText("Project id")).toHaveValue("proj");
		await user.clear(screen.getByLabelText("Project id"));
		await fill(user, screen.getByLabelText("Project id"), "proj-2");
		await user.click(submitButton("Save"));
		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
		expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
			siteUrl: "",
			clientId: "cid",
			projectId: "proj-2",
			organizationSlug: "",
			scope: "team",
			grants: [],
		});
	});

	test("reopening starts again from the stored values, dropping edits", async () => {
		stubApi({});
		const user = userEvent.setup();
		const target = manager();
		await renderWithProviders(<ReopenHost target={target} />);
		await user.clear(screen.getByLabelText("KV v2 mount"));
		await fill(user, screen.getByLabelText("KV v2 mount"), "changed");
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByLabelText("KV v2 mount")).not.toBeInTheDocument(),
		);
		await user.click(screen.getByRole("button", { name: "reopen" }));
		expect(await screen.findByLabelText("KV v2 mount")).toHaveValue("kv");
	});
});
