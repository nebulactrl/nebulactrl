import type { Point, Service, ServiceLink } from "#/api/models";
import { canCombine } from "#/features/combine/model";
import { type MenuEntryId, ROOT_ENTRIES } from "#/features/create/lib";
import { serviceCanScale } from "#/features/scaling/model";
import type { ServiceActionId } from "../use-service-actions";
import { groupIdOfNode } from "./flow";
import { INTERNET_ID } from "./internet";
import { isInFlight } from "./status";

/**
 * The canvas's right-click menus as data. Every target (a card, the empty
 * pane, the Internet node, a cable, a group, a selection of cards) has a
 * builder here that turns what is under the pointer into rows; the component
 * only draws them and runs the `action` of the row that is chosen. Nothing in
 * this file touches the DOM or React, so the rows are tested as plain values.
 */

/** Which glyph a row wears; `canvas-menu.tsx` maps each name to an icon. */
export type MenuIconName =
	| "open"
	| "deploy"
	| "scale"
	| "arrange"
	| "fit"
	| "search"
	| "shortcuts"
	| "external"
	| "variables"
	| "references"
	| "unlink"
	| "plus"
	| "rename"
	| "collapse"
	| "expand"
	| "zoom"
	| "ungroup"
	| "group"
	| "combine"
	| "restart"
	| "clear"
	| "trash"
	| `create-${MenuEntryId}`;

/** Every way to add a service, as the Create button lists them. */
export type CreateSource = MenuEntryId;
export type InspectorSection = "overview" | "variables" | "settings";

/** What choosing a row does. The canvas owns the handlers. */
export type MenuAction =
	// A card
	| { type: "service-open"; serviceId: string }
	| { type: "service-deploy"; serviceId: string }
	| { type: "service-scale"; serviceId: string }
	| { type: "service-action"; serviceId: string; id: ServiceActionId }
	// The empty pane
	| { type: "create"; source: CreateSource; at: Point | null }
	| { type: "arrange" }
	| { type: "fit" }
	| { type: "search" }
	| { type: "shortcuts" }
	// The Internet node and its cables
	| { type: "open-domain"; hostname: string }
	| { type: "add-domain"; serviceId: string }
	| {
			type: "open-service";
			serviceId: string;
			section: InspectorSection;
			/** In Settings: the category to land on (`danger` for Delete). */
			settingsSection?: string;
	  }
	// A cable
	| { type: "review-references"; edgeId: string }
	| { type: "remove-link"; edgeId: string }
	// A group
	| { type: "rename-group"; groupId: string }
	| { type: "toggle-group"; groupId: string }
	| { type: "zoom-group"; groupId: string }
	| { type: "ungroup"; groupId: string }
	| { type: "delete-stack"; groupId: string }
	// A selection of cards
	| { type: "deploy-selected"; serviceIds: string[] }
	| { type: "restart-selected"; serviceIds: string[] }
	| { type: "group-selected"; serviceIds: string[] }
	| { type: "combine-selected"; serviceIds: string[] }
	| { type: "arrange-selected"; serviceIds: string[] }
	| { type: "clear-selection" };

export interface MenuItem {
	/** Stable within its menu: the React key. */
	key: string;
	label: string;
	icon?: MenuIconName;
	shortcut?: string;
	disabled?: boolean;
	tone?: "destructive";
	action: MenuAction;
}

export interface MenuSection {
	label?: string;
	items: MenuItem[];
	/** A line under the rows: why one is off, or what is not listed. */
	note?: string;
}

export interface MenuModel {
	/** Mono title above the rows; omitted when the menu names no subject. */
	title?: string;
	ariaLabel: string;
	sections: MenuSection[];
}

/** How many domains or services a list-like section shows before it folds. */
export const MENU_LIST_LIMIT = 6;

function plural(count: number, one: string, many: string): string {
	return `${count} ${count === 1 ? one : many}`;
}

/** A section without rows has nothing to show: drop it, keep its note out. */
function nonEmpty(sections: readonly MenuSection[]): MenuSection[] {
	return sections.filter((section) => section.items.length > 0);
}

// --- a card -------------------------------------------------------------------

