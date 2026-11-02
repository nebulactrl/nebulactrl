import { describe, expect, test } from "bun:test";
import { fireEvent, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ComposeChoices, ComposePlanService } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { ServiceCard } from "./service-card";
import { ChoicesHost, envRow, service } from "./test-fixtures";

const NO_ACCOUNTS = {
	"GET /git/connections": {
		body: make("ListConnectionsOutputBody", { items: [] }),
	},
};

async function card(
	svc: ComposePlanService,
	initial: ComposeChoices = {},
	routes: Parameters<typeof stubApi>[0] = NO_ACCOUNTS,
) {
	const api = stubApi(routes);
	const seen = { choices: initial };
	const view = await renderWithProviders(
		<ul>
			<ChoicesHost initial={initial} seen={seen}>
				{(choices, onChoices) => (
					<ServiceCard service={svc} choices={choices} onChoices={onChoices} />
				)}
			</ChoicesHost>
		</ul>,
	);
	return { ...view, seen, api };
}

const web = (over: Partial<ComposePlanService> = {}) =>
	service({
		key: "web",
		name: "web",
		kind: "app",
		port: 8080,
		portHttp: true,
		...over,
	});

describe("header", () => {
	test("shows the name, slug, image chip and http port", async () => {
		await card(
			web({
				slug: "shop-web",
				source: { type: "image", image: "ghcr.io/acme/web:1.4" },
			}),
		);
		expect(screen.getByRole("textbox", { name: "Name of web" })).toHaveValue(
			"web",
		);
		expect(screen.getByText("slug shop-web")).toBeInTheDocument();
		expect(screen.getByText("http :8080")).toBeInTheDocument();
		// The registry host is trimmed from the chip.
		expect(screen.getByText("acme/web:1.4")).toBeInTheDocument();
	});

	test("renaming records the name; clearing it drops the choice", async () => {
		const { seen } = await card(web());
		const name = screen.getByRole("textbox", { name: "Name of web" });
		fireEvent.change(name, { target: { value: "frontend" } });
		expect(seen.choices).toEqual({ services: { web: { name: "frontend" } } });

		fireEvent.change(name, { target: { value: " " } });
		expect(seen.choices).toEqual({});
		expect(name).toHaveValue("web");
	});

	test("the Compose key is shown while the name differs from it", async () => {
		await card(web({ name: "Web App" }));
		expect(screen.getByText("compose: web")).toBeInTheDocument();
		fireEvent.change(screen.getByRole("textbox", { name: "Name of web" }), {
			target: { value: "web" },
		});
		expect(screen.queryByText("compose: web")).not.toBeInTheDocument();
	});

	test("the include checkbox folds an excluded service down to its header", async () => {
		const user = userEvent.setup();
		const { seen } = await card(
			web({
				env: [envRow({ key: "A", value: "1" })],
				includeNote: "Runs once and exits.",
			}),
		);
		const include = screen.getByRole("checkbox", { name: "Include web" });
		expect(include).toBeChecked();
		expect(screen.getByText("Network")).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: /Environment variables/ }),
		).toBeInTheDocument();
		expect(screen.getByText("Runs once and exits.")).toBeInTheDocument();

		await user.click(include);
		expect(seen.choices).toEqual({ services: { web: { include: false } } });
		expect(screen.getByRole("textbox", { name: "Name of web" })).toBeDisabled();
		expect(screen.queryByText("Network")).not.toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: /Environment variables/ }),
		).not.toBeInTheDocument();
		// The reason stays visible so the reader knows why it was left out.
		expect(screen.getByText("Runs once and exits.")).toBeInTheDocument();
	});

	test("a service the plan excluded is unchecked and can be included", async () => {
		const user = userEvent.setup();
		const { seen } = await card(web({ include: false }));
		await user.click(screen.getByRole("checkbox", { name: "Include web" }));
		expect(seen.choices).toEqual({ services: { web: { include: true } } });
		expect(screen.getByText("Network")).toBeInTheDocument();
	});

	test("badges: one-shot job, worker, git build, managed database", async () => {
		const { unmount } = await card(web({ oneShot: true }));
		expect(screen.getByText("One-shot job")).toBeInTheDocument();
		unmount();

		const worker = await card(web({ kind: "worker" }));
		expect(screen.getByText("Worker")).toBeInTheDocument();
		expect(screen.queryByText("One-shot job")).not.toBeInTheDocument();
		worker.unmount();

		const git = await card(
			web({ source: { type: "git", build: { argNames: [], context: "." } } }),
		);
		expect(screen.getByText("Build from git")).toBeInTheDocument();
		git.unmount();

		await card(
			service({
				key: "db",
				kind: "database",
				source: { type: "managed" },
				managed: {
					eligible: true,
					enabled: true,
					engine: "postgres",
					version: "16",
					notes: [],
				},
			}),
		);
		expect(screen.getByText(/^Managed /)).toBeInTheDocument();
	});

	test("facts: domain, published ports and volumes", async () => {
		await card(
			web({
				domains: [{ enabled: true, generate: true, source: "suggested" }],
				ports: [{ port: 5432, protocol: "tcp", publish: true, role: "tcp" }],
				volumes: [
					{ from: "volume", name: "data", path: "/data" },
					{ from: "anonymous", name: "cache", path: "/cache" },
				],
			}),
		);
		expect(screen.getByText("domain")).toBeInTheDocument();
		expect(screen.getByText("publishes 5432/tcp")).toBeInTheDocument();
		expect(screen.getByText("2 volumes")).toBeInTheDocument();
	});

	test("a single volume is singular", async () => {
		await card(
			web({ volumes: [{ from: "volume", name: "data", path: "/data" }] }),
		);
		expect(screen.getByText("1 volume")).toBeInTheDocument();
	});
});

