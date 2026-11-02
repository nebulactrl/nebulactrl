import { describe, expect, it, mock } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { DnsProviderSpec } from "#/features/dns01/queries";
import { type ApiStub, stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { CredentialForm } from "./credential-form";

const cloudflare: DnsProviderSpec = {
	code: "cloudflare",
	name: "Cloudflare",
	help: "Use an API token with Zone:DNS:Edit.",
	docs: "https://go-acme.github.io/lego/dns/cloudflare/",
	fields: [
		{
			env: "CF_DNS_API_TOKEN",
			label: "API token",
			help: "Scoped to the zone",
			secret: true,
			required: true,
		},
		{ env: "CF_ZONE_ID", label: "Zone ID", secret: false, required: false },
	],
};

const ovh: DnsProviderSpec = {
	code: "ovh",
	name: "OVH",
	docs: "https://go-acme.github.io/lego/dns/ovh/",
	oneOf: [["OVH_APPLICATION_KEY", "OVH_APPLICATION_SECRET"], ["OVH_CLIENT_ID"]],
	fields: [
		{
			env: "OVH_APPLICATION_KEY",
			label: "Application key",
			secret: false,
			required: false,
		},
		{
			env: "OVH_APPLICATION_SECRET",
			label: "Application secret",
			secret: true,
			required: false,
		},
		{
			env: "OVH_CLIENT_ID",
			label: "Client ID",
			secret: false,
			required: false,
		},
	],
};

const pdns: DnsProviderSpec = {
	code: "pdns",
	name: "PowerDNS",
	docs: "https://go-acme.github.io/lego/dns/pdns/",
	generic: true,
	fields: [
		{ env: "PDNS_API_URL", label: "API URL", secret: false, required: true },
		{ env: "PDNS_API_KEY", label: "API key", secret: true, required: true },
	],
};

const linode: DnsProviderSpec = {
	code: "linode",
	name: "Linode",
	docs: "https://go-acme.github.io/lego/dns/linode/",
	generic: true,
	aliases: ["linodev4"],
	fields: [
		{ env: "LINODE_TOKEN", label: "Token", secret: true, required: true },
	],
};

interface Setup {
	api: ApiStub;
	onSaved: ReturnType<typeof mock<() => Promise<void>>>;
	onDone: ReturnType<typeof mock<() => void>>;
	user: ReturnType<typeof userEvent.setup>;
}

async function setup({
	providers = [cloudflare, ovh],
	routes = {},
}: {
	providers?: DnsProviderSpec[];
	routes?: Parameters<typeof stubApi>[0];
} = {}): Promise<Setup> {
	const api = stubApi({
		"GET /dns/lego-providers": {
			body: make("DNSProviderList", {
				items: [pdns, linode].map((spec) =>
					make("DNSProviderSpec", { ...spec, generic: true }),
				),
			}),
		},
		"POST /dns/credentials": {
			status: 201,
			body: make("DNSCredential", { serverId: "srv_1", scope: "server" }),
		},
		...routes,
	});
	const onSaved = mock(async () => {});
	const onDone = mock(() => {});
	await renderWithProviders(
		<CredentialForm
			serverId="srv_1"
			providers={providers}
			onSaved={onSaved}
			onDone={onDone}
		/>,
	);
	return { api, onSaved, onDone, user: userEvent.setup() };
}

async function choose(
	user: Setup["user"],
	label: string,
	option: string,
): Promise<void> {
	await user.click(screen.getByRole("combobox", { name: label }));
	await user.click(await screen.findByRole("option", { name: option }));
}

const saveButton = () =>
	screen.getByRole("button", { name: "Save credential" });

describe("CredentialForm: curated provider", () => {
	it("shows the provider's help and fields, marking optional ones and hiding secrets", async () => {
		await setup();
		expect(
			screen.getByText("Use an API token with Zone:DNS:Edit."),
		).toBeInTheDocument();
		expect(screen.getByLabelText(/^API token/)).toHaveAttribute(
			"type",
			"password",
		);
		expect(screen.getByLabelText(/^Zone ID/)).toHaveAttribute("type", "text");
		expect(screen.getByText("Zone ID (optional)")).toBeInTheDocument();
		expect(screen.queryByText("API token (optional)")).not.toBeInTheDocument();
		expect(screen.getByText("Scoped to the zone")).toBeInTheDocument();
		expect(
			screen.getByRole("region", { name: "Cloudflare credential setup" }),
		).toBeInTheDocument();
	});

	it("keeps Save disabled until the credential has a name and every required value", async () => {
		const { user } = await setup();
		expect(saveButton()).toBeDisabled();

		await user.type(screen.getByLabelText("Credential name"), "cf-prod");
		expect(saveButton()).toBeDisabled();

		await user.type(screen.getByLabelText(/^Zone ID/), "zone-1");
		// An optional value alone does not satisfy the required token, and says so.
		expect(saveButton()).toBeDisabled();
		expect(
			screen.getByText("API token (CF_DNS_API_TOKEN) is required"),
		).toBeInTheDocument();

		await user.type(screen.getByLabelText(/^API token/), "tok");
		expect(saveButton()).toBeEnabled();
		expect(
			screen.queryByText("API token (CF_DNS_API_TOKEN) is required"),
		).not.toBeInTheDocument();
	});

	it("does not nag about missing values before anything is typed", async () => {
		await setup();
		expect(screen.queryByText(/is required/)).not.toBeInTheDocument();
	});

	it("saves a team credential: trimmed name, provider code, only the filled values, no serverId", async () => {
		const { api, user, onSaved, onDone } = await setup();
		await user.type(screen.getByLabelText("Credential name"), "  cf-prod  ");
		await user.type(screen.getByLabelText(/^API token/), "secret-token");
		await user.click(saveButton());

		await waitFor(() => expect(onDone).toHaveBeenCalledTimes(1));
		expect(api.calls).toEqual([
			{
				method: "POST",
				path: "/dns/credentials",
				query: expect.anything(),
				body: {
					name: "cf-prod",
					provider: "cloudflare",
					values: { CF_DNS_API_TOKEN: "secret-token" },
				},
			},
		]);
		expect(onSaved).toHaveBeenCalledTimes(1);
		expect(await screen.findAllByText("Credential saved")).not.toHaveLength(0);
		expect(api.unmatched).toEqual([]);
	});

	it("scopes the credential to the server when 'This server only' is chosen", async () => {
		const { api, user, onDone } = await setup();
		await user.type(screen.getByLabelText("Credential name"), "cf-here");
		await user.type(screen.getByLabelText(/^API token/), "tok");
		await user.click(screen.getByRole("button", { name: "This server only" }));
		await user.click(saveButton());

		await waitFor(() => expect(onDone).toHaveBeenCalled());
		expect(api.calls[0]?.body).toEqual({
			name: "cf-here",
			provider: "cloudflare",
			serverId: "srv_1",
			values: { CF_DNS_API_TOKEN: "tok" },
		});
	});

	it("shows the API's refusal and keeps the form open without reporting success", async () => {
		const { user, onSaved, onDone } = await setup({
			routes: {
				"POST /dns/credentials": problem(409, {
					code: "conflict",
					detail: "A credential named cf-prod exists",
				}),
			},
		});
		await user.type(screen.getByLabelText("Credential name"), "cf-prod");
		await user.type(screen.getByLabelText(/^API token/), "tok");
		await user.click(saveButton());

		expect(
			await screen.findByText("A credential named cf-prod exists"),
		).toBeInTheDocument();
		expect(onSaved).not.toHaveBeenCalled();
		expect(onDone).not.toHaveBeenCalled();
		expect(screen.queryAllByText("Credential saved")).toHaveLength(0);
		expect(saveButton()).toBeEnabled();
	});

	it("Cancel closes the form without sending anything", async () => {
		const { api, user, onDone } = await setup();
		await user.type(screen.getByLabelText("Credential name"), "x");
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		expect(onDone).toHaveBeenCalledTimes(1);
		expect(api.calls).toEqual([]);
	});

	it("does not submit an incomplete form with Enter", async () => {
		const { api, user } = await setup();
		await user.type(screen.getByLabelText("Credential name"), "cf-prod{Enter}");
		expect(api.calls).toEqual([]);
	});

	it("clears typed values when another provider is chosen", async () => {
		const { user } = await setup();
		await user.type(screen.getByLabelText(/^API token/), "tok");
		await choose(user, "DNS provider", "OVH");
		await choose(user, "DNS provider", "Cloudflare");
		expect(screen.getByLabelText(/^API token/)).toHaveValue("");
	});

	it("asks for every variable of one authentication method", async () => {
		const { api, user, onDone } = await setup();
		await choose(user, "DNS provider", "OVH");
		await user.type(screen.getByLabelText("Credential name"), "ovh-1");
		await user.type(screen.getByLabelText(/^Application key/), "ak");
		expect(saveButton()).toBeDisabled();
		expect(
			screen.getByText(
				"Set every variable of one method: OVH_APPLICATION_KEY + OVH_APPLICATION_SECRET or OVH_CLIENT_ID",
			),
		).toBeInTheDocument();

		await user.type(screen.getByLabelText(/^Client ID/), "cid");
		expect(saveButton()).toBeEnabled();
		await user.click(saveButton());
		await waitFor(() => expect(onDone).toHaveBeenCalled());
		expect(api.calls[0]?.body).toEqual({
			name: "ovh-1",
			provider: "ovh",
			values: { OVH_APPLICATION_KEY: "ak", OVH_CLIENT_ID: "cid" },
		});
	});
});

describe("CredentialForm: any other lego provider", () => {
	it("fetches lego's catalog only once the generic choice is made", async () => {
		const { api, user } = await setup();
		expect(api.calls).toEqual([]);
		await choose(user, "DNS provider", "Any other lego provider");
		await screen.findByText(/supports 2 other DNS providers/);
		expect(api.calls.map((c) => `${c.method} ${c.path}`)).toEqual([
			"GET /dns/lego-providers",
		]);
		expect(screen.queryByLabelText(/^API token/)).not.toBeInTheDocument();
	});

	it("says while the catalog loads, and keeps Save disabled", async () => {
		const { user } = await setup({
			routes: {
				"GET /dns/lego-providers": () => new Promise<Response>(() => {}),
			},
		});
		await choose(user, "DNS provider", "Any other lego provider");
		expect(screen.getByText("Loading lego's providers…")).toBeInTheDocument();
		expect(saveButton()).toBeDisabled();
	});

	it("explains a code that is not a lego provider and offers no variables", async () => {
		const { user } = await setup();
		await choose(user, "DNS provider", "Any other lego provider");
		await screen.findByText(/supports 2 other DNS providers/);
		await user.type(screen.getByLabelText("lego provider code"), "nope");
		expect(
			screen.getByText(
				/Not a lego DNS provider code\. Start typing to see the 2/,
			),
		).toBeInTheDocument();
		expect(screen.queryByText("Variables")).not.toBeInTheDocument();
		await user.type(screen.getByLabelText("Credential name"), "n");
		expect(saveButton()).toBeDisabled();
	});

	it("links the provider's docs and lists the variables lego reads for a known code", async () => {
		const { user } = await setup();
		await choose(user, "DNS provider", "Any other lego provider");
		await screen.findByText(/supports 2 other DNS providers/);
		await user.type(screen.getByLabelText("lego provider code"), "PDNS ");
		const docs = screen.getByRole("link", { name: "lego docs for pdns" });
		expect(docs).toHaveAttribute("href", pdns.docs);
		expect(docs).toHaveAttribute("target", "_blank");
		expect(
			screen.getByText("Variables lego reads for pdns"),
		).toBeInTheDocument();
		expect(screen.getByText("PDNS_API_URL")).toBeInTheDocument();
	});

	it("saves with the canonical code when an older alias is typed", async () => {
		const { api, user, onDone } = await setup();
		await choose(user, "DNS provider", "Any other lego provider");
		await screen.findByText(/supports 2 other DNS providers/);
		await user.type(screen.getByLabelText("lego provider code"), "linodev4");
		await user.type(screen.getByLabelText("Credential name"), "lin");
		await user.type(screen.getByLabelText("Variable name"), "LINODE_TOKEN");
		await user.type(screen.getByLabelText("Value of LINODE_TOKEN"), "abc");
		await user.click(saveButton());

		await waitFor(() => expect(onDone).toHaveBeenCalled());
		expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
			name: "lin",
			provider: "linode",
			values: { LINODE_TOKEN: "abc" },
		});
	});

	it("rejects a variable lego does not read for the code and blocks Save", async () => {
		const { api, user } = await setup();
		await choose(user, "DNS provider", "Any other lego provider");
		await screen.findByText(/supports 2 other DNS providers/);
		await user.type(screen.getByLabelText("lego provider code"), "pdns");
		await user.type(screen.getByLabelText("Credential name"), "p");
		await user.type(screen.getByLabelText("Variable name"), "PDNS_API_URL");
		await user.type(screen.getByLabelText("Value of PDNS_API_URL"), "http://x");
		await user.type(screen.getByLabelText("Variable name"), "x");
		// Typed into the same row: the name is now PDNS_API_URLx.
		expect(
			screen.getByText("PDNS_API_URLx is not read by lego for pdns."),
		).toBeInTheDocument();
		expect(saveButton()).toBeDisabled();
		await user.type(screen.getByLabelText("Credential name"), "{Enter}");
		expect(api.calls.filter((c) => c.method === "POST")).toEqual([]);
	});

	it("names several unknown variables in the plural", async () => {
		const { user } = await setup();
		await choose(user, "DNS provider", "Any other lego provider");
		await screen.findByText(/supports 2 other DNS providers/);
		await user.type(screen.getByLabelText("lego provider code"), "pdns");
		await user.type(screen.getByLabelText("Variable name"), "AAA");
		await user.type(screen.getByLabelText("Value of AAA"), "1");
		await user.click(screen.getByRole("button", { name: "Add variable" }));
		const names = screen.getAllByLabelText("Variable name");
		await user.type(names[1] as HTMLElement, "BBB");
		await user.type(screen.getByLabelText("Value of BBB"), "2");
		expect(
			screen.getByText("AAA, BBB are not read by lego for pdns."),
		).toBeInTheDocument();
	});

	it("hides the value of a secret or unknown variable and shows a plain one", async () => {
		const { user } = await setup();
		await choose(user, "DNS provider", "Any other lego provider");
		await screen.findByText(/supports 2 other DNS providers/);
		await user.type(screen.getByLabelText("lego provider code"), "pdns");
		const name = screen.getByLabelText("Variable name");
		// Unknown name: masked, a wrongly shown value would leak.
		await user.type(name, "WHO_KNOWS");
		expect(screen.getByLabelText("Value of WHO_KNOWS")).toHaveAttribute(
			"type",
			"password",
		);
		await user.clear(name);
		await user.type(name, "PDNS_API_URL");
		expect(screen.getByLabelText("Value of PDNS_API_URL")).toHaveAttribute(
			"type",
			"text",
		);
		await user.clear(name);
		await user.type(name, "PDNS_API_KEY");
		expect(screen.getByLabelText("Value of PDNS_API_KEY")).toHaveAttribute(
			"type",
			"password",
		);
	});

	it("adds and removes variable rows; the last empty row cannot be removed", async () => {
		const { user } = await setup();
		await choose(user, "DNS provider", "Any other lego provider");
		await screen.findByText(/supports 2 other DNS providers/);
		await user.type(screen.getByLabelText("lego provider code"), "pdns");

		expect(
			screen.getByRole("button", { name: "Remove variable" }),
		).toBeDisabled();

		await user.type(screen.getByLabelText("Variable name"), "PDNS_API_URL");
		await user.click(screen.getByRole("button", { name: "Add variable" }));
		expect(screen.getAllByLabelText("Variable name")).toHaveLength(2);

		// Remove the first row: the second stays.
		await user.click(
			screen.getAllByRole("button", {
				name: "Remove variable",
			})[0] as HTMLElement,
		);
		const left = screen.getAllByLabelText("Variable name");
		expect(left).toHaveLength(1);
		expect(left[0]).toHaveValue("");

		// A single row with content is reset to an empty one, not deleted.
		await user.type(left[0] as HTMLElement, "PDNS_API_KEY");
		await user.click(screen.getByRole("button", { name: "Remove variable" }));
		expect(screen.getByLabelText("Variable name")).toHaveValue("");
	});

	it("saves a complete generic credential with its trimmed variables", async () => {
		const { api, user, onSaved, onDone } = await setup();
		await choose(user, "DNS provider", "Any other lego provider");
		await screen.findByText(/supports 2 other DNS providers/);
		await user.type(screen.getByLabelText("lego provider code"), "pdns");
		await user.type(screen.getByLabelText("Credential name"), "pdns-1");
		expect(saveButton()).toBeDisabled();

		await user.type(screen.getByLabelText("Variable name"), "  PDNS_API_URL ");
		await user.type(
			screen.getByLabelText("Value of PDNS_API_URL"),
			"https://pdns",
		);
		await user.click(screen.getByRole("button", { name: "Add variable" }));
		// A half-filled row (name without a value) is not sent.
		const names = screen.getAllByLabelText("Variable name");
		await user.type(names[1] as HTMLElement, "PDNS_API_KEY");
		expect(saveButton()).toBeDisabled();
		await user.type(screen.getByLabelText("Value of PDNS_API_KEY"), "k");
		expect(saveButton()).toBeEnabled();
		await user.click(saveButton());

		await waitFor(() => expect(onDone).toHaveBeenCalled());
		expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
			name: "pdns-1",
			provider: "pdns",
			values: { PDNS_API_URL: "https://pdns", PDNS_API_KEY: "k" },
		});
		expect(onSaved).toHaveBeenCalledTimes(1);
	});

	it("resets the variable rows when the code changes", async () => {
		const { user } = await setup();
		await choose(user, "DNS provider", "Any other lego provider");
		await screen.findByText(/supports 2 other DNS providers/);
		const code = screen.getByLabelText("lego provider code");
		await user.type(code, "pdns");
		await user.type(screen.getByLabelText("Variable name"), "PDNS_API_URL");
		await user.type(code, "x");
		await user.clear(code);
		await user.type(code, "pdns");
		expect(screen.getByLabelText("Variable name")).toHaveValue("");
	});
});