export interface ServiceMenuInput {
	service: Pick<Service, "id" | "name" | "scaling" | "processes">;
	/** The rows `useServiceActions` offers, in order. */
	actions: ReadonlyArray<{
		id: ServiceActionId;
		label: string;
		disabled: boolean;
		destructive: boolean;
	}>;
	/** A deploy, restart or stop of this service is on its way. */
	busy: boolean;
	/**
	 * The cables this card is an end of, named by the card at their other end.
	 * A cable is not a tab stop, so this is how the keyboard reaches its review.
	 */
	links?: ReadonlyArray<{ edgeId: string; otherName: string }>;
}

/**
 * Open, Deploy and Scale first (the dock's own), then the rest of More, with
 * the destructive rows last behind a separator. The Deploy row is the
 * "redeploy" action under the word the dock uses for it.
 */
export function serviceMenu({
	service,
	actions,
	busy,
	links = [],
}: ServiceMenuInput): MenuModel {
	const eligible = serviceCanScale(service);
	const rows = actions.filter((action) => action.id !== "redeploy");
	const deployLabel =
		actions.find((action) => action.id === "redeploy")?.label ?? "Deploy";
	const toItem = (action: (typeof rows)[number]): MenuItem => ({
		key: `${action.id}:${action.label}`,
		label: action.label,
		disabled: action.disabled,
		...(action.destructive ? { tone: "destructive" as const } : {}),
		action: { type: "service-action", serviceId: service.id, id: action.id },
	});
	const first: MenuSection = {
		items: [
			{
				key: "open",
				label: "Open",
				icon: "open",
				shortcut: "↵",
				action: { type: "service-open", serviceId: service.id },
			},
			{
				key: "deploy",
				label: deployLabel,
				icon: "deploy",
				disabled: busy,
				action: { type: "service-deploy", serviceId: service.id },
			},
			{
				key: "scale",
				label: "Scale…",
				icon: "scale",
				disabled: !eligible,
				action: { type: "service-scale", serviceId: service.id },
			},
		],
	};
	if (!eligible && service.scaling.ineligibleReason) {
		first.note = service.scaling.ineligibleReason;
	}
	const shown = links.slice(0, MENU_LIST_LIMIT);
	const linked: MenuSection = {
		label: "Links",
		items: shown.map((link) => ({
			key: `link:${link.edgeId}`,
			label: `Review link to ${link.otherName}`,
			icon: "references",
			action: { type: "review-references", edgeId: link.edgeId },
		})),
	};
	if (links.length > shown.length) {
		linked.note = `${links.length - shown.length} more in Variables.`;
	}
	return {
		title: service.name,
		ariaLabel: `Actions for ${service.name}`,
		sections: nonEmpty([
			first,
			{ items: rows.filter((action) => !action.destructive).map(toItem) },
			linked,
			{ items: rows.filter((action) => action.destructive).map(toItem) },
		]),
	};
}

// --- the empty pane -------------------------------------------------------------

export function paneMenu({
	at,
	empty,
}: {
	at: Point | null;
	empty: boolean;
}): MenuModel {
	return {
		ariaLabel: "Canvas",
		sections: [
			{
				label: "Create here",
				items: ROOT_ENTRIES.map((entry) => {
					const id = entry.id as MenuEntryId;
					return {
						key: `create-${id}`,
						label: entry.label,
						icon: `create-${id}` as const,
						action: { type: "create", source: id, at },
					};
				}),
			},
			{
				items: [
					{
						key: "arrange",
						label: "Arrange",
						icon: "arrange",
						disabled: empty,
						action: { type: "arrange" },
					},
					{
						key: "fit",
						label: "Fit view",
						icon: "fit",
						shortcut: "1",
						disabled: empty,
						action: { type: "fit" },
					},
					{
						key: "search",
						label: "Search",
						icon: "search",
						shortcut: "⌘F",
						disabled: empty,
						action: { type: "search" },
					},
					{
						key: "shortcuts",
						label: "Keyboard shortcuts",
						icon: "shortcuts",
						shortcut: "?",
						action: { type: "shortcuts" },
					},
				],
			},
		],
	};
}

// --- the Internet node ---------------------------------------------------------

export interface InternetMenuInput {
	/** Every public hostname of the environment with the service that serves it. */
	domains: ReadonlyArray<{
		hostname: string;
		serviceId: string;
		serviceName: string;
	}>;
	/** Services that can take another domain, in canvas order. */
	candidates: ReadonlyArray<{ id: string; name: string }>;
}

