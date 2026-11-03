import { TimerIcon } from "lucide-react";
import type { CSSProperties } from "react";

/** The Create menu's mark for the Cron job entry: a timer on a tile. */
export function CronMark({ size = 22 }: { size?: number }) {
	return (
		<span
			aria-hidden
			data-slot="service-icon"
			className="service-tile select-none text-white"
			style={
				{
					width: size,
					height: size,
					borderRadius: Math.round(size * 0.29),
					"--t": "oklch(0.55 0.13 55)",
				} as CSSProperties
			}
		>
			<TimerIcon
				width={Math.round(size * 0.57)}
				height={Math.round(size * 0.57)}
				strokeWidth={1.8}
			/>
		</span>
	);
}
