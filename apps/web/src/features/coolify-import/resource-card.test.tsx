import { describe, expect, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { CoolifyPlanResource } from "#/api/gen/types.gen";
import { renderWithProviders } from "#/test/render";
import {
	type Baseline,
	buildChoices,
	EMPTY_DRAFT,
	type ImportDraft,
	rewritesFor,
} from "./lib";
import { ResourceCard } from "./resource-card";
import { envRow, resource } from "./test-fixtures";
import { DraftHarness } from "./test-harness";

// Builds a literal `${NAME}` placeholder (the escaped `$` keeps it from interpolating).
const ref = (name: string) => `\${${name}}`;

async function renderCard(
	res: CoolifyPlanResource,
	{
		baseline,
		initial = EMPTY_DRAFT,
		rewrites = [],
	}: {
		baseline?: Baseline;
		initial?: ImportDraft;
		rewrites?: Parameters<typeof rewritesFor>[0];
	} = {},
) {
	const seen = { current: initial };
	await renderWithProviders(
		<ul>
			<DraftHarness initial={initial} seen={seen}>
				{(draft, onDraft) => (
					<ResourceCard
						resource={res}
						draft={draft.resources[res.uuid]}
						rewrites={rewritesFor(rewrites, res.name)}
						baseline={baseline}
						onDraft={onDraft}
					/>
				)}
			</DraftHarness>
		</ul>,
	);
	return {
		choices: () => buildChoices(seen.current),
		open: () =>
			userEvent.setup().click(screen.getByRole("button", { name: /Details/ })),
	};
}

const DOMAINS = [
	{ host: "shop.example.com", enabled: true, forceHttps: true },
	{
		host: "www.example.com",
		path: "/store",
		enabled: false,
		forceHttps: true,
		generate: true,
		conflict: true,
		note: "Redirects to the apex.",
	},
];
const BASELINE: Baseline = {
	projectNames: {},
	domainHosts: { "res-web": ["shop.example.com", "www.example.com"] },
};

describe("ResourceCard summary", () => {
	test("an image app shows its name, slug, kind, image, port, enabled domains and volumes", async () => {
		await renderCard(
			resource({
				port: 3000,
				domains: DOMAINS,
				volumes: [{ name: "data", mountPath: "/data" }],
				source: { type: "image", image: "docker.io/acme/web:1.2@sha256:abc" },
			}),
		);
		const card = screen.getByRole("listitem");
		expect(card).toHaveAttribute("data-included", "true");
		expect(
			within(card).getByText("web", { selector: "span.font-medium" }),
		).toBeInTheDocument();
		expect(within(card).getByText("App")).toBeInTheDocument();
		expect(within(card).getByText("acme/web:1.2")).toBeInTheDocument();
		expect(within(card).getByText("port 3000")).toBeInTheDocument();
		expect(within(card).getByText("shop.example.com")).toBeInTheDocument();
		expect(within(card).queryByText(/www\.example\.com/)).toBeNull();
		expect(within(card).getByText("1 volume")).toBeInTheDocument();
		expect(screen.getByRole("checkbox", { name: "Include web" })).toBeChecked();
		expect(screen.queryByText("left out")).toBeNull();
	});

	test("a git worker shows its repository and branch, no port, and several volumes in the plural", async () => {
		await renderCard(
			resource({
				kind: "worker",
				name: "queue",
				slug: "queue",
				volumes: [
					{ name: "a", mountPath: "/a" },
					{ name: "b", mountPath: "/b" },
				],
				source: {
					type: "git",
					git: {
						url: "https://github.com/acme/queue",
						branch: "main",
						buildType: "nixpacks",
					},
				},
			}),
		);
		expect(screen.getByText("Worker")).toBeInTheDocument();
		expect(
			screen.getByText("https://github.com/acme/queue@main (nixpacks)"),
		).toBeInTheDocument();
		expect(screen.queryByText(/^port /)).toBeNull();
		expect(screen.getByText("2 volumes")).toBeInTheDocument();
	});

	test("a managed database shows the engine, its notes and the follow-up", async () => {
		await renderCard(
			resource({
				uuid: "res-db",
				kind: "database",
				name: "db",
				slug: "db",
				followup: "Restore the dump with pg_restore.",
				source: {
					type: "managed",
					managed: {
						engine: "postgres",
						version: "16",
						from: "postgres:16-alpine",
						notes: ["Runs the official postgres image."],
					},
				},
			}),
		);
		expect(screen.getByText("Database")).toBeInTheDocument();
		expect(screen.getByText("Managed PostgreSQL 16")).toBeInTheDocument();
		expect(
			screen.getByText("Runs the official postgres image."),
		).toBeInTheDocument();
		expect(screen.getByText(/After the import:/)).toBeInTheDocument();
		expect(
			screen.getByText("Restore the dump with pg_restore."),
		).toBeInTheDocument();
	});

	test("a managed database without notes or follow-up shows only its line", async () => {
		await renderCard(
			resource({
				kind: "database",
				source: {
					type: "managed",
					managed: {
						engine: "redis",
						version: "7",
						from: "redis:7",
						notes: null,
					},
				},
			}),
		);
		expect(screen.getByText("Managed Redis 7")).toBeInTheDocument();
		expect(screen.queryByText(/After the import:/)).toBeNull();
	});

	test("unchecking Include leaves it out, shows why, and records the choice; checking again removes it", async () => {
		const user = userEvent.setup();
		const card = await renderCard(
			resource({ include: true, includeNote: "Only one replica is imported." }),
		);
		expect(screen.queryByText("Only one replica is imported.")).toBeNull();

		await user.click(screen.getByRole("checkbox", { name: "Include web" }));
		expect(screen.getByRole("listitem")).toHaveAttribute(
			"data-included",
			"false",
		);
		expect(screen.getByText("left out")).toBeInTheDocument();
		expect(
			screen.getByText("Only one replica is imported."),
		).toBeInTheDocument();
		expect(card.choices()).toEqual({
			resources: { "res-web": { include: false } },
		});

		await user.click(screen.getByRole("checkbox", { name: "Include web" }));
		expect(screen.queryByText("left out")).toBeNull();
		expect(card.choices()).toEqual({
			resources: { "res-web": { include: true } },
		});
	});

	test("a resource the plan leaves out starts unchecked", async () => {
		await renderCard(resource({ include: false }));
		expect(
			screen.getByRole("checkbox", { name: "Include web" }),
		).not.toBeChecked();
		expect(screen.getByText("left out")).toBeInTheDocument();
	});

	test("Details toggles the panel", async () => {
		const user = userEvent.setup();
		await renderCard(resource());
		const details = screen.getByRole("button", { name: /Details/ });
		expect(details).toHaveAttribute("aria-expanded", "false");
		expect(screen.queryByLabelText("Name")).toBeNull();

		await user.click(details);
		expect(details).toHaveAttribute("aria-expanded", "true");
		expect(screen.getByLabelText("Name")).toHaveValue("web");

		await user.click(details);
		expect(details).toHaveAttribute("aria-expanded", "false");
	});
});

describe("ResourceCard details", () => {
	test("renaming records the new name; a draft name shows in the field", async () => {
		const user = userEvent.setup();
		const card = await renderCard(resource());
		await card.open();
		const name = screen.getByLabelText("Name");
		await user.clear(name);
		await user.type(name, "storefront");
		expect(name).toHaveValue("storefront");
		expect(card.choices()).toEqual({
			resources: { "res-web": { name: "storefront" } },
		});
	});

	test("a database has a version field with the plan's version as placeholder, and no port or kind", async () => {
		const user = userEvent.setup();
		const card = await renderCard(
			resource({
				kind: "database",
				source: {
					type: "managed",
					managed: {
						engine: "postgres",
						version: "16",
						from: "postgres:16",
						notes: null,
					},
				},
			}),
		);
		await card.open();
		const version = screen.getByLabelText("Version");
		expect(version).toHaveAttribute("placeholder", "16");
		expect(screen.queryByLabelText("Port")).toBeNull();
		expect(screen.queryByText("Runs as")).toBeNull();

		await user.type(version, " 17 ");
		expect(card.choices()).toEqual({
			resources: { "res-web": { engineVersion: "17" } },
		});
	});

	test("Runs as switches an app to a worker, and the badge follows", async () => {
		const user = userEvent.setup();
		const card = await renderCard(resource({ port: 8080 }));
		await card.open();
		const kind = screen.getByRole("combobox", { name: "Runs as" });
		expect(kind).toHaveTextContent("App");

		await user.click(kind);
		await user.click(screen.getByRole("option", { name: "Worker (no port)" }));
		expect(card.choices()).toEqual({
			resources: { "res-web": { kind: "worker" } },
		});
		expect(screen.getByRole("combobox", { name: "Runs as" })).toHaveTextContent(
			"Worker",
		);
		expect(screen.getAllByText("Worker").length).toBeGreaterThan(0);
	});

	test("the port field shows the plan's port as placeholder, takes numbers, ignores letters, and clearing drops the choice", async () => {
		const user = userEvent.setup();
		const card = await renderCard(resource({ port: 3000 }));
		await card.open();
		const port = screen.getByLabelText("Port");
		expect(port).toHaveAttribute("placeholder", "3000");

		await user.type(port, "8080");
		expect(card.choices()).toEqual({
			resources: { "res-web": { port: 8080 } },
		});
		expect(screen.getByText("port 8080")).toBeInTheDocument();

		await user.clear(port);
		expect(card.choices()).toEqual({});

		await user.type(port, "abc");
		expect(card.choices()).toEqual({});
	});

	test("a resource without a port says none", async () => {
		const card = await renderCard(resource());
		await card.open();
		expect(screen.getByLabelText("Port")).toHaveAttribute(
			"placeholder",
			"none",
		);
	});

	test("lists what needs an unrestricted server, volumes, unsupported settings and notes by severity", async () => {
		const card = await renderCard(
			resource({
				needsUnrestricted: ["privileged", "devices"],
				unsupported: ["Custom Nginx config", "Swarm placement"],
				volumes: [{ name: "data", mountPath: "/var/lib/data" }],
				notes: [
					{ key: "info-key", severity: "info", message: "An info note." },
					{ key: "warn-key", severity: "warning", message: "A warning note." },
					{ key: "danger-key", severity: "danger", message: "A danger note." },
				],
			}),
		);
		await card.open();
		const needs = screen
			.getByText(/Needs an unrestricted server:/)
			.closest("[role=note]") as HTMLElement;
		expect(needs).toHaveTextContent("privileged, devices");
		expect(screen.getByText("Not imported")).toBeInTheDocument();
		expect(screen.getByText("Custom Nginx config")).toBeInTheDocument();
		expect(screen.getByText("Swarm placement")).toBeInTheDocument();
		expect(
			screen.getByText("Created empty: the data is not copied."),
		).toBeInTheDocument();
		expect(screen.getByText("data → /var/lib/data")).toBeInTheDocument();
		expect(screen.getByText("A danger note.")).toBeInTheDocument();
		expect(screen.getByText("A warning note.")).toBeInTheDocument();
		// Info notes fold away under their own heading.
		expect(screen.queryByText("An info note.")).toBeNull();
		expect(
			screen.getByRole("button", { name: /Other notes/ }),
		).toBeInTheDocument();
	});

	test("no extra sections without notes, unsupported items, volumes or needs", async () => {
		const card = await renderCard(resource());
		await card.open();
		expect(screen.queryByText(/Needs an unrestricted server/)).toBeNull();
		expect(screen.queryByText("Not imported")).toBeNull();
		expect(screen.queryByText("Volumes")).toBeNull();
		expect(screen.queryByText("Domains")).toBeNull();
		expect(screen.queryByRole("button", { name: /Variables/ })).toBeNull();
	});

	test("domains can be turned off and given another host, keyed by the Coolify host", async () => {
		const user = userEvent.setup();
		const card = await renderCard(resource({ domains: DOMAINS }), {
			baseline: BASELINE,
		});
		await card.open();
		expect(screen.getByText("generated host")).toBeInTheDocument();
		expect(screen.getByText("host in use")).toBeInTheDocument();
		expect(screen.getByText("www.example.com/store")).toBeInTheDocument();
		expect(screen.getByText("Redirects to the apex.")).toBeInTheDocument();
		expect(screen.getByText("enabled")).toBeInTheDocument();
		expect(screen.getByText("left out")).toBeInTheDocument();

		const first = screen.getByRole("switch", {
			name: "Enable shop.example.com",
		});
		expect(first).toBeChecked();
		await user.click(first);
		expect(first).not.toBeChecked();
		await user.click(
			screen.getByRole("switch", { name: "Enable www.example.com" }),
		);

		const hosts = screen.getAllByPlaceholderText("Use another host name");
		await user.type(hosts[0] as HTMLElement, " store.example.org ");
		expect(card.choices()).toEqual({
			resources: {
				"res-web": {
					domains: {
						"shop.example.com": { enabled: false, host: "store.example.org" },
						"www.example.com": { enabled: true },
					},
				},
			},
		});
	});

	test("without a first plan to line up with, domains are read-only rather than editing the wrong one", async () => {
		const user = userEvent.setup();
		const card = await renderCard(resource({ domains: DOMAINS }), {
			baseline: {
				projectNames: {},
				domainHosts: { "res-web": ["only-one.example"] },
			},
		});
		await card.open();
		const first = screen.getByRole("switch", {
			name: "Enable shop.example.com",
		});
		expect(first).toHaveAttribute("data-disabled");
		expect(screen.queryByPlaceholderText("Use another host name")).toBeNull();
		await user.click(first);
		expect(first).toBeChecked();
		expect(card.choices()).toEqual({});
	});

	test("shows the variables list and the rewrites that apply to this resource", async () => {
		const card = await renderCard(
			resource({
				env: [envRow({ key: "DATABASE_URL", rewritten: true })],
			}),
			{
				rewrites: [
					{
						key: "DATABASE_URL",
						kind: "url",
						resource: "web",
						to: ref("{db.URL}"),
					},
					{ key: "OTHER", kind: "host", resource: "api", to: ref("{db.HOST}") },
				],
			},
		);
		await card.open();
		const toggle = screen.getByRole("button", { name: /Variables/ });
		await userEvent.setup().click(toggle);
		expect(screen.getByText(ref("{db.URL}"))).toBeInTheDocument();
		expect(screen.queryByText(ref("{db.HOST}"))).toBeNull();
	});
});
