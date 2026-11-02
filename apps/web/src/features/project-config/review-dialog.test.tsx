import { describe, expect, test } from "bun:test";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { type ApiStub, stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { onOpenProjectConfig, type ProjectConfigDialog } from "./open";
import { ReviewDialog } from "./review-dialog";
import { change, environment, envSource, plan } from "./test-support";

const SOURCE = "GET /environments/:id/config-source";
const PLAN = "POST /projects/:id/config/plan";
const APPLY = "POST /projects/:id/config/apply";

type Reply = object | ((r: Request) => Response | Promise<Response>);

const emptySummary = {
	create: 0,
	update: 0,
	unchanged: 3,
	notInFile: 0,
	staged: 0,
	immediate: 0,
	deferred: 0,
	errors: 0,
	warnings: 0,
};

async function open(
	routes: Record<string, Reply>,
	env = environment(),
): Promise<{
	api: ApiStub;
	closed: boolean[];
	user: ReturnType<typeof userEvent.setup>;
}> {
	const closed: boolean[] = [];
	const api = stubApi({ [SOURCE]: { body: envSource() }, ...routes });
	await renderWithProviders(
		<ReviewDialog
			environment={env}
			open
			onOpenChange={(next) => closed.push(next)}
		/>,
	);
	return { api, closed, user: userEvent.setup() };
}

/** The footer's Close: the dialog's corner X is also named Close and comes first. */
const footerClose = () =>
	screen.getAllByRole("button", { name: "Close" }).at(-1) as HTMLElement;
const stage = () => screen.getByRole("button", { name: /Stage changes/ });
const stageAndDeploy = () =>
	screen.getByRole("button", { name: "Stage and deploy" });

function applyCalls(api: ApiStub) {
	return api.calls.filter((c) => c.path.endsWith("/config/apply"));
}

describe("ReviewDialog: reading the plan", () => {
	test("plans the environment's file when opened and summarises it", async () => {
		const { api } = await open({ [PLAN]: { body: plan() } });
		expect(
			await screen.findByText(/1 update, 2 unchanged · 1 staged/),
		).toBeInTheDocument();
		expect(
			screen.getByText(/Nothing is deployed until you say so/),
		).toBeInTheDocument();
		const plans = api.calls.filter((c) => c.path.endsWith("/config/plan"));
		expect(plans).toHaveLength(1);
		expect(plans[0]?.path).toBe("/projects/p1/config/plan");
		expect(plans[0]?.body).toEqual({
			environment: "staging",
			source: { git: {} },
		});
		expect(
			screen.getByText(/acme\/shop@3f9c2a1 · nebula\.toml · 2 KiB/),
		).toBeInTheDocument();
	});

	test("shows a skeleton while the file is read", async () => {
		await open({ [PLAN]: () => new Promise<Response>(() => {}) });
		expect(
			await screen.findByRole("status", { name: "Reading nebula.toml" }),
		).toBeInTheDocument();
		expect(
			screen.getByText("Reading the file for Staging…"),
		).toBeInTheDocument();
		expect(stage()).toBeDisabled();
	});

	test("an update lists each change as a diff row with when it applies and why", async () => {
		await open({
			[PLAN]: {
				body: plan({
					services: [
						{
							slug: "web",
							kind: "git",
							action: "update",
							changes: [
								change(),
								change({
									path: "services.web.variables.LOG",
									field: "variables.LOG",
									from: "",
									to: "debug",
									applies: "immediate",
									reason: "drift",
								}),
								change({
									path: "services.web.cpus",
									field: "config.cpus",
									op: "keep",
								}),
							],
						},
					],
				}),
			},
		});
		const updates = await screen.findByRole("region", { name: "Updates" });
		const web = within(updates).getByRole("region", { name: "web" });
		expect(within(web).getByText("git")).toBeInTheDocument();
		const list = within(web).getByRole("list", { name: "Changes to web" });
		const rows = within(list).getAllByRole("listitem");
		expect(rows).toHaveLength(2);
		expect(rows[0]).toHaveTextContent("port");
		expect(rows[0]).toHaveTextContent("3000 → 8080");
		expect(rows[0]).toHaveTextContent("staged");
		expect(rows[0]).toHaveTextContent("file changed");
		expect(rows[1]).toHaveTextContent("variables.LOG");
		expect(rows[1]).toHaveTextContent("debug");
		expect(rows[1]).toHaveTextContent("applies at once");
		expect(rows[1]).toHaveTextContent("edited in dashboard");
	});

	test("masked secrets never show their value", async () => {
		await open({
			[PLAN]: {
				body: plan({
					services: [
						{
							slug: "web",
							action: "update",
							changes: [
								change({
									path: "services.web.variables.TOKEN",
									field: "variables.TOKEN",
									from: "old-secret-value",
									to: "new-secret-value",
									masked: true,
								}),
							],
						},
					],
				}),
			},
		});
		const list = await screen.findByRole("list", { name: "Changes to web" });
		expect(list).not.toHaveTextContent("secret-value");
	});

	test("a service the file creates, with and without changes", async () => {
		await open({
			[PLAN]: {
				body: plan({
					summary: { ...emptySummary, create: 2, unchanged: 0, staged: 2 },
					services: [
						{
							slug: "worker",
							kind: "image",
							action: "create",
							changes: [
								change({
									path: "services.worker.image",
									field: "config.image",
									op: "add",
									from: "",
									to: "acme/worker:1",
								}),
							],
						},
						{ slug: "cron", kind: "git", action: "create", changes: [] },
					],
				}),
			},
		});
		const creates = await screen.findByRole("region", { name: "Creates" });
		expect(
			within(creates).getByRole("list", { name: "Changes to worker" }),
		).toHaveTextContent("acme/worker:1");
		expect(
			within(creates).getByText("A new git service with the file's defaults."),
		).toBeInTheDocument();
	});

	test("shared variables and released paths get their own sections", async () => {
		await open({
			[PLAN]: {
				body: plan({
					services: [
						{
							slug: "web",
							action: "update",
							changes: [
								change({
									path: "services.web.variables.OLD",
									field: "variables.OLD",
									op: "release",
								}),
							],
							released: ["services.web.scale"],
						},
					],
					shared: {
						project: [
							change({
								path: "shared.project.REGION",
								field: "shared.REGION",
								from: "eu",
								to: "us",
							}),
						],
						environment: [
							change({
								path: "shared.environment.TIER",
								field: "shared.TIER",
								from: "",
								to: "gold",
							}),
						],
					},
				}),
			},
		});
		expect(
			await screen.findByRole("list", { name: "Shared variables, project" }),
		).toHaveTextContent("eu → us");
		expect(
			screen.getByRole("list", { name: "Shared variables, this environment" }),
		).toHaveTextContent("gold");
		const released = screen.getByRole("region", { name: "Released" });
		expect(released).toHaveTextContent("services.web.scale");
		expect(released).toHaveTextContent("services.web.variables.OLD");
		expect(released).toHaveTextContent(/stop being managed/);
	});

	test("services the file does not name are folded away until asked for", async () => {
		const { user } = await open({
			[PLAN]: {
				body: plan({
					summary: { ...emptySummary, notInFile: 2 },
					services: [
						{
							slug: "legacy",
							action: "not_in_file",
							note: "Managed elsewhere.",
						},
						{ slug: "scratch", action: "not_in_file" },
					],
				}),
			},
		});
		const folded = await screen.findByRole("region", { name: "Not in file" });
		const toggle = within(folded).getByRole("button", { name: /Not in file/ });
		expect(toggle).toHaveAttribute("aria-expanded", "false");
		await user.click(toggle);
		expect(toggle).toHaveAttribute("aria-expanded", "true");
		expect(within(folded).getByText("legacy")).toBeVisible();
		expect(within(folded).getByText("Managed elsewhere.")).toBeInTheDocument();
		expect(
			within(folded).getByText(
				"Kept. Remove it in the dashboard if you no longer want it.",
			),
		).toBeInTheDocument();
	});

	test("warnings show where, what, the hint and suggestions", async () => {
		await open({
			[PLAN]: {
				body: plan({
					diagnostics: [
						{
							severity: "warning",
							code: "unknown_key",
							message: 'Unknown key "prot"',
							path: "services.web",
							line: 19,
							column: 3,
							hint: "Keys are lower case.",
							suggestions: ["port", "ports"],
						},
					],
				}),
			},
		});
		const warnings = await screen.findByRole("region", { name: "Warnings" });
		expect(warnings).toHaveTextContent("nebula.toml:19:3");
		expect(warnings).toHaveTextContent('Unknown key "prot"');
		expect(warnings).toHaveTextContent("unknown_key");
		expect(warnings).toHaveTextContent("services.web");
		expect(warnings).toHaveTextContent("Keys are lower case.");
		expect(warnings).toHaveTextContent("Did you mean port, ports?");
		expect(stage()).toBeEnabled();
	});

	test("an error blocks the whole file: reasons first, nothing can be staged", async () => {
		await open({
			[PLAN]: {
				body: plan({
					valid: false,
					diagnostics: [
						{
							severity: "error",
							code: "bad_port",
							message: "port must be 1-65535",
							line: 14,
						},
						{ severity: "error", code: "bad_name", message: "name is empty" },
					],
				}),
			},
		});
		const blockers = await screen.findByRole("alert", { name: "Blockers" });
		expect(
			within(blockers).getByText("2 problems block this file"),
		).toBeInTheDocument();
		expect(blockers).toHaveTextContent("port must be 1-65535");
		expect(blockers).toHaveTextContent("name is empty");
		expect(stage()).toBeDisabled();
		expect(stageAndDeploy()).toBeDisabled();
	});

	test("a single blocker reads in the singular", async () => {
		await open({
			[PLAN]: {
				body: plan({
					valid: false,
					diagnostics: [
						{
							severity: "error",
							code: "bad_port",
							message: "port must be 1-65535",
						},
					],
				}),
			},
		});
		expect(
			await screen.findByText("One problem blocks this file"),
		).toBeInTheDocument();
	});

	test("a plan the control plane called invalid, with no reason given, still says so", async () => {
		await open({ [PLAN]: { body: plan({ valid: false }) } });
		expect(
			await screen.findByText("This file cannot be applied"),
		).toBeInTheDocument();
		expect(stage()).toBeDisabled();
	});

	test("nothing to change says the environment already matches", async () => {
		await open({
			[PLAN]: { body: plan({ summary: emptySummary, services: [] }) },
		});
		expect(
			await screen.findByText("This environment already matches the file."),
		).toBeInTheDocument();
		expect(stage()).toBeDisabled();
	});

	test("a failed plan shows the reason and offers no way to stage", async () => {
		await open({
			[PLAN]: problem(502, {
				code: "git_unreachable",
				detail: "GitHub did not answer.",
			}),
		});
		expect(
			await screen.findByText("Could not plan nebula.toml"),
		).toBeInTheDocument();
		expect(screen.getByText("GitHub did not answer.")).toBeInTheDocument();
		expect(stage()).toBeDisabled();
	});

	test("production approval is asked of a person, and Stage and deploy is not offered", async () => {
		await open({
			[PLAN]: {
				body: plan({
					guard: {
						productionApproval: "required",
						reason: "Two reviewers must approve.",
					},
				}),
			},
		});
		expect(
			await screen.findByText(/Needs approval in production\./),
		).toHaveTextContent("Two reviewers must approve.");
		expect(
			screen.queryByRole("button", { name: "Stage and deploy" }),
		).toBeNull();
		expect(stage()).toBeEnabled();
	});

	test("production approval without a reason falls back to a plain sentence", async () => {
		await open({
			[PLAN]: { body: plan({ guard: { productionApproval: "required" } }) },
		});
		expect(
			await screen.findByText(/A person has to approve this plan\./),
		).toBeInTheDocument();
	});
});

describe("ReviewDialog: no file", () => {
	test("an environment with no file offers to start from the current setup", async () => {
		const { closed, user } = await open({
			[SOURCE]: { body: envSource({ mode: "none", status: "unknown" }) },
			[PLAN]: problem(404, { code: "not_found", detail: "No file." }),
		});
		expect(
			await screen.findByText(/no nebula\.toml to review yet/),
		).toBeInTheDocument();
		expect(screen.queryByText("Could not plan nebula.toml")).toBeNull();
		expect(
			screen.getByText("What Staging would change when its file is applied."),
		).toBeInTheDocument();
		const opened: ProjectConfigDialog[] = [];
		const off = onOpenProjectConfig((d) => opened.push(d));
		await user.click(
			screen.getByRole("button", { name: "Start from your current setup" }),
		);
		off();
		expect(closed).toEqual([false]);
		expect(opened).toEqual(["export"]);
	});

	test("a file that was not found counts as no file", async () => {
		await open({
			[SOURCE]: { body: envSource({ status: "not_found" }) },
			[PLAN]: () => new Promise<Response>(() => {}),
		});
		expect(
			await screen.findByText(/no nebula\.toml to review yet/),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("status", { name: "Reading nebula.toml" }),
		).toBeNull();
	});

	test("with a plan in hand but no file the dialog says there is nothing to apply", async () => {
		await open({
			[SOURCE]: { body: envSource({ status: "not_found" }) },
			[PLAN]: { body: plan({ summary: emptySummary, services: [] }) },
		});
		expect(
			await screen.findByText("There is no file to apply."),
		).toBeInTheDocument();
	});
});

describe("ReviewDialog: staging", () => {
	test("Stage changes applies the reviewed plan without deploying, then closes", async () => {
		let key: string | null = null;
		const { api, closed, user } = await open({
			[PLAN]: { body: plan() },
			[APPLY]: (request) => {
				key = request.headers.get("Idempotency-Key");
				return Response.json(
					make("ConfigApplyResult", {
						status: "staged",
						planId: "x",
						plan: plan(),
					}),
				);
			},
		});
		await screen.findByText(/1 update/);
		await user.click(stage());
		expect(
			await screen.findByText("Staged from nebula.toml"),
		).toBeInTheDocument();
		expect(
			screen.getByText(/1 staged\. Nothing is deployed yet/),
		).toBeInTheDocument();
		expect(applyCalls(api)[0]?.body).toEqual({
			environment: "staging",
			source: { git: {} },
			planId: "pl_7d3c1f0a9b2e4c55e6a1d8f0b3c47a92",
			deploy: false,
		});
		expect(key).toMatch(/^[0-9a-f-]{36}$/);
		expect(closed).toEqual([false]);
	});

	test("Stage and deploy asks for a deploy and says services are deploying", async () => {
		const { api, closed, user } = await open({
			[PLAN]: { body: plan() },
			[APPLY]: {
				status: 201,
				body: make("ConfigApplyResult", {
					status: "staged",
					deployments: ["dep_1", "dep_2"],
					plan: plan(),
				}),
			},
		});
		await screen.findByText(/1 update/);
		await user.click(stageAndDeploy());
		expect(await screen.findByText("Staged and deploying")).toBeInTheDocument();
		expect(screen.getByText(/Deploying 2 services\./)).toBeInTheDocument();
		expect(applyCalls(api)[0]?.body).toMatchObject({ deploy: true });
		expect(closed).toEqual([false]);
	});

	test("an apply that found nothing to do says so and closes", async () => {
		const { closed, user } = await open({
			[PLAN]: { body: plan() },
			[APPLY]: {
				status: 201,
				body: make("ConfigApplyResult", { status: "noop" }),
			},
		});
		await screen.findByText(/1 update/);
		await user.click(stage());
		expect(await screen.findByText("Already in sync")).toBeInTheDocument();
		expect(closed).toEqual([false]);
	});

	test("a partly applied file stays open with each failed step", async () => {
		const { closed, user } = await open({
			[PLAN]: { body: plan() },
			[APPLY]: {
				status: 201,
				body: make("ConfigApplyResult", {
					status: "partial",
					steps: [
						{ step: "services.web.port", status: "applied" },
						{
							step: "services.api.scale",
							status: "failed",
							message: "server is full",
						},
						{ step: "services.db.size", status: "failed" },
					],
				}),
			},
		});
		await screen.findByText(/1 update/);
		await user.click(stage());
		const result = await screen.findByRole("region", { name: "Result" });
		expect(result).toHaveTextContent("Partly applied");
		expect(result).toHaveTextContent("2 steps failed");
		expect(result).toHaveTextContent("services.api.scale");
		expect(result).toHaveTextContent("server is full");
		expect(result).toHaveTextContent("services.db.size");
		expect(closed).toEqual([]);
		// The review gives way to the result: only Close is left.
		expect(screen.queryByRole("button", { name: /Stage changes/ })).toBeNull();
		expect(footerClose()).toBeInTheDocument();
		expect(screen.queryByRole("region", { name: "Updates" })).toBeNull();
	});

	test("a failed apply says nothing was applied", async () => {
		const { user } = await open({
			[PLAN]: { body: plan() },
			[APPLY]: {
				status: 201,
				body: make("ConfigApplyResult", {
					status: "failed",
					steps: [
						{ step: "services.web.port", status: "failed", message: "boom" },
					],
				}),
			},
		});
		await screen.findByText(/1 update/);
		await user.click(stage());
		const result = await screen.findByRole("region", { name: "Result" });
		expect(result).toHaveTextContent("Nothing was applied");
		expect(result).toHaveTextContent("boom");
	});

	test("Close on the result closes the dialog", async () => {
		const { closed, user } = await open({
			[PLAN]: { body: plan() },
			[APPLY]: {
				status: 201,
				body: make("ConfigApplyResult", { status: "failed" }),
			},
		});
		await screen.findByText(/1 update/);
		await user.click(stage());
		await screen.findByRole("region", { name: "Result" });
		await user.click(footerClose());
		expect(closed).toContain(false);
	});

	test("deploying to production asks first; backing out applies nothing", async () => {
		const { api, user } = await open(
			{
				[PLAN]: { body: plan() },
				[APPLY]: {
					status: 201,
					body: make("ConfigApplyResult", { status: "staged", plan: plan() }),
				},
			},
			environment({
				isProduction: true,
				name: "Production",
				slug: "production",
			}),
		);
		await screen.findByText(/1 update/);
		await user.click(stageAndDeploy());
		const confirm = await screen.findByRole("alertdialog");
		expect(
			within(confirm).getByText("Stage and deploy to Production?"),
		).toBeInTheDocument();
		await user.click(within(confirm).getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
		expect(applyCalls(api)).toEqual([]);
	});

	test("confirming the production deploy applies with deploy on", async () => {
		const { api, closed, user } = await open(
			{
				[PLAN]: { body: plan() },
				[APPLY]: {
					status: 201,
					body: make("ConfigApplyResult", {
						status: "staged",
						plan: plan(),
						deployments: ["d"],
					}),
				},
			},
			environment({
				isProduction: true,
				name: "Production",
				slug: "production",
			}),
		);
		await screen.findByText(/1 update/);
		await user.click(stageAndDeploy());
		const confirm = await screen.findByRole("alertdialog");
		await user.click(
			within(confirm).getByRole("button", { name: "Stage and deploy" }),
		);
		await waitFor(() => expect(applyCalls(api)).toHaveLength(1));
		expect(applyCalls(api)[0]?.body).toMatchObject({
			environment: "production",
			deploy: true,
		});
		await waitFor(() => expect(closed).toEqual([false]));
	});

	test("staging without deploying in production needs no confirmation", async () => {
		const { api, user } = await open(
			{
				[PLAN]: { body: plan() },
				[APPLY]: {
					status: 201,
					body: make("ConfigApplyResult", { status: "staged", plan: plan() }),
				},
			},
			environment({
				isProduction: true,
				name: "Production",
				slug: "production",
			}),
		);
		await screen.findByText(/1 update/);
		await user.click(stage());
		await waitFor(() => expect(applyCalls(api)).toHaveLength(1));
		expect(screen.queryByRole("alertdialog")).toBeNull();
		expect(applyCalls(api)[0]?.body).toMatchObject({ deploy: false });
	});

	test("pressing again after a failure replays the same idempotency key", async () => {
		const keys: (string | null)[] = [];
		const { user } = await open({
			[PLAN]: { body: plan() },
			[APPLY]: (request) => {
				keys.push(request.headers.get("Idempotency-Key"));
				return keys.length === 1
					? Response.json(
							problem(500, { code: "internal", detail: "Try again." }).body,
							{ status: 500 },
						)
					: Response.json(
							make("ConfigApplyResult", { status: "staged", plan: plan() }),
						);
			},
		});
		await screen.findByText(/1 update/);
		await user.click(stage());
		expect(await screen.findByText("Try again.")).toBeInTheDocument();
		await user.click(stage());
		await waitFor(() => expect(keys).toHaveLength(2));
		expect(keys[0]).toBeTruthy();
		expect(keys[1]).toBe(keys[0] as string);
	});

	test("a different intent (deploy vs stage) gets its own key", async () => {
		const keys: (string | null)[] = [];
		const { user } = await open({
			[PLAN]: { body: plan() },
			[APPLY]: (request) => {
				keys.push(request.headers.get("Idempotency-Key"));
				return Response.json(
					problem(500, { code: "internal", detail: "Try again." }).body,
					{ status: 500 },
				);
			},
		});
		await screen.findByText(/1 update/);
		await user.click(stage());
		await waitFor(() => expect(keys).toHaveLength(1));
		await screen.findByText("Try again.");
		await user.click(stageAndDeploy());
		await waitFor(() => expect(keys).toHaveLength(2));
		expect(keys[1]).not.toBe(keys[0]);
	});

	test("a stale plan swaps in the fresh one the server sent, with a notice", async () => {
		const fresh = plan({
			planId: "pl_fresh",
			summary: { ...emptySummary, update: 1, staged: 1 },
			services: [
				{ slug: "web", action: "update", changes: [change({ to: "9090" })] },
			],
		});
		const keys: (string | null)[] = [];
		const bodies: unknown[] = [];
		const { user } = await open({
			[PLAN]: { body: plan() },
			[APPLY]: async (request) => {
				keys.push(request.headers.get("Idempotency-Key"));
				bodies.push(await request.clone().json());
				return keys.length === 1
					? Response.json(
							problem(409, {
								code: "plan_stale",
								detail: "stale",
								details: { plan: fresh },
							}).body,
							{ status: 409 },
						)
					: Response.json(
							make("ConfigApplyResult", { status: "staged", plan: fresh }),
						);
			},
		});
		await screen.findByText(/1 update/);
		await user.click(stage());
		expect(
			await screen.findByText(
				/The plan below is up to date; check it and stage again\./,
			),
		).toBeInTheDocument();
		expect(await screen.findByText("3000 → 9090")).toBeInTheDocument();
		await user.click(stage());
		await waitFor(() => expect(bodies).toHaveLength(2));
		expect(bodies[1]).toMatchObject({ planId: "pl_fresh" });
		expect(keys[1]).not.toBe(keys[0]);
	});

	test("a stale plan without a fresh one is re-planned", async () => {
		let plans = 0;
		const { api, user } = await open({
			[PLAN]: () => {
				plans += 1;
				return Response.json(
					plans === 1
						? plan()
						: plan({
								planId: "pl_second",
								services: [
									{
										slug: "web",
										action: "update",
										changes: [change({ to: "7070" })],
									},
								],
							}),
				);
			},
			[APPLY]: problem(409, { code: "plan_stale", detail: "stale" }),
		});
		await screen.findByText(/1 update/);
		await user.click(stage());
		expect(
			await screen.findByText(
				/The plan is refreshed; check it and stage again\./,
			),
		).toBeInTheDocument();
		expect(await screen.findByText("3000 → 7070")).toBeInTheDocument();
		expect(
			api.calls.filter((c) => c.path.endsWith("/config/plan")),
		).toHaveLength(2);
	});

	test("a blocked apply shows the blockers the server found", async () => {
		const blocked = plan({
			valid: false,
			diagnostics: [
				{
					severity: "error",
					code: "secret_missing",
					message: "API_KEY has no value",
				},
			],
		});
		const { user } = await open({
			[PLAN]: { body: plan() },
			[APPLY]: problem(422, {
				code: "config_blocked",
				detail: "blocked",
				details: { plan: blocked },
			}),
		});
		await screen.findByText(/1 update/);
		await user.click(stage());
		const blockers = await screen.findByRole("alert", { name: "Blockers" });
		expect(blockers).toHaveTextContent("API_KEY has no value");
		expect(stage()).toBeDisabled();
	});

	test("any other apply error is shown under the plan, which stays reviewable", async () => {
		const { user } = await open({
			[PLAN]: { body: plan() },
			[APPLY]: problem(403, {
				code: "forbidden",
				detail: "You cannot apply in staging.",
			}),
		});
		await screen.findByText(/1 update/);
		await user.click(stage());
		expect(
			await screen.findByText("You cannot apply in staging."),
		).toBeInTheDocument();
		expect(screen.getByRole("region", { name: "Updates" })).toBeInTheDocument();
		expect(stage()).toBeEnabled();
	});

	test("Cancel asks the parent to close", async () => {
		const { closed, user } = await open({ [PLAN]: { body: plan() } });
		await screen.findByText(/1 update/);
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		expect(closed).toContain(false);
	});
});

function ControlledReview({
	exposeSet,
}: {
	exposeSet: (set: (open: boolean) => void) => void;
}) {
	const [isOpen, setOpen] = useState(true);
	exposeSet(setOpen);
	return (
		<ReviewDialog
			environment={environment()}
			open={isOpen}
			onOpenChange={setOpen}
		/>
	);
}

describe("ReviewDialog: opening and closing", () => {
	test("closing the dialog removes it and the plan it showed, without planning again", async () => {
		let set: (open: boolean) => void = () => {};
		const api = stubApi({
			[SOURCE]: { body: envSource() },
			[PLAN]: { body: plan() },
		});
		await renderWithProviders(
			<ControlledReview
				exposeSet={(setOpen) => {
					set = setOpen;
				}}
			/>,
		);
		expect(
			await screen.findByText(/1 update, 2 unchanged/),
		).toBeInTheDocument();
		act(() => set(false));
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		expect(screen.queryByText(/1 update, 2 unchanged/)).toBeNull();
		expect(
			api.calls.filter((c) => c.path.endsWith("/config/plan")),
		).toHaveLength(1);
	});

	test("asks nothing while closed", async () => {
		const api = stubApi({});
		await renderWithProviders(
			<ReviewDialog
				environment={environment()}
				open={false}
				onOpenChange={() => {}}
			/>,
		);
		expect(api.calls).toEqual([]);
		expect(api.unmatched).toEqual([]);
	});
});
