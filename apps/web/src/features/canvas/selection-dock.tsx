import { useStoreApi } from "@xyflow/react";
import {
	EllipsisIcon,
	PanelRightOpenIcon,
	RocketIcon,
	ScalingIcon,
} from "lucide-react";
import { memo, useLayoutEffect, useRef } from "react";
import type { Service } from "#/api/models";
import { Button, buttonVariants } from "#/components/ui/button";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "#/components/ui/dropdown-menu";
import { Kbd } from "#/components/ui/kbd";
import {
	Tooltip,
	TooltipContent,
	TooltipTrigger,
} from "#/components/ui/tooltip";
import { deployLabel } from "./lib/deploy-label";
import { dockPosition, type Rect } from "./lib/dock";
import { NODE_HEIGHT, NODE_WIDTH } from "./lib/layout";
import { type ServiceAction, useServiceActions } from "./use-service-actions";

// Wide enough for "Redeploy" beside Open ↵, Scale and More.
const DOCK_WIDTH = 300;
/** Icons only: four 28px keys, three gaps and the dock's own padding. */
const DOCK_WIDTH_COMPACT = 168;
const DOCK_HEIGHT = 40;
const DOCK_GAP = 14;
const EDGE_MARGIN = 12;

export interface SelectionDockProps {
	service: Service;
	isProduction: boolean;
	/** Pixels at the canvas's right edge that the inspector covers. */
	rightInset: number;
	/** Little free canvas (see `chromeMode`): icons only. */
	compact: boolean;
	onOpen(): void;
	onScale(): void;
	onDelete(): void;
}

/**
 * Keeps the dock under the card it belongs to. The position is written
 * straight onto the element (a `transform`) from the flow store's own
 * subscription: a pan, a zoom or a drag moves the dock without one React
 * render, so the buttons, the menu and the tooltip are not re-rendered per
 * frame. The dock hides while its card is being dragged.
 */
function useDockPlacement(
	serviceId: string,
	rightInset: number,
	dockWidth: number,
) {
	const store = useStoreApi();
	const ref = useRef<HTMLDivElement>(null);
	useLayoutEffect(() => {
		const el = ref.current;
		if (!el) return;
		let last = "";
		// The zoom stack sits in the canvas's lower-left corner; the dock keeps
		// off it. Its box is read where it is placed (offsets are in the canvas's
		// own coordinates, like the dock's) so a taller or wider stack just works.
		const zoomStack =
			el.parentElement?.querySelector<HTMLElement>("[data-canvas-zoom]");
		const keepOut = (): { keepOut?: Rect } => {
			if (!zoomStack) return {};
			const left = zoomStack.offsetLeft;
			const top = zoomStack.offsetTop;
			return {
				keepOut: {
					left,
					top,
					right: left + zoomStack.offsetWidth,
					bottom: top + zoomStack.offsetHeight,
				},
			};
		};
		const place = () => {
			const { nodeLookup, transform, width, height } = store.getState();
			const node = nodeLookup.get(serviceId);
			if (!node || node.dragging) {
				if (last !== "hidden") el.style.visibility = "hidden";
				last = "hidden";
				return;
			}
			const [tx, ty, zoom] = transform;
			const at = node.internals.positionAbsolute;
			const cardWidth = node.measured.width ?? NODE_WIDTH;
			const cardHeight = node.measured.height ?? NODE_HEIGHT;
			const { left, top } = dockPosition({
				cardCentreX: (at.x + cardWidth / 2) * zoom + tx,
				cardBottom: (at.y + cardHeight) * zoom + ty,
				dockWidth,
				dockHeight: DOCK_HEIGHT,
				gap: DOCK_GAP,
				margin: EDGE_MARGIN,
				viewportWidth: width - rightInset,
				viewportHeight: height,
				...keepOut(),
			});
			const key = `${left},${top}`;
			if (key === last) return;
			if (last === "hidden" || last === "") el.style.visibility = "";
			last = key;
			el.style.transform = `translate3d(${left}px, ${top}px, 0)`;
		};
		place();
		return store.subscribe(place);
	}, [store, serviceId, rightInset, dockWidth]);
	return ref;
}

