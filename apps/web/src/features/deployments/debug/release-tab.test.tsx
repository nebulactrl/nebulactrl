import { describe, expect, test } from "bun:test";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ApiError } from "#/api/client-config";
import type * as Gen from "#/api/gen/types.gen";
import type { DebugRelease } from "./queries";
import { ReleaseTab } from "./release-tab";

function release(overrides: Partial<DebugRelease> = {}): DebugRelease {
	return {
		id: "rel-9",
		deploymentId: "dep-1",
		serviceId: "svc-1",
		generation: 4,
		createdAt: "2026-10-31T10:00:00Z",
		imageRef: "registry.local/api:abc1234",
		config: { port: 8080, env: "masked" },
		source: {
			type: "git",
			gitUrl: "https://git.example/acme/api.git",
			branch: "main",
			commitSha: "abc1234def",
		},
		build: null,
		variables: [],
		variablesPruned: false,
		placement: {
			strategy: "single",
			targetServerId: "srv-1",
			targetServerName: "edge-1",
			buildServerId: null,
			buildServerName: "",
			registryId: null,
			registryRef: "",
			platform: "linux/amd64",
		},
		...overrides,
	};
}

function renderTab(props: Partial<Parameters<typeof ReleaseTab>[0]> = {}) {
	return render(
		<ReleaseTab
			release={undefined}
			pending={false}
			error={null}
			onRetry={() => {}}
			{...props}
		/>,
	);
}

/** The text of the <dd> that follows the <dt> with this label. */
function fact(label: string): string | undefined {
	const term = screen.queryByText(label, { selector: "dt" });
	return term?.nextElementSibling?.textContent ?? undefined;
}

describe("ReleaseTab states", () => {
	test("pending shows a busy placeholder", () => {
		const { container } = renderTab({ pending: true });
		expect(container.querySelector("[aria-busy]")).not.toBeNull();
		expect(screen.queryByText("Release")).not.toBeInTheDocument();
	});

	test("a 404 means the deployment has no release yet", () => {
		renderTab({ error: new ApiError(404, "not_found", "gone") });
		expect(screen.getByText("No release yet")).toBeInTheDocument();
		expect(
			screen.getByText(/may have been canceled while queued/),
		).toBeInTheDocument();
		expect(
			screen.queryByText("The release did not load"),
		).not.toBeInTheDocument();
	});

	test("any other error offers a retry that calls onRetry", async () => {
		let retries = 0;
		renderTab({
			error: new ApiError(500, "internal", "boom"),
			onRetry: () => {
				retries += 1;
			},
		});
		expect(screen.getByText("The release did not load")).toBeInTheDocument();

		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Try again" }));

		expect(retries).toBe(1);
	});

	test("no release and no error is also a load failure", () => {
		renderTab({ release: undefined });
		expect(screen.getByText("The release did not load")).toBeInTheDocument();
	});
});

