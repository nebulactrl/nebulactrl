import { describe, expect, mock, test } from "bun:test";
import { fireEvent, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ReactFlow, ReactFlowProvider } from "@xyflow/react";
import type { CanvasGroup, Service } from "#/api/models";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import {
	type CanvasUi,
	CanvasUiContext,
	DIMMED_OPACITY,
} from "./canvas-context";
import { GroupNode } from "./group-node";
import { makeDeployment, makeService } from "./lib/fixtures";

const nodeTypes = { cluster: GroupNode };

function makeGroup(overrides: Partial<CanvasGroup> = {}): CanvasGroup {
	return {
		id: "g1",
		name: "Backend",
		x: 0,
		y: 0,
		w: 400,
		h: 300,
		collapsed: false,
		serviceIds: [],
		...overrides,
	};
}

function ui(overrides: Partial<CanvasUi> = {}): CanvasUi {
	return {
		matchIds: null,
		tick: 0,
		toggleGroup: mock(() => {}),
		renameGroup: mock(() => {}),
		ungroup: mock(() => {}),
		renamingGroupId: null,
		finishRenameRequest: mock(() => {}),
		...overrides,
	};
}

async function show(
	group: CanvasGroup,
	members: Service[],
	context: CanvasUi = ui(),
	opts: { path?: string; routePath?: string; selected?: boolean } = {},
) {
	const view = await renderWithProviders(
		<div style={{ width: 800, height: 600 }}>
			<CanvasUiContext.Provider value={context}>
				<ReactFlowProvider>
					<ReactFlow
						nodes={[
							{
								id: group.id,
								type: "cluster",
								position: { x: 0, y: 0 },
								// Sized up front: React Flow keeps a node hidden until it is measured.
								width: 400,
								height: 300,
								selected: opts.selected ?? false,
								data: { group, members },
							},
						]}
						edges={[]}
						nodeTypes={nodeTypes}
					/>
				</ReactFlowProvider>
			</CanvasUiContext.Provider>
		</div>,
		{ path: opts.path ?? "/", routePath: opts.routePath ?? "$" },
	);
	return { ...view, context };
}

const live = makeService({
	id: "live",
	currentDeploymentId: "dep-1",
	latestDeployment: makeDeployment("succeeded"),
});
const failing = makeService({
	id: "failing",
	latestDeployment: makeDeployment("failed"),
});
const building = makeService({
	id: "building",
	latestDeployment: makeDeployment("building"),
});