describe("Network section", () => {
	test("port edits write the choice; an invalid port is flagged and not written", async () => {
		const user = userEvent.setup();
		const { seen } = await card(web());
		const port = screen.getByRole("textbox", { name: "Port" });
		expect(port).toHaveValue("8080");

		await user.clear(port);
		await user.type(port, "3000");
		expect(seen.choices).toEqual({ services: { web: { port: 3000 } } });

		await user.clear(port);
		await user.type(port, "99999");
		expect(port).toHaveAttribute("aria-invalid", "true");
		expect(
			screen.getByText("Use a number from 0 to 65535."),
		).toBeInTheDocument();
		// The last valid port stays in the choices.
		expect(seen.choices).toEqual({ services: { web: { port: 9999 } } });

		await user.clear(port);
		expect(seen.choices).toEqual({});
		expect(port).not.toHaveAttribute("aria-invalid");
	});

	test("turning the domain on and naming a host", async () => {
		const user = userEvent.setup();
		const { seen } = await card(web());
		expect(
			screen.getByText(
				"No domain yet. Turn it on to get a generated hostname.",
			),
		).toBeInTheDocument();
		await user.click(screen.getByRole("switch", { name: "Public domain" }));
		expect(seen.choices).toEqual({
			services: { web: { domain: { enabled: true } } },
		});

		await user.type(
			screen.getByRole("textbox", { name: "Custom host (optional)" }),
			"shop.example.com",
		);
		expect(seen.choices).toEqual({
			services: {
				web: { domain: { enabled: true, host: "shop.example.com" } },
			},
		});

		await user.click(screen.getByRole("switch", { name: "Public domain" }));
		expect(seen.choices).toEqual({
			services: { web: { domain: { enabled: false } } },
		});
		expect(
			screen.queryByRole("textbox", { name: "Custom host (optional)" }),
		).not.toBeInTheDocument();
	});

	test("lists the plan's domains with where each came from", async () => {
		await card(
			web({
				domains: [
					{
						enabled: true,
						generate: false,
						host: "shop.example.com",
						source: "traefik",
					},
					{ enabled: false, generate: true, source: "suggested" },
				],
			}),
		);
		const list = screen
			.getByRole("switch", { name: "Public domain" })
			.closest("div[class*='flex-col']")?.parentElement as HTMLElement;
		expect(within(list).getByText("shop.example.com")).toBeInTheDocument();
		expect(within(list).getByText("from Traefik labels")).toBeInTheDocument();
		expect(within(list).getByText("Generated hostname")).toBeInTheDocument();
		expect(within(list).getByText("suggested")).toBeInTheDocument();
		// A domain exists, so the "no domain yet" hint is gone.
		expect(
			screen.getByText("Routes traffic from the internet to this service."),
		).toBeInTheDocument();
	});

	test("publishing a TCP port toggles it in publishPorts", async () => {
		const user = userEvent.setup();
		const { seen } = await card(
			service({
				key: "db",
				kind: "database",
				ports: [
					{
						port: 5432,
						protocol: "tcp",
						publish: false,
						role: "tcp",
						hostPort: 5433,
					},
					{
						port: 53,
						protocol: "udp",
						publish: true,
						role: "udp",
						listen: 5353,
					},
					{ port: 80, protocol: "tcp", publish: false, role: "http" },
				],
			}),
		);
		// The HTTP port sits behind the domain and has no publish row.
		expect(screen.getAllByRole("switch", { name: /^Publish / })).toHaveLength(
			2,
		);
		expect(
			screen.getByText(/The Compose file publishes host port 5433\./),
		).toBeInTheDocument();
		expect(screen.getByText("Public port 5353.")).toBeInTheDocument();
		const tcp = screen.getByRole("switch", {
			name: /Publish 5432\/tcp through a TCP route/,
		});
		expect(tcp).not.toBeChecked();
		await user.click(tcp);
		expect(seen.choices).toEqual({
			services: { db: { publishPorts: ["53/udp", "5432/tcp"] } },
		});
		await user.click(
			screen.getByRole("switch", {
				name: /Publish 53\/udp through a UDP route/,
			}),
		);
		expect(seen.choices).toEqual({
			services: { db: { publishPorts: ["5432/tcp"] } },
		});
	});

	test("a database with no port, domain or routable port has no Network section", async () => {
		await card(service({ key: "db", kind: "database" }));
		expect(screen.queryByText("Network")).not.toBeInTheDocument();
	});
});

