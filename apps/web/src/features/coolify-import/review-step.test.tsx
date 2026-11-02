import { describe, expect, mock, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { CoolifyPlan, Project } from "#/api/gen/types.gen";
import type { Server } from "#/api/models";
import { toServer } from "#/api/models";
import { renderWithProviders } from "#/test/render";
import {
	baselineOf,
	buildChoices,
	EMPTY_DRAFT,
	type ImportDraft,
	usernameHints,
} from "./lib";
import { ReviewStep } from "./review-step";
import { envRow, plan, project, resource, serverWire } from "./test-fixtures";
import { DraftHarness } from "./test-harness";

// Builds a literal `${NAME}` placeholder (the escaped `$` keeps it from interpolating).
const ref = (name: string) => `\${${name}}`;

type Props = Partial<Parameters<typeof ReviewStep>[0]>;

async function renderReview(
	thePlan: CoolifyPlan,
	props: Props & { initial?: ImportDraft } = {},
) {
	const { initial = EMPTY_DRAFT, ...rest } = props;
	const seen = { current: initial };
	const handlers = {
		onDeploy: mock((_next: boolean) => {}),
		onRetry: mock(() => {}),
		onEditSource: mock(() => {}),
	};
	await renderWithProviders(
		<DraftHarness initial={initial} seen={seen}>
			{(draft, onDraft) => (
				<ReviewStep
					plan={thePlan}
					draft={draft}
					onDraft={onDraft}
					baseline={baselineOf(thePlan)}
					existingProjects={[] as Project[]}
					server={undefined}
					deploy={false}
					refreshing={false}
					problem={null}
					applyProblem={null}
					{...handlers}
					{...rest}
				/>
			)}
		</DraftHarness>,
	);
	return {
		...handlers,
		choices: () => buildChoices(seen.current, usernameHints(thePlan)),
	};
}

function serverOf(over: Record<string, unknown> = {}): Server {
	return toServer(serverWire(over) as never);
}

describe("ReviewStep header", () => {
	test("shows the totals, the Coolify version, and a green 'no blockers' with nothing else flagged", async () => {
		await renderReview(
			plan({
				coolifyVersion: "4.0.1",
				totals: {
					projects: 2,
					services: 5,
					databases: 1,
					variables: 14,
					secrets: 3,
					domains: 4,
				},
			}),
		);
		expect(
			screen.getByText(
				"2 projects · 5 services · 1 database · 14 variables (3 secret) · 4 domains",
			),
		).toBeInTheDocument();
		expect(screen.getByText("Coolify 4.0.1")).toBeInTheDocument();
		expect(screen.getByText("no blockers")).toBeInTheDocument();
		expect(screen.queryByText(/warning/)).toBeNull();
		expect(screen.queryByText(/check/)).toBeNull();
		expect(screen.queryByText(/needs? a value|need a value/)).toBeNull();
	});

	test("omits the version line when the plan has none", async () => {
		await renderReview(plan());
		expect(screen.queryByText(/^Coolify \d/)).toBeNull();
	});

	test("counts blockers, failed and passed checks, warnings (notes plus warned checks) and variables awaiting a value", async () => {
		await renderReview(
			plan({
				blockers: ["a", "b"],
				warnings: ["w1"],
				checks: [
					{ id: "1", status: "fail", message: "Port 80 is taken" },
					{ id: "2", status: "warn", message: "Disk is nearly full" },
					{ id: "3", status: "ok", message: "Docker is running" },
					{ id: "4", status: "ok", message: "Server is online" },
				],
				projects: [
					project({}, [
						resource({
							env: [
								envRow({ key: "A", needsValue: true }),
								envRow({ key: "B", needsValue: true }),
							],
						}),
					]),
				],
			}),
		);
		expect(screen.getByText("2 blockers")).toBeInTheDocument();
		expect(screen.getByText("1 check failed")).toBeInTheDocument();
		expect(screen.getByText("2 warnings")).toBeInTheDocument();
		expect(screen.getByText("2 variables need a value")).toBeInTheDocument();
		expect(screen.getByText("2 checks passed")).toBeInTheDocument();
	});

	test("uses singular wording for one of each", async () => {
		await renderReview(
			plan({
				blockers: ["only"],
				warnings: ["w"],
				checks: [
					{ id: "1", status: "ok", message: "fine" },
					{ id: "2", status: "fail", message: "bad" },
				],
				projects: [
					project({}, [
						resource({ env: [envRow({ key: "A", needsValue: true })] }),
					]),
				],
			}),
		);
		expect(screen.getByText("1 blocker")).toBeInTheDocument();
		expect(screen.getByText("1 warning")).toBeInTheDocument();
		expect(screen.getByText("1 check passed")).toBeInTheDocument();
		expect(screen.getByText("1 variable needs a value")).toBeInTheDocument();
	});

	test("the Updating label is visible only while refreshing, and the region is marked busy", async () => {
		await renderReview(plan(), { refreshing: true });
		expect(screen.getByText("Updating")).toHaveAttribute(
			"aria-hidden",
			"false",
		);
		expect(screen.getByText("Updating").closest("[aria-busy]")).toHaveAttribute(
			"aria-busy",
			"true",
		);
	});

	test("idle: Updating is hidden from assistive tech", async () => {
		await renderReview(plan());
		expect(screen.getByText("Updating")).toHaveAttribute("aria-hidden", "true");
	});
});

describe("ReviewStep alerts", () => {
	test("blockers are listed with a way back to connect", async () => {
		const user = userEvent.setup();
		const review = await renderReview(
			plan({ blockers: ["Slug clash", "No server"] }),
		);
		const alert = screen
			.getByText("This import is blocked")
			.closest("[role=alert]") as HTMLElement;
		expect(within(alert).getByText("Slug clash")).toBeInTheDocument();
		expect(within(alert).getByText("No server")).toBeInTheDocument();
		await user.click(
			within(alert).getByRole("button", { name: "Back to connect" }),
		);
		expect(review.onEditSource).toHaveBeenCalledTimes(1);
	});

	test("one warning reads 'A note', several count them", async () => {
		await renderReview(plan({ warnings: ["Only one"] }));
		expect(screen.getByText("A note about this import")).toBeInTheDocument();
		expect(screen.getByText("Only one")).toBeInTheDocument();
	});

	test("several warnings are counted in the title", async () => {
		await renderReview(plan({ warnings: ["One", "Two", "Three"] }));
		expect(screen.getByText("3 notes about this import")).toBeInTheDocument();
	});

	test("a plan problem keeps the cards and offers Try again", async () => {
		const user = userEvent.setup();
		const review = await renderReview(plan(), {
			problem: "Coolify timed out.",
		});
		expect(
			screen.getByText(
				"Coolify timed out. The cards show the last plan that worked.",
			),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Try again" }));
		expect(review.onRetry).toHaveBeenCalledTimes(1);
		expect(
			screen.getByRole("checkbox", { name: "Include web" }),
		).toBeInTheDocument();
	});

	test("an import failure explains that importing again is safe", async () => {
		await renderReview(plan(), { applyProblem: "Disk full." });
		expect(screen.getByText("The import failed")).toBeInTheDocument();
		expect(
			screen.getByText(/Disk full\. Importing again sends the same request/),
		).toBeInTheDocument();
	});

	test("no problem alerts when nothing failed", async () => {
		await renderReview(plan());
		expect(screen.queryByRole("alert")).toBeNull();
	});

	test("a restricted server is warned about services that need host access, and the warning names it", async () => {
		const user = userEvent.setup();
		const review = await renderReview(
			plan({
				projects: [
					project({}, [
						resource({ needsUnrestricted: ["privileged", "devices"] }),
					]),
				],
			}),
			{ server: serverOf({ name: "edge-1", unrestricted: false }) },
		);
		const note = screen
			.getByText("These need an unrestricted server")
			.closest("[role=note]") as HTMLElement;
		expect(
			within(note).getByText("web: privileged, devices"),
		).toBeInTheDocument();
		expect(
			within(note).getByText(/edge-1 is restricted and refuses these options/),
		).toBeInTheDocument();
		await user.click(
			within(note).getByRole("button", { name: "Back to connect" }),
		);
		expect(review.onEditSource).toHaveBeenCalled();
	});

	test("no host-access warning on an unrestricted server, with no server chosen, or once the service is left out", async () => {
		const needy = plan({
			projects: [
				project({}, [resource({ needsUnrestricted: ["privileged"] })]),
			],
		});
		await renderReview(needy, { server: serverOf({ unrestricted: true }) });
		expect(screen.queryByText("These need an unrestricted server")).toBeNull();
	});

	test("no host-access warning without a server", async () => {
		await renderReview(
			plan({
				projects: [
					project({}, [resource({ needsUnrestricted: ["privileged"] })]),
				],
			}),
		);
		expect(screen.queryByText("These need an unrestricted server")).toBeNull();
	});

	test("leaving the service out removes the host-access warning", async () => {
		const user = userEvent.setup();
		await renderReview(
			plan({
				projects: [
					project({}, [resource({ needsUnrestricted: ["privileged"] })]),
				],
			}),
			{ server: serverOf({ unrestricted: false }) },
		);
		expect(
			screen.getByText("These need an unrestricted server"),
		).toBeInTheDocument();
		await user.click(screen.getByRole("checkbox", { name: "Include web" }));
		expect(screen.queryByText("These need an unrestricted server")).toBeNull();
	});
});

describe("ReviewStep sections", () => {
	test("Projects heading counts what is imported and summarizes inclusion; the cards show", async () => {
		await renderReview(
			plan({
				projects: [
					project({ uuid: "p1", name: "Shop" }, [
						resource({ uuid: "r1", name: "web" }),
					]),
					project({ uuid: "p2", name: "Blog", slug: "blog" }, [
						resource({ uuid: "r2", name: "cms", slug: "cms", include: false }),
					]),
				],
			}),
		);
		const section = screen.getByRole("region", { name: "Projects" });
		expect(
			within(section).getByText(
				"2 of 2 · 1 service, 0 databases included, 1 left out",
			),
		).toBeInTheDocument();
		expect(within(section).getByText("Shop")).toBeInTheDocument();
		expect(within(section).getByText("Blog")).toBeInTheDocument();
	});

	test("skipping a project updates the heading", async () => {
		const user = userEvent.setup();
		await renderReview(plan());
		const section = screen.getByRole("region", { name: "Projects" });
		expect(within(section).getByText(/^1 of 1 /)).toBeInTheDocument();
		await user.click(within(section).getByRole("checkbox", { name: /Shop/ }));
		expect(within(section).getByText(/^0 of 1 /)).toBeInTheDocument();
	});

	test("registries: credentials are asked for, and the badge flips once a password is typed", async () => {
		const user = userEvent.setup();
		const review = await renderReview(
			plan({
				registries: [
					{
						host: "ghcr.io",
						provided: false,
						services: ["web", "worker"],
						username: "acme-bot",
					},
					{ host: "registry.example", provided: true, services: null },
				],
			}),
		);
		const section = screen.getByRole("region", {
			name: "Registry credentials",
		});
		expect(within(section).getByText("2")).toBeInTheDocument();
		expect(
			within(section).getByText("used by web, worker"),
		).toBeInTheDocument();
		expect(within(section).getByText("used by no service")).toBeInTheDocument();
		expect(within(section).getByText("no credentials")).toBeInTheDocument();
		expect(
			within(section).getByText("credentials provided"),
		).toBeInTheDocument();
		expect(document.getElementById("registry-user-ghcr.io")).toHaveValue(
			"acme-bot",
		);
		expect(document.getElementById("registry-pass-ghcr.io")).toHaveAttribute(
			"type",
			"password",
		);

		await user.type(
			document.getElementById("registry-pass-ghcr.io") as HTMLElement,
			"s3cret",
		);
		expect(within(section).getAllByText("credentials provided")).toHaveLength(
			2,
		);
		expect(within(section).queryByText("no credentials")).toBeNull();
		// The suggested username goes along with the password.
		expect(review.choices()).toEqual({
			registries: { "ghcr.io": { username: "acme-bot", password: "s3cret" } },
		});

		const user2 = document.getElementById(
			"registry-user-ghcr.io",
		) as HTMLElement;
		await user.clear(user2);
		await user.type(user2, "other");
		expect(review.choices()).toEqual({
			registries: { "ghcr.io": { username: "other", password: "s3cret" } },
		});
	});

	test("no registry section without registries", async () => {
		await renderReview(plan());
		expect(
			screen.queryByRole("region", { name: "Registry credentials" }),
		).toBeNull();
	});

	test("rewritten references are grouped by resource with what each became", async () => {
		await renderReview(
			plan({
				rewrites: [
					{
						key: "DATABASE_HOST",
						kind: "host",
						resource: "web",
						to: ref("{db.DATABASE_HOST}"),
					},
					{
						key: "DATABASE_URL",
						kind: "url",
						resource: "web",
						to: ref("{db.DATABASE_URL}"),
					},
					{
						key: "REDIS_URL",
						kind: "url",
						resource: "worker",
						to: ref("{cache.REDIS_URL}"),
					},
				],
			}),
		);
		const section = screen.getByRole("region", {
			name: "Rewritten references",
		});
		expect(within(section).getByText("3")).toBeInTheDocument();
		expect(
			within(section)
				.getAllByRole("heading", { level: 4 })
				.map((h) => h.textContent),
		).toEqual(["web", "worker"]);
		expect(within(section).getByText("web.DATABASE_HOST")).toBeInTheDocument();
		expect(
			within(section).getByText(ref("{db.DATABASE_HOST}")),
		).toBeInTheDocument();
		expect(within(section).getByText("(host)")).toBeInTheDocument();
		expect(within(section).getAllByText("(url)")).toHaveLength(2);
		expect(within(section).getByText("worker.REDIS_URL")).toBeInTheDocument();
	});

	test("what could not be imported is listed with its kind, project and reason", async () => {
		await renderReview(
			plan({
				unsupported: [
					{
						kind: "service",
						name: "Plausible",
						project: "Shop",
						reason: "One-click services are not supported.",
					},
					{
						kind: "other",
						name: "S3 backup",
						reason: "Backups are not imported.",
					},
				],
			}),
		);
		const section = screen.getByRole("region", { name: "Not imported" });
		expect(within(section).getByText("2")).toBeInTheDocument();
		expect(within(section).getByText("Plausible")).toBeInTheDocument();
		expect(within(section).getByText("service")).toBeInTheDocument();
		expect(within(section).getByText("Shop")).toBeInTheDocument();
		expect(
			within(section).getByText("One-click services are not supported."),
		).toBeInTheDocument();
		expect(within(section).getByText("S3 backup")).toBeInTheDocument();
		expect(within(section).getByText("other")).toBeInTheDocument();
	});

	test("manual follow-ups fold under 'After the import' and show their commands when opened", async () => {
		const user = userEvent.setup();
		await renderReview(
			plan({
				followups: [
					"db: dump it (docker exec abc pg_dump app > db.dump), then run: nebulactrl databases restore db --file db.dump",
				],
			}),
		);
		const toggle = screen.getByRole("button", { name: /After the import/ });
		expect(toggle).toHaveTextContent("(1)");
		expect(
			screen.queryByText("docker exec abc pg_dump app > db.dump"),
		).toBeNull();
		await user.click(toggle);
		expect(
			screen.getByText("docker exec abc pg_dump app > db.dump"),
		).toBeInTheDocument();
		expect(screen.getByText(/Data is never copied/)).toBeInTheDocument();
	});

	test("preflight checks are listed failures first, and say Checking while refreshing", async () => {
		await renderReview(
			plan({
				checks: [
					{ id: "1", status: "ok", message: "Docker is running" },
					{ id: "2", status: "fail", message: "Port 80 is taken" },
				],
			}),
			{ refreshing: true },
		);
		const section = screen.getByRole("region", { name: "Preflight" });
		const rows = within(section)
			.getAllByRole("listitem")
			.map((li) => li.textContent);
		expect(rows).toEqual([
			"Failed: Port 80 is taken",
			"Passed: Docker is running",
		]);
		expect(within(section).getByText("Checking")).toBeInTheDocument();
	});

	test("no preflight section without checks", async () => {
		await renderReview(plan());
		expect(screen.queryByRole("region", { name: "Preflight" })).toBeNull();
	});

	test("no optional sections for an empty plan", async () => {
		await renderReview(plan());
		for (const name of [
			"Rewritten references",
			"Not imported",
			"Registry credentials",
		]) {
			expect(screen.queryByRole("region", { name })).toBeNull();
		}
		expect(
			screen.queryByRole("button", { name: /After the import/ }),
		).toBeNull();
	});
});

describe("ReviewStep deploy option", () => {
	test("Deploy right away reflects the prop and reports the change", async () => {
		const user = userEvent.setup();
		const review = await renderReview(plan(), { deploy: false });
		const box = screen.getByRole("checkbox", { name: "Deploy right away" });
		expect(box).not.toBeChecked();
		expect(
			screen.getByText(/everything is staged and nothing is deployed/),
		).toBeInTheDocument();
		await user.click(box);
		expect(review.onDeploy).toHaveBeenCalledWith(true);
	});

	test("a checked Deploy right away reports false when unchecked", async () => {
		const user = userEvent.setup();
		const review = await renderReview(plan(), { deploy: true });
		const box = screen.getByRole("checkbox", { name: "Deploy right away" });
		expect(box).toBeChecked();
		await user.click(box);
		expect(review.onDeploy).toHaveBeenCalledWith(false);
	});
});
