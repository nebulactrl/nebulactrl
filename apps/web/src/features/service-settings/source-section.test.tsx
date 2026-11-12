import { describe, expect, it } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type { Service } from "#/api/models";
import { makeService } from "#/features/canvas/lib/fixtures";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { type SettingsDraft, toDraft } from "./draft";
import type { DraftErrors } from "./fields";
import { NameRow, ResourcesSection, SourceSection } from "./source-section";

interface HarnessProps {
	service: Service;
	errors?: DraftErrors;
	dirty?: boolean;
	/** Receives every draft the form holds, so a test can read what was staged. */
	onDraft: (draft: SettingsDraft) => void;
	children: (props: {
		draft: SettingsDraft;
		errors: DraftErrors;
		set: <K extends keyof SettingsDraft>(
			key: K,
			value: SettingsDraft[K],
		) => void;
	}) => React.ReactNode;
}

/** A stateful form around a section: `set` merges a key into the draft. */
function Harness({ service, errors = {}, onDraft, children }: HarnessProps) {
	const [draft, setDraft] = useState(() => toDraft(service));
	onDraft(draft);
	return children({
		draft,
		errors,
		set: (key, value) => setDraft((prev) => ({ ...prev, [key]: value })),
	});
}

interface Drafted {
	/** The last draft the harness rendered. */
	draft(): SettingsDraft;
}

async function renderSource(
	service: Service,
	options: { errors?: DraftErrors; dirty?: boolean } = {},
): Promise<Drafted> {
	let latest = toDraft(service);
	await renderWithProviders(
		<Harness
			service={service}
			{...(options.errors ? { errors: options.errors } : {})}
			onDraft={(draft) => {
				latest = draft;
			}}
		>
			{(props) => (
				<SourceSection
					service={service}
					dirty={options.dirty ?? false}
					{...props}
				/>
			)}
		</Harness>,
	);
	// The git rows load the repository link and the accounts; let them settle.
	if (service.sourceType === "git") {
		await screen.findByText(/No provider is connected\./);
	}
	return { draft: () => latest };
}

/** The routes the git source rows read: no link, no connected account, no plan. */
function gitRoutes() {
	return {
		"GET /services/:serviceId/git": {
			body: {
				linked: false,
				autoDeploy: false,
				branch: "",
				connectionId: "",
				connectionName: "",
				queuedCommit: "",
				waitForCi: false,
				ciWarning: "",
				repo: "",
				repoUrl: "",
				watchPaths: null,
				webhook: { message: "", mode: "app", status: "active", url: "" },
			},
		},
		"GET /git/connections": { body: { items: [] } },
	};
}

function gitService(
	overrides: Partial<Service["config"]> = {},
	rest: Partial<Service> = {},
) {
	const { image: _image, ...base } = makeService({ sourceType: "git" }).config;
	return makeService({
		sourceType: "git",
		...rest,
		config: {
			...base,
			gitUrl: "https://github.com/acme/shop.git",
			gitBranch: "main",
			buildType: "auto",
			...overrides,
		},
	});
}

