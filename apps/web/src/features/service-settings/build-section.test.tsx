import { describe, expect, it } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type { Server } from "#/api/gen/types.gen";
import type { Service } from "#/api/models";
import { makeService } from "#/features/canvas/lib/fixtures";
import { factsWire, serverWire } from "#/features/servers/server.fixture";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { BuildSection } from "./build-section";
import { type SettingsDraft, toDraft } from "./draft";
import type { DraftErrors } from "./fields";

// ------------------------------------------------------------------ fixtures

const wireServer = (over: Partial<Server> = {}) =>
	serverWire({
		id: "srv-b1",
		name: "build-01",
		buildEnabled: true,
		acceptWorkloads: false,
		facts: factsWire({
			os: "linux",
			arch: "arm64",
			capabilities: ["build.push"],
		}),
		...over,
	});

const wireRegistry = (over: Record<string, unknown> = {}) => ({
	id: "reg-1",
	name: "GHCR",
	kind: "external",
	host: "ghcr.io",
	namespace: "acme",
	hasPushAuth: true,
	hasPullAuth: false,
	manageRetention: false,
	lastCheck: {},
	imageCount: 0,
	sizeBytes: 0,
	inUse: false,
	insecure: false,
	references: { projects: 0, services: 0, teams: 0 },
	createdAt: "2026-01-01T00:00:00Z",
	updatedAt: "2026-01-01T00:00:00Z",
	...over,
});

const wireProject = (over: Record<string, unknown> = {}) => ({
	id: "proj-1",
	name: "Shop",
	slug: "shop",
	description: "",
	system: false,
	buildLocation: "inherit",
	buildServerId: "",
	registryId: "",
	createdAt: "2026-01-01T00:00:00Z",
	updatedAt: "2026-01-01T00:00:00Z",
	...over,
});

const wireMe = (scopes: string[]) => ({
	id: "user-1",
	name: "Ada",
	email: "ada@example.com",
	instanceAdmin: scopes.includes("admin"),
	workspaceId: "ws-1",
	createdAt: "2026-01-01T00:00:00Z",
	principal: { label: "Ada", type: "user", scopes },
});

const wirePlan = (placement: Record<string, unknown>) => ({
	strategy: "rolling",
	reasons: [],
	warnings: [],
	changes: [],
	blocking: [],
	processes: [],
	source: { type: "git" },
	server: {
		id: "server-1",
		name: "hetzner-01",
		status: "online",
		lastSeenAt: null,
	},
	mesh: { blockers: null, crossServer: false, devicesAdded: 0, links: null },
	placement: {
		activeBuilds: 0,
		buildServerId: "",
		buildServerName: "",
		emulated: false,
		fallback: false,
		location: "target",
		maxConcurrent: 0,
		platform: "",
		reason: "",
		registryHost: "",
		registryId: "",
		reuse: false,
		sticky: false,
		warnings: null,
		...placement,
	},
});

interface Backend {
	scopes?: string[];
	servers?: Server[];
	registries?: unknown[];
	projects?: unknown[];
	team?: { buildLocation: string; registryId: string };
	plan?: { status?: number; body?: unknown };
}

function backend({
	scopes = ["admin"],
	servers = [wireServer()],
	registries = [wireRegistry()],
	projects = [wireProject()],
	team = { buildLocation: "target", registryId: "" },
	plan = { body: wirePlan({}) },
}: Backend = {}) {
	return stubApi({
		"GET /me": { body: wireMe(scopes) },
		"GET /servers": { body: { items: servers } },
		"GET /registries": { body: { items: registries } },
		"GET /projects": { body: { items: projects } },
		"GET /team/build-settings": { body: { ...team, updatedAt: null } },
		"POST /services/:serviceId/deployments/plan": plan,
	});
}

// ------------------------------------------------------------------- harness

function gitService(
	rest: Partial<Service> = {},
	config: Partial<Service["config"]> = {},
) {
	const { image: _image, ...base } = makeService({ sourceType: "git" }).config;
	return makeService({
		sourceType: "git",
		...rest,
		config: { ...base, gitUrl: "https://github.com/acme/shop.git", ...config },
	});
}

function Harness({
	service,
	errors = {},
	onDraft,
}: {
	service: Service;
	errors?: DraftErrors;
	onDraft: (draft: SettingsDraft) => void;
}) {
	const [draft, setDraft] = useState(() => toDraft(service));
	onDraft(draft);
	return (
		<BuildSection
			service={service}
			draft={draft}
			errors={errors}
			set={(key, value) => setDraft((prev) => ({ ...prev, [key]: value }))}
		/>
	);
}

