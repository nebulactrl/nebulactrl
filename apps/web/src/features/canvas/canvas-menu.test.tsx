import { describe, expect, mock, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { CanvasGroup, Service } from "#/api/models";
import {
	ContextMenu,
	ContextMenuTrigger,
} from "#/components/motion/context-menu";
import { onOpenStagedReview } from "#/features/deploy-button/open-review";
import { useTemplatesState } from "#/features/templates/store";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { CanvasMenu, type CanvasMenuProps } from "./canvas-menu";
import { makeDeployment, makeObserved, makeService } from "./lib/fixtures";
import type { MenuAction } from "./lib/menu";

const INSTANCE = {
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
};

function Deleting() {
	return (
		<output data-testid="deleting">
			{useTemplatesState().deleting?.id ?? ""}
		</output>
	);
}

async function open(
	target: CanvasMenuProps["target"],
	props: Partial<Omit<CanvasMenuProps, "target">> = {},
	routes: Parameters<typeof stubApi>[0] = {},
) {
	const onAction = mock((_action: MenuAction) => {});
	const api = stubApi({
		"POST /services/:serviceId/deployments": {
			status: 202,
			body: make("DeploymentView", {
				id: "dep-new",
				serviceId: "svc-1",
				trigger: "manual",
				actorType: "user",
				actorLabel: "me",
				status: "queued",
				strategy: "rolling",
			}),
		},
		"POST /services/:serviceId/restart": {
			status: 202,
			body: make("ActionResult"),
		},
		"GET /environments/:environmentId/template-instances": { body: [] },
		...routes,
	});
	const view = await renderWithProviders(
		<>
			<ContextMenu open>
				<ContextMenuTrigger>
					<div>surface</div>
				</ContextMenuTrigger>
				<CanvasMenu
					target={target}
					isProduction={false}
					environmentId="env-1"
					empty={false}
					onAction={onAction}
					{...props}
				/>
			</ContextMenu>
			<Deleting />
		</>,
	);
	const menu = await screen.findByRole("menu");
	return { ...view, api, onAction, menu };
}

const labels = (menu: HTMLElement) =>
	within(menu)
		.getAllByRole("menuitem")
		.map((item) => item.textContent);

const running = (over: Partial<Service> = {}) =>
	makeService({
		id: "svc-1",
		name: "Storefront",
		currentDeploymentId: "dep-1",
		latestDeployment: makeDeployment("succeeded"),
		observed: makeObserved(),
		...over,
	});

describe("CanvasMenu: the empty pane", () => {
	test("offers Create here at the cursor, then the canvas actions", async () => {
		const { menu } = await open({ kind: "pane", at: { x: 10, y: 20 } });
		expect(menu).toHaveAccessibleName("Canvas");
		const rows = labels(menu);
		// Create sources first, then the canvas's own actions in this order.
		expect(rows.slice(0, 4)).toEqual([
			"Git repository",
			"Docker image",
			"Cron job",
			"Database",
		]);
		expect(rows.slice(-4)).toEqual([
			"Arrange",
			"Fit view1",
			"Search⌘F",
			"Keyboard shortcuts?",
		]);
		expect(within(menu).getByText("Create here")).toBeInTheDocument();
	});

	test("choosing a source asks the canvas to create it at the click point", async () => {
		const user = userEvent.setup();
		const { menu, onAction } = await open({
			kind: "pane",
			at: { x: 10, y: 20 },
		});
		await user.click(
			within(menu).getByRole("menuitem", { name: /Docker image/ }),
		);
		expect(onAction).toHaveBeenCalledWith({
			type: "create",
			source: "image",
			at: { x: 10, y: 20 },
		});
	});

	test("an empty canvas turns Arrange, Fit and Search off but keeps Create and shortcuts", async () => {
		const user = userEvent.setup();
		const { menu, onAction } = await open(
			{ kind: "pane", at: null },
			{ empty: true },
		);
		for (const name of [/^Arrange/, /^Fit view/, /^Search/]) {
			expect(within(menu).getByRole("menuitem", { name })).toBeDisabled();
		}
		expect(
			within(menu).getByRole("menuitem", { name: /Docker image/ }),
		).toBeEnabled();
		await user.click(
			within(menu).getByRole("menuitem", { name: /Keyboard shortcuts/ }),
		);
		expect(onAction).toHaveBeenCalledWith({ type: "shortcuts" });
	});

	test("on a populated canvas Arrange, Fit and Search call back", async () => {
		const user = userEvent.setup();
		const { menu, onAction } = await open({ kind: "pane", at: null });
		await user.click(within(menu).getByRole("menuitem", { name: /^Arrange/ }));
		await user.click(within(menu).getByRole("menuitem", { name: /^Fit view/ }));
		await user.click(within(menu).getByRole("menuitem", { name: /^Search/ }));
		expect(onAction.mock.calls.map((c) => c[0].type)).toEqual([
			"arrange",
			"fit",
			"search",
		]);
	});
});

describe("CanvasMenu: a card", () => {
	test("is named for the service and leads with Open, Redeploy and Scale", async () => {
		const { menu } = await open({
			kind: "service",
			service: running(),
			links: [],
		});
		expect(menu).toHaveAccessibleName("Actions for Storefront");
		expect(within(menu).getByText("Storefront")).toBeInTheDocument();
		expect(labels(menu).slice(0, 3)).toEqual(["Open↵", "Redeploy", "Scale…"]);
		expect(labels(menu).at(-1)).toBe("Delete…");
	});

	test("Open reports the service to open", async () => {
		const user = userEvent.setup();
		const { menu, onAction } = await open({
			kind: "service",
			service: running(),
			links: [],
		});
		await user.click(within(menu).getByRole("menuitem", { name: "Open" }));
		expect(onAction).toHaveBeenCalledWith({
			type: "service-open",
			serviceId: "svc-1",
		});
	});

	test("Scale… reports the service to scale", async () => {
		const user = userEvent.setup();
		const { menu, onAction } = await open({
			kind: "service",
			service: running(),
			links: [],
		});
		await user.click(within(menu).getByRole("menuitem", { name: "Scale…" }));
		expect(onAction).toHaveBeenCalledWith({
			type: "service-scale",
			serviceId: "svc-1",
		});
	});

	test("Scale… is off for a service that cannot scale, with the reason under the first group", async () => {
		const base = running();
		const { menu } = await open({
			kind: "service",
			service: {
				...base,
				scaling: {
					...base.scaling,
					eligible: false,
					ineligibleReason: "Scale this database from its tab",
				},
			},
			links: [],
		});
		expect(
			within(menu).getByRole("menuitem", { name: "Scale…" }),
		).toBeDisabled();
		expect(
			within(menu).getByText("Scale this database from its tab"),
		).toBeInTheDocument();
	});

	test("Redeploy outside production posts a deployment instead of reporting to the canvas", async () => {
		const user = userEvent.setup();
		const { menu, onAction, api } = await open({
			kind: "service",
			service: running(),
			links: [],
		});
		await user.click(within(menu).getByRole("menuitem", { name: "Redeploy" }));
		await waitFor(() =>
			expect(api.calls.map((c) => `${c.method} ${c.path}`)).toContain(
				"POST /services/svc-1/deployments",
			),
		);
		expect(onAction).not.toHaveBeenCalled();
	});

	test("in production Redeploy opens the scoped review sheet and posts nothing", async () => {
		const user = userEvent.setup();
		const opened = mock((_o: unknown) => {});
		const off = onOpenStagedReview(opened);
		try {
			const { menu, api } = await open(
				{ kind: "service", service: running(), links: [] },
				{ isProduction: true },
			);
			await user.click(
				within(menu).getByRole("menuitem", { name: "Redeploy" }),
			);
			expect(opened).toHaveBeenCalledWith({ serviceId: "svc-1" });
			expect(api.calls).toEqual([]);
		} finally {
			off();
		}
	});

	test("Restart runs the service's own request, not the canvas handler", async () => {
		const user = userEvent.setup();
		const { menu, onAction, api } = await open({
			kind: "service",
			service: running(),
			links: [],
		});
		await user.click(within(menu).getByRole("menuitem", { name: "Restart" }));
		await waitFor(() =>
			expect(api.calls.map((c) => `${c.method} ${c.path}`)).toContain(
				"POST /services/svc-1/restart",
			),
		);
		expect(onAction).not.toHaveBeenCalled();
	});

	test("Delete… opens the service's Danger zone in settings", async () => {
		const user = userEvent.setup();
		const { menu, onAction } = await open({
			kind: "service",
			service: running(),
			links: [],
		});
		await user.click(within(menu).getByRole("menuitem", { name: "Delete…" }));
		expect(onAction).toHaveBeenCalledWith({
			type: "open-service",
			serviceId: "svc-1",
			section: "settings",
			settingsSection: "danger",
		});
	});

	test("lists the card's cables so the keyboard can review them", async () => {
		const user = userEvent.setup();
		const { menu, onAction } = await open({
			kind: "service",
			service: running(),
			links: [{ edgeId: "edge-1", otherName: "Postgres" }],
		});
		expect(within(menu).getByText("Links")).toBeInTheDocument();
		await user.click(
			within(menu).getByRole("menuitem", { name: "Review link to Postgres" }),
		);
		expect(onAction).toHaveBeenCalledWith({
			type: "review-references",
			edgeId: "edge-1",
		});
	});

	test("a service that never ran cannot be restarted", async () => {
		const { menu } = await open({
			kind: "service",
			service: makeService({ id: "svc-1", name: "Storefront" }),
			links: [],
		});
		expect(
			within(menu).getByRole("menuitem", { name: "Restart" }),
		).toBeDisabled();
		expect(
			within(menu).getByRole("menuitem", { name: "Deploy" }),
		).toBeEnabled();
	});
});

describe("CanvasMenu: the Internet node", () => {
	const model = {
		domains: [
			{
				hostname: "shop.example.com",
				serviceId: "svc-1",
				serviceName: "Storefront",
			},
			{ hostname: "api.example.com", serviceId: "svc-2", serviceName: "Api" },
		],
		candidates: [{ id: "svc-1", name: "Storefront" }],
	};

	test("lists the public domains to open and the services that can take another", async () => {
		const user = userEvent.setup();
		const { menu, onAction } = await open({ kind: "internet", model });
		expect(menu).toHaveAccessibleName(
			"Actions for the Internet, 2 public domains",
		);
		expect(labels(menu)).toEqual([
			"shop.example.com",
			"api.example.com",
			"Storefront",
		]);
		await user.click(
			within(menu).getByRole("menuitem", { name: "api.example.com" }),
		);
		expect(onAction).toHaveBeenCalledWith({
			type: "open-domain",
			hostname: "api.example.com",
		});
		await user.click(
			within(menu).getByRole("menuitem", { name: "Storefront" }),
		);
		expect(onAction).toHaveBeenLastCalledWith({
			type: "add-domain",
			serviceId: "svc-1",
		});
	});

	test("with nothing that can take a domain, it says so", async () => {
		const { menu } = await open({
			kind: "internet",
			model: { domains: [], candidates: [] },
		});
		expect(
			within(menu).getByText("No app or route here can take a domain yet."),
		).toBeInTheDocument();
		expect(within(menu).queryAllByRole("menuitem")).toHaveLength(0);
	});
});

describe("CanvasMenu: a cable", () => {
	const ends = {
		from: { nodeId: "svc-1", name: "Storefront", isService: true },
		to: { nodeId: "svc-2", name: "Postgres", isService: true },
	};

	test("a referenced cable can be reviewed and jumped from, but not removed", async () => {
		const user = userEvent.setup();
		const { menu, onAction } = await open({
			kind: "edge",
			model: {
				edgeId: "edge-1",
				kind: "reference",
				...ends,
				link: { id: "link-1", source: "reference" },
				references: ["DATABASE_URL"],
				hostnames: [],
			},
		});
		expect(menu).toHaveAccessibleName(
			"Actions for the cable from Storefront to Postgres",
		);
		expect(
			within(menu).getByRole("menuitem", { name: "Remove link" }),
		).toBeDisabled();
		expect(
			within(menu).getByText(/Drawn from variable references/),
		).toBeInTheDocument();
		await user.click(
			within(menu).getByRole("menuitem", { name: "View references (1)" }),
		);
		expect(onAction).toHaveBeenCalledWith({
			type: "review-references",
			edgeId: "edge-1",
		});
		await user.click(
			within(menu).getByRole("menuitem", { name: "Variables of Storefront" }),
		);
		expect(onAction).toHaveBeenLastCalledWith({
			type: "open-service",
			serviceId: "svc-1",
			section: "variables",
		});
	});

	test("a hand-drawn cable can be removed", async () => {
		const user = userEvent.setup();
		const { menu, onAction } = await open({
			kind: "edge",
			model: {
				edgeId: "edge-2",
				kind: "manual",
				...ends,
				link: { id: "link-2", source: "manual" },
				references: [],
				hostnames: [],
			},
		});
		const remove = within(menu).getByRole("menuitem", { name: "Remove link" });
		expect(remove).toBeEnabled();
		await user.click(remove);
		expect(onAction).toHaveBeenCalledWith({
			type: "remove-link",
			edgeId: "edge-2",
		});
	});

	test("the Internet cable lists the hostnames it serves", async () => {
		const user = userEvent.setup();
		const { menu, onAction } = await open({
			kind: "edge",
			model: {
				edgeId: "edge-3",
				kind: "internet",
				from: { nodeId: "internet:public", name: "Internet", isService: false },
				to: ends.to,
				link: null,
				references: [],
				hostnames: ["db.example.com"],
			},
		});
		await user.click(
			within(menu).getByRole("menuitem", { name: "db.example.com" }),
		);
		expect(onAction).toHaveBeenCalledWith({
			type: "open-domain",
			hostname: "db.example.com",
		});
	});
});

describe("CanvasMenu: a group", () => {
	const group: CanvasGroup = {
		id: "g1",
		name: "Backend",
		x: 0,
		y: 0,
		w: 100,
		h: 100,
		collapsed: false,
		serviceIds: ["a", "b"],
	};

	test("Rename, Collapse, Zoom and Ungroup report the group", async () => {
		const user = userEvent.setup();
		const { menu, onAction } = await open({
			kind: "group",
			group,
			memberCount: 2,
		});
		expect(menu).toHaveAccessibleName("Actions for group Backend");
		await user.click(within(menu).getByRole("menuitem", { name: "Rename" }));
		await user.click(within(menu).getByRole("menuitem", { name: "Collapse" }));
		await user.click(
			within(menu).getByRole("menuitem", { name: "Zoom to group" }),
		);
		await user.click(within(menu).getByRole("menuitem", { name: "Ungroup" }));
		expect(onAction.mock.calls.map((c) => c[0])).toEqual([
			{ type: "rename-group", groupId: "g1" },
			{ type: "toggle-group", groupId: "g1" },
			{ type: "zoom-group", groupId: "g1" },
			{ type: "ungroup", groupId: "g1" },
		]);
	});

	test("a collapsed group offers Expand and an empty one cannot be zoomed to", async () => {
		const { menu } = await open({
			kind: "group",
			group: { ...group, collapsed: true },
			memberCount: 0,
		});
		expect(
			within(menu).getByRole("menuitem", { name: "Expand" }),
		).toBeInTheDocument();
		expect(
			within(menu).getByRole("menuitem", { name: "Zoom to group" }),
		).toBeDisabled();
	});

	test("an ordinary group has no Delete stack row", async () => {
		const { menu } = await open({ kind: "group", group, memberCount: 2 });
		expect(
			within(menu).queryByRole("menuitem", { name: /Delete stack/ }),
		).not.toBeInTheDocument();
	});

	test("an installed template stack offers Delete stack…, which asks the templates host to confirm", async () => {
		const user = userEvent.setup();
		const { menu, onAction } = await open(
			{ kind: "group", group, memberCount: 2 },
			{},
			{
				"GET /environments/:environmentId/template-instances": {
					body: [INSTANCE],
				},
			},
		);
		const del = await within(menu).findByRole("menuitem", {
			name: "Delete stack…",
		});
		await user.click(del);
		expect(screen.getByTestId("deleting")).toHaveTextContent("inst-1");
		expect(onAction).not.toHaveBeenCalled();
	});
});

describe("CanvasMenu: a selection", () => {
	const git = (id: string, over: Partial<Service> = {}) =>
		makeService({
			id,
			name: id,
			sourceType: "git",
			currentDeploymentId: "dep",
			...over,
		});

	test("bulk deploy and restart name how many services they reach and report their ids", async () => {
		const user = userEvent.setup();
		const services = [
			git("a"),
			git("b", { latestDeployment: makeDeployment("building") }),
			git("c", { currentDeploymentId: null }),
		];
		const { menu, onAction } = await open({ kind: "selection", services });
		expect(menu).toHaveAccessibleName("Actions for 3 selected services");
		await user.click(
			within(menu).getByRole("menuitem", { name: "Deploy 2 services" }),
		);
		expect(onAction).toHaveBeenCalledWith({
			type: "deploy-selected",
			serviceIds: ["a", "c"],
		});
		await user.click(
			within(menu).getByRole("menuitem", { name: "Restart 1 service" }),
		);
		expect(onAction).toHaveBeenLastCalledWith({
			type: "restart-selected",
			serviceIds: ["a"],
		});
		expect(
			within(menu).getByText(/1 service is already deploying and skipped/),
		).toBeInTheDocument();
	});

	test("in production the deploy row is the review sheet", async () => {
		const { menu } = await open(
			{ kind: "selection", services: [git("a"), git("b")] },
			{ isProduction: true },
		);
		expect(
			within(menu).getByRole("menuitem", { name: "Review and deploy…" }),
		).toBeEnabled();
		expect(
			within(menu).getByText(
				/Production deploys are confirmed in the review sheet/,
			),
		).toBeInTheDocument();
	});

	test("Group, Arrange and Combine need two services; Clear selection always works", async () => {
		const user = userEvent.setup();
		const { menu, onAction } = await open({
			kind: "selection",
			services: [git("a")],
		});
		expect(
			within(menu).getByRole("menuitem", { name: "Group 1 services" }),
		).toBeDisabled();
		expect(
			within(menu).getByRole("menuitem", { name: "Arrange 1 services" }),
		).toBeDisabled();
		expect(
			within(menu).queryByRole("menuitem", { name: /Combine/ }),
		).not.toBeInTheDocument();
		await user.click(
			within(menu).getByRole("menuitem", { name: /Clear selection/ }),
		);
		expect(onAction).toHaveBeenCalledWith({ type: "clear-selection" });
	});

	test("two git apps can be grouped, arranged and combined", async () => {
		const user = userEvent.setup();
		const { menu, onAction } = await open({
			kind: "selection",
			services: [git("a"), git("b")],
		});
		await user.click(
			within(menu).getByRole("menuitem", { name: "Group 2 services" }),
		);
		await user.click(
			within(menu).getByRole("menuitem", { name: "Arrange 2 services" }),
		);
		const combine = within(menu).getByRole("menuitem", {
			name: "Combine into one service…",
		});
		expect(combine).toBeEnabled();
		await user.click(combine);
		expect(onAction.mock.calls.map((c) => c[0])).toEqual([
			{ type: "group-selected", serviceIds: ["a", "b"] },
			{ type: "arrange-selected", serviceIds: ["a", "b"] },
			{ type: "combine-selected", serviceIds: ["a", "b"] },
		]);
	});

	test("image services cannot be combined, and the menu says why", async () => {
		const { menu } = await open({
			kind: "selection",
			services: [
				makeService({ id: "a", name: "a" }),
				makeService({ id: "b", name: "b" }),
			],
		});
		expect(
			within(menu).getByRole("menuitem", { name: "Combine into one service…" }),
		).toBeDisabled();
		expect(
			within(menu).getByText(/deploys an image, not a Git repository/),
		).toBeInTheDocument();
	});
});