/** Whether a service can be given a public domain from the canvas. */
export function canTakeDomain(service: Pick<Service, "kind">): boolean {
	return service.kind === "app" || service.kind === "route";
}

export function internetMenu({
	domains,
	candidates,
}: InternetMenuInput): MenuModel {
	const shown = domains.slice(0, MENU_LIST_LIMIT);
	const hiddenDomains = domains.length - shown.length;
	const offered = candidates.slice(0, MENU_LIST_LIMIT);
	const hiddenCandidates = candidates.length - offered.length;
	const open: MenuSection = {
		label: "Open",
		items: shown.map((domain) => ({
			key: `open:${domain.hostname}`,
			label: domain.hostname,
			icon: "external" as const,
			action: { type: "open-domain" as const, hostname: domain.hostname },
		})),
	};
	if (hiddenDomains > 0) {
		open.note = `${plural(hiddenDomains, "more domain", "more domains")}: open a service to see all of its domains.`;
	}
	const add: MenuSection = {
		label: "Add domain to",
		items: offered.map((candidate) => ({
			key: `add:${candidate.id}`,
			label: candidate.name,
			icon: "plus" as const,
			action: { type: "add-domain" as const, serviceId: candidate.id },
		})),
	};
	if (hiddenCandidates > 0) {
		add.note = `${plural(hiddenCandidates, "more service", "more services")}: open its Settings to add a domain.`;
	} else if (candidates.length === 0) {
		add.note = "No app or route here can take a domain yet.";
	}
	return {
		title: "Internet",
		ariaLabel: `Actions for the Internet, ${plural(domains.length, "public domain", "public domains")}`,
		sections: [
			...(open.items.length > 0 ? [open] : []),
			// Keep the section even when empty: its note says why.
			add,
		],
	};
}

// --- a cable ---------------------------------------------------------------------

export interface EdgeEnd {
	/** The node the cable ends on: a service, a collapsed group or the Internet. */
	nodeId: string;
	name: string;
	/** A service card (its inspector can be opened); false for groups and the Internet. */
	isService: boolean;
}

export interface EdgeMenuInput {
	edgeId: string;
	kind: "reference" | "manual" | "internet";
	from: EdgeEnd;
	to: EdgeEnd;
	/** The stored link; null for the derived Internet cable. */
	link: Pick<ServiceLink, "id" | "source"> | null;
	/** Variable keys on the source service that reference the target. */
	references: readonly string[];
	/** The target's public hostnames, for the Internet cable. */
	hostnames: readonly string[];
}

export function edgeMenu(input: EdgeMenuInput): MenuModel {
	const { edgeId, kind, from, to, link, references, hostnames } = input;
	const title = `${from.name} → ${to.name}`;
	const ariaLabel = `Actions for the cable from ${from.name} to ${to.name}`;
	const open = (end: EdgeEnd, section: InspectorSection): MenuItem => ({
		key: `open:${end.nodeId}:${section}`,
		label:
			section === "variables" ? `Variables of ${end.name}` : `Open ${end.name}`,
		icon: section === "variables" ? "variables" : "open",
		action: { type: "open-service", serviceId: end.nodeId, section },
	});

	if (kind === "internet") {
		const shown = hostnames.slice(0, MENU_LIST_LIMIT);
		const hidden = hostnames.length - shown.length;
		const sections: MenuSection[] = [
			{
				label: "Open",
				items: shown.map((hostname) => ({
					key: `open:${hostname}`,
					label: hostname,
					icon: "external" as const,
					action: { type: "open-domain" as const, hostname },
				})),
				...(hidden > 0
					? {
							note: `${plural(hidden, "more domain", "more domains")} on ${to.name}.`,
						}
					: {}),
			},
			{ items: to.isService ? [open(to, "settings")] : [] },
		];
		return { title, ariaLabel, sections: nonEmpty(sections) };
	}

	const review: MenuItem = {
		key: "references",
		label:
			references.length > 0
				? `View references (${references.length})`
				: "View references",
		icon: "references",
		disabled: link === null,
		action: { type: "review-references", edgeId },
	};
	const jump: MenuItem[] = [];
	if (from.isService) jump.push(open(from, "variables"));
	if (from.isService) jump.push(open(from, "overview"));
	if (to.isService) jump.push(open(to, "overview"));

	const removable = link !== null && link.source === "manual";
	const remove: MenuSection = {
		items: [
			{
				key: "remove",
				label: "Remove link",
				icon: "unlink",
				tone: "destructive",
				disabled: !removable,
				action: { type: "remove-link", edgeId },
			},
		],
	};
	if (!removable) {
		remove.note = `Drawn from variable references. It goes away when ${from.name}'s variables stop referencing ${to.name}.`;
	} else if (references.length > 0) {
		remove.note =
			"Variables still reference it, so the cable stays drawn after the link is removed.";
	}
	return {
		title,
		ariaLabel,
		sections: nonEmpty([{ items: [review] }, { items: jump }, remove]),
	};
}

