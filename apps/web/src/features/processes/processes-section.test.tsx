import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type { Service } from "#/api/models";
import { type SettingsDraft, toDraft } from "#/features/service-settings/draft";
import type { DraftErrors } from "#/features/service-settings/fields";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { MAX_NAMED_PROCESSES } from "./draft";
import { makeProcess, makeProcessService, makeScaling } from "./fixtures";
import { ProcessesSection } from "./processes-section";

type ProcessConfig = NonNullable<Service["config"]["processes"]>[number];

function config(
	name: string,
	over: Partial<ProcessConfig> = {},
): ProcessConfig {
	return {
		name,
		startCommand: ["bin/jobs"],
		healthcheck: {
			type: "none",
			intervalSeconds: 2,
			timeoutSeconds: 3,
			startPeriodSeconds: 0,
			deadlineSeconds: 120,
			successThreshold: 2,
		},
		...over,
	};
}

function domain(hostname: string, process: string): Service["domains"][number] {
	return {
		id: `d-${hostname}`,
		hostname,
		process,
	} as Service["domains"][number];
}

function serviceWith(
	processes: ProcessConfig[],
	over: Partial<Service> = {},
	configOver: Partial<Service["config"]> = {},
): Service {
	const base = makeProcessService();
	return {
		...base,
		...over,
		config: {
			...base.config,
			port: 3000,
			startCommand: ["bin/rails", "server"],
			processes,
			...configOver,
		},
	};
}

/** The section over a draft kept in state, as the settings form does. */
function SectionHarness({
	service,
	errors,
	tweak,
	drafts,
}: {
	service: Service;
	errors: DraftErrors;
	tweak: (draft: SettingsDraft) => SettingsDraft;
	drafts: SettingsDraft[];
}) {
	const [draft, setDraft] = useState(() => tweak(toDraft(service)));
	drafts.push(draft);
	return (
		<>
			<input id="set-command" aria-label="Start command of the service" />
			<ProcessesSection
				service={service}
				draft={draft}
				errors={errors}
				set={(key, value) =>
					setDraft((current) => ({ ...current, [key]: value }))
				}
			/>
		</>
	);
}

async function renderSection(
	service: Service,
	errors: DraftErrors = {},
	tweak: (draft: SettingsDraft) => SettingsDraft = (d) => d,
) {
	const drafts: SettingsDraft[] = [];
	await renderWithProviders(
		<SectionHarness
			service={service}
			errors={errors}
			tweak={tweak}
			drafts={drafts}
		/>,
	);
	return { latest: () => drafts[drafts.length - 1] as SettingsDraft };
}

const list = () => screen.getByRole("list", { name: "Additional processes" });
const cards = () =>
	within(list()).getAllByRole("listitem", { name: /^Process / });

describe("ProcessesSection without processes", () => {
	test("explains the single process and offers to add one", async () => {
		stubApi({});
		await renderSection(serviceWith([]));
		expect(
			screen.getByText(/This service runs one process/),
		).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Add process" })).toBeEnabled();
		expect(
			screen.queryByRole("list", { name: "Additional processes" }),
		).not.toBeInTheDocument();
		expect(
			screen.queryByLabelText("Default process name"),
		).not.toBeInTheDocument();
	});

	test("Add process adds a worker row and moves focus to its command", async () => {
		stubApi({});
		const user = userEvent.setup();
		const view = await renderSection(serviceWith([]));
		await user.click(screen.getByRole("button", { name: "Add process" }));
		expect(view.latest().processes).toHaveLength(1);
		expect(view.latest().processes[0]).toMatchObject({
			name: "worker",
			originalName: null,
			command: "",
		});
		const card = within(list()).getByRole("listitem", {
			name: "Process worker",
		});
		expect(card).toBeInTheDocument();
		await waitFor(() =>
			expect(within(card).getByLabelText("Start command")).toHaveFocus(),
		);
		expect(
			screen.getByText("2 processes", { exact: false }),
		).toBeInTheDocument();
	});
});