describe("Source section", () => {
	const buildFromGit = (over: Partial<ComposePlanService> = {}) =>
		web({
			source: {
				type: "git",
				build: {
					argNames: ["NODE_ENV", "TOKEN"],
					context: "./app",
					dockerfile: "Dockerfile.prod",
					target: "release",
				},
			},
			needs: ["gitUrl"],
			...over,
		});

	test("an image service has nothing to choose", async () => {
		await card(web());
		expect(
			screen.queryByRole("region", { name: "Source" }),
		).not.toBeInTheDocument();
	});

	test("a git build asks for a URL and branch, and shows the build details", async () => {
		const user = userEvent.setup();
		const { seen } = await card(buildFromGit());
		expect(
			screen.getByText(/This service is built from source\./),
		).toBeInTheDocument();
		expect(
			screen.getByText("build ./app · Dockerfile.prod · target release"),
		).toBeInTheDocument();
		expect(
			screen.getByText(/Build arguments \(NODE_ENV, TOKEN\) are not applied/),
		).toBeInTheDocument();
		// No connected account: no switch between account and URL.
		expect(
			screen.queryByRole("group", { name: "Where the repository comes from" }),
		).not.toBeInTheDocument();

		await user.type(
			screen.getByRole("textbox", { name: "Repository URL" }),
			"https://github.com/acme/shop.git",
		);
		await user.type(screen.getByRole("textbox", { name: "Branch" }), "dev");
		expect(seen.choices).toEqual({
			services: {
				web: { gitUrl: "https://github.com/acme/shop.git", gitBranch: "dev" },
			},
		});
	});

	test("switching to the image replaces the git fields with an image field", async () => {
		const user = userEvent.setup();
		const { seen } = await card(buildFromGit());
		await user.click(
			screen.getByRole("switch", { name: "Use the image instead" }),
		);
		expect(seen.choices).toEqual({ services: { web: { source: "image" } } });
		expect(
			screen.queryByRole("textbox", { name: "Repository URL" }),
		).not.toBeInTheDocument();
		// The chip follows the switch.
		await user.type(
			screen.getByRole("textbox", { name: "Image" }),
			"ghcr.io/acme/app:1.4",
		);
		expect(seen.choices).toEqual({
			services: { web: { source: "image", image: "ghcr.io/acme/app:1.4" } },
		});
		expect(screen.getByText("acme/app:1.4")).toBeInTheDocument();

		await user.click(
			screen.getByRole("switch", { name: "Use the image instead" }),
		);
		expect(seen.choices).toEqual({
			services: { web: { image: "ghcr.io/acme/app:1.4" } },
		});
		expect(
			screen.getByRole("textbox", { name: "Repository URL" }),
		).toBeInTheDocument();
	});

	test("with a connected account the reader can pick it or type any URL", async () => {
		const user = userEvent.setup();
		const { seen } = await card(
			buildFromGit(),
			{},
			{
				"GET /git/connections": {
					body: make("ListConnectionsOutputBody", {
						items: [
							make("ConnectionView", {
								id: "c1",
								name: "acme-org",
								provider: "github",
								status: "active",
								canListRepos: true,
								accountLogin: "acme",
							}),
						],
					}),
				},
				"GET /git/connections/:id/repos": {
					body: make("ReposOutputBody", {
						items: [
							make("Repo", {
								fullName: "acme/shop",
								defaultBranch: "main",
								cloneUrl: "https://github.com/acme/shop.git",
								description: "The shop",
								private: true,
							}),
						],
					}),
				},
				"GET /git/connections/:id/branches": {
					body: { items: ["main", "dev"] },
				},
			},
		);
		const kind = await screen.findByRole("group", {
			name: "Where the repository comes from",
		});
		// An account exists and no URL is set: the account side is selected.
		expect(
			within(kind).getByRole("button", { name: "Connected account" }),
		).toHaveAttribute("aria-pressed", "true");
		await user.click(await screen.findByRole("button", { name: /acme\/shop/ }));
		expect(seen.choices).toEqual({
			services: {
				web: { connectionId: "c1", repo: "acme/shop", gitBranch: "main" },
			},
		});

		await user.click(within(kind).getByRole("button", { name: "Any URL" }));
		expect(
			screen.getByRole("textbox", { name: "Repository URL" }),
		).toBeInTheDocument();
	});

	test("an eligible managed database can be turned on and shows what is created", async () => {
		const user = userEvent.setup();
		const { seen } = await card(
			service({
				key: "db",
				kind: "database",
				source: { type: "image", image: "postgres:16" },
				managed: {
					eligible: true,
					enabled: false,
					engine: "postgres",
					version: "16",
					database: "shop",
					username: "app",
					reason: "An official image.",
					notes: ["Password becomes a secret."],
				},
			}),
		);
		const toggle = screen.getByRole("switch", {
			name: /Use a managed .* database/,
		});
		expect(toggle).not.toBeChecked();
		expect(screen.getByText("An official image.")).toBeInTheDocument();
		// Notes show while it is off too.
		expect(screen.getByText("Password becomes a secret.")).toBeInTheDocument();

		await user.click(toggle);
		expect(seen.choices).toEqual({
			services: { db: { managedDatabase: true } },
		});
		expect(screen.getByText("database shop · user app")).toBeInTheDocument();
		expect(
			screen.getByText(
				"Existing data is not copied. The database starts empty.",
			),
		).toBeInTheDocument();
		expect(screen.getByText("Password becomes a secret.")).toBeInTheDocument();
	});

	test("a database that cannot be managed says why; without a reason the row is absent", async () => {
		const { unmount } = await card(
			service({
				key: "db",
				managed: {
					eligible: false,
					enabled: false,
					reason: "A custom image.",
					notes: [],
				},
			}),
		);
		expect(
			screen.getByText("Not a managed database: A custom image."),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("switch", { name: /managed/ }),
		).not.toBeInTheDocument();
		unmount();

		await card(
			service({
				key: "db",
				managed: { eligible: false, enabled: false, notes: [] },
			}),
		);
		expect(
			screen.queryByText(/Not a managed database/),
		).not.toBeInTheDocument();
		expect(
			screen.queryByRole("region", { name: "Source" }),
		).not.toBeInTheDocument();
	});
});