describe("SourceSection: image source", () => {
	it("shows the image and writes edits into the draft", async () => {
		const service = makeService({
			sourceType: "image",
			config: { ...makeService().config, image: "nginx:1.27" },
		});
		const form = await renderSource(service);
		const input = screen.getByLabelText("Image");
		expect(input).toHaveValue("nginx:1.27");

		const user = userEvent.setup();
		await user.clear(input);
		await user.click(input);
		await user.paste("ghcr.io/acme/app:2");
		expect(input).toHaveValue("ghcr.io/acme/app:2");
		expect(form.draft().image).toBe("ghcr.io/acme/app:2");
	});

	it("marks the image field invalid and shows its error", async () => {
		await renderSource(makeService({ sourceType: "image" }), {
			errors: { image: "Image is required." },
		});
		const input = screen.getByLabelText("Image");
		expect(input).toHaveAttribute("aria-invalid", "true");
		expect(screen.getByText("Image is required.")).toBeInTheDocument();
		expect(input).toHaveAccessibleDescription("Image is required.");
	});

	it("does not mark the image field invalid without an error", async () => {
		await renderSource(makeService({ sourceType: "image" }));
		expect(screen.getByLabelText("Image")).toHaveAttribute(
			"aria-invalid",
			"false",
		);
	});

	it("shows no git rows for an image", async () => {
		await renderSource(makeService({ sourceType: "image" }));
		expect(screen.queryByLabelText("Repository")).not.toBeInTheDocument();
		expect(screen.queryByLabelText("Git access token")).not.toBeInTheDocument();
		expect(screen.queryByText("Build plan")).not.toBeInTheDocument();
	});

	it("takes registry credentials as write-only fields that start empty", async () => {
		const form = await renderSource(makeService({ sourceType: "image" }));
		const username = screen.getByLabelText("Registry username");
		const password = screen.getByLabelText("Registry password");
		expect(username).toHaveValue("");
		expect(password).toHaveValue("");
		expect(username).toHaveAttribute("placeholder", "Username");
		expect(password).toHaveAttribute("placeholder", "Password or token");
		expect(password).toHaveAttribute("type", "password");
		expect(password).toHaveAttribute("autocomplete", "new-password");

		const user = userEvent.setup();
		await user.type(username, "robin");
		await user.type(password, "s3cret");
		expect(form.draft().registryUsername).toBe("robin");
		expect(form.draft().registryPassword).toBe("s3cret");
	});

	it("offers no way to remove credentials when none are stored", async () => {
		await renderSource(
			makeService({ sourceType: "image", registryAuthSet: false }),
		);
		expect(
			screen.queryByRole("checkbox", { name: "Remove the stored credentials" }),
		).not.toBeInTheDocument();
	});

	it("tells that credentials are stored without revealing them, and lets them be removed", async () => {
		const form = await renderSource(
			makeService({ sourceType: "image", registryAuthSet: true }),
		);
		expect(screen.getByLabelText("Registry username")).toHaveAttribute(
			"placeholder",
			"Set — type to replace",
		);
		expect(screen.getByLabelText("Registry password")).toHaveAttribute(
			"placeholder",
			"Set — type to replace",
		);
		expect(screen.getByLabelText("Registry password")).toHaveValue("");

		const clear = screen.getByRole("checkbox", {
			name: "Remove the stored credentials",
		});
		expect(clear).not.toBeChecked();
		const user = userEvent.setup();
		await user.click(clear);
		expect(clear).toBeChecked();
		expect(form.draft().clearRegistryAuth).toBe(true);
		await user.click(clear);
		expect(clear).not.toBeChecked();
		expect(form.draft().clearRegistryAuth).toBe(false);
	});

	it("keeps the registry steps closed while no credentials are stored", async () => {
		await renderSource(
			makeService({ sourceType: "image", registryAuthSet: false }),
		);
		expect(
			screen.getByText("How to create a registry token").closest("details"),
		).not.toHaveAttribute("open");
	});

	it("opens the registry steps for a service that has credentials stored", async () => {
		await renderSource(
			makeService({ sourceType: "image", registryAuthSet: true }),
		);
		expect(
			screen.getByText("How to create a registry token").closest("details"),
		).toHaveAttribute("open");
	});

	it("shows the setup guide for the registry the image comes from", async () => {
		const service = makeService({
			sourceType: "image",
			registryAuthSet: true,
			config: { ...makeService().config, image: "ghcr.io/acme/app:1" },
		});
		await renderSource(service);
		expect(
			screen.getByRole("region", { name: "GitHub credential setup" }),
		).toBeInTheDocument();
		expect(screen.getByText("read:packages")).toBeInTheDocument();
	});

	it("follows the image the user types for the registry guide", async () => {
		const service = makeService({
			sourceType: "image",
			registryAuthSet: true,
			config: { ...makeService().config, image: "nginx:1" },
		});
		await renderSource(service);
		expect(
			screen.getByRole("region", { name: "Docker Hub credential setup" }),
		).toBeInTheDocument();
		const user = userEvent.setup();
		const image = screen.getByLabelText("Image");
		await user.clear(image);
		await user.click(image);
		await user.paste("ghcr.io/acme/app:1");
		expect(
			screen.getByRole("region", { name: "GitHub credential setup" }),
		).toBeInTheDocument();
	});
});

