import { afterEach, describe, expect, spyOn, test } from "bun:test";
import { useSearch } from "@tanstack/react-router";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Service } from "#/api/models";
import { makeObserved } from "#/features/canvas/lib/fixtures";
import {
	closeFiles,
	closeShell,
	useFilesProcess,
	useFilesReplica,
	useFilesTarget,
	useShellProcess,
	useShellReplica,
	useShellTarget,
} from "#/features/terminal/store";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { makeProcess, makeProcessService, makeScaling } from "./fixtures";
import { ProcessesTable } from "./processes-table";

type Replica = NonNullable<
	NonNullable<Service["observed"]>["replicas"]
>[number];

function replica(
	process: string,
	index: number,
	over: Partial<Replica> = {},
): Replica {
	return {
		component: "",
		containerId: `c-${process}-${index}`,
		health: "healthy",
		index,
		process,
		restartCount: 0,
		startedAt: null,
		state: "running",
		...over,
	};
}

/** Prints the address' search and the terminal panels' targets, so a test reads what a click did. */
function Probe() {
	const search = useSearch({ strict: false }) as Record<string, unknown>;
	const shell = `${useShellTarget()}|${useShellProcess()}|${useShellReplica()}`;
	const files = `${useFilesTarget()}|${useFilesProcess()}|${useFilesReplica()}`;
	return (
		<>
			<output aria-label="search">{JSON.stringify(search)}</output>
			<output aria-label="shell">{shell}</output>
			<output aria-label="files">{files}</output>
		</>
	);
}

const SERVICE = makeProcessService({
	processes: [
		makeProcess({
			name: "web",
			default: true,
			port: 3000,
			command: ["bin/rails", "server"],
			scaling: makeScaling({ replicas: 2, ready: 1 }),
			observed: {
				name: "web",
				state: "running",
				health: "degraded",
				replicasDesired: 2,
				replicasReady: 1,
			},
		}),
		makeProcess({
			name: "worker",
			pending: true,
			observed: null,
			scaling: makeScaling({ replicas: 1, ready: 0 }),
		}),
		makeProcess({
			name: "clock",
			singleton: true,
			scaling: makeScaling({
				eligible: false,
				ineligibleReason: "A singleton never runs two instances.",
			}),
		}),
		makeProcess({ name: "old", removed: true }),
	],
	observed: makeObserved({
		replicas: [
			replica("web", 0),
			replica("", 1, { state: "exited", health: "" }),
			replica("worker", 0),
			replica("clock", 0),
		],
	}),
});

const searchOf = () =>
	JSON.parse(screen.getByLabelText("search").textContent ?? "{}") as Record<
		string,
		unknown
	>;

const rowOf = (name: string) =>
	screen
		.getAllByRole("row")
		.find(
			(candidate) => candidate.getAttribute("data-process") === name,
		) as HTMLElement;

async function renderTable(service: Service, path = "/") {
	return renderWithProviders(
		<>
			<ProcessesTable service={service} />
			<Probe />
		</>,
		{ path },
	);
}

afterEach(() => {
	act(() => {
		closeShell();
		closeFiles();
	});
});