describe("ProcessesSection with processes", () => {
	const SERVICE = serviceWith(
		[config("worker", { port: 4000 }), config("clock", { singleton: true })],
		{
			processes: [
				makeProcess({ name: "web", default: true, port: 3000 }),
				makeProcess({ name: "worker", port: 4000 }),
				makeProcess({ name: "clock", pending: true, observed: null }),
				makeProcess({ name: "old", removed: true }),
				makeProcess({ name: "older", removed: true }),
			],
		},
	);

	test("summarises the count and the default process, then one card per additional process", async () => {
		stubApi({});
		await renderSection(SERVICE);
		expect(
			screen.getByText(/3 processes run from the same build/),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Default process name")).toHaveValue("");
		expect(screen.getByLabelText("Default process name")).toHaveAttribute(
			"placeholder",
			"web",
		);
		expect(screen.getByText("The default process")).toBeInTheDocument();
		expect(screen.getByText("bin/rails server")).toBeInTheDocument();
		expect(screen.getByText("port 3000")).toBeInTheDocument();
		expect(cards().map((card) => card.getAttribute("aria-label"))).toEqual([
			"Process worker",
			"Process clock",
		]);
	});

	test("the default row follows the default process name as it is typed", async () => {
		stubApi({});
		const user = userEvent.setup();
		await renderSection(SERVICE);
		await user.type(screen.getByLabelText("Default process name"), "app");
		expect(screen.getByText("app", { selector: "span" })).toBeInTheDocument();
	});

	test("a default process without a port and a singleton say so", async () => {
		stubApi({});
		await renderSection(SERVICE, {}, (draft) => ({
			...draft,
			port: "",
			singleton: true,
		}));
		expect(
			screen.getByText("no port · one instance at a time"),
		).toBeInTheDocument();
	});

	test("lists the processes the next deploy removes", async () => {
		stubApi({});
		await renderSection(SERVICE);
		expect(
			screen.getByText("Removed, stops on the next deploy:", { exact: false }),
		).toHaveTextContent("old, older");
	});

	test("only a process missing from the running release is badged Staged", async () => {
		stubApi({});
		await renderSection(SERVICE);
		const [worker, clock] = cards();
		expect(
			within(worker as HTMLElement).queryByText("Staged"),
		).not.toBeInTheDocument();
		expect(
			within(clock as HTMLElement).getByText("Staged"),
		).toBeInTheDocument();
	});

	test("editing a card changes that process only", async () => {
		stubApi({});
		const user = userEvent.setup();
		const view = await renderSection(SERVICE);
		const [, clock] = cards();
		await user.type(
			within(clock as HTMLElement).getByLabelText("Start command"),
			" --fast",
		);
		expect(view.latest().processes.map((p) => p.command)).toEqual([
			"bin/jobs",
			"bin/jobs --fast",
		]);
	});

	test("Add process names the next free worker name, skipping the default's", async () => {
		stubApi({});
		const user = userEvent.setup();
		const view = await renderSection(SERVICE, {}, (draft) => ({
			...draft,
			processName: "worker-2",
			processes: [],
		}));
		// No rows yet: the empty state's button. Add twice.
		await user.click(screen.getByRole("button", { name: "Add process" }));
		await user.click(screen.getByRole("button", { name: "Add process" }));
		expect(view.latest().processes.map((p) => p.name)).toEqual([
			"worker",
			"worker-3",
		]);
	});

	test("Edit in Commands scrolls to and focuses the service's start command", async () => {
		stubApi({});
		const user = userEvent.setup();
		await renderSection(SERVICE);
		await user.click(screen.getByRole("button", { name: "Edit in Commands" }));
		expect(screen.getByLabelText("Start command of the service")).toHaveFocus();
	});

	test("shows the row errors the draft has, such as a port two processes share", async () => {
		stubApi({});
		await renderSection(SERVICE, {}, (draft) => ({
			...draft,
			processes: draft.processes.map((p) =>
				p.name === "clock" ? { ...p, port: "4000" } : p,
			),
		}));
		expect(
			screen.getAllByText(
				"Port 4000 is also used by worker. Give each process its own port.",
			).length,
		).toBeGreaterThan(0);
	});

	test("a default process name that a row also uses is an error on the row", async () => {
		stubApi({});
		await renderSection(SERVICE, {}, (draft) => ({
			...draft,
			processName: "worker",
		}));
		expect(
			screen.getByText(
				"worker is the name of the default process. Pick another name.",
			),
		).toBeInTheDocument();
	});

	test("shows the problem with the default process name it is given", async () => {
		stubApi({});
		await renderSection(SERVICE, { processName: "Name is required." });
		expect(screen.getByText("Name is required.")).toBeInTheDocument();
	});
});

describe("ProcessesSection limit", () => {
	test("at the limit Add process is disabled and says why", async () => {
		stubApi({});
		const rows = Array.from({ length: MAX_NAMED_PROCESSES }, (_, i) =>
			config(`p${i}`),
		);
		await renderSection(serviceWith(rows));
		const add = screen.getByRole("button", { name: "Add process" });
		expect(add).toBeDisabled();
		const reason =
			"A service has at most 10 processes, the default one included. Remove a process to add another.";
		expect(add).toHaveAttribute("title", reason);
		expect(screen.getByText(reason)).toBeInTheDocument();
	});

	test("one below the limit still adds", async () => {
		stubApi({});
		const rows = Array.from({ length: MAX_NAMED_PROCESSES - 1 }, (_, i) =>
			config(`p${i}`),
		);
		const view = await renderSection(serviceWith(rows));
		const add = screen.getByRole("button", { name: "Add process" });
		expect(add).toBeEnabled();
		await userEvent.setup().click(add);
		expect(view.latest().processes).toHaveLength(MAX_NAMED_PROCESSES);
		expect(screen.getByRole("button", { name: "Add process" })).toBeDisabled();
	});
});

describe("ProcessesSection remove", () => {
	const SERVICE = serviceWith([config("worker"), config("clock")], {
		domains: [
			domain("jobs.example.com", "worker"),
			domain("ui.example.com", "worker"),
			domain("shop.example.com", ""),
		],
	});

	test("asks first, and removing drops only that process from the draft", async () => {
		stubApi({});
		const user = userEvent.setup();
		const view = await renderSection(SERVICE);
		await user.click(screen.getByRole("button", { name: "Remove clock" }));
		const dialog = await screen.findByRole("alertdialog");
		expect(within(dialog).getByText("Remove clock?")).toBeInTheDocument();
		expect(dialog).toHaveTextContent(
			"It stops on the next deploy, and its scale settings are cleared. Nothing changes until you save.",
		);
		expect(view.latest().processes).toHaveLength(2);
		await user.click(
			within(dialog).getByRole("button", { name: "Remove process" }),
		);
		await waitFor(() =>
			expect(view.latest().processes.map((p) => p.name)).toEqual(["worker"]),
		);
	});

	test("cancelling keeps the process", async () => {
		stubApi({});
		const user = userEvent.setup();
		const view = await renderSection(SERVICE);
		await user.click(screen.getByRole("button", { name: "Remove clock" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
		);
		expect(view.latest().processes.map((p) => p.name)).toEqual([
			"worker",
			"clock",
		]);
	});

	test("names the domains that serve the process, since saving is refused while they do", async () => {
		stubApi({});
		const user = userEvent.setup();
		await renderSection(SERVICE);
		await user.click(screen.getByRole("button", { name: "Remove worker" }));
		const dialog = await screen.findByRole("alertdialog");
		expect(dialog).toHaveTextContent(
			"jobs.example.com, ui.example.com serve this process, and saving is refused until you remove those domains or add them back to another process.",
		);
		expect(dialog).not.toHaveTextContent("shop.example.com");
	});

	test("a single serving domain reads in the singular", async () => {
		stubApi({});
		const user = userEvent.setup();
		await renderSection(
			serviceWith([config("worker")], {
				domains: [domain("jobs.example.com", "worker")],
			}),
		);
		await user.click(screen.getByRole("button", { name: "Remove worker" }));
		expect(await screen.findByRole("alertdialog")).toHaveTextContent(
			"jobs.example.com serves this process, and saving is refused until you remove that domain",
		);
	});

	test("a row added in this session has no serving domains to warn about", async () => {
		stubApi({});
		const user = userEvent.setup();
		const view = await renderSection(
			serviceWith([], { domains: [domain("jobs.example.com", "worker")] }),
		);
		await user.click(screen.getByRole("button", { name: "Add process" }));
		await user.click(screen.getByRole("button", { name: "Remove worker" }));
		const dialog = await screen.findByRole("alertdialog");
		expect(dialog).not.toHaveTextContent("jobs.example.com");
		await user.click(
			within(dialog).getByRole("button", { name: "Remove process" }),
		);
		await waitFor(() => expect(view.latest().processes).toEqual([]));
	});

	test("the scale of a saved process is shown from the service, not the draft", async () => {
		stubApi({});
		await renderSection(
			serviceWith([config("worker")], {
				processes: [
					makeProcess({ name: "web", default: true, port: 3000 }),
					makeProcess({
						name: "worker",
						scaling: makeScaling({ replicas: 3, ready: 3 }),
					}),
				],
			}),
		);
		expect(screen.getByText("3/3 replicas")).toBeInTheDocument();
	});
});