// --- a group -----------------------------------------------------------------------

export interface GroupMenuInput {
	groupId: string;
	name: string;
	collapsed: boolean;
	memberCount: number;
	/** The group is an installed template stack, deletable as a whole. */
	isStack: boolean;
}

export function groupMenu({
	groupId,
	name,
	collapsed,
	memberCount,
	isStack,
}: GroupMenuInput): MenuModel {
	const sections: MenuSection[] = [
		{
			items: [
				{
					key: "rename",
					label: "Rename",
					icon: "rename",
					action: { type: "rename-group", groupId },
				},
				{
					key: "toggle",
					label: collapsed ? "Expand" : "Collapse",
					icon: collapsed ? "expand" : "collapse",
					action: { type: "toggle-group", groupId },
				},
				{
					key: "zoom",
					label: "Zoom to group",
					icon: "zoom",
					disabled: memberCount === 0,
					action: { type: "zoom-group", groupId },
				},
			],
		},
		{
			items: [
				{
					key: "ungroup",
					label: "Ungroup",
					icon: "ungroup",
					action: { type: "ungroup", groupId },
				},
			],
			note: `The ${plural(memberCount, "service", "services")} stay${memberCount === 1 ? "s" : ""}; only the frame goes.`,
		},
	];
	if (isStack) {
		sections.push({
			items: [
				{
					key: "delete-stack",
					label: "Delete stack…",
					icon: "trash",
					tone: "destructive",
					action: { type: "delete-stack", groupId },
				},
			],
		});
	}
	return {
		title: name,
		ariaLabel: `Actions for group ${name}`,
		sections,
	};
}

// --- several cards -------------------------------------------------------------------

export interface SelectionMenuInput {
	services: readonly Service[];
	isProduction: boolean;
}

/** The services of a selection a bulk deploy reaches: those not already deploying. */
export function deployableOf(services: readonly Service[]): Service[] {
	return services.filter((service) => !isInFlight(service));
}

/** Those a bulk restart reaches: deployed, and not mid-deployment. */
export function restartableOf(services: readonly Service[]): Service[] {
	return services.filter(
		(service) => service.currentDeploymentId !== null && !isInFlight(service),
	);
}

