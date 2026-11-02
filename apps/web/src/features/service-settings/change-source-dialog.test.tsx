import { describe, expect, it } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type { ConnectionView, Repo } from "#/api/gen/types.gen";
import type { Service } from "#/api/models";
import { makeService } from "#/features/canvas/lib/fixtures";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { ChangeSourceDialog } from "./change-source-dialog";

const imageService = () =>
	makeService({
		id: "svc-img",
		name: "shop",
		sourceType: "image",
		generation: 7,
	});
const gitService = () =>
	makeService({
		id: "svc-git",
		name: "api",
		sourceType: "git",
		generation: 3,
	});

function connection(overrides: Partial<ConnectionView> = {}): ConnectionView {
	return {
		id: "conn-1",
		name: "acme-github",
		provider: "github",
		status: "active",
		authType: "github_app",
		accountLogin: "acme",
		baseUrl: "",
		canListRepos: true,
		createdAt: "2026-01-01T00:00:00Z",
		updatedAt: "2026-01-01T00:00:00Z",
		github: {
			appId: 0,
			appSlug: "",
			appUrl: "",
			installUrl: "",
			installationId: 0,
		},
		manualWebhook: false,
		oauth: { authorized: false, clientId: "", redirectUri: "" },
		serviceCount: 0,
		statusMessage: "",
		supportsPreviews: true,
		webhookUrl: "",
		...overrides,
	};
}

function repo(overrides: Partial<Repo> = {}): Repo {
	return {
		fullName: "acme/shop",
		cloneUrl: "https://github.com/acme/shop.git",
		defaultBranch: "main",
		description: "The shop",
		htmlUrl: "https://github.com/acme/shop",
		private: false,
		archived: false,
		...overrides,
	};
}

interface StageRequest {
	ifMatch: string | null;
	body: unknown;
}

/** Routes for the dialog: the accounts it lists and the PUT /source it sends. */
function dialogRoutes(
	options: {
		connections?: ConnectionView[];
		repos?: Repo[];
		branches?: string[];
		stage?: (request: Request) => Response | Promise<Response>;
		link?: (request: Request) => Response | Promise<Response>;
		seen?: StageRequest[];
	} = {},
) {
	return {
		"GET /git/connections": { body: { items: options.connections ?? [] } },
		"GET /git/connections/:connectionId/repos": {
			body: { items: options.repos ?? [repo()] },
		},
		"GET /git/connections/:connectionId/branches": {
			body: { items: options.branches ?? ["main", "develop"] },
		},
		"PUT /services/:serviceId/source": async (request: Request) => {
			options.seen?.push({
				ifMatch: request.headers.get("If-Match"),
				body: await request.clone().json(),
			});
			return options.stage?.(request) ?? Response.json({}, { status: 200 });
		},
		"PUT /services/:serviceId/git": (request: Request) =>
			options.link?.(request) ?? Response.json({}, { status: 200 }),
	};
}

const problem = (status: number, code: string, detail: string) =>
	Response.json(
		{ status, code, title: "Error", detail },
		{ status, headers: { "Content-Type": "application/problem+json" } },
	);

async function renderDialog(service: Service) {
	const closed: boolean[] = [];
	await renderWithProviders(
		<ChangeSourceDialog
			service={service}
			open
			onOpenChange={(open) => closed.push(open)}
		/>,
	);
	return { closed };
}

const STAGE = { name: "Stage change" };

function stagePuts(api: { calls: { method: string; path: string }[] }) {
	return api.calls.filter((c) => c.method === "PUT");
}

describe("ChangeSourceDialog: the source choice", () => {
	it("for an image service offers Git only, with Image marked current", async () => {
		stubApi(dialogRoutes());
		await renderDialog(imageService());
		const group = screen.getByRole("group", { name: "New source" });
		const image = within(group).getByRole("button", {
			name: "Image (current)",
		});
		const git = within(group).getByRole("button", { name: "Git" });
		expect(image).toBeDisabled();
		expect(git).toHaveAttribute("aria-pressed", "true");
		expect(await screen.findByLabelText("Repository URL")).toBeInTheDocument();
		expect(screen.queryByLabelText("Image")).not.toBeInTheDocument();
	});

	it("for a git service offers an image, with Git marked current", async () => {
		stubApi(dialogRoutes());
		await renderDialog(gitService());
		const group = screen.getByRole("group", { name: "New source" });
		expect(
			within(group).getByRole("button", { name: "Git (current)" }),
		).toBeDisabled();
		expect(
			within(group).getByRole("button", { name: "Image" }),
		).toHaveAttribute("aria-pressed", "true");
		expect(screen.getByLabelText("Image")).toBeInTheDocument();
		expect(screen.queryByLabelText("Repository URL")).not.toBeInTheDocument();
	});

	it("says what stays and when the change applies", async () => {
		stubApi(dialogRoutes());
		await renderDialog(gitService());
		expect(
			screen.getByText(
				"Domains, variables and volumes stay. Applies on the next deploy.",
			),
		).toBeInTheDocument();
	});
});

