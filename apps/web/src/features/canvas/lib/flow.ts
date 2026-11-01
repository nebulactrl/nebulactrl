import type { CanvasLayout, Point, Service, ServiceLink } from "#/api/models";
import { DIMMED_OPACITY } from "../canvas-context";
import type { GroupFlowNode } from "../group-node";
import type { InternetFlowNode } from "../internet-node";
import type { LinkEdgeData, LinkFlowEdge } from "../service-edge";
import type { ServiceFlowNode } from "../service-node";
import {
	type Box,
	chooseSides,
	INTERNET_JACK,
	NO_PLUGS,
	type Plugs,
} from "./cable";
import { INTERNET_ID, internetPosition, internetSummary } from "./internet";
import {
	defaultSize,
	fitGroups,
	GROUP_SUMMARY_HEIGHT,
	groupOf,
	NODE_WIDTH,
	roundPoint,
	type SizeOf,
} from "./layout";
import { nodeStatus } from "./status";

export type AppNode = ServiceFlowNode | GroupFlowNode | InternetFlowNode;

const GROUP_PREFIX = "group:";

export function groupNodeId(groupId: string): string {
	return `${GROUP_PREFIX}${groupId}`;
}

function isGroupNodeId(id: string): boolean {
	return id.startsWith(GROUP_PREFIX);
}

/** The group id a group node's id stands for, or null for any other node. */
export function groupIdOfNode(nodeId: string): string | null {
	return isGroupNodeId(nodeId) ? nodeId.slice(GROUP_PREFIX.length) : null;
}

/** Links as the canvas reads them: the pair, plus what a cable's label needs. */
export type FlowLink = Pick<
	ServiceLink,
	"id" | "fromServiceId" | "toServiceId"
> &
	Partial<
		Pick<
			ServiceLink,
			"source" | "references" | "createdAt" | "environmentId" | "staged"
		>
	>;

function isFullLink(link: FlowLink): link is ServiceLink {
	return (
		link.source !== undefined &&
		link.createdAt !== undefined &&
		link.environmentId !== undefined &&
		link.references !== undefined
	);
}

/** The card's rectangle for choosing jack sides: a service, or a collapsed group's summary. */
function endBox(
	layout: CanvasLayout,
	serviceId: string,
	sizeOf: SizeOf = defaultSize,
): Box {
	const group = layout.groups.find(
		(candidate) =>
			candidate.collapsed && candidate.serviceIds.includes(serviceId),
	);
	if (group) {
		return { x: group.x, y: group.y, w: NODE_WIDTH, h: GROUP_SUMMARY_HEIGHT };
	}
	const at = layout.nodes[serviceId] ?? { x: 0, y: 0 };
	const size = sizeOf(serviceId);
	return { x: at.x, y: at.y, w: size.w, h: size.h };
}

/**
 * Keyboard focus lands on React Flow's node wrapper, whose stylesheet strips
 * the outline. Draw the one 2px ring around the card;
 * `!` beats the unlayered library rule.
 */
const NODE_FOCUS_CLASS =
	"rounded-xl focus-visible:outline-2! focus-visible:outline-accent-ink! focus-visible:outline-offset-2! focus-visible:outline-solid!";
/** The Internet node's ring: the same, on its 14px corners. */
const INTERNET_FOCUS_CLASS =
	"rounded-[14px] focus-visible:outline-2! focus-visible:outline-accent-ink! focus-visible:outline-offset-2! focus-visible:outline-solid!";

/**
 * React Flow nodes for a layout. Groups come first (a child must follow its
 * parent); each member is positioned relative to its group's frame so that
 * dragging the frame moves its members. A collapsed group's members stay in
 * the list, hidden, and keep their place inside the frame. The derived
 * Internet entry node (when any service has a domain) comes last.
 *
 * `links` only tell each card which of its jacks hold a plug, read from
 * `plugLayout` (where the cards are going, while Arrange moves them) so the
 * plugs do not flicker from side to side on the way.
 */
