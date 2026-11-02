import { describe, expect, mock, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import {
	newProcessDraft,
	type ProcessDraft,
	type ProcessErrors,
	toProcessDraft,
} from "./draft";
import { makeProcess, makeProcessService, makeScaling } from "./fixtures";
import { ProcessEditor } from "./process-editor";

const SERVICE = makeProcessService({
	processes: [
		makeProcess({ name: "web", default: true, port: 3000 }),
		makeProcess({
			name: "worker",
			scaling: makeScaling({ replicas: 2, ready: 2 }),
		}),
		makeProcess({ name: "queued", pending: true, observed: null }),
	],
});

const INHERITED = { stopGraceSeconds: "30", memoryMb: "", cpus: "0.5" };

function saved(over: Partial<ProcessDraft> = {}): ProcessDraft {
	return {
		...toProcessDraft({
			name: "worker",
			startCommand: ["bin/jobs", "--queue", "default"],
			port: 4000,
			healthcheck: {
				type: "http",
				path: "/up",
				intervalSeconds: 5,
				timeoutSeconds: 3,
				startPeriodSeconds: 0,
				deadlineSeconds: 120,
				successThreshold: 2,
			},
		}),
		...over,
	};
}

interface EditorOptions {
	errors?: ProcessErrors;
	pending?: boolean;
	onRemove?: () => void;
}

/** Keeps the draft in state like the section does, and records each patch. */
function EditorHarness({
	initial,
	options,
	patches,
}: {
	initial: ProcessDraft;
	options: EditorOptions;
	patches: Partial<ProcessDraft>[];
}) {
	const [process, setProcess] = useState(initial);
	return (
		<ul>
			<ProcessEditor
				service={SERVICE}
				process={process}
				errors={options.errors ?? {}}
				inherited={INHERITED}
				pending={options.pending ?? false}
				onChange={(patch) => {
					patches.push(patch);
					setProcess((current) => ({ ...current, ...patch }));
				}}
				onRemove={options.onRemove ?? (() => {})}
			/>
		</ul>
	);
}

async function renderEditor(
	initial: ProcessDraft,
	options: EditorOptions = {},
) {
	const patches: Partial<ProcessDraft>[] = [];
	await renderWithProviders(
		<EditorHarness initial={initial} options={options} patches={patches} />,
	);
	return patches;
}

// "Port" labels the process' own port first and, for HTTP/TCP checks, the health check's after it.
const processPort = () => screen.getAllByLabelText("Port")[0] as HTMLElement;
const healthPort = () => screen.getAllByLabelText("Port")[1] as HTMLElement;

describe("ProcessEditor header", () => {
	test("a saved process shows its name, no badge, and a remove button named after it", async () => {
		stubApi({});
		await renderEditor(saved());
		const card = screen.getByRole("listitem", { name: "Process worker" });
		expect(
			within(card).getByText("worker", { selector: "span" }),
		).toBeInTheDocument();
		expect(within(card).queryByText("New")).not.toBeInTheDocument();
		expect(within(card).queryByText("Staged")).not.toBeInTheDocument();
		expect(
			within(card).getByRole("button", { name: "Remove worker" }),
		).toBeInTheDocument();
	});

	test("a row added in this session is badged New and, unnamed, reads New process", async () => {
		stubApi({});
		await renderEditor({ ...newProcessDraft(""), name: "" });
		const card = screen.getByRole("listitem", { name: "Process this process" });
		expect(within(card).getByText("New process")).toBeInTheDocument();
		expect(within(card).getByText("New")).toBeInTheDocument();
		expect(
			within(card).getByRole("button", { name: "Remove this process" }),
		).toBeInTheDocument();
	});

	test("a saved process that is staged but not running yet is badged Staged", async () => {
		stubApi({});
		await renderEditor(saved(), { pending: true });
		expect(screen.getByText("Staged")).toBeInTheDocument();
		expect(screen.queryByText("New")).not.toBeInTheDocument();
	});

	test("Remove calls onRemove once", async () => {
		stubApi({});
		const onRemove = mock(() => {});
		await renderEditor(saved(), { onRemove });
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Remove worker" }));
		expect(onRemove).toHaveBeenCalledTimes(1);
	});
});

describe("ProcessEditor fields", () => {
	test("shows the draft's values", async () => {
		stubApi({});
		await renderEditor(saved({ singleton: true }));
		expect(screen.getByLabelText("Name")).toHaveValue("worker");
		expect(screen.getByLabelText("Start command")).toHaveValue(
			"bin/jobs --queue default",
		);
		expect(processPort()).toHaveValue("4000");
		expect(
			screen.getByRole("switch", { name: "Never run two instances at once" }),
		).toBeChecked();
	});

	test("typing in the name, command and port patches only that field", async () => {
		stubApi({});
		const user = userEvent.setup();
		const patches = await renderEditor(
			saved({ name: "", command: "", port: "" }),
		);
		await user.type(screen.getByLabelText("Name"), "a");
		await user.type(screen.getByLabelText("Start command"), "x");
		await user.type(processPort(), "8");
		expect(patches).toEqual([{ name: "a" }, { command: "x" }, { port: "8" }]);
		expect(screen.getByLabelText("Name")).toHaveValue("a");
	});

	test("the singleton switch patches singleton", async () => {
		stubApi({});
		const patches = await renderEditor(saved());
		await userEvent
			.setup()
			.click(
				screen.getByRole("switch", { name: "Never run two instances at once" }),
			);
		expect(patches).toEqual([{ singleton: true }]);
	});

	test("renaming a saved process says it was renamed and its scale and domains move", async () => {
		stubApi({});
		await renderEditor(saved({ name: "jobs" }));
		expect(
			screen.getByText(
				"Renamed from worker. Its scale and domains move with it.",
			),
		).toBeInTheDocument();
	});

	test("an unrenamed process describes what the name is for", async () => {
		stubApi({});
		await renderEditor(saved());
		expect(
			screen.getByText(
				"Names the containers, logs and metrics of this process.",
			),
		).toBeInTheDocument();
		expect(screen.queryByText(/Renamed from/)).not.toBeInTheDocument();
	});

	test("shows the errors it is given under their fields", async () => {
		stubApi({});
		await renderEditor(saved(), {
			errors: {
				name: "Name is required.",
				command: "A start command is required.",
				port: "Port must be a whole number.",
			},
		});
		expect(screen.getByLabelText("Name")).toBeInvalid();
		expect(screen.getByLabelText("Name")).toHaveAccessibleDescription(
			expect.stringContaining("Name is required."),
		);
		expect(
			screen.getByText("A start command is required."),
		).toBeInTheDocument();
		expect(
			screen.getByText("Port must be a whole number."),
		).toBeInTheDocument();
		expect(processPort()).toBeInvalid();
	});
});

describe("ProcessEditor limits", () => {
	const limits = () =>
		screen.getByText("Shutdown and limits (3)").closest("details");

	test("are collapsed by default and show the service's values as placeholders", async () => {
		stubApi({});
		await renderEditor(saved());
		expect(limits()).not.toHaveAttribute("open");
		expect(screen.getByLabelText("Stop grace period (s)")).toHaveAttribute(
			"placeholder",
			"30",
		);
		expect(screen.getByLabelText("Memory (MB)")).toHaveAttribute(
			"placeholder",
			"no limit",
		);
		expect(screen.getByLabelText("CPUs")).toHaveAttribute("placeholder", "0.5");
	});

	test("an inherited service without a stop grace falls back to the generic placeholder", async () => {
		stubApi({});
		const user = userEvent.setup();
		const patches: Partial<ProcessDraft>[] = [];
		await renderWithProviders(
			<ul>
				<ProcessEditor
					service={SERVICE}
					process={saved()}
					errors={{}}
					inherited={{ stopGraceSeconds: "", memoryMb: "256", cpus: "" }}
					pending={false}
					onChange={(patch) => patches.push(patch)}
					onRemove={() => {}}
				/>
			</ul>,
		);
		expect(screen.getByLabelText("Stop grace period (s)")).toHaveAttribute(
			"placeholder",
			"service setting",
		);
		expect(screen.getByLabelText("Memory (MB)")).toHaveAttribute(
			"placeholder",
			"256",
		);
		await user.type(screen.getByLabelText("CPUs"), "2");
		expect(patches).toEqual([{ cpus: "2" }]);
	});

	test("typing patches the limit fields", async () => {
		stubApi({});
		const user = userEvent.setup();
		const patches = await renderEditor(saved());
		await user.type(screen.getByLabelText("Stop grace period (s)"), "9");
		await user.type(screen.getByLabelText("Memory (MB)"), "5");
		await user.type(screen.getByLabelText("CPUs"), "1");
		expect(patches).toEqual([
			{ stopGraceSeconds: "9" },
			{ memoryMb: "5" },
			{ cpus: "1" },
		]);
	});

	test("a limit error opens the group so the problem is not hidden", async () => {
		stubApi({});
		await renderEditor(saved(), {
			errors: { memoryMb: "Memory must be between 6 and 1048576." },
		});
		expect(limits()).toHaveAttribute("open");
		expect(
			screen.getByText("Memory must be between 6 and 1048576."),
		).toBeVisible();
	});
});

describe("ProcessEditor health check", () => {
	test("shows the stored custom check of a saved process", async () => {
		stubApi({});
		await renderEditor(saved());
		expect(screen.getByLabelText("Path")).toHaveValue("/up");
		expect(screen.getByLabelText("Interval (s)")).toHaveValue("5");
	});

	test("a check nobody customized follows the process port", async () => {
		stubApi({});
		await renderEditor({ ...newProcessDraft("worker"), port: "7000" });
		const group = screen.getByRole("group", {
			name: "Health check type of worker",
		});
		expect(within(group).getByRole("button", { name: "HTTP" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		expect(healthPort()).toHaveAttribute("placeholder", "7000");
	});

	test("without a port the default check is None", async () => {
		stubApi({});
		await renderEditor({ ...newProcessDraft("worker"), port: "" });
		const group = screen.getByRole("group", {
			name: "Health check type of worker",
		});
		expect(within(group).getByRole("button", { name: "None" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
	});

	test("editing any field takes the check over from the default", async () => {
		stubApi({});
		const user = userEvent.setup();
		const patches = await renderEditor({
			...newProcessDraft("worker"),
			port: "7000",
		});
		await user.click(
			within(
				screen.getByRole("group", { name: "Health check type of worker" }),
			).getByRole("button", { name: "TCP" }),
		);
		expect(patches).toHaveLength(1);
		expect(patches[0]?.health).toMatchObject({ type: "tcp", custom: true });
		// The timings it carries over are the defaults the form showed.
		expect(patches[0]?.health).toMatchObject({
			intervalSeconds: "2",
			deadlineSeconds: "120",
		});
	});

	test("typing a timing patches the health draft and marks it custom", async () => {
		stubApi({});
		const user = userEvent.setup();
		const patches = await renderEditor(saved());
		await user.type(screen.getByLabelText("Interval (s)"), "1");
		expect(patches[0]?.health).toMatchObject({
			type: "http",
			path: "/up",
			intervalSeconds: "51",
			custom: true,
		});
	});

	test("a health error opens the group and is shown under the type", async () => {
		stubApi({});
		await renderEditor(saved(), {
			errors: { health: "This health check needs the process to have a port." },
		});
		expect(screen.getByText("Health check").closest("details")).toHaveAttribute(
			"open",
		);
		expect(
			screen.getByText("This health check needs the process to have a port."),
		).toBeVisible();
	});

	test("the label of a nameless process reads this process", async () => {
		stubApi({});
		await renderEditor({ ...newProcessDraft(""), name: "" });
		expect(
			screen.getByRole("group", { name: "Health check type of this process" }),
		).toBeInTheDocument();
	});
});

describe("ProcessEditor scale", () => {
	test("a saved process shows its scale from the service", async () => {
		stubApi({});
		await renderEditor(saved());
		expect(
			screen.getByText("Applies now, outside the save bar."),
		).toBeInTheDocument();
		expect(screen.getByText("2/2 replicas")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Scale…" })).toBeEnabled();
	});

	test("a renamed process keeps showing the scale of the name it was saved under", async () => {
		stubApi({});
		await renderEditor(saved({ name: "jobs" }));
		expect(screen.getByText("2/2 replicas")).toBeInTheDocument();
	});

	test("a new process has no scale until it is saved", async () => {
		stubApi({});
		await renderEditor(newProcessDraft("fresh"));
		expect(
			screen.getByText("Scale appears here once the process is saved."),
		).toBeInTheDocument();
	});
});
