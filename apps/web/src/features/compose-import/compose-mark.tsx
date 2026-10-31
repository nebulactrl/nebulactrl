import { FileCodeIcon } from "lucide-react";
import type { CSSProperties } from "react";

/** The Create menu's mark for the Docker Compose entry: a code file on a tile. */
export function ComposeMark({ size = 22 }: { size?: number }) {
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
					"--t": "oklch(0.5 0.12 235)",
				} as CSSProperties
			}
		>
			<FileCodeIcon
				width={Math.round(size * 0.57)}
				height={Math.round(size * 0.57)}
				strokeWidth={1.8}
			/>
		</span>
	);
}