describe("Storage section", () => {
	test("lists volumes, config files and the reason for a bind that is not imported", async () => {
		await card(
			web({
				volumes: [
					{
						from: "volume",
						name: "data",
						path: "/var/lib/data",
						note: "Starts empty.",
					},
				],
				files: [
					{
						bytes: 120,
						mode: "0644",
						path: "/etc/nginx.conf",
						source: "./nginx.conf",
					},
				],
				binds: [
					{
						canConvert: false,
						source: "/var/run/docker.sock",
						status: "rejected",
						target: "/sock",
						reason: "Host paths are not allowed.",
					},
					{ canConvert: false, source: "./ok", status: "file", target: "/ok" },
				],
			}),
		);
		const storage = screen.getByRole("region", { name: "Storage" });
		expect(within(storage).getByText("data")).toBeInTheDocument();
		expect(within(storage).getByText("named volume")).toBeInTheDocument();
		expect(within(storage).getByText("Starts empty.")).toBeInTheDocument();
		expect(within(storage).getByText("120 B · 0644")).toBeInTheDocument();
		expect(
			within(storage).getByText("config file from ./nginx.conf"),
		).toBeInTheDocument();
		expect(within(storage).getByText("Not imported")).toBeInTheDocument();
		expect(
			within(storage).getByText("Host paths are not allowed."),
		).toBeInTheDocument();
		// A bind that already became a file is not repeated as a problem.
		expect(within(storage).queryByText("./ok")).not.toBeInTheDocument();
	});

	test("a directory bind can become a named volume", async () => {
		const user = userEvent.setup();
		const { seen } = await card(
			web({
				binds: [
					{
						canConvert: true,
						source: "./data",
						status: "ignored",
						target: "/var/lib/data",
						reason: "A directory.",
					},
				],
			}),
		);
		const convert = screen.getByRole("checkbox", {
			name: "Use a named volume instead",
		});
		expect(convert).not.toBeChecked();
		expect(
			screen.getByText(/Files already in \.\/data are not copied/),
		).toBeInTheDocument();
		await user.click(convert);
		expect(seen.choices).toEqual({
			services: { web: { convertBinds: ["/var/lib/data"] } },
		});
		expect(
			screen.getByRole("checkbox", { name: "Use a named volume instead" }),
		).toBeChecked();
		await user.click(
			screen.getByRole("checkbox", { name: "Use a named volume instead" }),
		);
		expect(seen.choices).toEqual({
			services: { web: { convertBinds: [] } },
		});
	});

	test("no storage at all means no Storage section", async () => {
		await card(web());
		expect(
			screen.queryByRole("region", { name: "Storage" }),
		).not.toBeInTheDocument();
	});
});