describe("SourceSection: change source row", () => {
	it("offers a switch to Git for an image app", async () => {
		await renderSource(makeService({ kind: "app", sourceType: "image" }));
		expect(screen.getByText("Source type")).toBeInTheDocument();
		expect(screen.getByText("Switch to a Git repository.")).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Change source…" }),
		).toBeEnabled();
	});

	it("offers a switch to an image for a git worker", async () => {
		stubApi(gitRoutes());
		await renderSource(gitService({}, { kind: "worker" }));
		expect(screen.getByText("Switch to a Docker image.")).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Change source…" }),
		).toBeEnabled();
	});

	it("waits for a clean form: disabled with a reason while edits are unsaved", async () => {
		await renderSource(makeService({ sourceType: "image" }), { dirty: true });
		expect(
			screen.getByRole("button", { name: "Change source…" }),
		).toBeDisabled();
		expect(
			screen.getByText("Save or discard your edits first."),
		).toBeInTheDocument();
		expect(
			screen.queryByText("Switch to a Git repository."),
		).not.toBeInTheDocument();
	});

	it("is absent for services that cannot change source", async () => {
		await renderSource(makeService({ kind: "database", sourceType: "image" }));
		expect(screen.queryByText("Source type")).not.toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Change source…" }),
		).not.toBeInTheDocument();
	});

	it("opens the change source dialog", async () => {
		stubApi({ "GET /git/connections": { body: { items: [] } } });
		await renderSource(makeService({ sourceType: "image" }));
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Change source…" }));
		const dialog = await screen.findByRole("dialog");
		expect(within(dialog).getByText("Change source")).toBeInTheDocument();
		await within(dialog).findByLabelText("Repository URL");
	});
});

