import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { GitPollView } from "#/api/gen/types.gen";
import type { Service } from "#/api/models";
import { makeService } from "#/features/canvas/lib/fixtures";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { GitPollRows } from "./git-poll-section";

const PUT = "PUT /services/:serviceId/git-poll";
const CHECK = "POST /services/:serviceId/git-poll/check";
const DEPLOY = "POST /services/:id/deployments";

const HEAD = "abc1234def5678abc1234def5678abc1234def56";

const poll = (over: Partial<GitPollView> = {}): GitPollView => ({
	enabled: true,
	intervalMinutes: 5,
	deploy: true,
	checkedAt: new Date(Date.now() - 2 * 60_000).toISOString(),
	headSha: HEAD,
	headBranch: "main",
	updateAvailable: false,
	...over,
});

function gitService(over: Partial<Service> = {}): Service {
	return makeService({
		id: "svc-1",
		name: "api",
		sourceType: "git",
		currentDeploymentId: "dep-1",
		...over,
	});
}

const render = (service: Service) =>
	renderWithProviders(<GitPollRows service={service} />);

const calls = (api: ReturnType<typeof stubApi>, method: string) =>
	api.calls.filter((c) => c.method === method);

describe("GitPollRows", () => {
	test("is hidden for a service without a git source", async () => {
		for (const service of [
			makeService({ sourceType: "image" }),
			makeService({ kind: "database", sourceType: "image" }),
			makeService({ kind: "route", sourceType: "external" }),
			makeService({ kind: "stack", sourceType: "compose" } as Partial<Service>),
		]) {
			const { container, unmount } = await render(service);
			expect(container).toBeEmptyDOMElement();
			unmount();
		}
	});

	test("reads Off from a service without a row and disables the deploy switch", async () => {
		stubApi({});
		await render(gitService());
		expect(
			screen.getByRole("combobox", { name: "Check for new commits" }),
		).toHaveTextContent("Off");
		const sw = screen.getByRole("switch", {
			name: "Deploy new commits automatically",
		});
		expect(sw).toBeChecked();
		expect(sw).toHaveAttribute("aria-disabled", "true");
	});

	test("reflects the saved interval and deploy flag", async () => {
		stubApi({});
		await render(
			gitService({ gitPoll: poll({ intervalMinutes: 15, deploy: false }) }),
		);
		expect(
			screen.getByRole("combobox", { name: "Check for new commits" }),
		).toHaveTextContent("Every 15 minutes");
		const sw = screen.getByRole("switch", {
			name: "Deploy new commits automatically",
		});
		expect(sw).not.toBeChecked();
		expect(sw).not.toHaveAttribute("aria-disabled", "true");
	});

	test("choosing an interval saves at once with all three fields", async () => {
		const api = stubApi({ [PUT]: { body: poll({ intervalMinutes: 60 }) } });
		await render(gitService());
		const user = userEvent.setup();
		await user.click(
			screen.getByRole("combobox", { name: "Check for new commits" }),
		);
		await user.click(await screen.findByRole("option", { name: "Every hour" }));
		await waitFor(() => expect(calls(api, "PUT")).toHaveLength(1));
		expect(calls(api, "PUT")[0]?.path).toBe("/services/svc-1/git-poll");
		expect(calls(api, "PUT")[0]?.body).toEqual({
			enabled: true,
			intervalMinutes: 60,
			deploy: true,
		});
	});

	test("choosing Off keeps the interval and the deploy flag in the body", async () => {
		const api = stubApi({ [PUT]: { body: poll({ enabled: false }) } });
		await render(
			gitService({ gitPoll: poll({ intervalMinutes: 15, deploy: false }) }),
		);
		const user = userEvent.setup();
		await user.click(
			screen.getByRole("combobox", { name: "Check for new commits" }),
		);
		await user.click(await screen.findByRole("option", { name: "Off" }));
		await waitFor(() => expect(calls(api, "PUT")).toHaveLength(1));
		expect(calls(api, "PUT")[0]?.body).toEqual({
			enabled: false,
			intervalMinutes: 15,
			deploy: false,
		});
	});

	test("toggling the switch saves at once with all three fields", async () => {
		const api = stubApi({
			[PUT]: { body: poll({ intervalMinutes: 15, deploy: false }) },
		});
		await render(gitService({ gitPoll: poll({ intervalMinutes: 15 }) }));
		await userEvent.setup().click(
			screen.getByRole("switch", {
				name: "Deploy new commits automatically",
			}),
		);
		await waitFor(() => expect(calls(api, "PUT")).toHaveLength(1));
		expect(calls(api, "PUT")[0]?.body).toEqual({
			enabled: true,
			intervalMinutes: 15,
			deploy: false,
		});
	});

	test("a refused save is reported", async () => {
		stubApi({
			[PUT]: problem(422, {
				code: "git_poll_not_applicable",
				detail: "no git source",
			}),
		});
		await render(gitService({ gitPoll: poll() }));
		await userEvent.setup().click(
			screen.getByRole("switch", {
				name: "Deploy new commits automatically",
			}),
		);
		expect(await screen.findByText("Could not save")).toBeInTheDocument();
	});

	test("the status line says when it was checked and that the head is running", async () => {
		stubApi({});
		await render(
			gitService({
				gitPoll: poll(),
				currentRelease: {
					id: "rel-1",
					imageRef: "img",
					source: { type: "git", commitSha: HEAD },
				},
			}),
		);
		expect(
			screen.getByText("Last checked 2m ago · main is at abc1234 (running)"),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Deploy latest" }),
		).not.toBeInTheDocument();
	});

	test("a new commit is named and Deploy latest starts a deployment", async () => {
		const api = stubApi({
			[DEPLOY]: {
				status: 202,
				body: make("DeploymentView", { id: "d2", status: "queued" }),
			},
		});
		await render(gitService({ gitPoll: poll({ updateAvailable: true }) }));
		expect(screen.getByText("New commit abc1234 on main")).toBeInTheDocument();
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Deploy latest" }));
		await waitFor(() => expect(calls(api, "POST")).toHaveLength(1));
		expect(calls(api, "POST")[0]?.path).toBe("/services/svc-1/deployments");
	});

	test("the error of the last check is shown as the status", async () => {
		stubApi({});
		await render(
			gitService({ gitPoll: poll({ error: "remote unreachable" }) }),
		);
		expect(screen.getByRole("alert")).toHaveTextContent("remote unreachable");
	});

	test("Check now posts the check and shows its answer when polling is off", async () => {
		const api = stubApi({
			[CHECK]: {
				body: poll({
					enabled: false,
					updateAvailable: true,
					checkedAt: new Date().toISOString(),
				}),
			},
		});
		await render(gitService());
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Check now" }));
		expect(
			await screen.findByText("New commit abc1234 on main"),
		).toBeInTheDocument();
		expect(calls(api, "POST").map((c) => c.path)).toEqual([
			"/services/svc-1/git-poll/check",
		]);
		expect(calls(api, "PUT")).toHaveLength(0);
	});

	test("the watch-paths note shows only with watch paths", async () => {
		stubApi({});
		const note = "Watch paths do not filter commits found by checking.";
		const plain = await render(gitService());
		expect(screen.queryByText(note)).not.toBeInTheDocument();
		plain.unmount();
		await render(
			gitService({
				git: {
					autoDeploy: true,
					connectionId: "c1",
					connectionName: "Acme",
					provider: "github",
					repo: "acme/api",
					waitForCi: false,
					watchPaths: ["apps/api/**"],
				},
			}),
		);
		expect(within(document.body).getByText(note)).toBeInTheDocument();
	});
});
