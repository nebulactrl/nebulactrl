import { describe, expect, test } from "bun:test";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ComposeChoices, ComposePlan } from "#/api/gen/types.gen";
import { StackReviewStep } from "./stack-review";
import {
	ChoicesHost,
	component,
	envRow,
	plan,
	stackModePlan,
	stackPlan,
} from "./test-fixtures";

function review(
	p: ComposePlan,
	over: {
		choices?: ComposeChoices;
		refreshing?: boolean;
		problem?: string | null;
	} = {},
) {
	const seen = { choices: over.choices ?? {} };
	const calls = { editSource: 0 };
	const view = render(
		<ChoicesHost initial={over.choices ?? {}} seen={seen}>
			{(choices, onChoices) => (
				<StackReviewStep
					plan={p}
					choices={choices}
					onChoices={onChoices}
					onEditSource={() => {
						calls.editSource++;
					}}
					refreshing={over.refreshing ?? false}
					problem={over.problem ?? null}
				/>
			)}
		</ChoicesHost>,
	);
	return { ...view, seen, calls };
}

describe("StackReviewStep", () => {
	test("renders nothing for a plan without a stack", () => {
		const { container } = review(plan());
		expect(container).toBeEmptyDOMElement();
	});

	test("summarises components, problems and variables and names the stack", () => {
		review(
			stackModePlan(
				stackPlan({
					name: "shop-stack",
					components: [component({ name: "web" }), component({ name: "db" })],
					variables: [
						envRow({ key: "A", value: "1" }),
						envRow({ key: "DOCKER_HOST", skipped: true }),
					],
				}),
			),
		);
		expect(screen.getByRole("status")).toHaveTextContent(
			"2 components, 0 problems, 1 variable",
		);
		expect(screen.getByText("shop-stack")).toBeInTheDocument();
		expect(
			screen.getByRole("region", { name: "Components" }),
		).toHaveTextContent("2");
	});

	test("a component shows its image, ports and whether a deploy starts it", () => {
		review(
			stackModePlan(
				stackPlan({
					components: [
						component({ name: "web", image: "nginx:1.27", ports: [80, 443] }),
						component({
							name: "worker",
							image: "",
							action: "profile",
							profiles: ["jobs", "debug"],
						}),
					],
				}),
			),
		);
		const items = screen.getAllByRole("listitem");
		const web = items.find((li) =>
			li.textContent?.includes("nginx:1.27"),
		) as HTMLElement;
		expect(within(web).getByText(":80 :443")).toBeInTheDocument();
		expect(within(web).getByText("runs")).toBeInTheDocument();
		const worker = items.find((li) =>
			li.textContent?.includes("worker"),
		) as HTMLElement;
		expect(within(worker).getByText("no image")).toBeInTheDocument();
		expect(within(worker).getByText("not started")).toBeInTheDocument();
		expect(
			within(worker).getByText(
				"It has Compose profiles (jobs, debug), so a default deploy does not start it.",
			),
		).toBeInTheDocument();
	});

	test("a component with no HTTP port gets no domain switch and says why", () => {
		review(
			stackModePlan(
				stackPlan({ components: [component({ name: "db", port: 0 })] }),
			),
		);
		expect(
			screen.getByText(
				"No HTTP port: other components reach it by name, and no domain is suggested.",
			),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("switch", { name: /Domain for/ }),
		).not.toBeInTheDocument();
	});

	test("a routable component's domain: suggested on, host editable, switch off clears the host field", async () => {
		const user = userEvent.setup();
		const { seen } = review(
			stackModePlan(
				stackPlan({
					components: [
						component({
							name: "web",
							port: 8080,
							domains: [
								{
									enabled: true,
									generate: true,
									host: "web.example.com",
									source: "suggested",
								},
							],
						}),
					],
				}),
			),
		);
		const toggle = screen.getByRole("switch", { name: "Domain for web" });
		expect(toggle).toBeChecked();
		expect(screen.getByText("8080")).toBeInTheDocument();
		const host = screen.getByRole("textbox", { name: "Hostname for web" });
		expect(host).toHaveValue("web.example.com");

		await user.type(host, "x");
		expect(seen.choices).toEqual({
			services: {
				web: { domain: { enabled: true, host: "web.example.comx" } },
			},
		});

		await user.click(toggle);
		expect(seen.choices).toEqual({
			services: { web: { domain: { enabled: false } } },
		});
		expect(
			screen.queryByRole("textbox", { name: "Hostname for web" }),
		).not.toBeInTheDocument();
	});

	test("a routable component without a suggestion starts with the domain off", async () => {
		const user = userEvent.setup();
		const { seen } = review(
			stackModePlan(
				stackPlan({ components: [component({ name: "web", port: 80 })] }),
			),
		);
		const toggle = screen.getByRole("switch", { name: "Domain for web" });
		expect(toggle).not.toBeChecked();
		await user.click(toggle);
		expect(seen.choices).toEqual({
			services: { web: { domain: { enabled: true } } },
		});
		expect(
			screen.getByRole("textbox", { name: "Hostname for web" }),
		).toHaveValue("");
	});

	test("component notes are listed", () => {
		review(
			stackModePlan(
				stackPlan({
					components: [
						component({ name: "web", notes: ["Publishes 80 on the host."] }),
					],
				}),
			),
		);
		expect(screen.getByText("Publishes 80 on the host.")).toBeInTheDocument();
	});

	test("restricted problems show the admin hint and count in the summary", () => {
		review(
			stackModePlan(
				stackPlan({
					components: [component({ name: "web" })],
					violations: [
						{
							key: "privileged",
							kind: "restricted",
							message: "Needs privileges.",
							service: "web",
						},
					],
				}),
				{ blockers: ["A restricted setting needs an unrestricted server."] },
			),
		);
		const [problems] = screen.getAllByRole("region", { name: "Problems" }) as [
			HTMLElement,
		];
		expect(within(problems).getAllByRole("note")).toHaveLength(1);
		expect(within(problems).getByText("Restricted")).toBeInTheDocument();
		expect(
			within(problems).getByText(
				"Ask an admin to enable Unrestricted for this server, or remove these settings from the Compose file.",
			),
		).toBeInTheDocument();
		expect(screen.getByRole("status")).toHaveTextContent("1 problem");
	});

	test("on an unrestricted server a restricted setting is allowed and not counted", () => {
		review(
			stackModePlan(
				stackPlan({
					unrestricted: true,
					components: [component({ name: "web" })],
					violations: [
						{
							key: "privileged",
							kind: "restricted",
							message: "Needs privileges.",
							service: "web",
						},
					],
				}),
			),
		);
		expect(screen.getByText("Allowed here")).toBeInTheDocument();
		expect(screen.getByRole("status")).toHaveTextContent("0 problems");
	});

	test("blockers list each reason and Edit source goes back", async () => {
		const user = userEvent.setup();
		const { calls } = review(
			stackModePlan(stackPlan({ components: [component({ name: "web" })] }), {
				blockers: ["No services."],
			}),
		);
		const alert = screen
			.getByText("This import is blocked")
			.closest("[role=alert]") as HTMLElement;
		expect(within(alert).getByText("No services.")).toBeInTheDocument();
		await user.click(
			within(alert).getByRole("button", { name: "Edit source" }),
		);
		expect(calls.editSource).toBe(1);
	});

	test("warns when the server lacks the Compose plugin, and only then", () => {
		const { unmount } = review(
			stackModePlan(stackPlan({ composeAvailable: false })),
		);
		expect(
			screen.getByText(/does not have the Docker Compose plugin/),
		).toBeInTheDocument();
		unmount();
		// Unknown (null) is not a warning: no server was chosen yet.
		review(stackModePlan(stackPlan({ composeAvailable: null })));
		expect(
			screen.queryByText(/does not have the Docker Compose plugin/),
		).not.toBeInTheDocument();
	});

	test("a failed refresh keeps the last plan and says so", () => {
		review(
			stackModePlan(stackPlan({ components: [component({ name: "web" })] })),
			{
				problem: "network down",
			},
		);
		expect(
			screen.getByText(
				"network down The review shows the last plan that worked.",
			),
		).toBeInTheDocument();
		expect(screen.getByText("web")).toBeInTheDocument();
	});

	test("refreshing flags the region busy", () => {
		const { container } = review(stackModePlan(stackPlan()), {
			refreshing: true,
		});
		expect(container.querySelector("[aria-busy]")).toHaveAttribute(
			"aria-busy",
			"true",
		);
		expect(screen.getByText("Updating")).not.toHaveAttribute(
			"aria-hidden",
			"true",
		);
	});

	test("notes and project warnings share the Notes section", () => {
		review(
			stackModePlan(stackPlan({ notes: ["Uses an external network."] }), {
				warnings: ["Extension fields were ignored."],
			}),
		);
		const notes = screen.getByRole("region", { name: "Notes" });
		expect(
			within(notes).getByText("Extension fields were ignored."),
		).toBeInTheDocument();
		expect(
			within(notes).getByText("Uses an external network."),
		).toBeInTheDocument();
	});

	test("no notes means no Notes section", () => {
		review(stackModePlan(stackPlan()));
		expect(
			screen.queryByRole("region", { name: "Notes" }),
		).not.toBeInTheDocument();
	});

	test("preflight checks show when the plan has them", () => {
		review(
			stackModePlan(stackPlan(), {
				checks: [{ id: "c", message: "Compose 2.29 found.", status: "ok" }],
			}),
		);
		expect(
			within(screen.getByRole("region", { name: "Preflight" })).getByText(
				/Compose 2.29 found\./,
			),
		).toBeInTheDocument();
	});

	describe("variables", () => {
		const variables = [
			envRow({ key: "TAG", value: "1.4" }),
			envRow({ key: "PASSWORD", value: "x", secret: true }),
			envRow({ key: "TOKEN", value: "y", secretGuess: true }),
			envRow({
				key: "DOCKER_HOST",
				value: "",
				skipped: true,
				note: "Never reaches compose.",
			}),
		];

		async function opened() {
			const user = userEvent.setup();
			const view = review(stackModePlan(stackPlan({ variables })));
			await user.click(
				screen.getByRole("button", { name: "Variables for interpolation (4)" }),
			);
			return { user, ...view };
		}

		test("are collapsed until opened; secrets are masked; empty values are labelled", async () => {
			const user = userEvent.setup();
			review(stackModePlan(stackPlan({ variables })));
			expect(screen.queryByText("TAG")).not.toBeInTheDocument();
			await user.click(
				screen.getByRole("button", { name: /Variables for interpolation/ }),
			);
			expect(screen.getByText("TAG")).toBeInTheDocument();
			expect(screen.getAllByText("1.4").length).toBeGreaterThan(0);
			expect(screen.queryByText("x")).not.toBeInTheDocument();
			expect(screen.getAllByText(/\(secret, hidden\)/)).toHaveLength(1);
			expect(screen.getByText("empty")).toBeInTheDocument();
			expect(screen.getByText("Never reaches compose.")).toBeInTheDocument();
		});

		test("the secret switch writes variables, and a guess disappears once decided", async () => {
			const { user, seen } = await opened();
			expect(screen.getAllByText("guessed")).toHaveLength(1);
			await user.click(screen.getByRole("switch", { name: "Secret: TOKEN" }));
			expect(seen.choices).toEqual({ variables: { TOKEN: { secret: true } } });
			expect(screen.queryByText("guessed")).not.toBeInTheDocument();

			await user.click(
				screen.getByRole("switch", { name: "Secret: PASSWORD" }),
			);
			expect(seen.choices).toEqual({
				variables: { TOKEN: { secret: true }, PASSWORD: { secret: false } },
			});
		});

		test("skip toggles on and off; a plan-skipped variable stays locked", async () => {
			const { user, seen } = await opened();
			await user.click(screen.getByRole("switch", { name: "Skip: TAG" }));
			expect(seen.choices).toEqual({ variables: { TAG: { skip: true } } });
			await user.click(screen.getByRole("switch", { name: "Skip: TAG" }));
			expect(seen.choices).toEqual({});

			const locked = screen.getByRole("switch", { name: "Skip: DOCKER_HOST" });
			expect(locked).toBeChecked();
			expect(locked).toHaveAttribute("data-disabled");
			await user.click(locked);
			expect(seen.choices).toEqual({});
		});
	});
});