/**
 * The floating dock under the selected card: Open · Deploy · Scale · More.
 * It lives in screen space (it does not shrink with the zoom) and keeps
 * itself inside the part of the canvas the inspector leaves free.
 */
export const SelectionDock = memo(function SelectionDock({
	service,
	isProduction,
	rightInset,
	compact,
	onOpen,
	onScale,
	onDelete,
}: SelectionDockProps) {
	const actions = useServiceActions(service, { isProduction, onDelete });
	const dockWidth = compact ? DOCK_WIDTH_COMPACT : DOCK_WIDTH;
	const dockRef = useDockPlacement(service.id, rightInset, dockWidth);
	const deploy = deployLabel(service);
	const size = compact ? "icon-sm" : "sm";
	// Icon keys grow to 36px under a finger.
	const keyClass = compact ? "pointer-coarse:size-9" : undefined;

	const eligible = service.scaling.eligible;
	const scaleButton = (
		<Button
			size={size}
			className={keyClass}
			variant="secondary"
			aria-disabled={!eligible || undefined}
			aria-label={compact ? "Scale" : undefined}
			title={compact ? "Scale" : undefined}
			onClick={eligible ? onScale : undefined}
		>
			<ScalingIcon aria-hidden />
			{compact ? null : "Scale"}
		</Button>
	);

	return (
		<div
			ref={dockRef}
			role="toolbar"
			aria-label={`Actions for ${service.name}`}
			data-canvas-chrome
			data-dock-for={service.id}
			className="surface-float absolute z-20 flex items-center gap-1 rounded-[11px] p-1"
			style={{
				left: 0,
				top: 0,
				width: dockWidth,
				height: DOCK_HEIGHT,
				willChange: "transform",
			}}
		>
			<Button
				size={size}
				className={keyClass}
				variant="secondary"
				aria-label={compact ? "Open" : undefined}
				title={compact ? "Open (Enter)" : undefined}
				onClick={onOpen}
			>
				<PanelRightOpenIcon aria-hidden />
				{compact ? null : (
					<>
						Open
						<Kbd variant="key">↵</Kbd>
					</>
				)}
			</Button>
			<Button
				size={size}
				className={keyClass}
				variant="secondary"
				disabled={actions.busy}
				aria-label={compact ? deploy : undefined}
				title={compact ? deploy : undefined}
				onClick={actions.deploy}
			>
				<RocketIcon aria-hidden />
				{compact ? null : deploy}
			</Button>
			{eligible ? (
				scaleButton
			) : (
				<Tooltip>
					<TooltipTrigger render={<span className="inline-flex" />}>
						{scaleButton}
					</TooltipTrigger>
					<TooltipContent>
						{service.scaling.ineligibleReason ??
							"Replicas are not available for this service"}
					</TooltipContent>
				</Tooltip>
			)}
			<DropdownMenu>
				<DropdownMenuTrigger
					aria-label={`More actions for ${service.name}`}
					className={buttonVariants({
						variant: "ghost",
						size: "icon-sm",
						className: keyClass,
					})}
				>
					<EllipsisIcon aria-hidden />
				</DropdownMenuTrigger>
				<DropdownMenuContent align="end" side="bottom" className="w-[200px]">
					{actions.menu.map((action) => (
						<MenuRow key={action.id} action={action} />
					))}
				</DropdownMenuContent>
			</DropdownMenu>
		</div>
	);
});

function MenuRow({ action }: { action: ServiceAction }) {
	return (
		<>
			{action.destructive ? <DropdownMenuSeparator /> : null}
			<DropdownMenuItem
				disabled={action.disabled}
				variant={action.destructive ? "destructive" : "default"}
				onClick={action.run}
			>
				<action.icon aria-hidden />
				{action.label}
			</DropdownMenuItem>
		</>
	);
}