export function selectionMenu({
	services,
	isProduction,
}: SelectionMenuInput): MenuModel {
	const ids = services.map((service) => service.id);
	const count = services.length;
	const deployable = deployableOf(services);
	const restartable = restartableOf(services);
	const deploying = count - deployable.length;

	const deploy: MenuItem = {
		key: "deploy",
		label: isProduction
			? "Review and deploy…"
			: `Deploy ${plural(deployable.length, "service", "services")}`,
		icon: "deploy",
		disabled: deployable.length === 0,
		action: {
			type: "deploy-selected",
			serviceIds: deployable.map((service) => service.id),
		},
	};
	const restart: MenuItem = {
		key: "restart",
		label: `Restart ${plural(restartable.length, "service", "services")}`,
		icon: "restart",
		disabled: restartable.length === 0,
		action: {
			type: "restart-selected",
			serviceIds: restartable.map((service) => service.id),
		},
	};
	const run: MenuSection = { items: [deploy, restart] };
	const notes: string[] = [];
	if (isProduction) {
		notes.push("Production deploys are confirmed in the review sheet.");
	}
	if (deploying > 0) {
		notes.push(
			`${plural(deploying, "service is", "services are")} already deploying and skipped.`,
		);
	}
	const idle = count - restartable.length - deploying;
	if (idle > 0 && restartable.length > 0) {
		notes.push(
			`${plural(idle, "service has", "services have")} nothing running to restart.`,
		);
	}
	if (notes.length > 0) run.note = notes.join(" ");

	// The pre-check only spares a round trip: when it fails the row is off and
	// the note says what to change; the plan the dialog asks for stays the judge.
	const combinable = canCombine(services);
	const combineSection: MenuSection = {
		items: [
			{
				key: "combine",
				label: "Combine into one service…",
				icon: "combine",
				disabled: !combinable.ok,
				action: { type: "combine-selected", serviceIds: ids },
			},
		],
		...(combinable.ok ? {} : { note: combinable.reason }),
	};

	return {
		title: `${plural(count, "service", "services")} selected`,
		ariaLabel: `Actions for ${plural(count, "selected service", "selected services")}`,
		sections: [
			run,
			{
				items: [
					{
						key: "group",
						label: `Group ${count} services`,
						icon: "group",
						disabled: count < 2,
						action: { type: "group-selected", serviceIds: ids },
					},
					{
						key: "arrange",
						label: `Arrange ${count} services`,
						icon: "arrange",
						disabled: count < 2,
						action: { type: "arrange-selected", serviceIds: ids },
					},
				],
			},
			...(count >= 2 ? [combineSection] : []),
			{
				items: [
					{
						key: "clear",
						label: "Clear selection",
						icon: "clear",
						shortcut: "esc",
						action: { type: "clear-selection" },
					},
				],
			},
		],
	};
}

// --- what is under the pointer ----------------------------------------------------------

/** The subject of a menu, by id: the canvas looks the data up when it draws. */
export type MenuTargetRef =
	| { kind: "service"; serviceId: string }
	| { kind: "pane" }
	| { kind: "internet" }
	| { kind: "edge"; edgeId: string }
	| { kind: "group"; groupId: string }
	| { kind: "selection"; serviceIds: string[] };

/** What the DOM under the pointer (or the focused element) says. */
export interface TargetProbe {
	/** `data-id` of the React Flow node the element is in. */
	nodeId: string | null;
	/** `data-id` of the React Flow edge the element is in. */
	edgeId: string | null;
	/** The element is the box React Flow draws around a multi-selection. */
	selectionRect: boolean;
	/** The element is in a card's dock, which belongs to this service. */
	dockServiceId: string | null;
}

export interface TargetKnown {
	serviceIds: ReadonlySet<string>;
	groupIds: ReadonlySet<string>;
	edgeIds: ReadonlySet<string>;
	/** Visible selected service cards, in canvas order. */
	selectedServiceIds: readonly string[];
}

/**
 * Decides what a right-click is for. With two or more cards selected, a press
 * on the selection box or on any selected card is for the selection (the box
 * sits on top of the cards, so it is what the pointer meets); a press on an
 * unselected card is for that card. Anything the canvas does not know (a stale
 * id, chrome, the pane) is the pane.
 */
export function resolveMenuTarget(
	probe: TargetProbe,
	known: TargetKnown,
): MenuTargetRef {
	const selection = known.selectedServiceIds;
	const several = selection.length >= 2;
	if (probe.selectionRect && several) {
		return { kind: "selection", serviceIds: [...selection] };
	}
	const { nodeId } = probe;
	if (nodeId !== null) {
		if (nodeId === INTERNET_ID) return { kind: "internet" };
		const groupId = groupIdOfNode(nodeId);
		if (groupId !== null) {
			return known.groupIds.has(groupId)
				? { kind: "group", groupId }
				: { kind: "pane" };
		}
		if (known.serviceIds.has(nodeId)) {
			return several && selection.includes(nodeId)
				? { kind: "selection", serviceIds: [...selection] }
				: { kind: "service", serviceId: nodeId };
		}
	}
	if (
		probe.dockServiceId !== null &&
		known.serviceIds.has(probe.dockServiceId)
	) {
		return { kind: "service", serviceId: probe.dockServiceId };
	}
	if (probe.edgeId !== null && known.edgeIds.has(probe.edgeId)) {
		return { kind: "edge", edgeId: probe.edgeId };
	}
	return { kind: "pane" };
}