describe("ReleaseTab content", () => {
	test("lists the release identity and its git source, leaving out facts it does not have", () => {
		renderTab({ release: release() });

		expect(fact("Release")).toBe("rel-9");
		expect(fact("Generation")).toBe("4");
		expect(fact("Image reference")).toBe("registry.local/api:abc1234");
		expect(fact("Type")).toBe("git");
		expect(fact("Git URL")).toBe("https://git.example/acme/api.git");
		expect(fact("Branch")).toBe("main");
		expect(fact("Commit")).toBe("abc1234def");
		expect(fact("Digest")).toBeUndefined();
		expect(fact("Image")).toBeUndefined();
		expect(fact("Build hash")).toBeUndefined();
	});

	test("without build info there is no Build section", () => {
		renderTab({ release: release({ build: null }) });
		expect(
			screen.queryByText("Build", { selector: "h2,h3,div,span,p" }),
		).toBeNull();
		expect(fact("Method")).toBeUndefined();
	});

	test("a build shows its method, builder, packages and numbered steps with their commands", () => {
		const build: Gen.BuildInfo = {
			method: "railpack",
			builder: "railpack 0.9",
			dockerfile: "Dockerfile.prod",
			reason: "No Dockerfile found",
			startCommand: "node server.js",
			providers: ["node", "python"],
			packages: [
				{ name: "node", version: "22.1" },
				{ name: "pnpm", version: "9.0" },
			],
			steps: [
				{ name: "install", commands: ["pnpm install", "pnpm prune"] },
				{ name: "build", commands: ["pnpm build"] },
			],
		};
		renderTab({ release: release({ build }) });

		expect(fact("Method")).toBe("railpack");
		expect(fact("Builder")).toBe("railpack 0.9");
		expect(fact("Dockerfile")).toBe("Dockerfile.prod");
		expect(fact("Why")).toBe("No Dockerfile found");
		expect(fact("Start command")).toBe("node server.js");
		expect(fact("Providers")).toBe("node, python");
		expect(fact("Packages")).toBe("node 22.1, pnpm 9.0");
		const steps = screen.getAllByRole("listitem");
		const install = steps.find((li) => li.textContent?.includes("install"));
		expect(install?.textContent).toContain("1");
		expect(install?.textContent).toContain("pnpm install");
		expect(install?.textContent).toContain("pnpm prune");
		const buildStep = steps.find((li) => li.textContent?.startsWith("2"));
		expect(buildStep?.textContent).toContain("pnpm build");
	});

	test("placement names the target and build servers", () => {
		renderTab({
			release: release({
				placement: {
					strategy: "build_server",
					targetServerId: "srv-1",
					targetServerName: "edge-1",
					buildServerId: "srv-b",
					buildServerName: "build-1",
					registryId: "reg-1",
					registryRef: "registry.local/api",
					platform: "linux/arm64",
				},
			}),
		});
		expect(fact("Strategy")).toBe("build_server");
		expect(fact("Target server")).toBe("edge-1");
		expect(fact("Build server")).toBe("build-1");
		expect(fact("Registry ref")).toBe("registry.local/api");
		expect(fact("Platform")).toBe("linux/arm64");
	});

	test("shows the frozen configuration as JSON", () => {
		renderTab({ release: release({ config: { port: 8080 } }) });
		const box = screen.getByRole("textbox", {
			name: "Configuration snapshot as JSON",
		}) as HTMLTextAreaElement;
		expect(JSON.parse(box.value)).toEqual({ port: 8080 });
	});
});

describe("ReleaseTab variables", () => {
	test("a release without variables says so", () => {
		renderTab({ release: release({ variables: [] }) });
		expect(screen.getByText("Variables (0)")).toBeInTheDocument();
		expect(
			screen.getByText("The release has no variables."),
		).toBeInTheDocument();
	});

	test("a pruned release says its keys are gone, even with an empty list", () => {
		renderTab({ release: release({ variables: [], variablesPruned: true }) });
		expect(screen.getByText(/left the rollback window/)).toBeInTheDocument();
		expect(
			screen.queryByText("The release has no variables."),
		).not.toBeInTheDocument();
	});

	test("lists keys only, each with a badge for what kind it is", () => {
		renderTab({
			release: release({
				variables: [
					{
						key: "PLAIN",
						scope: "runtime",
						secret: false,
						reference: false,
						platform: false,
						external: false,
					},
					{
						key: "DB_PASSWORD",
						scope: "runtime",
						secret: true,
						reference: false,
						platform: false,
						external: false,
					},
					{
						key: "API_URL",
						scope: "runtime",
						secret: false,
						reference: true,
						platform: false,
						external: false,
					},
					{
						key: "NEBULA_HOST",
						scope: "runtime",
						secret: false,
						reference: false,
						platform: true,
						external: false,
					},
					{
						key: "VAULT_TOKEN",
						scope: "runtime",
						secret: false,
						reference: false,
						platform: false,
						external: true,
					},
					{
						key: "NPM_TOKEN",
						scope: "build",
						secret: false,
						reference: false,
						platform: false,
						external: false,
					},
					{
						key: "SHARED",
						scope: "both",
						secret: false,
						reference: false,
						platform: false,
						external: false,
					},
				],
			}),
		});

		expect(screen.getByText("Variables (7)")).toBeInTheDocument();
		expect(
			screen.getByText("Keys only: a value is never shown here."),
		).toBeInTheDocument();
		const row = (key: string) =>
			within(screen.getByText(key).closest("li") as HTMLElement);
		expect(
			row("PLAIN").queryByText(/secret|reference|platform|build/),
		).toBeNull();
		expect(row("DB_PASSWORD").getByText("secret")).toBeInTheDocument();
		expect(row("API_URL").getByText("reference")).toBeInTheDocument();
		expect(row("NEBULA_HOST").getByText("platform")).toBeInTheDocument();
		expect(row("VAULT_TOKEN").getByText("secret manager")).toBeInTheDocument();
		expect(row("NPM_TOKEN").getByText("build only")).toBeInTheDocument();
		expect(row("SHARED").getByText("build and runtime")).toBeInTheDocument();
	});
});