async function renderBuild(service: Service, errors: DraftErrors = {}) {
	let latest = toDraft(service);
	await renderWithProviders(
		<Harness
			service={service}
			errors={errors}
			onDraft={(draft) => {
				latest = draft;
			}}
		/>,
	);
	return { draft: () => latest };
}

const locationText = () => screen.getByRole("status");

describe("BuildSection: Dockerfile path", () => {
	it.each(["auto", "dockerfile"])(
		"is shown for the %s build method",
		async (buildType) => {
			backend();
			await renderBuild(gitService({}, { buildType }));
			expect(await screen.findByLabelText("Dockerfile path")).toHaveValue(
				"Dockerfile",
			);
		},
	);

	it.each(["railpack", "starter:node", "starter:static"])(
		"is not shown for the %s build method",
		async (buildType) => {
			backend();
			await renderBuild(gitService({}, { buildType }));
			await screen.findByText("Build location");
			expect(
				screen.queryByLabelText("Dockerfile path"),
			).not.toBeInTheDocument();
		},
	);

	it("writes edits into the draft and shows its error", async () => {
		backend();
		const form = await renderBuild(
			gitService(
				{},
				{ buildType: "dockerfile", dockerfilePath: "docker/Dockerfile" },
			),
			{ dockerfilePath: "Dockerfile path must be relative." },
		);
		const input = await screen.findByLabelText("Dockerfile path");
		expect(input).toHaveValue("docker/Dockerfile");
		expect(input).toHaveAttribute("aria-invalid", "true");
		expect(
			screen.getByText("Dockerfile path must be relative."),
		).toBeInTheDocument();
		const user = userEvent.setup();
		await user.clear(input);
		await user.click(input);
		await user.paste("build/Dockerfile.prod");
		expect(form.draft().dockerfilePath).toBe("build/Dockerfile.prod");
	});
});

describe("BuildSection: build location", () => {
	it("explains an inherited location from the team's setting", async () => {
		backend({ team: { buildLocation: "build_server", registryId: "" } });
		await renderBuild(gitService());
		await waitFor(() =>
			expect(locationText()).toHaveTextContent(
				"Uses the team setting: builds run on a build server.",
			),
		);
		expect(
			screen.getByRole("button", { name: "Inherit", pressed: true }),
		).toBeInTheDocument();
	});

	it("explains an inherited location from the project's setting when it has one", async () => {
		backend({
			projects: [wireProject({ buildLocation: "build_server" })],
			team: { buildLocation: "target", registryId: "" },
		});
		await renderBuild(gitService({ projectId: "proj-1" }));
		await waitFor(() =>
			expect(locationText()).toHaveTextContent(
				"Uses the project setting: builds run on a build server.",
			),
		);
	});

	it("ignores another project's setting", async () => {
		backend({
			projects: [wireProject({ id: "proj-2", buildLocation: "build_server" })],
		});
		await renderBuild(gitService({ projectId: "proj-1" }));
		await waitFor(() =>
			expect(locationText()).toHaveTextContent(
				"Uses the team setting: builds run on the app's server.",
			),
		);
	});

	it("starts from the service's own saved location", async () => {
		backend();
		await renderBuild(gitService({}, { buildLocation: "target" }));
		expect(
			await screen.findByRole("button", {
				name: "On the app's server",
				pressed: true,
			}),
		).toBeInTheDocument();
		expect(locationText()).toHaveTextContent(
			"The image is built on the server that runs the app and used from there. No registry is needed.",
		);
	});

	it("stages a switch to a build server, which then offers the pin and the registry", async () => {
		backend();
		const form = await renderBuild(gitService());
		const user = userEvent.setup();
		await user.click(
			await screen.findByRole("button", { name: "On a build server" }),
		);
		expect(form.draft().buildLocation).toBe("build_server");
		expect(await screen.findByLabelText("Build server")).toBeInTheDocument();
		expect(await screen.findByLabelText("Registry")).toBeInTheDocument();
	});

	it("pins a build server, and goes back to automatic", async () => {
		backend();
		const form = await renderBuild(
			gitService({}, { buildLocation: "build_server" }),
		);
		const user = userEvent.setup();
		const pin = await screen.findByRole("combobox", { name: "Build server" });
		expect(pin).toHaveTextContent("Automatic");
		await user.click(pin);
		await user.click(await screen.findByRole("option", { name: "build-01" }));
		expect(form.draft().buildServerId).toBe("srv-b1");
		expect(
			screen.getByText(/Always builds here\. If it is offline or cannot build/),
		).toBeInTheDocument();
		await user.click(screen.getByRole("combobox", { name: "Build server" }));
		await user.click(
			await screen.findByRole("option", { name: "Automatic (sticky)" }),
		);
		expect(form.draft().buildServerId).toBe("");
	});

	it("has no build server pin for a service built on the app's server", async () => {
		backend();
		await renderBuild(gitService({}, { buildLocation: "target" }));
		await screen.findByText("Build location");
		expect(screen.queryByLabelText("Build server")).not.toBeInTheDocument();
	});

	it("warns when the pinned server is no longer a build server", async () => {
		backend({ servers: [wireServer()] });
		await renderBuild(
			gitService(
				{},
				{ buildLocation: "build_server", buildServerId: "srv-gone" },
			),
		);
		expect(
			await screen.findByText(/The pinned server is no longer a build server/),
		).toBeInTheDocument();
	});

	it("tells that no build server exists yet", async () => {
		backend({ servers: [] });
		await renderBuild(gitService({}, { buildLocation: "build_server" }));
		expect(await screen.findByText("No build server yet")).toBeInTheDocument();
	});
});