export function buildNodes(
	services: readonly Service[],
	layout: CanvasLayout,
	selected: ReadonlySet<string>,
	links: readonly Pick<ServiceLink, "fromServiceId" | "toServiceId">[] = [],
	plugLayout: CanvasLayout = layout,
): AppNode[] {
	const byId = new Map(services.map((service) => [service.id, service]));
	const nodes: AppNode[] = [];
	const summary = internetSummary(services);

	// Which of each card's two jacks hold a plug: the sides its cables take.
	const plugs = new Map<string, Plugs>();
	const plug = (id: string, side: keyof Plugs) => {
		const held = plugs.get(id) ?? NO_PLUGS;
		plugs.set(id, { ...held, [side]: true });
	};
	for (const link of links) {
		const sides = chooseSides(
			endBox(plugLayout, link.fromServiceId),
			endBox(plugLayout, link.toServiceId),
		);
		plug(link.fromServiceId, sides.source);
		plug(link.toServiceId, sides.target);
	}
	if (summary) {
		for (const id of summary.serviceIds) plug(id, INTERNET_JACK);
	}

	for (const group of layout.groups) {
		const members = group.serviceIds.flatMap((id) => {
			const service = byId.get(id);
			return service ? [service] : [];
		});
		const id = groupNodeId(group.id);
		nodes.push({
			id,
			type: "cluster",
			position: { x: group.x, y: group.y },
			data: { group, members },
			selected: selected.has(id),
			ariaLabel: `${group.name}, group of ${members.length}`,
			zIndex: group.collapsed ? 0 : -1,
			...(group.collapsed
				? {}
				: {
						style: { width: group.w, height: group.h },
						dragHandle: ".nc-group-handle",
					}),
		});
	}

	for (const service of services) {
		const abs = layout.nodes[service.id] ?? { x: 0, y: 0 };
		const group = groupOf(layout, service.id);
		const status = nodeStatus(service);
		nodes.push({
			id: service.id,
			type: "service",
			position: group ? { x: abs.x - group.x, y: abs.y - group.y } : abs,
			data: { service, plugs: plugs.get(service.id) ?? NO_PLUGS },
			selected: selected.has(service.id),
			ariaLabel: `${service.name}, ${service.kind}, ${status.label}`,
			className: NODE_FOCUS_CLASS,
			...(group
				? { parentId: groupNodeId(group.id), hidden: group.collapsed }
				: {}),
		});
	}

	if (summary) {
		nodes.push({
			id: INTERNET_ID,
			type: "internet",
			position: internetPosition(layout, summary.serviceIds),
			data: { summary },
			draggable: false,
			selectable: false,
			deletable: false,
			connectable: false,
			className: INTERNET_FOCUS_CLASS,
			ariaLabel: `Internet, ${summary.domains.length} public ${summary.domains.length === 1 ? "domain" : "domains"}`,
		});
	}
	return nodes;
}

/**
 * The persisted layout implied by the nodes' current positions: absolute,
 * whole pixels, with every expanded group's frame re-fitted to its members.
 * Anything absent from `nodes` keeps its saved place.
 */
export function layoutFromNodes(
	prev: CanvasLayout,
	nodes: readonly AppNode[],
	sizeOf: SizeOf = defaultSize,
): CanvasLayout {
	const byId = new Map(nodes.map((node) => [node.id, node]));

	const groups = prev.groups.map((group) => {
		const node = byId.get(groupNodeId(group.id));
		if (!node) return group;
		const at = roundPoint(node.position);
		return { ...group, x: at.x, y: at.y };
	});
	const groupAt = new Map(
		groups.map((group) => [groupNodeId(group.id), group]),
	);

	const placed: Record<string, Point> = {};
	for (const [id, saved] of Object.entries(prev.nodes)) {
		const node = byId.get(id);
		if (!node) {
			placed[id] = saved;
			continue;
		}
		const parent = node.parentId ? groupAt.get(node.parentId) : undefined;
		placed[id] = roundPoint(
			parent
				? { x: parent.x + node.position.x, y: parent.y + node.position.y }
				: node.position,
		);
	}
	return fitGroups({ ...prev, nodes: placed, groups }, sizeOf);
}

export interface EdgeExtras {
	/** Services with a deployment on its way: cables into them carry a bead. */
	inFlightIds?: ReadonlySet<string> | undefined;
	/** Services that have a domain, from `internetSummary`: they get a cable from the Internet node. */
	internetIds?: readonly string[] | undefined;
	/** A node's real size (a stack card is taller and wider than a service card). */
	sizeOf?: SizeOf | undefined;
	/** A node's name, for a cable's accessible label: a service, a group or "Internet". Defaults to the id. */
	nameOf?: ((nodeId: string) => string) | undefined;
}