describe("ChangeSourceDialog: switching to an image", () => {
	it("keeps Stage change disabled until an image is entered", async () => {
		const api = stubApi(dialogRoutes());
		await renderDialog(gitService());
		const stage = screen.getByRole("button", STAGE);
		expect(stage).toBeDisabled();
		const user = userEvent.setup();
		await user.click(screen.getByLabelText("Image"));
		await user.paste("   ");
		expect(stage).toBeDisabled();
		await user.paste("ghcr.io/acme/app:1.4");
		expect(stage).toBeEnabled();
		expect(stagePuts(api)).toEqual([]);
	});

	it("does not stage on Enter while the image is blank", async () => {
		const api = stubApi(dialogRoutes());
		await renderDialog(gitService());
		const user = userEvent.setup();
		await user.click(screen.getByLabelText("Image"));
		await user.keyboard("{Enter}");
		expect(stagePuts(api)).toEqual([]);
	});

	it("stages the trimmed image with If-Match, then reports success and closes", async () => {
		const seen: StageRequest[] = [];
		const api = stubApi(dialogRoutes({ seen }));
		const { closed } = await renderDialog(gitService());
		const user = userEvent.setup();
		await user.click(screen.getByLabelText("Image"));
		await user.paste("  ghcr.io/acme/app:1.4  ");
		await user.click(screen.getByRole("button", STAGE));

		expect(await screen.findByText("Source change staged")).toBeInTheDocument();
		expect(screen.getByText("Deploy to apply it.")).toBeInTheDocument();
		expect(seen).toEqual([
			{
				ifMatch: '"3"',
				body: { sourceType: "image", image: "ghcr.io/acme/app:1.4" },
			},
		]);
		expect(
			api.calls.filter((c) => c.method === "PUT").map((c) => c.path),
		).toEqual(["/services/svc-git/source"]);
		expect(closed).toEqual([false]);
		expect(api.unmatched).toEqual([]);
	});

	it("sends registry credentials only when some were typed, trimming the username but not the password", async () => {
		const seen: StageRequest[] = [];
		stubApi(dialogRoutes({ seen }));
		await renderDialog(gitService());
		const user = userEvent.setup();
		await user.click(screen.getByLabelText("Image"));
		await user.paste("registry.example.com/app:1");
		await user.click(screen.getByLabelText("Registry username"));
		await user.paste("  robin ");
		await user.click(screen.getByLabelText("Registry password"));
		await user.paste(" p@ss ");
		await user.click(screen.getByRole("button", STAGE));
		await screen.findByText("Source change staged");
		expect(seen[0]?.body).toEqual({
			sourceType: "image",
			image: "registry.example.com/app:1",
			registryAuth: { username: "robin", password: " p@ss " },
		});
	});

	it("sends registry credentials when only a password was typed", async () => {
		const seen: StageRequest[] = [];
		stubApi(dialogRoutes({ seen }));
		await renderDialog(gitService());
		const user = userEvent.setup();
		await user.click(screen.getByLabelText("Image"));
		await user.paste("registry.example.com/app:1");
		await user.click(screen.getByLabelText("Registry password"));
		await user.paste("token");
		await user.click(screen.getByRole("button", STAGE));
		await screen.findByText("Source change staged");
		expect(seen[0]?.body).toMatchObject({
			registryAuth: { username: "", password: "token" },
		});
	});

	it("keeps the registry password in a password field", async () => {
		stubApi(dialogRoutes());
		await renderDialog(gitService());
		expect(screen.getByLabelText("Registry password")).toHaveAttribute(
			"type",
			"password",
		);
	});

	it("shows the registry setup guide only once an image is entered", async () => {
		stubApi(dialogRoutes());
		await renderDialog(gitService());
		expect(
			screen.queryByRole("region", { name: /credential setup/ }),
		).not.toBeInTheDocument();
		const user = userEvent.setup();
		await user.click(screen.getByLabelText("Image"));
		await user.paste("ghcr.io/acme/app:1");
		expect(
			screen.getByRole("region", { name: "GitHub credential setup" }),
		).toBeInTheDocument();
	});
});