describe("BuildSection: registry", () => {
	it("warns that builds stay on the app's server when no registry is set above or here", async () => {
		backend();
		await renderBuild(gitService({}, { buildLocation: "build_server" }));
		expect(
			await screen.findByText(
				"No registry, so builds stay on the app's server",
			),
		).toBeInTheDocument();
	});

	it("picks a registry, which settles the warning", async () => {
		backend({
			registries: [
				wireRegistry({ id: "reg-1", name: "GHCR" }),
				wireRegistry({ id: "reg-2", name: "Quay", host: "quay.io" }),
			],
		});
		const form = await renderBuild(
			gitService({}, { buildLocation: "build_server" }),
		);
		const user = userEvent.setup();
		await screen.findByText("No registry, so builds stay on the app's server");
		await user.click(await screen.findByRole("combobox", { name: "Registry" }));
		await user.click(
			await screen.findByRole("option", { name: "Quay (quay.io/acme)" }),
		);
		expect(form.draft().registryId).toBe("reg-2");
		await waitFor(() =>
			expect(
				screen.queryByText("No registry, so builds stay on the app's server"),
			).not.toBeInTheDocument(),
		);
	});

	it("names the team's default registry for an empty choice", async () => {
		backend({ team: { buildLocation: "build_server", registryId: "reg-1" } });
		await renderBuild(gitService());
		await waitFor(() =>
			expect(
				screen.getByRole("combobox", { name: "Registry" }),
			).toHaveTextContent("Inherit (team default: GHCR)"),
		);
	});

	it("names the project's default registry before the team's", async () => {
		backend({
			projects: [wireProject({ registryId: "reg-2" })],
			registries: [
				wireRegistry({ id: "reg-1", name: "GHCR" }),
				wireRegistry({ id: "reg-2", name: "Quay", host: "quay.io" }),
			],
			team: { buildLocation: "build_server", registryId: "reg-1" },
		});
		await renderBuild(gitService({ projectId: "proj-1" }));
		await waitFor(() =>
			expect(
				screen.getByRole("combobox", { name: "Registry" }),
			).toHaveTextContent("Inherit (project default: Quay)"),
		);
	});

	it("says none is set above when neither project nor team has one", async () => {
		backend({ team: { buildLocation: "build_server", registryId: "" } });
		await renderBuild(gitService());
		await waitFor(() =>
			expect(
				screen.getByRole("combobox", { name: "Registry" }),
			).toHaveTextContent("Inherit (none set above)"),
		);
	});

	it("does not list registries for someone who may not read them", async () => {
		const api = backend({ scopes: ["services:read"] });
		await renderBuild(gitService({}, { buildLocation: "build_server" }));
		await screen.findByRole("combobox", { name: "Build server" });
		expect(screen.queryByLabelText("Registry")).not.toBeInTheDocument();
		expect(api.calls.some((c) => c.path === "/registries")).toBe(false);
	});
});