describe("SourceSection: git source", () => {
	it("shows the repository, branch and build method from the draft", async () => {
		stubApi(gitRoutes());
		await renderSource(
			gitService({ gitBranch: "release", buildType: "railpack" }),
		);
		expect(screen.getByLabelText("Repository")).toHaveValue(
			"https://github.com/acme/shop.git",
		);
		expect(screen.getByLabelText("Branch")).toHaveValue("release");
		expect(
			screen.getByRole("combobox", { name: "Build method" }),
		).toHaveTextContent("Railpack");
		expect(
			screen.getByText(
				"Builds without a Dockerfile and ignores one in the repository.",
			),
		).toBeInTheDocument();
		expect(screen.queryByLabelText("Image")).not.toBeInTheDocument();
	});

	it("writes repository and branch edits into the draft", async () => {
		stubApi(gitRoutes());
		const form = await renderSource(gitService());
		const user = userEvent.setup();
		const url = screen.getByLabelText("Repository");
		await user.clear(url);
		await user.click(url);
		await user.paste("https://gitlab.com/acme/api.git");
		const branch = screen.getByLabelText("Branch");
		await user.clear(branch);
		await user.type(branch, "develop");
		expect(form.draft().gitUrl).toBe("https://gitlab.com/acme/api.git");
		expect(form.draft().gitBranch).toBe("develop");
	});

	it("shows the field errors of the repository and branch", async () => {
		stubApi(gitRoutes());
		await renderSource(gitService(), {
			errors: {
				gitUrl: "Repository URL is required.",
				gitBranch: "Branch is required.",
			},
		});
		expect(screen.getByLabelText("Repository")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
		expect(screen.getByText("Repository URL is required.")).toBeInTheDocument();
		expect(screen.getByLabelText("Branch")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
		expect(screen.getByText("Branch is required.")).toBeInTheDocument();
	});

	it("changes the build method and the line that explains it", async () => {
		stubApi(gitRoutes());
		const form = await renderSource(gitService({ buildType: "auto" }));
		expect(
			screen.getByText(
				"Uses the Dockerfile when there is one, otherwise Railpack.",
			),
		).toBeInTheDocument();
		const user = userEvent.setup();
		await user.click(screen.getByRole("combobox", { name: "Build method" }));
		await user.click(
			await screen.findByRole("option", {
				name: "Starter Dockerfile · static site",
			}),
		);
		expect(form.draft().buildType).toBe("starter:static");
		expect(
			screen.getByText("Serves prebuilt files with nginx."),
		).toBeInTheDocument();
	});

	it("lists every build method", async () => {
		stubApi(gitRoutes());
		await renderSource(gitService());
		await userEvent
			.setup()
			.click(screen.getByRole("combobox", { name: "Build method" }));
		const options = (await screen.findAllByRole("option")).map(
			(o) => o.textContent,
		);
		expect(options).toEqual([
			"Automatic (Dockerfile, else Railpack)",
			"Dockerfile",
			"Railpack",
			"Starter Dockerfile · Node.js",
			"Starter Dockerfile · Python",
			"Starter Dockerfile · Go",
			"Starter Dockerfile · Ruby",
			"Starter Dockerfile · PHP",
			"Starter Dockerfile · static site",
		]);
	});

	it("includes the build plan preview, blocked while the form is dirty", async () => {
		stubApi(gitRoutes());
		await renderSource(gitService(), { dirty: true });
		expect(screen.getByText("Build plan")).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Preview build plan" }),
		).toBeDisabled();
		// The repository link and the source switch are blocked by the same dirty form.
		expect(
			screen.getByRole("button", { name: "Change source…" }),
		).toBeDisabled();
	});

	it("points to the integrations settings when no git provider is connected", async () => {
		stubApi(gitRoutes());
		await renderSource(gitService());
		expect(
			screen.getByRole("link", { name: /Connect GitHub, GitLab/ }),
		).toHaveAttribute("href", "/settings/integrations");
	});

	it("takes the access token as a write-only password field", async () => {
		stubApi(gitRoutes());
		const form = await renderSource(gitService({}, { gitTokenSet: false }));
		const token = screen.getByLabelText("Git access token");
		expect(token).toHaveAttribute("type", "password");
		expect(token).toHaveAttribute("placeholder", "Not set");
		expect(token).toHaveValue("");
		expect(
			screen.queryByRole("checkbox", { name: "Remove the stored token" }),
		).not.toBeInTheDocument();
		await userEvent.setup().type(token, "ghp_abc");
		expect(form.draft().gitToken).toBe("ghp_abc");
	});

	it("says a token is stored without showing it, and lets it be cleared", async () => {
		stubApi(gitRoutes());
		const form = await renderSource(gitService({}, { gitTokenSet: true }));
		const token = screen.getByLabelText("Git access token");
		expect(token).toHaveAttribute("placeholder", "Set — type to replace");
		expect(token).toHaveValue("");
		const clear = screen.getByRole("checkbox", {
			name: "Remove the stored token",
		});
		expect(clear).not.toBeChecked();
		const user = userEvent.setup();
		await user.click(clear);
		expect(clear).toBeChecked();
		expect(form.draft().clearGitToken).toBe(true);
		await user.click(clear);
		expect(form.draft().clearGitToken).toBe(false);
	});

	it("shows the provider's token guide for the repository's host", async () => {
		stubApi(gitRoutes());
		await renderSource(
			gitService({ gitUrl: "https://github.com/acme/shop.git" }),
		);
		expect(
			screen.getByRole("region", { name: "GitHub credential setup" }),
		).toBeInTheDocument();
	});

	it("gives a generic guide without a link for a host it does not know", async () => {
		stubApi(gitRoutes());
		await renderSource(
			gitService({ gitUrl: "https://git.example.internal/acme/shop.git" }),
		);
		const guide = screen.getByRole("region", { name: "Git credential setup" });
		expect(guide).toHaveTextContent(
			"cannot tell which software runs git.example.internal",
		);
		expect(within(guide).queryByRole("link")).not.toBeInTheDocument();
		expect(screen.getByLabelText("Git access token")).toBeInTheDocument();
	});

	it("swaps the token for the deploy key rows when the repository is an SSH URL", async () => {
		const service = gitService({ gitUrl: "git@github.com:acme/shop.git" });
		const api = stubApi({
			...gitRoutes(),
			"GET /services/:serviceId/ssh": {
				body: { usesSshUrl: true, hostKeys: [], host: "github.com" },
			},
		});
		await renderSource(service);
		expect(
			await screen.findByText(/An SSH URL clones with a key pair/),
		).toBeInTheDocument();
		expect(screen.queryByLabelText("Git access token")).not.toBeInTheDocument();
		expect(api.calls.map((c) => `${c.method} ${c.path}`)).toContain(
			`GET /services/${service.id}/ssh`,
		);
		expect(api.unmatched).toEqual([]);
	});

	it("switches from the token to the deploy key as the URL becomes an SSH URL", async () => {
		stubApi({
			...gitRoutes(),
			"GET /services/:serviceId/ssh": {
				body: { usesSshUrl: true, hostKeys: [] },
			},
		});
		await renderSource(gitService());
		expect(screen.getByLabelText("Git access token")).toBeInTheDocument();
		const user = userEvent.setup();
		const url = screen.getByLabelText("Repository");
		await user.clear(url);
		await user.click(url);
		await user.paste("ssh://git@github.com/acme/shop.git");
		expect(screen.queryByLabelText("Git access token")).not.toBeInTheDocument();
		expect(
			await screen.findByText(/An SSH URL clones with a key pair/),
		).toBeInTheDocument();
	});
});

describe("ResourcesSection", () => {
	async function renderResources(errors: DraftErrors = {}) {
		let latest = toDraft(makeService());
		await renderWithProviders(
			<Harness
				service={makeService({
					config: { ...makeService().config, memoryMb: 512, cpus: 0.5 },
				})}
				errors={errors}
				onDraft={(draft) => {
					latest = draft;
				}}
			>
				{(props) => <ResourcesSection {...props} />}
			</Harness>,
		);
		return () => latest;
	}

	it("shows the limits as typed text", async () => {
		await renderResources();
		expect(screen.getByLabelText("Memory (MB)")).toHaveValue("512");
		expect(screen.getByLabelText("CPUs")).toHaveValue("0.5");
		expect(screen.getByText("Applies on next deploy")).toBeInTheDocument();
	});

	it("writes edits into the draft as text, so a half-typed number survives", async () => {
		const draft = await renderResources();
		const user = userEvent.setup();
		const cpus = screen.getByLabelText("CPUs");
		await user.clear(cpus);
		await user.type(cpus, "1.");
		expect(cpus).toHaveValue("1.");
		expect(draft().cpus).toBe("1.");
		const memory = screen.getByLabelText("Memory (MB)");
		await user.clear(memory);
		expect(draft().memoryMb).toBe("");
		await user.type(memory, "2048");
		expect(draft().memoryMb).toBe("2048");
	});

	it("shows an error per limit", async () => {
		await renderResources({
			memoryMb: "Memory must be a whole number.",
			cpus: "CPUs must be a number.",
		});
		expect(screen.getByLabelText("Memory (MB)")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
		expect(
			screen.getByText("Memory must be a whole number."),
		).toBeInTheDocument();
		expect(screen.getByLabelText("CPUs")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
		expect(screen.getByText("CPUs must be a number.")).toBeInTheDocument();
	});

	it("shows the empty limits as unlimited", async () => {
		await renderWithProviders(
			<Harness service={makeService()} onDraft={() => {}}>
				{(props) => <ResourcesSection {...props} />}
			</Harness>,
		);
		expect(screen.getByLabelText("Memory (MB)")).toHaveAttribute(
			"placeholder",
			"unlimited",
		);
		expect(screen.getByLabelText("CPUs")).toHaveValue("");
	});
});

describe("NameRow", () => {
	it("edits the service name in the draft", async () => {
		let latest = toDraft(makeService());
		const service = makeService({ name: "api" });
		await renderWithProviders(
			<Harness
				service={service}
				onDraft={(draft) => {
					latest = draft;
				}}
			>
				{(props) => <NameRow {...props} />}
			</Harness>,
		);
		const name = screen.getByLabelText("Name");
		expect(name).toHaveValue("api");
		await userEvent.setup().type(name, "-v2");
		expect(name).toHaveValue("api-v2");
		expect(latest.name).toBe("api-v2");
	});

	it("shows the name error", async () => {
		await renderWithProviders(
			<Harness
				service={makeService({ name: "" })}
				errors={{ name: "Name is required." }}
				onDraft={() => {}}
			>
				{(props) => <NameRow {...props} />}
			</Harness>,
		);
		expect(screen.getByLabelText("Name")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
		expect(screen.getByText("Name is required.")).toBeInTheDocument();
	});
});