describe("ChangeSourceDialog: switching to a repository by URL", () => {
	async function openUrlForm(service = imageService()) {
		const view = await renderDialog(service);
		const url = await screen.findByLabelText("Repository URL");
		return { ...view, url };
	}

	it("starts with branch main and the automatic build method", async () => {
		stubApi(dialogRoutes());
		await openUrlForm();
		expect(screen.getByLabelText("Branch")).toHaveValue("main");
		expect(
			screen.getByRole("combobox", { name: "Build method" }),
		).toHaveTextContent("Automatic");
		expect(
			screen.getByText(
				"Uses the Dockerfile when there is one, otherwise Railpack.",
			),
		).toBeInTheDocument();
		expect(screen.getByRole("button", STAGE)).toBeDisabled();
	});

	it("needs both a URL and a branch", async () => {
		stubApi(dialogRoutes());
		const { url } = await openUrlForm();
		const user = userEvent.setup();
		await user.click(url);
		await user.paste("https://github.com/acme/shop.git");
		expect(screen.getByRole("button", STAGE)).toBeEnabled();
		await user.clear(screen.getByLabelText("Branch"));
		expect(screen.getByRole("button", STAGE)).toBeDisabled();
		await user.click(screen.getByLabelText("Branch"));
		await user.paste("  ");
		expect(screen.getByRole("button", STAGE)).toBeDisabled();
	});

	it("stages the repository, branch, build method and token", async () => {
		const seen: StageRequest[] = [];
		const api = stubApi(dialogRoutes({ seen }));
		const { url, closed } = await openUrlForm();
		const user = userEvent.setup();
		await user.click(url);
		await user.paste("  https://github.com/acme/shop.git ");
		const branch = screen.getByLabelText("Branch");
		await user.clear(branch);
		await user.paste(" release ");
		await user.click(screen.getByLabelText("Access token (private)"));
		await user.paste("ghp_secret");
		await user.click(screen.getByRole("combobox", { name: "Build method" }));
		await user.click(await screen.findByRole("option", { name: "Dockerfile" }));
		expect(
			screen.getByText("Always builds the Dockerfile; fails if it is missing."),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", STAGE));

		expect(await screen.findByText("Source change staged")).toBeInTheDocument();
		expect(seen).toEqual([
			{
				ifMatch: '"7"',
				body: {
					sourceType: "git",
					gitUrl: "https://github.com/acme/shop.git",
					gitBranch: "release",
					buildType: "dockerfile",
					gitToken: "ghp_secret",
				},
			},
		]);
		// A pasted URL is not a connected-account pick: no repository link is made.
		expect(api.calls.filter((c) => c.path.endsWith("/git"))).toEqual([]);
		expect(closed).toEqual([false]);
	});

	it("omits the token when none was typed", async () => {
		const seen: StageRequest[] = [];
		stubApi(dialogRoutes({ seen }));
		const { url } = await openUrlForm();
		const user = userEvent.setup();
		await user.click(url);
		await user.paste("https://github.com/acme/shop.git");
		await user.click(screen.getByRole("button", STAGE));
		await screen.findByText("Source change staged");
		expect(seen[0]?.body).toEqual({
			sourceType: "git",
			gitUrl: "https://github.com/acme/shop.git",
			gitBranch: "main",
			buildType: "auto",
		});
	});

	it("replaces the token field with nothing for an SSH URL and never sends a token", async () => {
		const seen: StageRequest[] = [];
		stubApi(dialogRoutes({ seen }));
		const { url } = await openUrlForm();
		const user = userEvent.setup();
		await user.click(url);
		await user.paste("https://github.com/acme/shop.git");
		await user.click(screen.getByLabelText("Access token (private)"));
		await user.paste("ghp_typed_before_switching");
		await user.clear(url);
		await user.paste("git@github.com:acme/shop.git");
		expect(
			screen.queryByLabelText("Access token (private)"),
		).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", STAGE));
		await screen.findByText("Source change staged");
		expect(seen[0]?.body).toEqual({
			sourceType: "git",
			gitUrl: "git@github.com:acme/shop.git",
			gitBranch: "main",
			buildType: "auto",
		});
	});

	it("shows the token guide of the repository's host", async () => {
		stubApi(dialogRoutes());
		const { url } = await openUrlForm();
		expect(
			screen.queryByRole("region", { name: /credential setup/ }),
		).not.toBeInTheDocument();
		const user = userEvent.setup();
		await user.click(url);
		await user.paste("https://github.com/acme/shop.git");
		expect(
			screen.getByRole("region", { name: "GitHub credential setup" }),
		).toBeInTheDocument();
	});

	it("also works from a worker", async () => {
		const seen: StageRequest[] = [];
		stubApi(dialogRoutes({ seen }));
		const { url } = await openUrlForm(
			makeService({ id: "w", kind: "worker", sourceType: "image" }),
		);
		const user = userEvent.setup();
		await user.click(url);
		await user.paste("https://github.com/acme/jobs.git");
		await user.click(screen.getByRole("button", STAGE));
		await screen.findByText("Source change staged");
		expect(seen[0]?.body).toMatchObject({ sourceType: "git" });
	});
});

/** Holds the open state, with a button to open the dialog again. */
function ReopenHost({ service }: { service: Service }) {
	const [open, setOpen] = useState(true);
	return (
		<>
			<button type="button" onClick={() => setOpen(true)}>
				Reopen
			</button>
			<ChangeSourceDialog
				service={service}
				open={open}
				onOpenChange={setOpen}
			/>
		</>
	);
}

describe("ChangeSourceDialog: failures and progress", () => {
	it("shows the API's reason, stays open and sends no toast", async () => {
		const seen: StageRequest[] = [];
		stubApi(
			dialogRoutes({
				seen,
				stage: () =>
					problem(
						422,
						"invalid_config",
						"The repository could not be reached.",
					),
			}),
		);
		const { closed } = await renderDialog(gitService());
		const user = userEvent.setup();
		await user.click(screen.getByLabelText("Image"));
		await user.paste("nginx");
		await user.click(screen.getByRole("button", STAGE));

		expect(
			await screen.findByText("The repository could not be reached."),
		).toBeInTheDocument();
		expect(closed).toEqual([]);
		expect(screen.queryByText("Source change staged")).not.toBeInTheDocument();
		// The form can be corrected and sent again.
		expect(screen.getByRole("button", STAGE)).toBeEnabled();
		expect(screen.getByLabelText("Image")).toHaveValue("nginx");
	});

	it("shows a conflict when the service changed under the dialog", async () => {
		stubApi(
			dialogRoutes({
				stage: () =>
					problem(
						412,
						"generation_conflict",
						"The service was changed by someone else.",
					),
			}),
		);
		const { closed } = await renderDialog(gitService());
		const user = userEvent.setup();
		await user.click(screen.getByLabelText("Image"));
		await user.paste("nginx");
		await user.click(screen.getByRole("button", STAGE));
		expect(
			await screen.findByText("The service was changed by someone else."),
		).toBeInTheDocument();
		expect(closed).toEqual([]);
	});

	it("clears the error when the request is sent again", async () => {
		let attempt = 0;
		stubApi(
			dialogRoutes({
				stage: () => {
					attempt += 1;
					return attempt === 1
						? problem(422, "invalid_config", "Image not found.")
						: Response.json({}, { status: 200 });
				},
			}),
		);
		const { closed } = await renderDialog(gitService());
		const user = userEvent.setup();
		await user.click(screen.getByLabelText("Image"));
		await user.paste("nginx");
		await user.click(screen.getByRole("button", STAGE));
		expect(await screen.findByText("Image not found.")).toBeInTheDocument();
		await user.click(screen.getByRole("button", STAGE));
		expect(await screen.findByText("Source change staged")).toBeInTheDocument();
		expect(screen.queryByText("Image not found.")).not.toBeInTheDocument();
		expect(closed).toEqual([false]);
	});

	it("disables Stage change while the request is out and sends it only once", async () => {
		const { promise, resolve } = Promise.withResolvers<Response>();
		const seen: StageRequest[] = [];
		stubApi(dialogRoutes({ seen, stage: () => promise }));
		await renderDialog(gitService());
		const user = userEvent.setup();
		const image = screen.getByLabelText("Image");
		await user.click(image);
		await user.paste("nginx");
		await user.click(screen.getByRole("button", STAGE));
		await waitFor(() => expect(seen).toHaveLength(1));
		// While pending the button also holds a spinner labelled "Loading".
		expect(
			screen.getByRole("button", { name: "Loading Stage change" }),
		).toBeDisabled();
		// Enter in the field submits the form, but a request is already out.
		await user.keyboard("{Enter}");
		expect(seen).toHaveLength(1);
		resolve(Response.json({}, { status: 200 }));
		expect(await screen.findByText("Source change staged")).toBeInTheDocument();
		expect(seen).toHaveLength(1);
	});

	it("Cancel closes without a request", async () => {
		const api = stubApi(dialogRoutes());
		const { closed } = await renderDialog(gitService());
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Cancel" }));
		expect(closed).toEqual([false]);
		expect(stagePuts(api)).toEqual([]);
	});

	it("starts clean every time it opens", async () => {
		stubApi(dialogRoutes());
		await renderWithProviders(<ReopenHost service={gitService()} />);
		const user = userEvent.setup();
		await user.click(screen.getByLabelText("Image"));
		await user.paste("half-typed");
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
		);
		await user.click(screen.getByRole("button", { name: "Reopen" }));
		expect(await screen.findByLabelText("Image")).toHaveValue("");
	});
});

