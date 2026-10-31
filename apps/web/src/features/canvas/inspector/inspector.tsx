import { useReducedMotion } from "motion/react";
import {
	memo,
	Suspense,
	useEffect,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import type { Service } from "#/api/models";
import { Badge } from "#/components/ui/badge";
import { Sheet, SheetContent } from "#/components/ui/sheet";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "#/components/ui/tabs";
import { whenIdle } from "#/lib/when-idle";
import {
	INSPECTOR_DOCKED_CSS,
	isFullScreen,
	loadSavedWidth,
	publishInspectorWidth,
	resolveInspectorWidth,
	saveWidth,
	type WidthClass,
	widthClassOf,
} from "./geometry";
import { InspectorHeader } from "./header";
import { InspectorContext } from "./inspector-context";
import { usePanelSlide } from "./panel-motion";
import { bodyFor, preloadRemainingTabs, TabFallback } from "./tab-bodies";
import {
	INSPECTOR_TABS,
	type InspectorTab,
	resolveTab,
	TAB_LABEL,
	tabsFor,
} from "./tab-ids";
import { fadeMask, scrollTarget, useEdgeFade } from "./tab-strip";
import { useTabChords } from "./use-tab-chords";

/** `data-screen-label` the panel is found by (initial focus). */
export const INSPECTOR_SCREEN_LABEL = "service-inspector";

/** What the tab strip counts: staged changes under the tab that shows them. */
function stagedCount(service: Service, tab: InspectorTab): number {
	if (tab === "variables") return service.pendingChangeCounts.variables;
	if (tab === "settings") return service.pendingChangeCounts.settings;
	return 0;
}

export interface InspectorProps {
	/** The inspected service; undefined closes the panel (it slides out showing the last one). */
	service: Service | undefined;
	tab: InspectorTab;
	/** Settings: `?section=`, the category to show. */
	section: string | undefined;
	onTabChange(tab: InspectorTab): void;
	onSectionChange(section: string): void;
	onClose(): void;
}

function storage(): Storage | undefined {
	return typeof window === "undefined" ? undefined : window.localStorage;
}

function useViewportWidth(): number {
	const [width, setWidth] = useState(() => window.innerWidth);
	useEffect(() => {
		const onResize = () => setWidth(window.innerWidth);
		window.addEventListener("resize", onResize);
		return () => window.removeEventListener("resize", onResize);
	}, []);
	return width;
}

/**
 * Where the canvas starts, in px from the viewport's left edge: the app
 * shell's sidebar sits before it, and the wide panel counts its free canvas
 * from there. 0 until the canvas has mounted.
 */
function useCanvasLeft(): number {
	const [left, setLeft] = useState(0);
	useLayoutEffect(() => {
		const root = document.querySelector("[data-canvas-root]");
		if (!root) return;
		const measure = () =>
			setLeft(Math.round(root.getBoundingClientRect().left));
		measure();
		// The root resizes with the window and when the sidebar folds.
		const observer = new ResizeObserver(measure);
		observer.observe(root);
		return () => observer.disconnect();
	}, []);
	return left;
}

/**
 * The service inspector: a panel docked to the canvas's right edge, below the
 * top bar. It is docked-width for Overview, Deployments and Variables and
 * wider for Metrics, Analytics, Logs and Settings; the width follows the tab
 * at once (a width is a layout property, so it is never animated). Dragging
 * its left edge resizes it; the width is remembered for the tab's class. `g`
 * then `o`, `d`, `v`, `m`, `a`, `l` or `s` jumps between tabs. It keeps the
 * canvas usable behind it: only Esc and the close button dismiss it.
 *
 * It slides in from the right (from the bottom on a phone) and out again with
 * `usePanelSlide`; selecting another service while it is open only swaps the
 * content.
 */
function InspectorView({
	service,
	tab,
	section,
	onTabChange,
	onSectionChange,
	onClose,
}: InspectorProps) {
	const reduced = useReducedMotion() ?? false;
	const open = service !== undefined;

	// While it slides out the panel keeps showing what it showed.
	const [held, setHeld] = useState<{
		service: Service;
		tab: InspectorTab;
		section: string | undefined;
	}>();
	const resolved = service ? resolveTab(tab, service) : tab;
	if (
		service &&
		(held === undefined ||
			held.service !== service ||
			held.tab !== resolved ||
			held.section !== section)
	) {
		setHeld({ service, tab: resolved, section });
	}
	const shown = service ? { service, tab: resolved, section } : held;
	const tabs = shown ? tabsFor(shown.service) : INSPECTOR_TABS;

	const viewport = useViewportWidth();
	const fullScreen = isFullScreen(viewport);
	const [saved, setSaved] = useState<Partial<Record<WidthClass, number>>>(
		() => {
			const next: Partial<Record<WidthClass, number>> = {};
			for (const widthClass of ["docked", "wide"] as const) {
				const px = loadSavedWidth(storage(), widthClass);
				if (px !== undefined) next[widthClass] = px;
			}
			return next;
		},
	);

	const widthClass = widthClassOf(shown?.tab ?? tab);
	const savedPx = saved[widthClass];
	const canvasLeft = useCanvasLeft();
	const widthPx = resolveInspectorWidth(
		widthClass,
		savedPx,
		viewport,
		canvasLeft,
	);
	const widthProp =
		savedPx !== undefined
			? savedPx
			: widthClass === "wide"
				? widthPx
				: INSPECTOR_DOCKED_CSS;

	// A full-screen sheet leaves no canvas to keep in view, so it reserves none.
	const reservedPx = open && !fullScreen ? widthPx : 0;
	useEffect(() => {
		publishInspectorWidth(reservedPx);
	}, [reservedPx]);
	useEffect(() => () => publishInspectorWidth(0), []);

	// The panel's own slide: `transform` and `opacity` only, started with the
	// panel's first frame. A swap to another service is not an open, so it
	// has none; the content just changes.
	const slideRef = usePanelSlide(open, fullScreen ? "y" : "x", reduced);

	useTabChords(open, onTabChange);

	// What the tab bodies reach for: stable, so a re-render of the panel does not
	// re-render every body that reads it.
	const actions = useMemo(
		() => ({ close: onClose, openTab: onTabChange }),
		[onClose, onTabChange],
	);

	// Once the panel is up and the browser is idle, fetch the chunks of the
	// other tabs, so switching tabs never waits on the network.
	const kind = shown?.service.kind;
	useEffect(() => {
		if (!open || kind === undefined) return;
		return whenIdle(() => preloadRemainingTabs({ kind }));
	}, [open, kind]);

	// The strip scrolls at 420px and on a phone; keep the active tab in
	// sight, clear of the fade, and fade the ends that hide tabs.
	const listRef = useRef<HTMLDivElement>(null);
	const fade = useEdgeFade(listRef);
	const shownTab = shown?.tab;
	// biome-ignore lint/correctness/useExhaustiveDependencies: the strip order only changes with the tab list
	useEffect(() => {
		const list = listRef.current;
		if (shownTab === undefined || !list) return;
		const tab =
			list.querySelectorAll<HTMLElement>('[role="tab"]')[
				tabs.indexOf(shownTab)
			];
		if (!tab) return;
		const strip = list.getBoundingClientRect();
		const box = tab.getBoundingClientRect();
		const next = scrollTarget(
			list.scrollLeft,
			{ left: strip.left, width: strip.width },
			{ left: box.left, width: box.width },
		);
		if (next !== null) list.scrollLeft = next;
	}, [shownTab, fullScreen]);

	if (!shown) return null;
	const Body = bodyFor(shown.service, shown.tab);
	const stripMask = fadeMask(fade);

	return (
		<Sheet
			open={open}
			modal={false}
			disablePointerDismissal
			onOpenChange={(next) => {
				if (!next) onClose();
			}}
		>
			<SheetContent
				variant="docked"
				motion="external"
				ref={slideRef}
				showCloseButton={false}
				resizable={!fullScreen}
				width={widthProp}
				onWidthChange={(px) => {
					setSaved((current) => ({ ...current, [widthClass]: px }));
					saveWidth(storage(), widthClass, px);
				}}
				data-screen-label={INSPECTOR_SCREEN_LABEL}
				aria-label={`${shown.service.name} inspector`}
				initialFocus={() =>
					document.querySelector<HTMLElement>(
						`[data-screen-label="${INSPECTOR_SCREEN_LABEL}"]`,
					)
				}
				finalFocus={false}
				className="top-[52px] border-line border-l max-[759px]:inset-0 max-[759px]:w-auto! max-[759px]:max-w-none max-[759px]:border-l-0"
			>
				<InspectorHeader
					service={shown.service}
					onClose={onClose}
					onTab={onTabChange}
				/>
				<Tabs
					value={shown.tab}
					onValueChange={(next) => onTabChange(next as InspectorTab)}
					className="min-h-0 flex-1"
				>
					<TabsList
						ref={listRef}
						variant="sheet"
						aria-label="Service sections"
						className="shrink-0 gap-3 overflow-x-auto border-line border-b px-3 no-scrollbar"
						style={
							stripMask
								? { maskImage: stripMask, WebkitMaskImage: stripMask }
								: undefined
						}
					>
						{tabs.map((id) => {
							const staged = stagedCount(shown.service, id);
							return (
								<TabsTrigger
									key={id}
									value={id}
									className="h-10 shrink-0 font-medium text-[13px]"
								>
									{TAB_LABEL[id]}
									{staged > 0 ? (
										<Badge
											variant="accent"
											shape="tab-count"
											aria-label={`${staged} staged`}
										>
											{staged}
										</Badge>
									) : null}
								</TabsTrigger>
							);
						})}
					</TabsList>
					<TabsContent
						key={`${shown.service.id}:${shown.tab}`}
						value={shown.tab}
						className="min-h-0 flex-1 overflow-y-auto overscroll-contain"
					>
						<InspectorContext.Provider value={actions}>
							<Suspense fallback={<TabFallback />}>
								<Body
									service={shown.service}
									section={shown.section}
									onSectionChange={onSectionChange}
								/>
							</Suspense>
						</InspectorContext.Provider>
					</TabsContent>
				</Tabs>
			</SheetContent>
		</Sheet>
	);
}

/**
 * Memoised: the canvas re-renders for the camera, the selection and every poll,
 * and none of that changes what the inspector shows.
 */
export const Inspector = memo(InspectorView);
