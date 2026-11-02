import { describe, expect, test } from "bun:test";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { type ReactNode, useState } from "react";
import type { PlacementView } from "#/api/gen/types.gen";
import type { Service } from "#/api/models";
import { makeService } from "#/features/canvas/lib/fixtures";
import { placementWire, planWire } from "#/features/deployments/test-fixtures";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import {
	buildPatch,
	type SettingsDraft,
	toDraft,
	validateDraft,
} from "./draft";
import type { DraftErrors, DraftProps } from "./fields";
import {
	CommandsSection,
	HealthFields,
	HealthSection,
	type HealthValue,
	ReleasesSection,
} from "./health-deploy-sections";

interface Seen {
	draft: SettingsDraft;
}

function Harness({
	seen,
	errors,
	children,
}: {
	seen: Seen;
	errors: DraftErrors;
	children: (props: DraftProps) => ReactNode;
}) {
	const [draft, setDraft] = useState<SettingsDraft>(seen.draft);
	seen.draft = draft;
	return (
		<>
			{children({
				draft,
				errors,
				set: (key, value) => setDraft((d) => ({ ...d, [key]: value })),
			})}
		</>
	);
}

/** Renders `children` over a stateful draft; `seen.draft` is always the latest. */
function mount(
	service: Service,
	errors: DraftErrors,
	over: Partial<SettingsDraft>,
	children: (props: DraftProps) => ReactNode,
) {
	const baseline = toDraft(service);
	const seen: Seen = { draft: { ...baseline, ...over } };
	return {
		baseline,
		seen,
		ui: (
			<Harness seen={seen} errors={errors}>
				{children}
			</Harness>
		),
	};
}

/** The `<details>` ("More options") a control sits in, or null when it is not in one. */
function detailsOf(element: HTMLElement): HTMLDetailsElement | null {
	return element.closest("details");
}

async function pickType(label: string) {
	const user = userEvent.setup();
	const group = screen.getByRole("group", { name: "Health check type" });
	await user.click(within(group).getByText(label));
	return user;
}