describe("ChangeSourceDialog: connected accounts", () => {
	const account = connection();

	it("offers the account picker first when an active account exists", async () => {
		stubApi(dialogRoutes({ connections: [account] }));
		await renderDialog(imageService());
		const mode = await screen.findByRole("group", {
			name: "Where the repository comes from",
		});
		expect(
			within(mode).getByRole("button", { name: "Connected account" }),
		).toHaveAttribute("aria-pressed", "true");
		expect(await screen.findByText("acme-github")).toBeInTheDocument();
		expect(await screen.findByText("acme/shop")).toBeInTheDocument();
		expect(screen.queryByLabelText("Repository URL")).not.toBeInTheDocument();
		expect(screen.getByRole("button", STAGE)).toBeDisabled();
	});

	it("ignores accounts that are not active", async () => {
		stubApi(
			dialogRoutes({
				connections: [
					connection({ status: "pending" }),
					connection({ id: "c2", status: "error" }),
				],
			}),
		);
		await renderDialog(imageService());
		expect(await screen.findByLabelText("Repository URL")).toBeInTheDocument();
		expect(
			screen.queryByRole("group", { name: "Where the repository comes from" }),
		).not.toBeInTheDocument();
	});

	it("stages the picked repository's clone URL and branch, then links it for deploy on push", async () => {
		const seen: StageRequest[] = [];
		const api = stubApi(dialogRoutes({ connections: [account], seen }));
		const { closed } = await renderDialog(imageService());
		const user = userEvent.setup();
		await user.click(await screen.findByRole("button", { name: /acme\/shop/ }));
		const stage = screen.getByRole("button", STAGE);
		await waitFor(() => expect(stage).toBeEnabled());
		await user.click(stage);

		expect(await screen.findByText("Source change staged")).toBeInTheDocument();
		expect(seen).toEqual([
			{
				ifMatch: '"7"',
				body: {
					sourceType: "git",
					gitUrl: "https://github.com/acme/shop.git",
					gitBranch: "main",
					buildType: "auto",
				},
			},
		]);
		const link = api.calls.find((c) => c.path === "/services/svc-img/git");
		expect(link).toMatchObject({
			method: "PUT",
			body: { connectionId: "conn-1", repo: "acme/shop", branch: "main" },
		});
		// The stage goes first: linking a service that is still an image would be refused.
		const puts = api.calls.filter((c) => c.method === "PUT").map((c) => c.path);
		expect(puts).toEqual(["/services/svc-img/source", "/services/svc-img/git"]);
		expect(closed).toEqual([false]);
		expect(
			screen.queryByText(/has no repository link/),
		).not.toBeInTheDocument();
	});

	it("builds the repository with the branch the user picked", async () => {
		const seen: StageRequest[] = [];
		stubApi(dialogRoutes({ connections: [account], seen }));
		await renderDialog(imageService());
		const user = userEvent.setup();
		await user.click(await screen.findByRole("button", { name: /acme\/shop/ }));
		await user.click(await screen.findByRole("combobox", { name: "Branch" }));
		await user.click(await screen.findByRole("option", { name: "develop" }));
		await user.click(screen.getByRole("button", STAGE));
		await screen.findByText("Source change staged");
		expect(seen[0]?.body).toMatchObject({ gitBranch: "develop" });
	});

	it("keeps the staged change but warns when the repository link fails", async () => {
		const api = stubApi(
			dialogRoutes({
				connections: [account],
				link: () =>
					problem(403, "forbidden", "The app is not installed on acme/shop."),
			}),
		);
		const { closed } = await renderDialog(imageService());
		const user = userEvent.setup();
		await user.click(await screen.findByRole("button", { name: /acme\/shop/ }));
		const stage = screen.getByRole("button", STAGE);
		await waitFor(() => expect(stage).toBeEnabled());
		await user.click(stage);

		expect(
			await screen.findByText("shop has no repository link"),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				"Deploy on push is off: The app is not installed on acme/shop.",
			),
		).toBeInTheDocument();
		expect(await screen.findByText("Source change staged")).toBeInTheDocument();
		expect(closed).toEqual([false]);
		expect(api.calls.filter((c) => c.path.endsWith("/source"))).toHaveLength(1);
	});

	it("does not link when the stage itself fails", async () => {
		const api = stubApi(
			dialogRoutes({
				connections: [account],
				stage: () => problem(422, "invalid_config", "Repository not found."),
			}),
		);
		const { closed } = await renderDialog(imageService());
		const user = userEvent.setup();
		await user.click(await screen.findByRole("button", { name: /acme\/shop/ }));
		const stage = screen.getByRole("button", STAGE);
		await waitFor(() => expect(stage).toBeEnabled());
		await user.click(stage);
		expect(
			await screen.findByText("Repository not found."),
		).toBeInTheDocument();
		expect(api.calls.some((c) => c.path.endsWith("/git"))).toBe(false);
		expect(closed).toEqual([]);
	});

	it("switches to a pasted URL, which stages without a repository link", async () => {
		const seen: StageRequest[] = [];
		const api = stubApi(dialogRoutes({ connections: [account], seen }));
		await renderDialog(imageService());
		const user = userEvent.setup();
		await user.click(await screen.findByRole("button", { name: /acme\/shop/ }));
		await user.click(screen.getByRole("button", { name: "Any URL" }));
		await user.click(await screen.findByLabelText("Repository URL"));
		await user.paste("https://git.example.com/acme/shop.git");
		await user.click(screen.getByRole("button", STAGE));
		await screen.findByText("Source change staged");
		expect(seen[0]?.body).toEqual({
			sourceType: "git",
			gitUrl: "https://git.example.com/acme/shop.git",
			gitBranch: "main",
			buildType: "auto",
		});
		expect(api.calls.some((c) => c.path.endsWith("/git"))).toBe(false);
	});

	it("needs a repository from the account before it can stage, even with a URL typed in the other mode", async () => {
		stubApi(dialogRoutes({ connections: [account] }));
		await renderDialog(imageService());
		const user = userEvent.setup();
		await user.click(await screen.findByRole("button", { name: "Any URL" }));
		await user.click(await screen.findByLabelText("Repository URL"));
		await user.paste("https://git.example.com/acme/shop.git");
		expect(screen.getByRole("button", STAGE)).toBeEnabled();
		await user.click(screen.getByRole("button", { name: "Connected account" }));
		expect(screen.getByRole("button", STAGE)).toBeDisabled();
	});

	it("types the repository name for an account that cannot list repositories", async () => {
		const seen: StageRequest[] = [];
		const api = stubApi(
			dialogRoutes({
				connections: [
					connection({ provider: "bitbucket", canListRepos: false }),
				],
				seen,
			}),
		);
		await renderDialog(imageService());
		const name = await screen.findByLabelText("Repository");
		expect(name).toHaveAttribute("placeholder", "owner/repository");
		const stage = screen.getByRole("button", STAGE);
		expect(stage).toBeDisabled();
		const user = userEvent.setup();
		await user.click(name);
		await user.paste("acme/shop");
		await waitFor(() => expect(stage).toBeEnabled());
		await user.click(stage);
		await screen.findByText("Source change staged");
		expect(seen[0]?.body).toMatchObject({
			sourceType: "git",
			gitBranch: "main",
			buildType: "auto",
		});
		expect(
			api.calls.find((c) => c.path === "/services/svc-img/git"),
		).toMatchObject({
			body: { connectionId: "conn-1", repo: "acme/shop", branch: "main" },
		});
	});

	it("does not look up accounts while switching to an image", async () => {
		const api = stubApi(dialogRoutes({ connections: [account] }));
		await renderDialog(gitService());
		await screen.findByLabelText("Image");
		expect(api.calls.filter((c) => c.path.startsWith("/git/"))).toEqual([]);
	});
});