describe("Runtime section", () => {
	test("summarises command, health, limits, stop grace and start order", async () => {
		await card(
			web({
				command: ["node", "server.js"],
				health: {
					fromCompose: true,
					type: "cmd",
					command: ["curl", "-f", "/"],
					intervalSeconds: 10,
				},
				memoryMb: 2048,
				cpus: 0.5,
				replicas: 3,
				stopGraceSeconds: 20,
				dependsOn: ["db", "cache"],
			}),
		);
		const runtime = screen.getByRole("region", { name: "Runtime" });
		const row = (label: string) =>
			within(runtime).getByText(label).nextElementSibling?.textContent;
		expect(row("Command")).toBe("node server.js");
		expect(row("Health check")).toBe("cmd curl -f / every 10s");
		expect(row("Limits")).toBe("2 GB · 0.5 CPU · 3 replicas");
		expect(row("Stop grace")).toBe("20s");
		expect(row("Starts after")).toBe("db, cache");
	});

	test("a health check the importer added says so; container options and replica notes show", async () => {
		await card(
			web({
				health: { fromCompose: false, type: "http-get", command: null },
				replicasNote: "Replicas are not supported; one runs.",
				docker: {
					capAdd: ["NET_ADMIN"],
					init: true,
					shmSizeMb: 256,
					stopSignal: "SIGINT",
				},
			}),
		);
		const runtime = screen.getByRole("region", { name: "Runtime" });
		expect(
			within(runtime).getByText("Health check (added)"),
		).toBeInTheDocument();
		expect(
			within(runtime).getByText("Replicas are not supported; one runs."),
		).toBeInTheDocument();
		const options = within(runtime).getByText("Container options")
			.nextElementSibling as HTMLElement;
		expect(
			within(options)
				.getAllByRole("listitem")
				.map((li) => li.textContent),
		).toEqual([
			"cap_add NET_ADMIN",
			"shm 256 MB",
			"init",
			"stop signal SIGINT",
		]);
	});

	test("a service with nothing to say has no Runtime section", async () => {
		await card(web());
		expect(
			screen.queryByRole("region", { name: "Runtime" }),
		).not.toBeInTheDocument();
	});
});

describe("notes", () => {
	test("service notes appear as alerts below the sections", async () => {
		await card(
			web({
				notes: [
					{
						key: "privileged",
						message: "Not carried over.",
						severity: "danger",
					},
				],
			}),
		);
		expect(screen.getByRole("note")).toHaveTextContent(
			"privilegedNot carried over.",
		);
	});
});