describe("ProcessesTable rows", () => {
	test("shows nothing for a service without processes", async () => {
		stubApi({});
		await renderTable(makeProcessService({ processes: [] }));
		expect(
			screen.queryByRole("table", { name: "Processes" }),
		).not.toBeInTheDocument();
	});

	test("one row per process in rollout order, with state, port, replicas and command", async () => {
		stubApi({});
		await renderTable(SERVICE);
		const rows = screen
			.getAllByRole("row")
			.filter((candidate) => candidate.hasAttribute("data-process"));
		expect(rows.map((r) => r.getAttribute("data-process"))).toEqual([
			"web",
			"worker",
			"clock",
			"old",
		]);
		const web = rowOf("web");
		expect(web).toHaveTextContent("Degraded");
		expect(web).toHaveTextContent(":3000");
		expect(web).toHaveTextContent("1/2");
		expect(web).toHaveTextContent("bin/rails server");
		const worker = rowOf("worker");
		expect(worker).toHaveTextContent("Staged");
		expect(worker).toHaveTextContent("staged");
		expect(worker).toHaveTextContent("—");
		const clock = rowOf("clock");
		expect(clock).toHaveTextContent("1/1 · singleton");
		expect(rowOf("old")).toHaveTextContent("Removed");
	});

	test("the headers name the columns", async () => {
		stubApi({});
		await renderTable(SERVICE);
		const headers = screen
			.getAllByRole("columnheader")
			.map((header) => header.textContent);
		expect(headers).toEqual([
			"Process",
			"State",
			"Port",
			"Replicas",
			"Actions",
		]);
	});

	test("a default process without a port carries the quiet default tag", async () => {
		stubApi({});
		await renderTable(
			makeProcessService({
				processes: [makeProcess({ name: "app", default: true, port: 0 })],
			}),
		);
		expect(rowOf("app")).toHaveTextContent("default");
	});
});

describe("ProcessesTable highlight", () => {
	test("?process= highlights its row, lists its replicas and scrolls it into view", async () => {
		stubApi({});
		const scroll = spyOn(
			Element.prototype,
			"scrollIntoView",
		).mockImplementation(() => {});
		try {
			await renderTable(SERVICE, "/?process=web");
			expect(rowOf("web")).toHaveAttribute("aria-selected", "true");
			expect(rowOf("worker")).not.toHaveAttribute("aria-selected");
			// Both the labelled replica and the unlabelled one of the default process.
			const list = screen.getByRole("list", { name: "Replicas of web" });
			const items = within(list).getAllByRole("listitem");
			expect(items.map((item) => item.textContent)).toEqual([
				"web · replica 0Healthy",
				"web · replica 1Exited",
			]);
			expect(
				screen.queryByRole("list", { name: "Replicas of worker" }),
			).not.toBeInTheDocument();
			await waitFor(() =>
				expect(scroll).toHaveBeenCalledWith({ block: "nearest" }),
			);
		} finally {
			scroll.mockRestore();
		}
	});

	test("without ?process= no row is highlighted and no replicas are listed", async () => {
		stubApi({});
		await renderTable(SERVICE);
		expect(
			screen.queryByRole("list", { name: /Replicas of/ }),
		).not.toBeInTheDocument();
		expect(
			screen.getAllByRole("row").filter((r) => r.hasAttribute("aria-selected")),
		).toHaveLength(0);
	});

	test("clicking a process names it in the address, clicking it again clears it", async () => {
		stubApi({});
		const user = userEvent.setup();
		await renderTable(SERVICE);
		await user.click(
			within(rowOf("clock")).getByRole("button", { name: "clock" }),
		);
		await waitFor(() => expect(searchOf()).toEqual({ process: "clock" }));
		expect(rowOf("clock")).toHaveAttribute("aria-selected", "true");
		const pressed = within(rowOf("clock")).getByRole("button", {
			name: "clock",
		});
		expect(pressed).toHaveAttribute("aria-pressed", "true");
		await user.click(pressed);
		await waitFor(() => expect(searchOf()).toEqual({}));
		expect(rowOf("clock")).not.toHaveAttribute("aria-selected");
	});

	test("picking another process moves the highlight", async () => {
		stubApi({});
		const user = userEvent.setup();
		await renderTable(SERVICE, "/?process=web");
		await user.click(
			within(rowOf("worker")).getByRole("button", { name: "worker" }),
		);
		await waitFor(() => expect(searchOf()).toEqual({ process: "worker" }));
		expect(rowOf("worker")).toHaveAttribute("aria-selected", "true");
		expect(rowOf("web")).not.toHaveAttribute("aria-selected");
	});
});