/**
 * A cable's accessible name: the two ends by name, then what kind of cable it
 * is when that is not the plain case ("web → postgres", "web → api, linked by
 * hand"). React Flow's own default is "Edge from <uuid> to <uuid>".
 */
export function edgeLabel(
	from: string,
	to: string,
	kind: LinkEdgeData["kind"],
	staged: boolean,
): string {
	const ends = `${from} → ${to}`;
	if (kind === "manual") return `${ends}, linked by hand`;
	if (kind === "reference" && staged) return `${ends}, staged`;
	return ends;
}

/**
 * Edges for the links. An end inside a collapsed group attaches to the
 * group's summary card; parallel edges collapse into one, and an edge
 * between two members of the same collapsed group disappears. Under a
 * filter an edge is as dim as its dimmest end. A link that variables
 * reference is a solid cable (stitched while every reference is still
 * staged, so the cable is drawn but not live); one the user only drew is
 * dashed. The
 * Internet node gets a cable to every service that has a domain. Each cable
 * picks its jacks from where its two cards stand (`chooseSides`), so it
 * never doubles back behind a card.
 */
export function buildEdges(
	links: readonly FlowLink[],
	layout: CanvasLayout,
	matchIds: ReadonlySet<string> | null,
	extras: EdgeExtras = {},
): LinkFlowEdge[] {
	const collapsed = new Map<string, string>();
	for (const group of layout.groups) {
		if (!group.collapsed) continue;
		for (const id of group.serviceIds) collapsed.set(id, groupNodeId(group.id));
	}
	const inFlightIds = extras.inFlightIds;
	const nameOf = extras.nameOf ?? ((nodeId: string) => nodeId);
	const edges: LinkFlowEdge[] = [];
	const seen = new Set<string>();
	for (const link of links) {
		const source = collapsed.get(link.fromServiceId) ?? link.fromServiceId;
		const target = collapsed.get(link.toServiceId) ?? link.toServiceId;
		const key = `${source}>${target}`;
		if (source === target || seen.has(key)) continue;
		seen.add(key);
		const dimmed =
			matchIds !== null &&
			!(matchIds.has(link.fromServiceId) && matchIds.has(link.toServiceId));
		const references = (link.references ?? []).map((ref) => ref.key);
		const data: LinkEdgeData = {
			kind:
				link.source === "reference" || references.length > 0
					? "reference"
					: "manual",
			link: isFullLink(link) ? link : null,
			references,
			staged: link.staged === true,
			inFlight: inFlightIds?.has(link.toServiceId) === true,
		};
		const sides = chooseSides(
			endBox(layout, link.fromServiceId, extras.sizeOf),
			endBox(layout, link.toServiceId, extras.sizeOf),
		);
		edges.push({
			id: link.id,
			type: "link",
			source,
			target,
			sourceHandle: sides.source,
			targetHandle: sides.target,
			data,
			selectable: false,
			// Cables are not tab stops: the cards come first, and a card's menu
			// reaches the cable's review. The name is still read in browse mode.
			focusable: false,
			ariaLabel: edgeLabel(
				nameOf(source),
				nameOf(target),
				data.kind,
				data.staged === true,
			),
			...(dimmed ? { style: { opacity: DIMMED_OPACITY } } : {}),
		});
	}

	const internetIds = extras.internetIds ?? [];
	for (const id of internetIds) {
		const target = collapsed.get(id) ?? id;
		const key = `${INTERNET_ID}>${target}`;
		if (seen.has(key)) continue;
		seen.add(key);
		const dimmed = matchIds !== null && !matchIds.has(id);
		edges.push({
			id: `internet:${target}`,
			type: "link",
			source: INTERNET_ID,
			target,
			sourceHandle: "bottom",
			targetHandle: INTERNET_JACK,
			data: {
				kind: "internet",
				link: null,
				references: [],
				staged: false,
				inFlight: inFlightIds?.has(id) === true,
			},
			selectable: false,
			focusable: false,
			ariaLabel: edgeLabel(
				nameOf(INTERNET_ID),
				nameOf(target),
				"internet",
				false,
			),
			...(dimmed ? { style: { opacity: DIMMED_OPACITY } } : {}),
		});
	}
	return edges;
}