describe("GroupNode (expanded frame)", () => {
	test("shows the group's name, its member count and its controls", async () => {
		await show(makeGroup(), [live, failing]);
		expect(screen.getByRole("button", { name: "Backend" })).toBeInTheDocument();
		expect(screen.getByText("2")).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Collapse Backend" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Ungroup Backend" }),
		).toBeInTheDocument();
	});

	test("Collapse asks the canvas to toggle this group", async () => {
		const user = userEvent.setup();
		const { context } = await show(makeGroup(), [live]);
		await user.click(screen.getByRole("button", { name: "Collapse Backend" }));
		expect(context.toggleGroup).toHaveBeenCalledWith("g1");
	});

	test("Ungroup asks the canvas to ungroup this group", async () => {
		const user = userEvent.setup();
		const { context } = await show(makeGroup(), [live]);
		await user.click(screen.getByRole("button", { name: "Ungroup Backend" }));
		expect(context.ungroup).toHaveBeenCalledWith("g1");
		expect(context.toggleGroup).not.toHaveBeenCalled();
	});

	test("double-click edits the name; Enter saves the trimmed new name", async () => {
		const user = userEvent.setup();
		const { context } = await show(makeGroup(), [live]);
		await user.dblClick(screen.getByRole("button", { name: "Backend" }));
		const input = screen.getByRole("textbox", { name: "Group name" });
		expect(input).toHaveFocus();
		expect(input).toHaveValue("Backend");
		await user.clear(input);
		await user.type(input, "  API tier  {Enter}");
		expect(context.renameGroup).toHaveBeenCalledWith("g1", "API tier");
		expect(
			screen.queryByRole("textbox", { name: "Group name" }),
		).not.toBeInTheDocument();
	});

	test("a single click does not start editing", async () => {
		const user = userEvent.setup();
		await show(makeGroup(), [live]);
		await user.click(screen.getByRole("button", { name: "Backend" }));
		expect(
			screen.queryByRole("textbox", { name: "Group name" }),
		).not.toBeInTheDocument();
	});

	test("Escape abandons the edit without renaming", async () => {
		const user = userEvent.setup();
		const { context } = await show(makeGroup(), [live]);
		await user.dblClick(screen.getByRole("button", { name: "Backend" }));
		await user.type(
			screen.getByRole("textbox", { name: "Group name" }),
			"zzz{Escape}",
		);
		expect(context.renameGroup).not.toHaveBeenCalled();
		expect(screen.getByRole("button", { name: "Backend" })).toBeInTheDocument();
	});

	test("an unchanged name is not saved", async () => {
		const user = userEvent.setup();
		const { context } = await show(makeGroup(), [live]);
		await user.dblClick(screen.getByRole("button", { name: "Backend" }));
		await user.type(
			screen.getByRole("textbox", { name: "Group name" }),
			"{Enter}",
		);
		expect(context.renameGroup).not.toHaveBeenCalled();
	});

	test("clearing the name and leaving keeps the old one", async () => {
		const user = userEvent.setup();
		const { context } = await show(makeGroup(), [live]);
		await user.dblClick(screen.getByRole("button", { name: "Backend" }));
		await user.clear(screen.getByRole("textbox", { name: "Group name" }));
		fireEvent.blur(screen.getByRole("textbox", { name: "Group name" }));
		expect(context.renameGroup).not.toHaveBeenCalled();
		expect(screen.getByRole("button", { name: "Backend" })).toBeInTheDocument();
	});

	test("blurring the field saves the edit", async () => {
		const user = userEvent.setup();
		const { context } = await show(makeGroup(), [live]);
		await user.dblClick(screen.getByRole("button", { name: "Backend" }));
		const input = screen.getByRole("textbox", { name: "Group name" });
		await user.clear(input);
		await user.type(input, "Data");
		fireEvent.blur(input);
		expect(context.renameGroup).toHaveBeenCalledWith("g1", "Data");
	});

	test("the name field caps at 60 characters", async () => {
		const user = userEvent.setup();
		await show(makeGroup(), [live]);
		await user.dblClick(screen.getByRole("button", { name: "Backend" }));
		expect(screen.getByRole("textbox", { name: "Group name" })).toHaveAttribute(
			"maxlength",
			"60",
		);
	});

	test("a rename requested by the context menu opens the editor and consumes the request", async () => {
		const context = ui({ renamingGroupId: "g1" });
		await show(makeGroup(), [live], context);
		expect(
			await screen.findByRole("textbox", { name: "Group name" }),
		).toHaveFocus();
		expect(context.finishRenameRequest).toHaveBeenCalled();
	});

	test("a rename requested for another group is ignored", async () => {
		const context = ui({ renamingGroupId: "other" });
		await show(makeGroup(), [live], context);
		expect(
			screen.queryByRole("textbox", { name: "Group name" }),
		).not.toBeInTheDocument();
		expect(context.finishRenameRequest).not.toHaveBeenCalled();
	});

	test("is full strength when nothing is filtered or a member matches, dim when none do", async () => {
		const lit = await show(
			makeGroup(),
			[live, failing],
			ui({ matchIds: new Set(["failing"]) }),
		);
		expect(
			lit.container.querySelector<HTMLElement>("[data-group-id]")?.style
				.opacity,
		).toBe("1");
		lit.unmount();
		const dim = await show(
			makeGroup(),
			[live, failing],
			ui({ matchIds: new Set(["elsewhere"]) }),
		);
		expect(
			dim.container.querySelector<HTMLElement>("[data-group-id]")?.style
				.opacity,
		).toBe(String(DIMMED_OPACITY));
	});

	test("an installed template stack's group offers deleting the stack", async () => {
		stubApi({
			"GET /projects": {
				body: make("PageProject", {
					items: [make("Project", { id: "p1", name: "Shop" })],
				}),
			},
			"GET /projects/:projectId/environments": {
				body: {
					items: [
						{
							id: "env-1",
							projectId: "p1",
							slug: "production",
							name: "Production",
							isProduction: false,
							kind: "standard",
							parentEnvironmentId: null,
							prNumber: null,
							prTitle: "",
							createdAt: "2026-01-01T00:00:00Z",
						},
					],
				},
			},
			"GET /environments/:environmentId/template-instances": {
				body: [
					{
						id: "inst-1",
						groupId: "g1",
						name: "Plausible",
						templateId: "plausible",
						currentRevision: 1,
						templateRevision: 1,
						currentVersion: "1",
						templateVersion: "1",
						updateAvailable: false,
						environmentId: "env-1",
						createdAt: "2026-01-01T00:00:00Z",
						inputs: {},
						services: [],
					},
				],
			},
		});
		await show(makeGroup(), [live], ui(), {
			path: "/p/p1/production",
			routePath: "/p/$projectId/$envSlug",
		});
		expect(
			await screen.findByRole("button", { name: "Delete stack Backend" }),
		).toBeInTheDocument();
	});

	test("an ordinary group has no stack actions", async () => {
		await show(makeGroup(), [live]);
		expect(
			screen.queryByRole("button", { name: /stack/i }),
		).not.toBeInTheDocument();
	});
});