describe("ProcessesTable actions", () => {
	test("Logs of a process opens the Logs tab narrowed to it", async () => {
		stubApi({});
		const user = userEvent.setup();
		await renderTable(SERVICE, "/?tab=overview");
		await user.click(screen.getByRole("button", { name: "Logs of web" }));
		await waitFor(() =>
			expect(searchOf()).toMatchObject({ tab: "logs", process: "web" }),
		);
	});

	test("a staged process has no logs yet and a removed one points to All processes", async () => {
		stubApi({});
		await renderTable(SERVICE);
		const staged = screen.getByRole("button", { name: "Logs of worker" });
		expect(staged).toBeDisabled();
		expect(staged).toHaveAttribute("title", "Not running yet");
		const removed = screen.getByRole("button", { name: "Logs of old" });
		expect(removed).toBeDisabled();
		expect(removed).toHaveAttribute(
			"title",
			"Removed: its logs show under All processes",
		);
	});

	test("a replica's shell and file browser open on that replica of the process", async () => {
		stubApi({});
		const user = userEvent.setup();
		await renderTable(SERVICE, "/?process=web");
		await user.click(
			screen.getByRole("button", { name: "Open a shell in web · replica 0" }),
		);
		expect(screen.getByLabelText("shell")).toHaveTextContent("svc-shop|web|0");
		await user.click(
			screen.getByRole("button", { name: "Browse files of web · replica 0" }),
		);
		expect(screen.getByLabelText("files")).toHaveTextContent("svc-shop|web|0");
	});

	test("a replica that is not running cannot be entered", async () => {
		stubApi({});
		await renderTable(SERVICE, "/?process=web");
		const shell = screen.getByRole("button", {
			name: "Open a shell in web · replica 1",
		});
		const files = screen.getByRole("button", {
			name: "Browse files of web · replica 1",
		});
		expect(shell).toBeDisabled();
		expect(shell).toHaveAttribute("title", "Not running");
		expect(files).toBeDisabled();
		expect(files).toHaveAttribute("title", "Not running");
	});
});

describe("ProcessesTable scaling", () => {
	test("the replica count of a process that can scale opens the dialog on it", async () => {
		stubApi({ "GET /services/:id": { body: SERVICE } });
		const user = userEvent.setup();
		await renderTable(SERVICE);
		await user.click(
			screen.getByRole("button", { name: "Scale worker, 0/1 replicas" }),
		);
		expect(
			await screen.findByRole("heading", { name: "Scale shop" }),
		).toBeInTheDocument();
		expect(await screen.findByRole("combobox")).toHaveTextContent("worker");
	});

	test("an autoscaled process names its autoscaling in the button", async () => {
		const service = makeProcessService({
			processes: [
				makeProcess({
					name: "web",
					default: true,
					port: 3000,
					scaling: makeScaling({
						replicas: 2,
						ready: 2,
						autoscaling: {
							enabled: true,
							min: 1,
							max: 4,
							targetCpuPercent: 60,
						},
					}),
				}),
			],
		});
		stubApi({});
		await renderTable(service);
		const button = screen.getByRole("button", {
			name: "Scale web, 2/2 replicas · autoscaling 1–4 @ 60% CPU",
		});
		expect(button).toHaveTextContent("2/2 · auto");
		expect(button).toHaveAttribute(
			"title",
			"Scale web… 2/2 replicas · autoscaling 1–4 @ 60% CPU",
		);
	});

	test("a process that cannot scale shows its count as plain text with the reason", async () => {
		stubApi({});
		await renderTable(SERVICE);
		expect(
			screen.queryByRole("button", { name: /^Scale clock/ }),
		).not.toBeInTheDocument();
		const count = within(rowOf("clock")).getByText("1/1 · singleton");
		expect(count).toHaveAttribute(
			"title",
			"A singleton never runs two instances.",
		);
	});

	test("a removed process cannot be scaled", async () => {
		stubApi({});
		await renderTable(SERVICE);
		expect(
			screen.queryByRole("button", { name: /^Scale old/ }),
		).not.toBeInTheDocument();
	});
});