describe("HealthSection", () => {
	const service = makeService();

	function health(
		over: Partial<SettingsDraft> = {},
		errors: DraftErrors = {},
		svc: Service = service,
	) {
		return mount(svc, errors, over, (props) => <HealthSection {...props} />);
	}

	test("an HTTP check asks for a path and a port, not a command", async () => {
		const { ui } = health();
		await renderWithProviders(ui);
		expect(screen.getByLabelText("Path")).toHaveValue("/");
		expect(screen.getByLabelText("Port")).toBeInTheDocument();
		expect(screen.queryByLabelText("Command")).toBeNull();
		expect(
			screen.getByText("A status from 200 to 399 passes."),
		).toBeInTheDocument();
	});

	test("the port placeholder is the service port, or 'service port' without one", async () => {
		const first = health({ port: "8080" });
		const { unmount } = await renderWithProviders(first.ui);
		expect(screen.getByLabelText("Port")).toHaveAttribute(
			"placeholder",
			"8080",
		);
		unmount();
		const second = health({ port: "" });
		await renderWithProviders(second.ui);
		expect(screen.getByLabelText("Port")).toHaveAttribute(
			"placeholder",
			"service port",
		);
	});

	test("switching to TCP drops the path, keeps the port and stages the type", async () => {
		const { ui, seen, baseline } = health();
		await renderWithProviders(ui);
		await pickType("TCP");
		expect(seen.draft.healthType).toBe("tcp");
		expect(screen.queryByLabelText("Path")).toBeNull();
		expect(screen.getByLabelText("Port")).toBeInTheDocument();
		expect(
			screen.getByText("Passes when a connection to the port opens."),
		).toBeInTheDocument();
		const patch = buildPatch(baseline, seen.draft, "image");
		expect(patch?.config?.healthcheck?.type).toBe("tcp");
	});

	test("a command check asks for the command only, and it is sent as argv", async () => {
		const { ui, seen, baseline } = health();
		await renderWithProviders(ui);
		await pickType("Command");
		expect(screen.queryByLabelText("Path")).toBeNull();
		expect(screen.queryByLabelText("Port")).toBeNull();
		const command = screen.getByLabelText("Command");
		expect(command).toHaveAttribute(
			"placeholder",
			'CMD-SHELL "pg_isready -U app"',
		);
		fireEvent.change(command, {
			target: { value: 'CMD-SHELL "pg_isready -U app"' },
		});
		expect(seen.draft.healthType).toBe("exec");
		expect(seen.draft.healthCommand).toBe('CMD-SHELL "pg_isready -U app"');
		expect(validateDraft(seen.draft, "image").healthCommand).toBeUndefined();
		const patch = buildPatch(baseline, seen.draft, "image");
		expect(patch?.config?.healthcheck).toMatchObject({
			type: "exec",
			command: ["CMD-SHELL", "pg_isready -U app"],
		});
	});

	test("a command without CMD or CMD-SHELL is refused with the form's message", async () => {
		const { ui, seen } = health({ healthType: "exec", healthCommand: "" });
		await renderWithProviders(ui);
		fireEvent.change(screen.getByLabelText("Command"), {
			target: { value: "pg_isready" },
		});
		const errors = validateDraft(seen.draft, "image");
		expect(errors.healthCommand).toBe(
			'Start with CMD or CMD-SHELL, then the command: CMD-SHELL "pg_isready -U app".',
		);
	});

	test("shows the command error under the command field", async () => {
		const { ui } = health(
			{ healthType: "exec", healthCommand: "oops" },
			{ healthCommand: "Start with CMD or CMD-SHELL." },
		);
		await renderWithProviders(ui);
		expect(
			screen.getByText("Start with CMD or CMD-SHELL."),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Command")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
	});

	test("the Docker check uses the image's own status and has no probe fields", async () => {
		const { ui, seen } = health();
		await renderWithProviders(ui);
		await pickType("Docker");
		expect(seen.draft.healthType).toBe("docker");
		expect(screen.queryByLabelText("Path")).toBeNull();
		expect(screen.queryByLabelText("Port")).toBeNull();
		expect(screen.queryByLabelText("Command")).toBeNull();
		expect(
			screen.getByText("Uses the image's own HEALTHCHECK status."),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Interval (s)")).toBeInTheDocument();
	});

	test("None hides every field and warns that a broken release can go live", async () => {
		const { ui, seen } = health();
		await renderWithProviders(ui);
		await pickType("None");
		expect(seen.draft.healthType).toBe("none");
		for (const label of [
			"Path",
			"Port",
			"Command",
			"Interval (s)",
			"Timeout (s)",
			"Deadline (s)",
		]) {
			expect(screen.queryByLabelText(label)).toBeNull();
		}
		expect(screen.queryByText("More options (5)")).toBeNull();
		expect(
			screen.getByText(/A broken release can go live unnoticed/),
		).toBeInTheDocument();
	});

	test("the path and port are typed into the draft and sent in the patch", async () => {
		const { ui, seen, baseline } = health({ port: "3000" });
		await renderWithProviders(ui);
		fireEvent.change(screen.getByLabelText("Path"), {
			target: { value: " /healthz " },
		});
		fireEvent.change(screen.getByLabelText("Port"), {
			target: { value: "3001" },
		});
		expect(seen.draft.healthPath).toBe(" /healthz ");
		expect(seen.draft.healthPort).toBe("3001");
		const patch = buildPatch(baseline, seen.draft, "image");
		expect(patch?.config?.healthcheck).toMatchObject({
			type: "http",
			path: "/healthz",
			port: 3001,
		});
	});

	test("the five timings sit behind 'More options (5)' and edit the draft", async () => {
		const { ui, seen, baseline } = health();
		await renderWithProviders(ui);
		expect(screen.getByText("More options (5)")).toBeInTheDocument();
		const interval = screen.getByLabelText("Interval (s)");
		expect(detailsOf(interval)?.open).toBe(false);
		expect(interval).toHaveValue("2");
		expect(screen.getByLabelText("Timeout (s)")).toHaveValue("3");
		expect(screen.getByLabelText("Start period (s)")).toHaveValue("0");
		expect(screen.getByLabelText("Deadline (s)")).toHaveValue("120");
		expect(screen.getByLabelText("Success threshold")).toHaveValue("2");

		fireEvent.change(interval, { target: { value: "5" } });
		fireEvent.change(screen.getByLabelText("Timeout (s)"), {
			target: { value: "4" },
		});
		fireEvent.change(screen.getByLabelText("Start period (s)"), {
			target: { value: "10" },
		});
		fireEvent.change(screen.getByLabelText("Deadline (s)"), {
			target: { value: "60" },
		});
		fireEvent.change(screen.getByLabelText("Success threshold"), {
			target: { value: "3" },
		});
		expect(seen.draft).toMatchObject({
			intervalSeconds: "5",
			timeoutSeconds: "4",
			startPeriodSeconds: "10",
			deadlineSeconds: "60",
			successThreshold: "3",
		});
		expect(
			buildPatch(baseline, seen.draft, "image")?.config?.healthcheck,
		).toMatchObject({
			intervalSeconds: 5,
			timeoutSeconds: 4,
			startPeriodSeconds: 10,
			deadlineSeconds: 60,
			successThreshold: 3,
		});
	});

	test("timing boundaries: 0 start period passes, 0 interval and 3601 deadline fail", async () => {
		const { ui, seen } = health();
		await renderWithProviders(ui);
		fireEvent.change(screen.getByLabelText("Start period (s)"), {
			target: { value: "0" },
		});
		fireEvent.change(screen.getByLabelText("Interval (s)"), {
			target: { value: "0" },
		});
		fireEvent.change(screen.getByLabelText("Deadline (s)"), {
			target: { value: "3601" },
		});
		fireEvent.change(screen.getByLabelText("Success threshold"), {
			target: { value: "1.5" },
		});
		const errors = validateDraft(seen.draft, "image");
		expect(errors.startPeriodSeconds).toBeUndefined();
		expect(errors.intervalSeconds).toBe("Interval must be between 1 and 3600.");
		expect(errors.deadlineSeconds).toBe("Deadline must be between 1 and 3600.");
		expect(errors.successThreshold).toBe(
			"Success threshold must be a whole number.",
		);
	});

	test("a timing error opens the group and shows its message under the field", async () => {
		const { ui } = health(
			{},
			{
				intervalSeconds: "Interval must be between 1 and 3600.",
				successThreshold: "Success threshold is required.",
			},
		);
		await renderWithProviders(ui);
		const interval = screen.getByLabelText("Interval (s)");
		expect(detailsOf(interval)?.open).toBe(true);
		expect(
			screen.getByText("Interval must be between 1 and 3600."),
		).toBeInTheDocument();
		expect(
			screen.getByText("Success threshold is required."),
		).toBeInTheDocument();
		expect(interval).toHaveAttribute("aria-invalid", "true");
		expect(screen.getByLabelText("Timeout (s)")).toHaveAttribute(
			"aria-invalid",
			"false",
		);
	});

	test("shows the port error and the type error", async () => {
		const { ui } = health(
			{},
			{
				healthPort: "Health check port must be between 1 and 65535.",
				healthType: "An HTTP health check needs the service to have a port.",
			},
		);
		await renderWithProviders(ui);
		expect(
			screen.getByText("Health check port must be between 1 and 65535."),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				"An HTTP health check needs the service to have a port.",
			),
		).toBeInTheDocument();
	});

	test("an HTTP check without a service port is refused by validation", async () => {
		const { ui, seen } = health({ port: "" });
		await renderWithProviders(ui);
		expect(validateDraft(seen.draft, "image").healthType).toBe(
			"An HTTP health check needs the service to have a port.",
		);
		await pickType("TCP");
		expect(validateDraft(seen.draft, "image").healthType).toBeUndefined();
	});
});

describe("HealthFields", () => {
	const value: HealthValue = {
		type: "http",
		command: "",
		path: "/up",
		port: "",
		intervalSeconds: "2",
		timeoutSeconds: "3",
		startPeriodSeconds: "0",
		deadlineSeconds: "120",
		successThreshold: "2",
	};

	function fields(
		over: Partial<Parameters<typeof HealthFields>[0]> = {},
	): ReactNode {
		return (
			<HealthFields
				value={value}
				errors={{}}
				onChange={() => {}}
				idPrefix="proc-worker"
				portHint="process port"
				portPlaceholder="9000"
				{...over}
			/>
		);
	}

	test("control ids are prefixed, the port hint and placeholder come from the caller", async () => {
		await renderWithProviders(fields());
		expect(screen.getByLabelText("Path")).toHaveAttribute(
			"id",
			"proc-worker-path",
		);
		const port = screen.getByLabelText("Port");
		expect(port).toHaveAttribute("id", "proc-worker-port");
		expect(port).toHaveAttribute("placeholder", "9000");
		expect(
			screen.getByText("Defaults to the process port"),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Interval (s)")).toHaveAttribute(
			"id",
			"proc-worker-interval",
		);
	});

	test("names whose check it is in the type control's label", async () => {
		await renderWithProviders(fields({ subject: " of worker" }));
		expect(
			screen.getByRole("group", { name: "Health check type of worker" }),
		).toBeInTheDocument();
	});

	test("inline timings are shown in place, not behind More options", async () => {
		await renderWithProviders(fields({ inlineTimings: true }));
		expect(screen.queryByText(/More options/)).toBeNull();
		expect(detailsOf(screen.getByLabelText("Interval (s)"))).toBeNull();
	});

	test("reports each change with its field key", async () => {
		const changes: Array<[string, string]> = [];
		await renderWithProviders(
			fields({
				onChange: (key, next) => changes.push([key, String(next)]),
			}),
		);
		fireEvent.change(screen.getByLabelText("Path"), {
			target: { value: "/x" },
		});
		fireEvent.change(screen.getByLabelText("Port"), {
			target: { value: "81" },
		});
		fireEvent.change(screen.getByLabelText("Deadline (s)"), {
			target: { value: "9" },
		});
		const group = screen.getByRole("group", { name: "Health check type" });
		await userEvent.setup().click(within(group).getByText("TCP"));
		expect(changes).toEqual([
			["path", "/x"],
			["port", "81"],
			["deadlineSeconds", "9"],
			["type", "tcp"],
		]);
	});

	test("shows the whole-check error under the type when there is no type error", async () => {
		await renderWithProviders(fields({ error: "Probe unreachable." }));
		expect(screen.getByText("Probe unreachable.")).toBeInTheDocument();
	});

	test("a type error wins over the whole-check error", async () => {
		await renderWithProviders(
			fields({ error: "Probe unreachable.", errors: { type: "Bad type." } }),
		);
		expect(screen.getByText("Bad type.")).toBeInTheDocument();
		expect(screen.queryByText("Probe unreachable.")).toBeNull();
	});
});

describe("CommandsSection", () => {
	const service = makeService();

	function commands(
		over: Partial<SettingsDraft> = {},
		errors: DraftErrors = {},
		svc: Service = service,
	) {
		return mount(svc, errors, over, (props) => (
			<CommandsSection service={svc} {...props} />
		));
	}

	test("a single-process service shows a 'Commands' section", async () => {
		const { ui } = commands();
		await renderWithProviders(ui);
		expect(
			screen.getByRole("region", { name: "Commands" }),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Start command")).toHaveValue("");
	});

	test("with additional processes it is the default process' section, named by processName", async () => {
		const withProcess = makeService({
			processes: [{ name: "worker" } as Service["processes"][number]],
		});
		const { ui } = commands({ processName: "api  " }, {}, withProcess);
		await renderWithProviders(ui);
		expect(
			screen.getByRole("region", { name: "Default process (api)" }),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				"What runs the default process, and around the release.",
			),
		).toBeInTheDocument();
	});

	test("a draft that already has processes counts as several, and the name defaults to web", async () => {
		const base = toDraft(service);
		const { ui } = commands({
			processes: [
				{
					...(base.processes[0] ?? {}),
					name: "worker",
				} as SettingsDraft["processes"][number],
			],
		});
		await renderWithProviders(ui);
		expect(
			screen.getByRole("region", { name: "Default process (web)" }),
		).toBeInTheDocument();
	});

	test("the start command is stored as typed and sent as argv with quoting", async () => {
		const { ui, seen, baseline } = commands();
		await renderWithProviders(ui);
		fireEvent.change(screen.getByLabelText("Start command"), {
			target: { value: 'node server.js --name "my app"' },
		});
		expect(seen.draft.startCommand).toBe('node server.js --name "my app"');
		expect(
			buildPatch(baseline, seen.draft, "image")?.config?.startCommand,
		).toEqual(["node", "server.js", "--name", "my app"]);
	});

	test("shows the saved start command with spaced arguments quoted", async () => {
		const svc = makeService();
		svc.config = { ...svc.config, startCommand: ["sh", "-c", "echo hi"] };
		const { ui } = commands(toDraft(svc), {}, svc);
		await renderWithProviders(ui);
		expect(screen.getByLabelText("Start command")).toHaveValue(
			'sh -c "echo hi"',
		);
	});

	test("the singleton switch toggles the draft and the patch", async () => {
		const user = userEvent.setup();
		const { ui, seen, baseline } = commands();
		await renderWithProviders(ui);
		const toggle = screen.getByRole("switch", {
			name: "Never run two instances at once",
		});
		expect(toggle).not.toBeChecked();
		await user.click(toggle);
		expect(seen.draft.singleton).toBe(true);
		expect(toggle).toBeChecked();
		expect(buildPatch(baseline, seen.draft, "image")?.config?.singleton).toBe(
			true,
		);
		await user.click(toggle);
		expect(seen.draft.singleton).toBe(false);
		expect(buildPatch(baseline, seen.draft, "image")).toBeNull();
	});

	test("without pre- or post-deploy commands there are no timeout fields", async () => {
		const { ui } = commands();
		await renderWithProviders(ui);
		expect(screen.queryByLabelText("Pre-deploy timeout (s)")).toBeNull();
		expect(screen.queryByLabelText("Post-deploy timeout (s)")).toBeNull();
		expect(screen.queryByText(/More options/)).toBeNull();
	});

	test("a whitespace-only pre-deploy command does not offer a timeout", async () => {
		const { ui } = commands({ preDeployCommand: "   " });
		await renderWithProviders(ui);
		expect(screen.queryByLabelText("Pre-deploy timeout (s)")).toBeNull();
	});

	test("typing a pre-deploy command offers its timeout, and both go into the patch", async () => {
		const { ui, seen, baseline } = commands();
		await renderWithProviders(ui);
		fireEvent.change(screen.getByLabelText("Pre-deploy command"), {
			target: { value: "rake db:migrate" },
		});
		expect(await screen.findByText("More options (1)")).toBeInTheDocument();
		expect(screen.queryByLabelText("Post-deploy timeout (s)")).toBeNull();
		const timeout = screen.getByLabelText("Pre-deploy timeout (s)");
		expect(timeout).toHaveAttribute("placeholder", "600");
		fireEvent.change(timeout, { target: { value: "120" } });
		expect(seen.draft.preDeployTimeoutSeconds).toBe("120");
		const config = buildPatch(baseline, seen.draft, "image")?.config;
		expect(config?.preDeployCommand).toEqual(["rake", "db:migrate"]);
		expect(config?.preDeployTimeoutSeconds).toBe(120);
	});

	test("post-deploy command and timeout are independent of the pre-deploy ones", async () => {
		const { ui, seen, baseline } = commands();
		await renderWithProviders(ui);
		fireEvent.change(screen.getByLabelText("Post-deploy command"), {
			target: { value: "rake cache:warm" },
		});
		expect(await screen.findByText("More options (1)")).toBeInTheDocument();
		expect(screen.queryByLabelText("Pre-deploy timeout (s)")).toBeNull();
		fireEvent.change(screen.getByLabelText("Post-deploy timeout (s)"), {
			target: { value: "30" },
		});
		const config = buildPatch(baseline, seen.draft, "image")?.config;
		expect(config?.postDeployCommand).toEqual(["rake", "cache:warm"]);
		expect(config?.postDeployTimeoutSeconds).toBe(30);
		expect(config?.preDeployCommand).toBeUndefined();
	});

	test("both commands count as two options", async () => {
		const { ui } = commands({
			preDeployCommand: "a",
			postDeployCommand: "b",
		});
		await renderWithProviders(ui);
		expect(screen.getByText("More options (2)")).toBeInTheDocument();
		expect(screen.getByLabelText("Pre-deploy timeout (s)")).toBeInTheDocument();
		expect(
			screen.getByLabelText("Post-deploy timeout (s)"),
		).toBeInTheDocument();
	});

	test("timeouts are validated: 0 and 3601 are refused, 3600 passes", async () => {
		const { ui, seen } = commands({
			preDeployCommand: "a",
			postDeployCommand: "b",
		});
		await renderWithProviders(ui);
		fireEvent.change(screen.getByLabelText("Pre-deploy timeout (s)"), {
			target: { value: "0" },
		});
		fireEvent.change(screen.getByLabelText("Post-deploy timeout (s)"), {
			target: { value: "3600" },
		});
		let errors = validateDraft(seen.draft, "image");
		expect(errors.preDeployTimeoutSeconds).toBe(
			"Pre-deploy timeout must be between 1 and 3600.",
		);
		expect(errors.postDeployTimeoutSeconds).toBeUndefined();
		fireEvent.change(screen.getByLabelText("Post-deploy timeout (s)"), {
			target: { value: "3601" },
		});
		errors = validateDraft(seen.draft, "image");
		expect(errors.postDeployTimeoutSeconds).toBe(
			"Post-deploy timeout must be between 1 and 3600.",
		);
	});

	test("shows command and timeout errors, and opens the timeout group", async () => {
		const { ui } = commands(
			{ preDeployCommand: "a", postDeployCommand: "b" },
			{
				preDeployCommand: "Pre-deploy command is malformed.",
				postDeployCommand: "Post-deploy command is malformed.",
				preDeployTimeoutSeconds:
					"Pre-deploy timeout must be between 1 and 3600.",
				postDeployTimeoutSeconds: "Post-deploy timeout must be a whole number.",
			},
		);
		await renderWithProviders(ui);
		expect(
			screen.getByText("Pre-deploy command is malformed."),
		).toBeInTheDocument();
		expect(
			screen.getByText("Post-deploy command is malformed."),
		).toBeInTheDocument();
		expect(
			screen.getByText("Pre-deploy timeout must be between 1 and 3600."),
		).toBeInTheDocument();
		expect(
			screen.getByText("Post-deploy timeout must be a whole number."),
		).toBeInTheDocument();
		expect(
			detailsOf(screen.getByLabelText("Pre-deploy timeout (s)"))?.open,
		).toBe(true);
	});
});

interface PlanWire {
	strategy: "rolling" | "recreate" | "route_only";
	reasons?: string[] | null;
	warnings?: string[] | null;
	blocking?: Array<{ code: string; message: string }> | null;
	placement?: Partial<PlacementView>;
}

function plan(over: PlanWire) {
	return planWire({
		strategy: over.strategy,
		reasons: over.reasons ?? [],
		warnings: over.warnings ?? [],
		blocking: over.blocking ?? [],
		placement: over.placement ? placementWire(over.placement) : null,
	});
}

describe("ReleasesSection", () => {
	const service = makeService({ id: "svc-1" });

	function releases(
		over: Partial<SettingsDraft> = {},
		errors: DraftErrors = {},
	) {
		return mount(service, errors, over, (props) => (
			<ReleasesSection service={service} {...props} />
		));
	}

	test("asks the control plane for this service's plan", async () => {
		const api = stubApi({
			"POST /services/:serviceId/deployments/plan": {
				body: plan({ strategy: "rolling" }),
			},
		});
		const { ui } = releases();
		await renderWithProviders(ui);
		await screen.findByText(/Next deploy:/);
		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]?.method).toBe("POST");
		expect(api.calls[0]?.path).toBe("/services/svc-1/deployments/plan");
		expect(api.unmatched).toEqual([]);
	});

	test("says it is planning while the plan loads", async () => {
		const { promise, resolve } = Promise.withResolvers<void>();
		stubApi({
			"POST /services/:serviceId/deployments/plan": async () => {
				await promise;
				return Response.json(plan({ strategy: "rolling" }));
			},
		});
		const { ui } = releases();
		await renderWithProviders(ui);
		expect(screen.getByText("Planning the next deploy…")).toBeInTheDocument();
		resolve();
		expect(await screen.findByText(/Next deploy:/)).toBeInTheDocument();
		expect(screen.queryByText("Planning the next deploy…")).toBeNull();
	});

	test("a rolling plan says the new container starts first", async () => {
		stubApi({
			"POST /services/:serviceId/deployments/plan": {
				body: plan({ strategy: "rolling" }),
			},
		});
		const { ui } = releases();
		await renderWithProviders(ui);
		const line = await screen.findByText(/Next deploy:/);
		expect(line).toHaveTextContent("Next deploy: Rolling.");
		expect(line).toHaveTextContent(
			"The new container starts before the old one stops.",
		);
		expect(screen.queryByText(/^Why:/)).toBeNull();
	});

	test("a recreate plan says there is a short gap, with the reasons", async () => {
		stubApi({
			"POST /services/:serviceId/deployments/plan": {
				body: plan({
					strategy: "recreate",
					reasons: ["has volumes", "singleton"],
				}),
			},
		});
		const { ui } = releases();
		await renderWithProviders(ui);
		const line = await screen.findByText(/Next deploy:/);
		expect(line).toHaveTextContent("Next deploy: Recreate.");
		expect(line).toHaveTextContent(
			"The old container stops first, so there is a short gap.",
		);
		expect(
			screen.getByText("Why: has volumes · singleton"),
		).toBeInTheDocument();
	});

	test("lists plan warnings and blockers as alerts", async () => {
		stubApi({
			"POST /services/:serviceId/deployments/plan": {
				body: plan({
					strategy: "recreate",
					warnings: ["Volumes are not migrated."],
					blocking: [
						{ code: "no_server", message: "No server accepts this service." },
					],
				}),
			},
		});
		const { ui } = releases();
		await renderWithProviders(ui);
		expect(
			await screen.findByText("Volumes are not migrated."),
		).toBeInTheDocument();
		expect(
			screen.getByText("No server accepts this service."),
		).toBeInTheDocument();
		expect(screen.getAllByRole("alert")).toHaveLength(2);
	});

	test("a warning the placement already shows is not repeated", async () => {
		stubApi({
			"POST /services/:serviceId/deployments/plan": {
				body: plan({
					strategy: "rolling",
					warnings: ["Built on the app's server instead.", "Image is large."],
					placement: {
						location: "target",
						warnings: [
							{
								code: "fallback",
								message: "Built on the app's server instead.",
							},
						],
					},
				}),
			},
		});
		const { ui } = releases();
		await renderWithProviders(ui);
		expect(await screen.findByText("Image is large.")).toBeInTheDocument();
		expect(screen.queryByText("Built on the app's server instead.")).toBeNull();
	});

	test("shows the control plane's reason when no plan can be made", async () => {
		stubApi({
			"POST /services/:serviceId/deployments/plan": {
				status: 422,
				body: {
					status: 422,
					code: "invalid_config",
					title: "Unprocessable",
					detail: "the image is missing",
				},
			},
		});
		const { ui } = releases();
		await renderWithProviders(ui);
		expect(
			await screen.findByText("No plan yet: the image is missing"),
		).toBeInTheDocument();
		expect(screen.queryByText(/Next deploy:/)).toBeNull();
	});

	test("stop grace and images kept show the saved values behind 'More options (2)'", async () => {
		stubApi({
			"POST /services/:serviceId/deployments/plan": {
				body: plan({ strategy: "rolling" }),
			},
		});
		const { ui } = releases();
		await renderWithProviders(ui);
		expect(screen.getByText("More options (2)")).toBeInTheDocument();
		const grace = screen.getByLabelText("Stop grace period (s)");
		expect(grace).toHaveValue("30");
		expect(detailsOf(grace)?.open).toBe(false);
		expect(screen.getByLabelText("Images kept")).toHaveValue("5");
		await screen.findByText(/Next deploy:/);
	});

	test("editing them stages the new numbers in the patch", async () => {
		stubApi({
			"POST /services/:serviceId/deployments/plan": {
				body: plan({ strategy: "rolling" }),
			},
		});
		const { ui, seen, baseline } = releases();
		await renderWithProviders(ui);
		fireEvent.change(screen.getByLabelText("Stop grace period (s)"), {
			target: { value: "90" },
		});
		fireEvent.change(screen.getByLabelText("Images kept"), {
			target: { value: "10" },
		});
		expect(seen.draft.stopGraceSeconds).toBe("90");
		expect(seen.draft.imageRetention).toBe("10");
		const config = buildPatch(baseline, seen.draft, "image")?.config;
		expect(config?.stopGraceSeconds).toBe(90);
		expect(config?.imageRetention).toBe(10);
		await screen.findByText(/Next deploy:/);
	});

	test("stop grace and retention boundaries are validated", async () => {
		stubApi({
			"POST /services/:serviceId/deployments/plan": {
				body: plan({ strategy: "rolling" }),
			},
		});
		const { ui, seen } = releases();
		await renderWithProviders(ui);
		fireEvent.change(screen.getByLabelText("Stop grace period (s)"), {
			target: { value: "0" },
		});
		fireEvent.change(screen.getByLabelText("Images kept"), {
			target: { value: "51" },
		});
		let errors = validateDraft(seen.draft, "image");
		expect(errors.stopGraceSeconds).toBe(
			"Stop grace period must be between 1 and 3600.",
		);
		expect(errors.imageRetention).toBe(
			"Image retention must be between 1 and 50.",
		);
		fireEvent.change(screen.getByLabelText("Stop grace period (s)"), {
			target: { value: "3600" },
		});
		fireEvent.change(screen.getByLabelText("Images kept"), {
			target: { value: "50" },
		});
		errors = validateDraft(seen.draft, "image");
		expect(errors.stopGraceSeconds).toBeUndefined();
		expect(errors.imageRetention).toBeUndefined();
		await screen.findByText(/Next deploy:/);
	});

	test("an error opens the group and shows under the field", async () => {
		stubApi({
			"POST /services/:serviceId/deployments/plan": {
				body: plan({ strategy: "rolling" }),
			},
		});
		const { ui } = releases(
			{},
			{ imageRetention: "Image retention must be between 1 and 50." },
		);
		await renderWithProviders(ui);
		const retention = screen.getByLabelText("Images kept");
		expect(detailsOf(retention)?.open).toBe(true);
		expect(
			screen.getByText("Image retention must be between 1 and 50."),
		).toBeInTheDocument();
		expect(retention).toHaveAttribute("aria-invalid", "true");
		await waitFor(() => screen.getByText(/Next deploy:/));
	});
});