describe("GroupNode (collapsed summary)", () => {
	test("counts its services and rolls up the most urgent status", async () => {
		await show(makeGroup({ collapsed: true }), [live, failing, building]);
		expect(screen.getByText("3 services · Failed")).toBeInTheDocument();
		expect(screen.getByText("Backend")).toBeInTheDocument();
	});

	test("a single service is singular, and its status is the card's own label", async () => {
		await show(makeGroup({ collapsed: true }), [building]);
		expect(screen.getByText("1 service · Building")).toBeInTheDocument();
	});

	test("waiting beats in-flight and failed beats waiting in the rollup", async () => {
		const offlineBuilding = makeService({
			id: "waiting",
			server: {
				id: "server-1",
				name: "box",
				status: "offline",
				lastSeenAt: null,
			},
			latestDeployment: makeDeployment("building"),
		});
		await show(makeGroup({ collapsed: true }), [building, offlineBuilding]);
		expect(
			screen.getByText(/^2 services · Waiting for box/),
		).toBeInTheDocument();
	});

	test("deployed members roll up to their deployed label", async () => {
		await show(makeGroup({ collapsed: true }), [live]);
		expect(screen.getByText(/^1 service · Deployed/)).toBeInTheDocument();
	});

	test("an empty group says '0 services' with no status", async () => {
		await show(makeGroup({ collapsed: true }), []);
		expect(screen.getByText("0 services")).toBeInTheDocument();
	});

	test("Expand toggles the group", async () => {
		const user = userEvent.setup();
		const { context } = await show(makeGroup({ collapsed: true }), [live]);
		await user.click(screen.getByRole("button", { name: "Expand Backend" }));
		expect(context.toggleGroup).toHaveBeenCalledWith("g1");
		expect(
			screen.queryByRole("button", { name: /Collapse|Ungroup/ }),
		).not.toBeInTheDocument();
	});

	test("the name is plain text on a summary card, but a requested rename still edits it", async () => {
		const context = ui({ renamingGroupId: "g1" });
		const user = userEvent.setup();
		await show(makeGroup({ collapsed: true }), [live], context);
		const input = await screen.findByRole("textbox", { name: "Group name" });
		await user.clear(input);
		await user.type(input, "Renamed{Enter}");
		expect(context.renameGroup).toHaveBeenCalledWith("g1", "Renamed");
	});

	test("dims when no member matches the filter", async () => {
		const { container } = await show(
			makeGroup({ collapsed: true }),
			[live],
			ui({ matchIds: new Set() }),
		);
		expect(
			container.querySelector<HTMLElement>("[data-group-id]")?.style.opacity,
		).toBe(String(DIMMED_OPACITY));
	});

	test("a selected summary card is outlined; an unselected one is not", async () => {
		const outlineOf = (container: HTMLElement) =>
			(
				within(
					container.querySelector<HTMLElement>(
						"[data-group-id]",
					) as HTMLElement,
				)
					.getByText("Backend")
					.closest(".surface-raised") as HTMLElement
			).style.outline;
		const selected = await show(makeGroup({ collapsed: true }), [live], ui(), {
			selected: true,
		});
		expect(outlineOf(selected.container)).not.toBe("");
		selected.unmount();
		const plain = await show(makeGroup({ collapsed: true }), [live]);
		expect(outlineOf(plain.container)).toBe("");
	});
});
