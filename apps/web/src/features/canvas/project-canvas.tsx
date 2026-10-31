import "@xyflow/react/dist/style.css";
import {
	applyNodeChanges,
	ConnectionMode,
	type FinalConnectionState,
	MiniMap,
	type NodeChange,
	type NodeMouseHandler,
	type NodeTypes,
	type OnMoveEnd,
	type OnNodeDrag,
	ReactFlow,
	ReactFlowProvider,
	useReactFlow,
	useStore,
} from "@xyflow/react";
import { useReducedMotion } from "motion/react";
import {
	type ReactNode,
	useCallback,
	useEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import type {
	CanvasDocument,
	CanvasLayout,
	Point,
	Server,
	Service,
	ServiceLink,
} from "#/api/models";
import {
	ContextMenu,
	type ContextMenuOpener,
	ContextMenuTrigger,
	useContextMenuOpener,
} from "#/components/motion/context-menu";
import { toast } from "#/components/ui/toast";
import { CombineDialog } from "#/features/combine/combine-dialog";
import { openCreateEntry } from "#/features/create";
import { useLinkReview } from "#/features/links";
import { PrivateNetworkCaption } from "#/features/networks";
import { ScaleDialog } from "#/features/scaling";
import { lastInputWasKeyboard } from "#/lib/input-modality";
import { CanvasUiContext } from "./canvas-context";
import { CanvasFloor } from "./canvas-floor";
import {
	fitWhenMeasured,
	type Landing,
	registerLanding,
} from "./canvas-landing";
import { CanvasMenu, type MenuTarget } from "./canvas-menu";
import { CanvasToolbar } from "./canvas-toolbar";
import { CanvasZoomToolbar } from "./canvas-zoom";
import { CanvasEmptyState, type CanvasLoadError } from "./empty-state";
import { FilterChips } from "./filter-chips";
import { GroupNode } from "./group-node";
import { PAN_EASE, panDuration, revealTiming } from "./inspector/camera";
import { isFullScreen, useInspectorWidthPx } from "./inspector/geometry";
import { Inspector } from "./inspector/inspector";
import type { InspectorTab } from "./inspector/tab-ids";
import { InternetNode } from "./internet-node";
import { chromeModeFor } from "./lib/chrome";
import { revealShift } from "./lib/dock";
import { ARRANGE_MS, arrangeEase } from "./lib/ease";
import {
	type CanvasFilter,
	dimmingIds,
	EMPTY_FILTER,
	matchingIds,
} from "./lib/filter";
import {
	type AppNode,
	buildEdges,
	buildNodes,
	groupIdOfNode,
	groupNodeId,
	layoutFromNodes,
} from "./lib/flow";
import { INTERNET_ID, internetSummary } from "./lib/internet";
import {
	arrangeSelection,
	autoLayout,
	blendLayout,
	createGroup,
	type Direction,
	fitGroups,
	GROUP_SUMMARY_HEIGHT,
	mergeLayout,
	NODE_HEIGHT,
	NODE_WIDTH,
	neighbourInDirection,
	removeGroup,
	renameGroup,
	roundPoint,
	type SizeOf,
	samePlaces,
	toggleGroup,
} from "./lib/layout";
import {
	canTakeDomain,
	type MenuAction,
	type MenuTargetRef,
	resolveMenuTarget,
	type TargetProbe,
} from "./lib/menu";
import { reuseEdges, reuseNodes } from "./lib/reuse";
import { layoutTraits, sizesOf } from "./lib/sizes";
import { isInFlight } from "./lib/status";
import { initialViewport } from "./lib/viewport";
import { CanvasSearch } from "./search-bar";
import { SelectionDock } from "./selection-dock";
import {
	ConnectionCable,
	type LinkFlowEdge,
	ServiceEdge,
} from "./service-edge";
import { ServiceNode } from "./service-node";
import { ShortcutsDialog } from "./shortcuts-dialog";
import { useCanvasFloor } from "./use-canvas-floor";
import { useCanvasMenuActions } from "./use-canvas-menu-actions";
import { registerPlacer, useLayoutPersistence } from "./use-layout-persistence";

const nodeTypes = {
	service: ServiceNode,
	cluster: GroupNode,
	internet: InternetNode,
} satisfies NodeTypes;
const edgeTypes = { link: ServiceEdge };

const FIT_OPTIONS = { padding: 0.3, maxZoom: 1 } as const;
/** The toast that offers to undo an Arrange: a second Arrange replaces it. */
const ARRANGE_TOAST = "canvas-arrange";
/** Held to add a card to the selection. A module constant: ReactFlow compares it by identity. */
const MULTI_SELECT_KEYS = ["Shift", "Meta", "Control"];
const PRO_OPTIONS = { hideAttribution: true } as const;

const TICK_MS = 30_000;
const SEARCH_FIT_DELAY_MS = 250;
/** From this many nodes on, cards and cables off screen are not drawn. */
const VIRTUALIZE_FROM = 100;
const DOCK_RESERVE = 14 + 40;

export interface ProjectCanvasProps {
	projectId: string;
	environmentId: string;
	/** Production deploys from the dock and the menu are held to confirm. */
	isProduction: boolean;
	/** The environment's name, for the "Private network · <name>" caption at the floor. */
	environmentName: string;
	services: readonly Service[];
	links: readonly ServiceLink[];
	servers: readonly Server[];
	/** The saved layout and the version it came with. */
	document: CanvasDocument;
	/** The service whose inspector is open (`?service=`). */
	inspectedServiceId: string | undefined;
	tab: InspectorTab;
	/** `?section=`: the Settings category the inspector shows. */
	section: string | undefined;
	/**
	 * Opens a service's inspector (on `tab`, and in Settings on `section`), or
	 * with undefined closes it.
	 */
	onInspect(
		serviceId: string | undefined,
		tab?: InspectorTab,
		section?: string,
	): void;
	onTabChange(tab: InspectorTab): void;
	onSectionChange(section: string): void;
	/** The Create button: the right end of the canvas toolbar, inside the free area. */
	toolbarEnd?: ReactNode;
	/** Set when the services could not be loaded; shown with the empty state. */
	error?: CanvasLoadError | undefined;
}

export function ProjectCanvas(props: ProjectCanvasProps) {
	return (
		<ReactFlowProvider>
			<CanvasInner {...props} />
		</ReactFlowProvider>
	);
}

function isEditable(target: EventTarget | null): boolean {
	if (!(target instanceof HTMLElement)) return false;
	return (
		target.isContentEditable ||
		target.tagName === "INPUT" ||
		target.tagName === "TEXTAREA" ||
		target.tagName === "SELECT"
	);
}

/** A dialog, menu or listbox is open: Esc and the arrows are theirs. */
function dialogOpen(): boolean {
	return (
		document.querySelector(
			'[role="dialog"], [role="alertdialog"], [role="menu"], [role="listbox"]',
		) !== null
	);
}

function focusNode(id: string) {
	document
		.querySelector<HTMLElement>(
			`.react-flow__node[data-id="${CSS.escape(id)}"]`,
		)
		?.focus({ preventScroll: true });
}

/** Whether `next` is drawn at the size `previous` was measured at. */
function sameShape(previous: AppNode, next: AppNode): boolean {
	if (previous.type === "cluster" && next.type === "cluster") {
		const a = previous.data.group;
		const b = next.data.group;
		return a.collapsed === b.collapsed && a.w === b.w && a.h === b.h;
	}
	return previous.type === next.type;
}

/**
 * Surfaces that keep the browser's own menu: text fields (paste, spellcheck)
 * and the layers portalled out of the canvas (dialogs, menus, listboxes),
 * whose React events still bubble through the trigger. Everything else on
 * the canvas, its chrome included, gets one of our menus.
 */
function ignoreMenu(target: HTMLElement): boolean {
	return (
		target.closest(
			"input, textarea, select, [contenteditable]:not([contenteditable='false']), [role='dialog'], [role='alertdialog'], [role='menu'], [role='listbox'], [role='tooltip'], [data-native-menu]",
		) !== null
	);
}

/** What the element under the pointer (or the focused card) is part of. */
function readProbe(target: HTMLElement | null): TargetProbe {
	return {
		nodeId:
			target?.closest(".react-flow__node")?.getAttribute("data-id") ?? null,
		edgeId:
			target?.closest(".react-flow__edge")?.getAttribute("data-id") ?? null,
		selectionRect:
			(target?.closest(".react-flow__nodesselection-rect") ?? null) !== null,
		dockServiceId:
			target?.closest("[data-dock-for]")?.getAttribute("data-dock-for") ?? null,
	};
}

/** Hands the surrounding menu's opener to the canvas, which lives outside it. */
function MenuOpenerBridge({
	openerRef,
}: {
	openerRef: { current: ContextMenuOpener | null };
}) {
	const opener = useContextMenuOpener();
	useEffect(() => {
		openerRef.current = opener;
		return () => {
			openerRef.current = null;
		};
	}, [opener, openerRef]);
	return null;
}

function CanvasInner({
	projectId,
	environmentId,
	isProduction,
	environmentName,
	services,
	links,
	servers,
	document: saved,
	inspectedServiceId,
	tab,
	section,
	onInspect,
	onTabChange,
	onSectionChange,
	toolbarEnd,
	error,
}: ProjectCanvasProps) {
	const rf = useReactFlow<AppNode, LinkFlowEdge>();
	const reduced = useReducedMotion() ?? false;
	const inspectorPx = useInspectorWidthPx();
	const linkReview = useLinkReview();
	const latticeRef = useRef<HTMLDivElement>(null);
	useCanvasFloor(latticeRef);

	// --- layout -------------------------------------------------------------
	const serviceIds = useMemo(() => services.map((s) => s.id), [services]);
	const serviceIdsRef = useRef(serviceIds);
	serviceIdsRef.current = serviceIds;
	// The real size of every node: a stack card is taller and wider than a service card.
	const sizeOfRef = useRef<SizeOf>(sizesOf(services));
	sizeOfRef.current = sizesOf(services);
	const sizeOf = useCallback<SizeOf>((id) => sizeOfRef.current(id), []);
	// What Arrange reads off the services: list order, the entries, the databases.
	const traits = useMemo(() => layoutTraits(services), [services]);
	const traitsRef = useRef(traits);
	traitsRef.current = traits;

	// Only the layout the canvas opened with seeds local state.
	const [initialMerge] = useState(() =>
		mergeLayout(serviceIds, saved.layout, sizeOf),
	);
	const [layout, setLayout] = useState(() =>
		fitGroups(initialMerge.layout, sizeOf),
	);
	const layoutRef = useRef(layout);
	const unsavedMerge = useRef(
		initialMerge.placed.length > 0 || initialMerge.pruned,
	);

	const adopt = useCallback(
		(next: CanvasLayout) => {
			const merged = fitGroups(
				mergeLayout(serviceIds, next, sizeOf).layout,
				sizeOf,
			);
			layoutRef.current = merged;
			setLayout(merged);
		},
		[serviceIds, sizeOf],
	);
	const persistence = useLayoutPersistence({
		projectId,
		environmentId,
		initialVersion: saved.version,
		onAdopt: adopt,
	});
	const commit = useCallback(
		(next: CanvasLayout) => {
			layoutRef.current = next;
			setLayout(next);
			persistence.schedule(next);
		},
		[persistence],
	);

	// A service created with a chosen place (right-click, Create here): the
	// create flow calls `placeNode` before the list has refetched, so the
	// position waits for its service.
	const pendingPlacements = useRef(new Map<string, Point>());
	useEffect(
		() =>
			registerPlacer((serviceId, position) => {
				const at = roundPoint(position);
				if (serviceIdsRef.current.includes(serviceId)) {
					commit({
						...layoutRef.current,
						nodes: { ...layoutRef.current.nodes, [serviceId]: at },
					});
				} else {
					pendingPlacements.current.set(serviceId, at);
				}
			}),
		[commit],
	);

	// New services get a place, deleted ones lose theirs.
	useEffect(() => {
		let base = layoutRef.current;
		for (const [id, at] of pendingPlacements.current) {
			if (!serviceIds.includes(id)) continue;
			base = { ...base, nodes: { ...base.nodes, [id]: at } };
			pendingPlacements.current.delete(id);
		}
		const merged = mergeLayout(serviceIds, base, sizeOf);
		if (
			merged.placed.length > 0 ||
			merged.pruned ||
			base !== layoutRef.current
		) {
			commit(fitGroups(merged.layout, sizeOf));
		} else if (unsavedMerge.current) {
			persistence.schedule(layoutRef.current);
		}
		unsavedMerge.current = false;
	}, [serviceIds, commit, persistence, sizeOf]);

	// --- nodes and edges ----------------------------------------------------
	const [nodes, setNodes] = useState<AppNode[]>(() =>
		buildNodes(services, layout, new Set(), links),
	);
	const nodesRef = useRef(nodes);
	nodesRef.current = nodes;
	const dragging = useRef(false);
	// Arrange is moving the cards: the layout is already the new one, the nodes follow it frame by frame.
	const arranging = useRef(false);

	const onNodesChange = useCallback(
		(changes: NodeChange<AppNode>[]) =>
			setNodes((current) => applyNodeChanges(changes, current)),
		[],
	);

	// Builds the nodes from a layout; `plugged` is where the cards are going,
	// which differs from `placed` only while Arrange moves them.
	const rebuildNodes = useCallback(
		(placed: CanvasLayout, plugged: CanvasLayout) => {
			setNodes((previous) => {
				const before = new Map(previous.map((node) => [node.id, node]));
				const built = buildNodes(
					services,
					placed,
					new Set(),
					links,
					plugged,
				).map((node) => {
					const old = before.get(node.id);
					if (!old) return node;
					return {
						...node,
						selected: old.selected ?? false,
						// A group that collapsed or expanded has a new size to measure.
						...(old.measured && sameShape(old, node)
							? { measured: old.measured }
							: {}),
					} as AppNode;
				});
				return reuseNodes(previous, built);
			});
		},
		[services, links],
	);
	const rebuildRef = useRef(rebuildNodes);
	rebuildRef.current = rebuildNodes;
	useEffect(() => {
		if (dragging.current || arranging.current) return;
		rebuildNodes(layout, layout);
	}, [layout, rebuildNodes]);

	// The inspected service is the selected one.
	useEffect(() => {
		if (inspectedServiceId === undefined) return;
		setNodes((current) => {
			// A click on a card has already selected it: return the same array
			// so React skips the render this effect would otherwise cost.
			if (
				current.every(
					(node) => Boolean(node.selected) === (node.id === inspectedServiceId),
				)
			) {
				return current;
			}
			return current.map((node) =>
				Boolean(node.selected) === (node.id === inspectedServiceId)
					? node
					: ({ ...node, selected: node.id === inspectedServiceId } as AppNode),
			);
		});
	}, [inspectedServiceId]);

	// A service that no longer exists cannot be inspected.
	const inspected = useMemo(
		() => services.find((service) => service.id === inspectedServiceId),
		[services, inspectedServiceId],
	);
	useEffect(() => {
		if (inspectedServiceId !== undefined && inspected === undefined) {
			onInspect(undefined);
		}
	}, [inspectedServiceId, inspected, onInspect]);

	// --- search and filter --------------------------------------------------
	const [filter, setFilter] = useState<CanvasFilter>(EMPTY_FILTER);
	const [searchOpen, setSearchOpen] = useState(false);
	const [shortcutsOpen, setShortcutsOpen] = useState(false);
	const openShortcuts = useCallback(() => setShortcutsOpen(true), []);
	const matchIds = useMemo(
		() => matchingIds(services, filter),
		[services, filter],
	);
	// What dims the cards: nothing when nothing matches ("No matches" says so once).
	const dimIds = useMemo(() => dimmingIds(matchIds), [matchIds]);

	// The cables the canvas holds: a rebuild keeps every unchanged one by
	// identity, so a drag or a poll re-renders only the cables that changed.
	const edgesRef = useRef<LinkFlowEdge[]>([]);
	const edges = useMemo(() => {
		const names = new Map(
			services.map((service) => [service.id, service.name]),
		);
		const built = buildEdges(links, layout, dimIds, {
			sizeOf,
			inFlightIds: new Set(
				services.filter(isInFlight).map((service) => service.id),
			),
			internetIds: internetSummary(services)?.serviceIds ?? [],
			// Cables are named for the cards they join, never by id.
			nameOf: (nodeId) => {
				if (nodeId === INTERNET_ID) return "Internet";
				const groupId = groupIdOfNode(nodeId);
				if (groupId !== null) {
					return layout.groups.find((g) => g.id === groupId)?.name ?? "Group";
				}
				return names.get(nodeId) ?? "Service";
			},
		});
		edgesRef.current = reuseEdges(edgesRef.current, built);
		return edgesRef.current;
	}, [links, layout, dimIds, services, sizeOf]);

	const fitMatches = useCallback(() => {
		if (matchIds === null || matchIds.size === 0) return;
		void rf.fitView({
			nodes: [...matchIds].map((id) => ({ id })),
			padding: 0.3,
			maxZoom: 1,
			duration: 300,
		});
	}, [matchIds, rf]);
	const fitMatchesRef = useRef(fitMatches);
	fitMatchesRef.current = fitMatches;

	// Only typing refits; a poll that changes the match set must not.
	const query = filter.query;
	useEffect(() => {
		if (query.trim() === "") return;
		const timer = window.setTimeout(
			() => fitMatchesRef.current(),
			SEARCH_FIT_DELAY_MS,
		);
		return () => window.clearTimeout(timer);
	}, [query]);

	const closeSearch = useCallback(() => {
		setSearchOpen(false);
		setFilter((current) => ({ ...current, query: "" }));
	}, []);

	// --- group and layout actions ------------------------------------------
	const [tick, setTick] = useState(0);
	useEffect(() => {
		const timer = window.setInterval(() => setTick((n) => n + 1), TICK_MS);
		return () => window.clearInterval(timer);
	}, []);

	// The group whose title the context menu asked to edit.
	const [renamingGroupId, setRenamingGroupId] = useState<string | null>(null);
	const finishRenameRequest = useCallback(() => setRenamingGroupId(null), []);

	const ui = useMemo(
		() => ({
			matchIds: dimIds,
			tick,
			toggleGroup: (id: string) =>
				commit(toggleGroup(layoutRef.current, id, sizeOf)),
			renameGroup: (id: string, name: string) =>
				commit(renameGroup(layoutRef.current, id, name)),
			ungroup: (id: string) => commit(removeGroup(layoutRef.current, id)),
			renamingGroupId,
			finishRenameRequest,
		}),
		[dimIds, tick, commit, renamingGroupId, finishRenameRequest, sizeOf],
	);

	const selectedServiceIds = useMemo(
		() =>
			nodes
				.filter(
					(node) =>
						node.selected && node.type === "service" && node.hidden !== true,
				)
				.map((node) => node.id),
		[nodes],
	);
	const selectedService = useMemo(
		() =>
			selectedServiceIds.length === 1
				? services.find((service) => service.id === selectedServiceIds[0])
				: undefined,
		[selectedServiceIds, services],
	);

	// More than one card selected: the sheet showed the first and nothing
	// said so. It closes; the dock gives way to the selection's menu.
	const manySelected = selectedServiceIds.length >= 2;
	useEffect(() => {
		if (manySelected && inspectedServiceId !== undefined) onInspect(undefined);
	}, [manySelected, inspectedServiceId, onInspect]);

	const groupServices = useCallback(
		(ids: readonly string[]) => {
			if (ids.length < 2) return;
			const current = layoutRef.current;
			const groupId = crypto.randomUUID();
			const name = `Group ${current.groups.length + 1}`;
			commit(createGroup(current, ids, groupId, name, sizeOf));
			// The name is the next thing to set: its field opens in the frame's title.
			setRenamingGroupId(groupId);
			toast.success("Group created", `Type a name, or Esc to keep “${name}”.`);
		},
		[commit, sizeOf],
	);
	const groupSelection = useCallback(
		() => groupServices(selectedServiceIds),
		[groupServices, selectedServiceIds],
	);
	// The toolbar's Group button must not change identity with every selection.
	const groupSelectionRef = useRef(groupSelection);
	groupSelectionRef.current = groupSelection;
	const groupSelectionStable = useCallback(
		() => groupSelectionRef.current(),
		[],
	);
	const openSearch = useCallback(() => setSearchOpen(true), []);

	const fit = useCallback(
		() => void rf.fitView({ ...FIT_OPTIONS, duration: reduced ? 0 : 300 }),
		[rf, reduced],
	);

	// While the canvas moves the root carries `data-moving` (`view` for a pan
	// or zoom, `drag` for a card, `select` for a marquee, `arrange` while
	// Arrange glides the cards), which holds every running animation and, for
	// a pan or zoom, lifts the cards onto their own layers (styles.css). It is
	// set on the element directly: React renders nothing for it.
	const setMoving = useCallback(
		(mode: "view" | "drag" | "select" | "arrange" | null) => {
			const root =
				latticeRef.current?.closest<HTMLElement>("[data-canvas-root]");
			if (!root) return;
			if (mode) root.dataset.moving = mode;
			else delete root.dataset.moving;
		},
		[],
	);

	// Arrange: the cards glide to their new places in 300ms on the strong
	// ease-in-out, then the camera fits them. A frame loop and not a CSS
	// transition, so every cable stays plugged into its jacks on the way
	// (React Flow draws a cable from the node's state, not from its style).
	// Reduced motion and the keyboard skip the glide: the cards jump.
	const arrangeFrame = useRef(0);
	useEffect(() => () => window.cancelAnimationFrame(arrangeFrame.current), []);
	const glideTo = useCallback(
		(target: CanvasLayout, options: { animate: boolean; fit: boolean }) => {
			window.cancelAnimationFrame(arrangeFrame.current);
			const start = layoutRef.current;
			const settle = () => {
				arranging.current = false;
				setMoving(null);
				rebuildRef.current(layoutRef.current, layoutRef.current);
				if (!options.fit) return;
				window.requestAnimationFrame(
					() =>
						void rf.fitView({
							...FIT_OPTIONS,
							duration: options.animate ? ARRANGE_MS : 0,
						}),
				);
			};
			arranging.current = options.animate;
			commit(target);
			if (!options.animate) {
				if (options.fit) {
					window.requestAnimationFrame(
						() => void rf.fitView({ ...FIT_OPTIONS, duration: 0 }),
					);
				}
				return;
			}
			setMoving("arrange");
			const began = performance.now();
			const step = (now: number) => {
				const t = Math.min(1, (now - began) / ARRANGE_MS);
				if (t >= 1 || dragging.current) {
					settle();
					return;
				}
				rebuildRef.current(blendLayout(start, target, arrangeEase(t)), target);
				arrangeFrame.current = window.requestAnimationFrame(step);
			};
			arrangeFrame.current = window.requestAnimationFrame(step);
		},
		[commit, rf, setMoving],
	);

	// Moves the cards to `next`; one "Undo arrange" action on the toast puts every card back.
	const rearrange = useCallback(
		(next: CanvasLayout, options: { fit: boolean; title: string }) => {
			const before = layoutRef.current;
			if (samePlaces(before, next)) {
				if (options.fit) fit();
				return;
			}
			const animate = () => !reduced && !lastInputWasKeyboard();
			glideTo(next, { animate: animate(), fit: options.fit });
			toast.add({
				id: ARRANGE_TOAST,
				type: "info",
				title: options.title,
				timeout: 8000,
				secondaryActionProps: {
					children: "Undo arrange",
					onClick: () => {
						toast.close(ARRANGE_TOAST);
						// A service made since has no place in `before`: it gets one.
						const restored = fitGroups(
							mergeLayout(serviceIdsRef.current, before, sizeOf).layout,
							sizeOf,
						);
						glideTo(restored, { animate: animate(), fit: options.fit });
					},
				},
			});
		},
		[glideTo, fit, reduced, sizeOf],
	);

	const runAutoLayout = useCallback(
		() =>
			rearrange(
				autoLayout(layoutRef.current, links, sizeOf, traitsRef.current),
				{ fit: true, title: "Canvas arranged" },
			),
		[rearrange, links, sizeOf],
	);

	// The first services on an empty canvas bring themselves into view.
	const hadNodes = useRef(nodes.length > 0);
	useEffect(() => {
		if (!hadNodes.current && nodes.length > 0)
			window.requestAnimationFrame(fit);
		hadNodes.current = nodes.length > 0;
	}, [nodes.length, fit]);

	// A service that appears after the canvas opened (just created, or made
	// elsewhere) is brought into view when it landed off screen.
	const knownIds = useRef(new Set(serviceIds));
	useEffect(() => {
		const fresh = serviceIds.filter((id) => !knownIds.current.has(id));
		knownIds.current = new Set(serviceIds);
		const target = fresh.at(-1);
		if (target === undefined || !hadNodes.current) return;
		window.requestAnimationFrame(() => {
			const point = layoutRef.current.nodes[target];
			const bounds = document
				.querySelector("[data-canvas-root]")
				?.getBoundingClientRect();
			if (!point || !bounds) return;
			const viewport = rf.getViewport();
			const left = point.x * viewport.zoom + viewport.x;
			const top = point.y * viewport.zoom + viewport.y;
			const right = left + NODE_WIDTH * viewport.zoom;
			const bottom = top + NODE_HEIGHT * viewport.zoom;
			if (
				left < 0 ||
				top < 0 ||
				right > bounds.width - inspectorPx ||
				bottom > bounds.height
			) {
				void rf.setCenter(point.x + NODE_WIDTH / 2, point.y + NODE_HEIGHT / 2, {
					zoom: viewport.zoom,
					duration: 300,
				});
			}
		});
	}, [serviceIds, rf, inspectorPx]);

	// A template stack: the server saved its group and the cards' places, so
	// once every new card is listed the canvas takes that layout (queued
	// writes of its own would overwrite it) and brings the group into view.
	const landing = useRef<Landing | null>(null);
	const [landingTick, setLandingTick] = useState(0);
	useEffect(
		() =>
			registerLanding((next) => {
				landing.current = next;
				setLandingTick((n) => n + 1);
			}),
		[],
	);
	useEffect(() => {
		const pending = landing.current;
		if (!pending || landingTick === 0) return;
		if (!pending.serviceIds.every((id) => serviceIds.includes(id))) return;
		landing.current = null;
		persistence.adopt(pending.document);
		window.requestAnimationFrame(() =>
			fitWhenMeasured(rf, groupNodeId(pending.groupId), reduced ? 0 : 300),
		);
	}, [landingTick, serviceIds, persistence, rf, reduced]);

	// --- inspector ----------------------------------------------------------
	const open = useCallback(
		(id: string, tab?: InspectorTab, section?: string) => {
			onInspect(id, tab, section);
		},
		[onInspect],
	);

	const close = useCallback(() => {
		const id = inspectedServiceId;
		onInspect(undefined);
		if (id) window.requestAnimationFrame(() => focusNode(id));
	}, [inspectedServiceId, onInspect]);

	// Keep the selected card and the dock under it in the part of the canvas
	// the inspector leaves free: opening, resizing or changing the inspected
	// service pans just far enough. The pan is the panel's slide seen from
	// the canvas (same length, same curve) and starts after the panel's first
	// frame, so the two never compete for the first frames.
	const selectedId = selectedService?.id;
	const lastInspectorPx = useRef(0);
	useEffect(() => {
		const previousPx = lastInspectorPx.current;
		lastInspectorPx.current = inspectorPx;
		if (inspectorPx === 0 || selectedId === undefined) return;
		const reveal = () => {
			const internal = rf.getInternalNode(selectedId);
			const bounds = document
				.querySelector("[data-canvas-root]")
				?.getBoundingClientRect();
			if (!internal || !bounds) return;
			const viewport = rf.getViewport();
			const width = (internal.measured.width ?? NODE_WIDTH) * viewport.zoom;
			const height = (internal.measured.height ?? NODE_HEIGHT) * viewport.zoom;
			const left =
				internal.internals.positionAbsolute.x * viewport.zoom + viewport.x;
			const top =
				internal.internals.positionAbsolute.y * viewport.zoom + viewport.y;
			const { dx, dy } = revealShift({
				card: { left, top, right: left + width, bottom: top + height },
				dockBottom: top + height + DOCK_RESERVE,
				viewWidth: bounds.width,
				viewHeight: bounds.height,
				rightInset: inspectorPx,
				margin: 16,
				topMargin: 64,
			});
			if (dx !== 0 || dy !== 0) {
				void rf.setViewport(
					{ x: viewport.x + dx, y: viewport.y + dy, zoom: viewport.zoom },
					{
						duration: panDuration(reduced, lastInputWasKeyboard()),
						ease: PAN_EASE,
						interpolate: "linear",
					},
				);
			}
		};
		const timing = revealTiming(previousPx, inspectorPx);
		if (timing.kind === "after-rest") {
			const timer = window.setTimeout(reveal, timing.ms);
			return () => window.clearTimeout(timer);
		}
		let inner = 0;
		const outer = window.requestAnimationFrame(() => {
			inner = window.requestAnimationFrame(reveal);
		});
		return () => {
			window.cancelAnimationFrame(outer);
			window.cancelAnimationFrame(inner);
		};
	}, [inspectorPx, selectedId, rf, reduced]);

	// --- keyboard -----------------------------------------------------------
	const selectOnly = useCallback((id: string) => {
		setNodes((current) =>
			current.map((node) =>
				node.selected === (node.id === id)
					? node
					: ({ ...node, selected: node.id === id } as AppNode),
			),
		);
	}, []);

	const selectAll = useCallback(() => {
		setNodes((current) =>
			current.map((node) => {
				const wanted = node.type === "service" && node.hidden !== true;
				return node.selected === wanted
					? node
					: ({ ...node, selected: wanted } as AppNode);
			}),
		);
	}, []);

	const moveSelection = useCallback(
		(direction: Direction) => {
			const current = layoutRef.current;
			const collapsedOf = new Map<string, string>();
			const centres: Record<string, { x: number; y: number }> = {};
			for (const group of current.groups) {
				if (!group.collapsed) continue;
				for (const id of group.serviceIds) collapsedOf.set(id, group.id);
				centres[groupNodeId(group.id)] = {
					x: group.x + NODE_WIDTH / 2,
					y: group.y + GROUP_SUMMARY_HEIGHT / 2,
				};
			}
			for (const [id, point] of Object.entries(current.nodes)) {
				if (collapsedOf.has(id)) continue;
				centres[id] = {
					x: point.x + NODE_WIDTH / 2,
					y: point.y + NODE_HEIGHT / 2,
				};
			}
			const from = nodesRef.current.find(
				(node) => node.selected && !node.hidden,
			)?.id;
			let target: string | undefined;
			if (from === undefined || centres[from] === undefined) {
				// Nothing selected yet: start at the top-left card.
				target = Object.entries(centres).sort(
					([, a], [, b]) => a.x + a.y * 0.01 - (b.x + b.y * 0.01),
				)[0]?.[0];
			} else {
				target = neighbourInDirection(from, direction, centres);
			}
			if (target === undefined) return;
			selectOnly(target);
			focusNode(target);
			const centre = centres[target];
			if (centre) {
				const viewport = rf.getViewport();
				const bounds = document
					.querySelector("[data-canvas-root]")
					?.getBoundingClientRect();
				if (bounds) {
					const x = centre.x * viewport.zoom + viewport.x;
					const y = centre.y * viewport.zoom + viewport.y;
					const margin = 60;
					if (
						x < margin ||
						y < margin ||
						x > bounds.width - inspectorPx - margin ||
						y > bounds.height - margin
					) {
						void rf.setCenter(centre.x, centre.y, {
							zoom: viewport.zoom,
							duration: reduced ? 0 : 250,
						});
					}
				}
			}
		},
		[rf, selectOnly, inspectorPx, reduced],
	);

	// Set where the menu is built: opens it for a key press that is not on a card.
	const menuKey = useRef<((anchor?: HTMLElement) => void) | null>(null);

	const keyState = useRef({
		inspectedServiceId,
		searchOpen,
		moveSelection,
		selectAll,
		open,
		close,
		closeSearch,
		onInspect,
		fit,
		rf,
	});
	keyState.current = {
		inspectedServiceId,
		searchOpen,
		moveSelection,
		selectAll,
		open,
		close,
		closeSearch,
		onInspect,
		fit,
		rf,
	};

	useEffect(() => {
		const arrows: Record<string, Direction> = {
			ArrowLeft: "left",
			ArrowRight: "right",
			ArrowUp: "up",
			ArrowDown: "down",
		};
		const onKeyDown = (event: KeyboardEvent) => {
			const state = keyState.current;
			if (event.defaultPrevented || dialogOpen()) return;
			const target = event.target;
			// Arrows, Enter and the shortcuts belong to the canvas only while
			// focus is on it (or nowhere); a focused link, menu or the dock keeps
			// its own keys.
			const onCanvas =
				target === document.body ||
				(target instanceof Element &&
					target.closest("#main-content, .react-flow") !== null &&
					target.closest("[data-canvas-chrome]") === null);
			const key = event.key.toLowerCase();
			if ((event.metaKey || event.ctrlKey) && key === "f") {
				event.preventDefault();
				setSearchOpen(true);
				return;
			}
			if (
				(event.metaKey || event.ctrlKey) &&
				key === "a" &&
				onCanvas &&
				!isEditable(target)
			) {
				event.preventDefault();
				state.selectAll();
				return;
			}
			if (event.metaKey || event.ctrlKey || event.altKey) return;
			if (isEditable(target)) return;
			const inInspector =
				target instanceof Element &&
				target.closest('[data-screen-label="service-inspector"]') !== null;

			// A focused card opens its own menu (the trigger's key handler ran
			// first); with focus anywhere else on the page it is the selection's
			// or the canvas's, and the browser's menu never shows.
			if (
				event.key === "ContextMenu" ||
				(event.shiftKey && event.key === "F10")
			) {
				const onPage =
					target === document.body ||
					(target instanceof Element &&
						target.closest("#main-content") !== null);
				if (onPage && !inInspector) {
					event.preventDefault();
					menuKey.current?.();
				}
				return;
			}

			if (event.key === "Escape") {
				if (state.inspectedServiceId !== undefined) {
					event.preventDefault();
					state.close();
				} else if (state.searchOpen) {
					event.preventDefault();
					state.closeSearch();
				} else {
					setNodes((current) =>
						current.some((node) => node.selected)
							? current.map((node) =>
									node.selected
										? ({ ...node, selected: false } as AppNode)
										: node,
								)
							: current,
					);
				}
				return;
			}
			if (inInspector || !onCanvas) return;

			const direction = arrows[event.key];
			if (direction) {
				event.preventDefault();
				state.moveSelection(direction);
				return;
			}
			if (event.key === "Enter" || event.key === " ") {
				// The focused Internet node has no default action but its menu.
				const internet =
					target instanceof Element
						? target.closest<HTMLElement>(
								`.react-flow__node[data-id="${INTERNET_ID}"]`,
							)
						: null;
				if (internet) {
					event.preventDefault();
					menuKey.current?.(internet);
					return;
				}
				const node = nodesRef.current.find(
					(candidate) => candidate.selected && candidate.type === "service",
				);
				if (node && !(target instanceof HTMLButtonElement)) {
					event.preventDefault();
					state.open(node.id);
				}
				return;
			}
			if (event.key === "Delete" || event.key === "Backspace") {
				const picked = nodesRef.current.filter(
					(candidate) => candidate.selected && candidate.type === "service",
				);
				if (picked.length === 0) return;
				event.preventDefault();
				const only = picked[0];
				if (picked.length === 1 && only) {
					// Deleting a service is typed-name confirmed in its settings.
					state.onInspect(only.id, "settings", "danger");
				} else {
					toast.add({
						type: "info",
						title: "Delete one service at a time",
						description: "Select a single service, then press Delete.",
					});
				}
				return;
			}
			switch (event.key) {
				case "+":
				case "=":
					void state.rf.zoomIn({ duration: 200 });
					break;
				case "-":
				case "_":
					void state.rf.zoomOut({ duration: 200 });
					break;
				case "0":
					void state.rf.zoomTo(1, { duration: 200 });
					break;
				case "1":
					state.fit();
					break;
				case "?":
					setShortcutsOpen(true);
					break;
			}
		};
		document.addEventListener("keydown", onKeyDown);
		return () => document.removeEventListener("keydown", onKeyDown);
	}, []);

	// --- links --------------------------------------------------------------
	const knownServices = useMemo(() => new Set(serviceIds), [serviceIds]);
	// Two different services are always a valid pair, even when a cable already
	// joins them: the popover then offers more references, or says it is done.
	const isValidConnection = useCallback(
		(connection: { source: string; target: string }) =>
			connection.source !== connection.target &&
			knownServices.has(connection.source) &&
			knownServices.has(connection.target),
		[knownServices],
	);
	// A cable let go on a jack or on a card (not only on its jack) asks the
	// connect popover, at the drop point, what to stage. Nothing is created
	// until the reader confirms; the cable is drawn from the staged references.
	const onConnectEnd = useCallback(
		(event: MouseEvent | TouchEvent, state: FinalConnectionState) => {
			if (!state.fromNode || !state.fromHandle) return;
			const pointer =
				"changedTouches" in event ? event.changedTouches[0] : event;
			if (!pointer) return;
			const otherId =
				state.toNode?.id ??
				document
					.elementFromPoint(pointer.clientX, pointer.clientY)
					?.closest(".react-flow__node")
					?.getAttribute("data-id");
			if (!otherId) return;
			const pair =
				state.fromHandle.type === "source"
					? { source: state.fromNode.id, target: otherId }
					: { source: otherId, target: state.fromNode.id };
			if (!isValidConnection(pair)) return;
			linkReview.request(pair.source, pair.target, {
				x: pointer.clientX,
				y: pointer.clientY,
			});
		},
		[isValidConnection, linkReview],
	);

	// --- dock, menu and dialogs --------------------------------------------
	const [scaleFor, setScaleFor] = useState<string | null>(null);
	const [combineFor, setCombineFor] = useState<readonly Service[] | null>(null);
	const closeCombine = useCallback(() => setCombineFor(null), []);
	const [minimapOpen, setMinimapOpen] = useState(false);
	const toggleMinimap = useCallback(
		() => setMinimapOpen((current) => !current),
		[],
	);
	const [menu, setMenu] = useState<MenuTarget>({ kind: "pane", at: null });
	const lastPointer = useRef<Point | null>(null);
	const menuOpener = useRef<ContextMenuOpener | null>(null);
	const menuActions = useCanvasMenuActions(isProduction);

	const openInspector = useCallback((id: string) => open(id), [open]);
	const openDangerZone = useCallback(
		(id: string) => onInspect(id, "settings", "danger"),
		[onInspect],
	);

	/** The data a menu is drawn from, looked up when it opens. */
	const buildMenuTarget = useCallback(
		(ref: MenuTargetRef, pointer: Point | null): MenuTarget => {
			const byId = (id: string) => services.find((s) => s.id === id);
			const groups = layoutRef.current.groups;
			const nameOf = (nodeId: string): string => {
				if (nodeId === INTERNET_ID) return "Internet";
				const groupId = groupIdOfNode(nodeId);
				if (groupId !== null) {
					return groups.find((g) => g.id === groupId)?.name ?? "Group";
				}
				return byId(nodeId)?.name ?? "Service";
			};
			const hostnamesOf = (nodeId: string): string[] => {
				const groupId = groupIdOfNode(nodeId);
				const ids =
					groupId === null
						? [nodeId]
						: (groups.find((g) => g.id === groupId)?.serviceIds ?? []);
				return ids.flatMap(
					(id) => byId(id)?.domains.map((domain) => domain.hostname) ?? [],
				);
			};
			const flow = pointer ? rf.screenToFlowPosition(pointer) : null;
			const pane: MenuTarget = {
				kind: "pane",
				at: flow
					? {
							x: Math.round(flow.x - NODE_WIDTH / 2),
							y: Math.round(flow.y - 40),
						}
					: null,
			};
			switch (ref.kind) {
				case "pane":
					return pane;
				case "service": {
					const service = byId(ref.serviceId);
					if (!service) return pane;
					// A cable is not a tab stop: the card's menu is how the keyboard reaches it.
					const links = edges.flatMap((edge) => {
						if (!edge.data?.link) return [];
						const otherId =
							edge.source === service.id
								? edge.target
								: edge.target === service.id
									? edge.source
									: null;
						return otherId === null
							? []
							: [{ edgeId: edge.id, otherName: nameOf(otherId) }];
					});
					return { kind: "service", service, links };
				}
				case "selection": {
					const picked = ref.serviceIds.flatMap((id) => byId(id) ?? []);
					return picked.length >= 2
						? { kind: "selection", services: picked }
						: pane;
				}
				case "group": {
					const group = groups.find((g) => g.id === ref.groupId);
					if (!group) return pane;
					return {
						kind: "group",
						group,
						memberCount: group.serviceIds.filter((id) => knownServices.has(id))
							.length,
					};
				}
				case "internet":
					return {
						kind: "internet",
						model: {
							domains: services.flatMap((service) =>
								service.domains.map((domain) => ({
									hostname: domain.hostname,
									serviceId: service.id,
									serviceName: service.name,
								})),
							),
							candidates: services
								.filter(canTakeDomain)
								.map((service) => ({ id: service.id, name: service.name })),
						},
					};
				case "edge": {
					const edge = edges.find((candidate) => candidate.id === ref.edgeId);
					const data = edge?.data;
					if (!edge || !data) return pane;
					const end = (nodeId: string) => ({
						nodeId,
						name: nameOf(nodeId),
						isService: knownServices.has(nodeId),
					});
					return {
						kind: "edge",
						model: {
							edgeId: edge.id,
							kind: data.kind,
							from: end(edge.source),
							to: end(edge.target),
							link: data.link
								? { id: data.link.id, source: data.link.source }
								: null,
							references: data.references,
							hostnames:
								data.kind === "internet" ? hostnamesOf(edge.target) : [],
						},
					};
				}
			}
		},
		[services, edges, knownServices, rf],
	);

	const onMenuTrigger = useCallback(
		(target: HTMLElement | null) => {
			const picked = nodesRef.current
				.filter(
					(node) =>
						node.selected && node.type === "service" && node.hidden !== true,
				)
				.map((node) => node.id);
			const ref = resolveMenuTarget(readProbe(target), {
				serviceIds: knownServices,
				groupIds: new Set(layoutRef.current.groups.map((g) => g.id)),
				edgeIds: new Set(edges.map((edge) => edge.id)),
				selectedServiceIds: picked,
			});
			if (ref.kind === "service") selectOnly(ref.serviceId);
			else if (ref.kind === "group") selectOnly(groupNodeId(ref.groupId));
			setMenu(buildMenuTarget(ref, lastPointer.current));
		},
		[knownServices, edges, selectOnly, buildMenuTarget],
	);

	const clearSelection = useCallback(
		() =>
			setNodes((current) =>
				current.map((node) =>
					node.selected ? ({ ...node, selected: false } as AppNode) : node,
				),
			),
		[],
	);

	const runMenuAction = useCallback(
		(action: MenuAction) => {
			const linkOf = (edgeId: string) =>
				edges.find((edge) => edge.id === edgeId)?.data?.link ?? null;
			switch (action.type) {
				case "service-open":
					openInspector(action.serviceId);
					break;
				case "service-scale":
					setScaleFor(action.serviceId);
					break;
				case "service-deploy":
				case "service-action":
					// Run by the card's own menu, which holds its actions.
					break;
				case "create":
					openCreateEntry(
						action.source,
						action.at ? { position: action.at } : {},
					);
					break;
				case "arrange":
					runAutoLayout();
					break;
				case "fit":
					fit();
					break;
				case "search":
					setSearchOpen(true);
					break;
				case "shortcuts":
					setShortcutsOpen(true);
					break;
				case "open-domain":
					window.open(
						`https://${action.hostname}`,
						"_blank",
						"noopener,noreferrer",
					);
					break;
				case "add-domain":
					onInspect(action.serviceId, "settings", "networking");
					toast.add({
						type: "info",
						title: "Add the domain under Networking",
						description: "It is in the service's Settings.",
					});
					break;
				case "open-service":
					open(action.serviceId, action.section, action.settingsSection);
					break;
				case "review-references": {
					const link = linkOf(action.edgeId);
					if (link) linkReview.openReferences(link);
					break;
				}
				case "remove-link": {
					const link = linkOf(action.edgeId);
					if (link) void menuActions.removeLink(link);
					break;
				}
				case "rename-group":
					setRenamingGroupId(action.groupId);
					break;
				case "toggle-group":
					commit(toggleGroup(layoutRef.current, action.groupId, sizeOf));
					break;
				case "zoom-group":
					void rf.fitView({
						nodes: [{ id: groupNodeId(action.groupId) }],
						padding: 0.2,
						maxZoom: 1,
						duration: reduced ? 0 : 300,
					});
					break;
				case "ungroup":
					commit(removeGroup(layoutRef.current, action.groupId));
					break;
				case "delete-stack":
					// Run by the group's own menu, which finds the stack.
					break;
				case "deploy-selected":
					void menuActions.deploySelected(action.serviceIds);
					break;
				case "restart-selected":
					void menuActions.restartSelected(action.serviceIds);
					break;
				case "group-selected":
					groupServices(action.serviceIds);
					break;
				case "arrange-selected":
					rearrange(
						arrangeSelection(
							layoutRef.current,
							links,
							action.serviceIds,
							sizeOf,
							traitsRef.current,
						),
						{ fit: false, title: "Services arranged" },
					);
					break;
				case "combine-selected":
					setCombineFor(
						action.serviceIds.flatMap(
							(id) => services.find((service) => service.id === id) ?? [],
						),
					);
					break;
				case "clear-selection":
					clearSelection();
					break;
			}
		},
		[
			edges,
			links,
			rf,
			reduced,
			open,
			onInspect,
			openInspector,
			linkReview,
			menuActions,
			commit,
			rearrange,
			runAutoLayout,
			fit,
			groupServices,
			clearSelection,
			sizeOf,
			services,
		],
	);

	// Shift+F10 / the ContextMenu key with focus on the page (not on a card,
	// whose own key handler opens the menu): the menu of the selection, or of
	// the canvas when nothing is selected, beside the selected card.
	menuKey.current = (anchor) => {
		const opener = menuOpener.current;
		if (!opener) return;
		const picked = nodesRef.current.filter(
			(node) => node.selected && node.hidden !== true,
		);
		const anchorId = (
			picked.find((node) => node.type === "service") ?? picked[0]
		)?.id;
		const element =
			anchor ??
			(anchorId === undefined
				? null
				: document.querySelector<HTMLElement>(
						`.react-flow__node[data-id="${CSS.escape(anchorId)}"]`,
					));
		const rect =
			element?.getBoundingClientRect() ??
			document.querySelector("[data-canvas-root]")?.getBoundingClientRect();
		if (!rect) return;
		const point = element
			? {
					x: rect.left + Math.min(24, rect.width / 2),
					y: rect.top + rect.height / 2,
				}
			: { x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 };
		lastPointer.current = point;
		onMenuTrigger(element);
		opener(point, "keyboard");
	};

	const empty = services.length === 0;
	// The dock's handlers change only when the selection does, so the dock (a
	// memo) is not re-rendered by every render of the canvas.
	const openSelected = useCallback(() => {
		if (selectedId !== undefined) openInspector(selectedId);
	}, [selectedId, openInspector]);
	const scaleSelected = useCallback(() => {
		if (selectedId !== undefined) setScaleFor(selectedId);
	}, [selectedId]);
	const deleteSelected = useCallback(() => {
		if (selectedId !== undefined) openDangerZone(selectedId);
	}, [selectedId, openDangerZone]);
	const [startViewport] = useState(() =>
		initialViewport(saved.layout.viewport, window.innerWidth),
	);
	const canvasWidth = useStore((state) => state.width);
	const mode = chromeModeFor(canvasWidth, inspectorPx);
	const chromeRight = inspectorPx > 0 ? inspectorPx + 12 : 12;
	const search = searchOpen ? (
		<CanvasSearch
			query={filter.query}
			onQueryChange={(next) =>
				setFilter((current) => ({ ...current, query: next }))
			}
			matchCount={matchIds?.size ?? services.length}
			total={services.length}
			inline={mode.searchInline}
			rightInset={inspectorPx}
			onSubmit={fitMatches}
			onClose={closeSearch}
		/>
	) : null;

	// ReactFlow hands these to every card: a closure made fresh on each render
	// would re-render all of them on every frame of a drag. They read what
	// changes through `latest`, so their identity never does.
	const latest = useRef({ open, close, inspectedServiceId });
	latest.current = { open, close, inspectedServiceId };
	const onNodeClick = useCallback<NodeMouseHandler<AppNode>>((event, node) => {
		if (node.type !== "service") return;
		if (event.shiftKey || event.metaKey || event.ctrlKey) return;
		latest.current.open(node.id);
	}, []);
	const onPaneClick = useCallback(() => {
		const { inspectedServiceId: id, close: closeInspector } = latest.current;
		if (id !== undefined) closeInspector();
	}, []);
	const onMoveStart = useCallback(() => setMoving("view"), [setMoving]);
	const onSelectionStart = useCallback(() => setMoving("select"), [setMoving]);
	const onSelectionEnd = useCallback(() => setMoving(null), [setMoving]);
	const onNodeDragStart = useCallback(() => {
		dragging.current = true;
		setMoving("drag");
	}, [setMoving]);
	const onNodeDragStop = useCallback<OnNodeDrag<AppNode>>(
		(_event, _node, dragged) => {
			dragging.current = false;
			setMoving(null);
			const byId = new Map(dragged.map((node) => [node.id, node]));
			const merged = nodesRef.current.map(
				(node) => (byId.get(node.id) as AppNode | undefined) ?? node,
			);
			commit(layoutFromNodes(layoutRef.current, merged, sizeOf));
		},
		[commit, sizeOf, setMoving],
	);
	const onMoveEnd = useCallback<OnMoveEnd>(
		(event, viewport) => {
			setMoving(null);
			// Programmatic moves (fit, centre) carry no event: not the user's view.
			if (event === null) return;
			// A phone always opens fit-all, so a pan there is not a view to keep
			// (and must not replace the one a desktop saved).
			if (isFullScreen(window.innerWidth)) return;
			layoutRef.current = { ...layoutRef.current, viewport };
			persistence.schedule(layoutRef.current);
		},
		[persistence, setMoving],
	);

	return (
		<CanvasUiContext.Provider value={ui}>
			<ContextMenu>
				<ContextMenuTrigger
					onTrigger={onMenuTrigger}
					ignore={ignoreMenu}
					longPress={false}
				>
					<div
						className="absolute inset-0 isolate overflow-hidden"
						data-canvas-root
						onContextMenuCapture={(event) => {
							lastPointer.current = { x: event.clientX, y: event.clientY };
						}}
					>
						<CanvasFloor production={isProduction} latticeRef={latticeRef} />
						{/*
						 * Toolbar chrome comes first in the DOM, so Tab reaches it before
						 * the cables and cards. It spans the part of the canvas the
						 * inspector leaves free: Create sits at its right edge and wraps
						 * under the toolbar when the room is short, so the two never overlap.
						 */}
						<div
							data-canvas-chrome
							className="pointer-events-none absolute top-3 left-3 z-10 flex flex-col items-start gap-2"
							style={{ right: chromeRight }}
						>
							<div className="flex w-full flex-wrap items-start justify-between gap-2">
								<CanvasToolbar
									empty={empty}
									canGroup={selectedServiceIds.length >= 2}
									compact={mode.compact}
									onSearch={openSearch}
									onArrange={runAutoLayout}
									onGroup={groupSelectionStable}
								/>
								{toolbarEnd && !empty ? (
									<div className="pointer-events-auto ml-auto">
										{toolbarEnd}
									</div>
								) : null}
							</div>
							{mode.searchInline ? search : null}
							<FilterChips
								services={services}
								servers={servers}
								filter={filter}
								onChange={setFilter}
							/>
						</div>

						<ReactFlow<AppNode, LinkFlowEdge>
							className="absolute inset-0 z-[1] bg-transparent!"
							colorMode="dark"
							nodes={nodes}
							edges={edges}
							nodeTypes={nodeTypes}
							edgeTypes={edgeTypes}
							onNodesChange={onNodesChange}
							// A click with a few px of hand jitter is a click, not a drag:
							// below 6px nothing moves, the dock stays and the card never
							// shows the grab state.
							nodeDragThreshold={6}
							nodeClickDistance={6}
							onNodeClick={onNodeClick}
							onPaneClick={onPaneClick}
							onNodeDragStart={onNodeDragStart}
							onNodeDragStop={onNodeDragStop}
							onMoveStart={onMoveStart}
							onMoveEnd={onMoveEnd}
							onSelectionStart={onSelectionStart}
							onSelectionEnd={onSelectionEnd}
							onConnectEnd={onConnectEnd}
							isValidConnection={isValidConnection}
							connectionLineComponent={ConnectionCable}
							connectionMode={ConnectionMode.Loose}
							deleteKeyCode={null}
							multiSelectionKeyCode={MULTI_SELECT_KEYS}
							disableKeyboardA11y
							nodesFocusable
							minZoom={0.2}
							maxZoom={2}
							onlyRenderVisibleElements={nodes.length >= VIRTUALIZE_FROM}
							{...(startViewport
								? { defaultViewport: startViewport }
								: { fitView: true, fitViewOptions: FIT_OPTIONS })}
							proOptions={PRO_OPTIONS}
						>
							{minimapOpen ? (
								<MiniMap
									pannable
									zoomable
									ariaLabel="Canvas overview"
									className="surface-float! overflow-hidden rounded-card!"
									style={{ width: 180, height: 120, right: inspectorPx }}
									bgColor="transparent"
									maskColor="rgb(11 11 9 / 0.6)"
									nodeColor={(node) =>
										node.type === "cluster"
											? "var(--group-frame)"
											: "var(--border-strong)"
									}
									nodeBorderRadius={4}
								/>
							) : null}
						</ReactFlow>

						{empty ? <CanvasEmptyState error={error} /> : null}

						{selectedService ? (
							<SelectionDock
								service={selectedService}
								isProduction={isProduction}
								rightInset={inspectorPx}
								compact={mode.compact}
								onOpen={openSelected}
								onScale={scaleSelected}
								onDelete={deleteSelected}
							/>
						) : null}

						{mode.searchInline ? null : search}

						<div
							data-canvas-chrome
							data-canvas-zoom
							className="absolute bottom-3 left-3 z-10"
						>
							<CanvasZoomToolbar
								mapOpen={minimapOpen}
								onFit={fit}
								onToggleMap={toggleMinimap}
								onShortcuts={openShortcuts}
							/>
						</div>
						{empty ? null : (
							<PrivateNetworkCaption
								environmentName={environmentName}
								rightInset={inspectorPx}
							/>
						)}
					</div>
				</ContextMenuTrigger>
				<CanvasMenu
					target={menu}
					isProduction={isProduction}
					environmentId={environmentId}
					empty={empty}
					onAction={runMenuAction}
				/>
				<MenuOpenerBridge openerRef={menuOpener} />
			</ContextMenu>

			<ShortcutsDialog open={shortcutsOpen} onOpenChange={setShortcutsOpen} />
			{scaleFor ? (
				<ScaleDialog
					serviceId={scaleFor}
					open
					onOpenChange={(next) => {
						if (!next) setScaleFor(null);
					}}
				/>
			) : null}
			<CombineDialog
				services={combineFor}
				environmentId={environmentId}
				isProduction={isProduction}
				onClose={closeCombine}
			/>
			<Inspector
				service={inspected}
				tab={tab}
				section={section}
				onTabChange={onTabChange}
				onSectionChange={onSectionChange}
				onClose={close}
			/>
		</CanvasUiContext.Provider>
	);
}
