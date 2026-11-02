import { describe, expect, it } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { BuildPlanResult } from "#/api/gen/types.gen";
import { makeService } from "#/features/canvas/lib/fixtures";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { BuildPlanPreview } from "./build-plan-preview";

const git = () => makeService({ id: "svc-git", sourceType: "git" });

function plan(overrides: Partial<BuildPlanResult> = {}): BuildPlanResult {
	return {
		outcome: "succeeded",
		requested: "auto",
		dockerfileFound: true,
		dockerfilePath: "Dockerfile",
		...overrides,
	};
}

const BUTTON = { name: "Preview build plan" };

/** The "No <Dockerfile> in the repository" line, which is split across elements. */
const missingDockerfileNote = (_: string, el: Element | null) =>
	el?.tagName === "SPAN" &&
	/^No .* in the repository\./.test(el.textContent ?? "");

describe("BuildPlanPreview", () => {
	it("posts to the service's build-plan endpoint and shows the Railpack plan", async () => {
		const api = stubApi({
			"POST /services/:serviceId/build-plan": {
				body: plan({
					requested: "railpack",
					commitSha: "abcdef1234567890",
					info: {
						method: "railpack",
						railpackVersion: "0.9.1",
						providers: ["node", "python"],
						reason: "No Dockerfile in the repository.",
						startCommand: "node server.js",
						packages: [
							{ name: "node", version: "22.1.0" },
							{ name: "pnpm", version: "9.0.0" },
						],
						steps: [
							{ name: "install", commands: ["pnpm install", "pnpm audit"] },
							{ name: "build", commands: ["pnpm build"] },
						],
					},
				}),
			},
		});
		await renderWithProviders(
			<BuildPlanPreview service={git()} dirty={false} />,
		);
		const user = userEvent.setup();
		await user.click(screen.getByRole("button", BUTTON));

		expect(await screen.findByText("Railpack")).toBeInTheDocument();
		expect(screen.getByText("v0.9.1")).toBeInTheDocument();
		expect(screen.getByText("node")).toBeInTheDocument();
		expect(screen.getByText("python")).toBeInTheDocument();
		expect(
			screen.getByText("No Dockerfile in the repository."),
		).toBeInTheDocument();
		expect(screen.getByText("node server.js")).toBeInTheDocument();
		expect(screen.getByText("Toolchain")).toBeInTheDocument();
		expect(screen.getByText("node 22.1.0 · pnpm 9.0.0")).toBeInTheDocument();
		expect(screen.getByText("2 build steps")).toBeInTheDocument();
		expect(screen.getByText("$ pnpm install")).toBeInTheDocument();
		expect(screen.getByText("$ pnpm audit")).toBeInTheDocument();
		expect(screen.getByText("$ pnpm build")).toBeInTheDocument();
		// Only the first seven characters of the commit.
		expect(screen.getByText("abcdef1")).toBeInTheDocument();
		expect(screen.queryByText("abcdef12")).not.toBeInTheDocument();

		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]).toMatchObject({
			method: "POST",
			path: "/services/svc-git/build-plan",
		});
		expect(api.unmatched).toEqual([]);
	});

	it("labels a Dockerfile build and a starter by their method", async () => {
		stubApi({
			"POST /services/:serviceId/build-plan": {
				body: plan({
					info: { method: "starter", starter: "node" },
				}),
			},
		});
		await renderWithProviders(
			<BuildPlanPreview service={git()} dirty={false} />,
		);
		await userEvent.setup().click(screen.getByRole("button", BUTTON));
		expect(await screen.findByText("Starter Dockerfile")).toBeInTheDocument();
		expect(screen.getByText("node")).toBeInTheDocument();
	});

	it("falls back to the raw method name for a method it does not know", async () => {
		stubApi({
			"POST /services/:serviceId/build-plan": {
				body: plan({ info: { method: "buildpacks" } }),
			},
		});
		await renderWithProviders(
			<BuildPlanPreview service={git()} dirty={false} />,
		);
		await userEvent.setup().click(screen.getByRole("button", BUTTON));
		expect(await screen.findByText("buildpacks")).toBeInTheDocument();
	});

	it("names the Dockerfile in a Dockerfile plan and hides the sections it has no data for", async () => {
		stubApi({
			"POST /services/:serviceId/build-plan": {
				body: plan({
					dockerfilePath: "docker/Dockerfile.prod",
					info: { method: "dockerfile", dockerfile: "docker/Dockerfile.prod" },
				}),
			},
		});
		await renderWithProviders(
			<BuildPlanPreview service={git()} dirty={false} />,
		);
		await userEvent.setup().click(screen.getByRole("button", BUTTON));
		expect(await screen.findByText("Dockerfile")).toBeInTheDocument();
		expect(screen.queryByText("Start command")).not.toBeInTheDocument();
		expect(screen.queryByText("Toolchain")).not.toBeInTheDocument();
		expect(screen.queryByText(/build steps/)).not.toBeInTheDocument();
		expect(screen.queryByText(/^Planned at/)).not.toBeInTheDocument();
		expect(screen.queryByText(missingDockerfileNote)).not.toBeInTheDocument();
	});

	it("explains a missing Dockerfile and suggests the matching starter", async () => {
		stubApi({
			"POST /services/:serviceId/build-plan": {
				body: plan({
					dockerfileFound: false,
					dockerfilePath: "Dockerfile",
					suggestedStarter: "go",
					info: { method: "railpack" },
				}),
			},
		});
		await renderWithProviders(
			<BuildPlanPreview service={git()} dirty={false} />,
		);
		await userEvent.setup().click(screen.getByRole("button", BUTTON));
		const note = await screen.findByText(missingDockerfileNote);
		expect(note).toHaveTextContent(
			'No Dockerfile in the repository. It looks like a go project; the "go" starter Dockerfile is an alternative to Railpack.',
		);
	});

	it("says only that the Dockerfile is missing when no starter fits", async () => {
		stubApi({
			"POST /services/:serviceId/build-plan": {
				body: plan({
					dockerfileFound: false,
					dockerfilePath: "build/Dockerfile",
					info: { method: "railpack" },
				}),
			},
		});
		await renderWithProviders(
			<BuildPlanPreview service={git()} dirty={false} />,
		);
		await userEvent.setup().click(screen.getByRole("button", BUTTON));
		const note = await screen.findByText(missingDockerfileNote);
		expect(note).toHaveTextContent("No build/Dockerfile in the repository.");
		expect(note).not.toHaveTextContent("starter");
	});

	it("shows plan messages, marking only warnings", async () => {
		stubApi({
			"POST /services/:serviceId/build-plan": {
				body: plan({
					info: { method: "railpack" },
					messages: [
						{ level: "warn", msg: "Node version not pinned" },
						{ level: "info", msg: "Detected pnpm" },
					],
				}),
			},
		});
		await renderWithProviders(
			<BuildPlanPreview service={git()} dirty={false} />,
		);
		await userEvent.setup().click(screen.getByRole("button", BUTTON));
		expect(
			await screen.findByText("Warning: Node version not pinned"),
		).toBeInTheDocument();
		expect(screen.getByText("Detected pnpm")).toBeInTheDocument();
		expect(
			screen.queryByText("Warning: Detected pnpm"),
		).not.toBeInTheDocument();
	});

	it("shows the failure message of a plan that did not succeed", async () => {
		stubApi({
			"POST /services/:serviceId/build-plan": {
				body: plan({
					outcome: "failed",
					message: "could not clone: authentication required",
					info: { method: "railpack" },
				}),
			},
		});
		await renderWithProviders(
			<BuildPlanPreview service={git()} dirty={false} />,
		);
		await userEvent.setup().click(screen.getByRole("button", BUTTON));
		expect(
			await screen.findByText("could not clone: authentication required"),
		).toBeInTheDocument();
	});

	it("shows only the failure when the plan has no build information", async () => {
		stubApi({
			"POST /services/:serviceId/build-plan": {
				body: plan({
					outcome: "failed",
					message: "branch develop not found",
					dockerfileFound: true,
				}),
			},
		});
		await renderWithProviders(
			<BuildPlanPreview service={git()} dirty={false} />,
		);
		await userEvent.setup().click(screen.getByRole("button", BUTTON));
		expect(
			await screen.findByText("branch develop not found"),
		).toBeInTheDocument();
		expect(screen.queryByText("Dockerfile")).not.toBeInTheDocument();
		expect(screen.queryByText("Railpack")).not.toBeInTheDocument();
	});

	it("does not show the message of a plan that succeeded", async () => {
		stubApi({
			"POST /services/:serviceId/build-plan": {
				body: plan({
					message: "internal note",
					info: { method: "dockerfile" },
				}),
			},
		});
		await renderWithProviders(
			<BuildPlanPreview service={git()} dirty={false} />,
		);
		await userEvent.setup().click(screen.getByRole("button", BUTTON));
		await screen.findByText("Dockerfile");
		expect(screen.queryByText("internal note")).not.toBeInTheDocument();
	});

	it("shows the API's error and no plan when the request fails", async () => {
		stubApi({
			"POST /services/:serviceId/build-plan": {
				status: 409,
				body: {
					status: 409,
					code: "server_offline",
					title: "Conflict",
					detail: "The build server is offline.",
				},
			},
		});
		await renderWithProviders(
			<BuildPlanPreview service={git()} dirty={false} />,
		);
		await userEvent.setup().click(screen.getByRole("button", BUTTON));
		expect(
			await screen.findByText("The build server is offline."),
		).toBeInTheDocument();
		expect(screen.queryByText("Start command")).not.toBeInTheDocument();
		// The button is usable again so the user can retry.
		expect(screen.getByRole("button", BUTTON)).toBeEnabled();
	});

	it("disables the button while the plan is being made", async () => {
		const { promise: pending, resolve: release } =
			Promise.withResolvers<Response>();
		stubApi({ "POST /services/:serviceId/build-plan": () => pending });
		await renderWithProviders(
			<BuildPlanPreview service={git()} dirty={false} />,
		);
		await userEvent.setup().click(screen.getByRole("button", BUTTON));
		const busy = await screen.findByRole("button", { name: "Planning…" });
		expect(busy).toBeDisabled();
		release(
			Response.json(plan({ info: { method: "dockerfile" } }), { status: 200 }),
		);
		expect(await screen.findByText("Dockerfile")).toBeInTheDocument();
		expect(screen.getByRole("button", BUTTON)).toBeEnabled();
	});

	it("is blocked, with a reason, while the settings have unsaved edits", async () => {
		const api = stubApi({});
		await renderWithProviders(
			<BuildPlanPreview service={git()} dirty={true} />,
		);
		expect(screen.getByRole("button", BUTTON)).toBeDisabled();
		expect(
			screen.getByText(
				"Save your changes first: the preview uses the saved settings.",
			),
		).toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});

	it("is blocked until the service has a server, even when the form is clean", async () => {
		const api = stubApi({});
		await renderWithProviders(
			<BuildPlanPreview
				service={makeService({
					sourceType: "git",
					serverId: null,
					server: null,
				})}
				dirty={false}
			/>,
		);
		expect(screen.getByRole("button", BUTTON)).toBeDisabled();
		expect(
			screen.getByText(/Assign the service to a server first/),
		).toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});

	it("puts the missing server before the unsaved edits in the reason", async () => {
		stubApi({});
		await renderWithProviders(
			<BuildPlanPreview
				service={makeService({
					sourceType: "git",
					serverId: null,
					server: null,
				})}
				dirty={true}
			/>,
		);
		expect(
			screen.getByText(/Assign the service to a server first/),
		).toBeInTheDocument();
		expect(
			screen.queryByText(/Save your changes first/),
		).not.toBeInTheDocument();
	});

	it("shows nothing but the idle button before it is used", async () => {
		await renderWithProviders(
			<BuildPlanPreview service={git()} dirty={false} />,
		);
		const button = screen.getByRole("button", BUTTON);
		expect(button).toBeEnabled();
		const row = button.closest("[data-slot='setting-row']") as HTMLElement;
		expect(within(row).queryByText("Start command")).not.toBeInTheDocument();
		expect(within(row).queryByRole("alert")).not.toBeInTheDocument();
	});
});