describe("BuildSection: where the next build runs", () => {
	it("reads the placement from the deployment plan", async () => {
		backend({
			plan: {
				body: wirePlan({
					location: "build_server",
					buildServerName: "build-01",
					registryHost: "ghcr.io/acme",
					platform: "linux/arm64",
					sticky: true,
				}),
			},
		});
		await renderBuild(gitService());
		const line = await screen.findByTestId("plan-placement");
		expect(line).toHaveTextContent(
			"Builds on build-01 (sticky), pushes to ghcr.io/acme, deploys to hetzner-01",
		);
		expect(line).toHaveTextContent("for linux/arm64");
	});

	it("shows the plan's warning under the line", async () => {
		backend({
			plan: {
				body: wirePlan({
					location: "target",
					fallback: true,
					warnings: [
						{
							code: "fallback",
							message: "build-01 is offline; built on the app's server.",
						},
					],
				}),
			},
		});
		await renderBuild(gitService());
		const line = await screen.findByTestId("plan-placement");
		expect(line).toHaveTextContent(
			"Builds and deploys on hetzner-01 (fallback)",
		);
		expect(
			within(line).getByText("build-01 is offline; built on the app's server."),
		).toBeInTheDocument();
	});

	it("reads the plan for the service's saved generation", async () => {
		const api = backend();
		await renderBuild(gitService({ id: "svc-plan" }));
		await screen.findByTestId("plan-placement");
		expect(
			api.calls.find((c) => c.path.endsWith("/deployments/plan")),
		).toMatchObject({
			method: "POST",
			path: "/services/svc-plan/deployments/plan",
		});
	});

	it("shows nothing when the plan cannot be made", async () => {
		backend({
			plan: {
				status: 409,
				body: {
					status: 409,
					code: "server_offline",
					title: "Conflict",
					detail: "offline",
				},
			},
		});
		await renderBuild(gitService());
		await screen.findByText("Build location");
		await waitFor(() =>
			expect(screen.queryByTestId("plan-placement")).not.toBeInTheDocument(),
		);
	});
});

describe("BuildSection: more options", () => {
	it("keeps submodules and the build context behind a closed group of two", async () => {
		backend();
		await renderBuild(gitService());
		const summary = await screen.findByText("More options (2)");
		expect(summary.closest("details")).not.toHaveAttribute("open");
	});

	it("shows submodules as automatic by default and explains each mode", async () => {
		backend();
		const form = await renderBuild(gitService());
		const select = await screen.findByRole("combobox", {
			name: "Git submodules",
		});
		expect(select).toHaveTextContent("Automatic");
		expect(
			screen.getByText("Checked out when the commit has a .gitmodules file."),
		).toBeInTheDocument();
		const user = userEvent.setup();
		await user.click(select);
		await user.click(
			await screen.findByRole("option", {
				name: "Always (a failure fails the build)",
			}),
		);
		expect(form.draft().submodules).toBe("on");
		expect(
			screen.getByText(
				"Checked out every time. A submodule that cannot be fetched fails the build.",
			),
		).toBeInTheDocument();
		await user.click(screen.getByRole("combobox", { name: "Git submodules" }));
		await user.click(await screen.findByRole("option", { name: "Never" }));
		expect(form.draft().submodules).toBe("off");
		expect(
			screen.getByText("Submodule folders stay empty."),
		).toBeInTheDocument();
	});

	it("starts from the saved submodule mode", async () => {
		backend();
		await renderBuild(gitService({}, { submodules: "off" }));
		expect(
			await screen.findByRole("combobox", { name: "Git submodules" }),
		).toHaveTextContent("Never");
	});

	it("writes the build context into the draft", async () => {
		backend();
		const form = await renderBuild(
			gitService({}, { buildContext: "apps/web" }),
		);
		const input = await screen.findByLabelText("Build context");
		expect(input).toHaveValue("apps/web");
		const user = userEvent.setup();
		await user.clear(input);
		await user.click(input);
		await user.paste("services/api");
		expect(form.draft().buildContext).toBe("services/api");
	});

	it("opens the group by itself when a field inside has an error", async () => {
		backend();
		await renderBuild(gitService(), {
			buildContext: "Build context must be relative.",
		});
		const summary = await screen.findByText("More options (2)");
		expect(summary.closest("details")).toHaveAttribute("open");
		expect(
			screen.getByText("Build context must be relative."),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Build context")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
	});

	it("opens the group for a submodule error too", async () => {
		backend();
		await renderBuild(gitService(), { submodules: "Pick a mode." });
		const summary = await screen.findByText("More options (2)");
		expect(summary.closest("details")).toHaveAttribute("open");
	});
});
