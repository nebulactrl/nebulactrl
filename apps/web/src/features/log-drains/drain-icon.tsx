import { cn } from "cn";
import {
	BracesIcon,
	DatabaseIcon,
	type LucideIcon,
	RadioTowerIcon,
	TelescopeIcon,
} from "lucide-react";
import type { CSSProperties } from "react";
import type { DrainKind } from "./model";

const MARKS: Record<DrainKind, { Icon: LucideIcon; tile: string }> = {
	loki: { Icon: DatabaseIcon, tile: "oklch(0.58 0.15 55)" },
	otlp: { Icon: TelescopeIcon, tile: "oklch(0.5 0.15 295)" },
	syslog: { Icon: RadioTowerIcon, tile: "oklch(0.52 0.12 235)" },
	http: { Icon: BracesIcon, tile: "oklch(0.5 0.12 175)" },
};

/** A drain kind's mark on the same glossy tile as a service's (`service-tile`). */
export function DrainIcon({
	kind,
	size = 36,
	className,
}: {
	kind: DrainKind;
	/** Tile edge in px. */
	size?: number;
	className?: string;
}) {
	const { Icon, tile } = MARKS[kind];
	const glyph = Math.round(size * 0.57);
	return (
		<span
			data-slot="service-icon"
			aria-hidden
			className={cn("service-tile select-none", className)}
			style={
				{
					width: size,
					height: size,
					borderRadius: Math.round(size * 0.29),
					color: "#ffffff",
					"--t": tile,
				} as CSSProperties
			}
		>
			<Icon
				width={glyph}
				height={glyph}
				strokeWidth={1.8}
				aria-hidden="true"
				style={{ filter: "drop-shadow(0 1px 0 rgb(2 4 10 / 0.35))" }}
			/>
		</span>
	);
}
