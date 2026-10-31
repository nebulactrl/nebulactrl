import { cn } from "cn";
import { KeyboardIcon, MapIcon, MaximizeIcon } from "lucide-react";

export interface ZoomToolbarProps {
	zoom: number;
	onZoomIn: () => void;
	onZoomOut: () => void;
	/** Back to 100%. */
	onZoomReset: () => void;
	onFit: () => void;
	/** Whether the minimap is showing; with `onToggleMap` adds its toggle. */
	mapOpen?: boolean;
	onToggleMap?: () => void;
	/** Opens the keyboard shortcuts dialog. */
	onShortcuts: () => void;
	className?: string;
}

const key =
	"btn-ghost pressable grid size-7 shrink-0 cursor-pointer place-items-center rounded-ctl p-0 text-ink-3 leading-none hover:text-ink-1 focus-visible:focus-ring [&_svg]:size-3.5";

/**
 * The zoom column in the canvas's lower-left corner: +, the zoom percent (a
 * reset), −, a rule, Fit, the minimap toggle and ?. Opaque `surface-float`: no blur,
 * no translucency, so the floor's halftone never shows through.
 */
export function ZoomToolbar({
	zoom,
	onZoomIn,
	onZoomOut,
	onZoomReset,
	onFit,
	mapOpen,
	onToggleMap,
	onShortcuts,
	className,
}: ZoomToolbarProps) {
	return (
		<div
			role="toolbar"
			aria-label="Canvas zoom"
			aria-orientation="vertical"
			className={cn(
				"surface-float flex flex-col items-center gap-0.5 rounded-card p-[3px]",
				className,
			)}
		>
			<button
				type="button"
				title="Zoom in (+)"
				aria-label="Zoom in"
				onClick={onZoomIn}
				className={cn(key, "font-medium font-mono text-[14px]")}
			>
				+
			</button>
			<button
				type="button"
				title="Reset to 100% (0)"
				aria-label={`Zoom ${Math.round(zoom * 100)}%, reset to 100%`}
				onClick={onZoomReset}
				className={cn(
					key,
					"h-[22px] font-medium font-mono text-[10px] text-ink-1",
				)}
			>
				{Math.round(zoom * 100)}%
			</button>
			<button
				type="button"
				title="Zoom out (−)"
				aria-label="Zoom out"
				onClick={onZoomOut}
				className={cn(key, "font-medium font-mono text-[14px]")}
			>
				−
			</button>
			<span aria-hidden="true" className="my-0.5 h-px w-4 bg-line" />
			<button
				type="button"
				title="Fit all services (1)"
				aria-label="Fit all services"
				onClick={onFit}
				className={key}
			>
				<MaximizeIcon aria-hidden />
			</button>
			{onToggleMap ? (
				<button
					type="button"
					title={mapOpen ? "Hide minimap" : "Show minimap"}
					aria-label={mapOpen ? "Hide minimap" : "Show minimap"}
					aria-pressed={mapOpen === true}
					onClick={onToggleMap}
					className={cn(key, mapOpen && "bg-surface-pop text-ink-1")}
				>
					<MapIcon aria-hidden />
				</button>
			) : null}
			<button
				type="button"
				title="Keyboard shortcuts (?)"
				aria-label="Keyboard shortcuts"
				onClick={onShortcuts}
				className={key}
			>
				<KeyboardIcon aria-hidden />
			</button>
		</div>
	);
}
