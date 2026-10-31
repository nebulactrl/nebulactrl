import { Handle, Position } from "@xyflow/react";
import type { CSSProperties } from "react";
import { JACK_RADIUS } from "./lib/cable";

const SIZE = JACK_RADIUS * 2;

// React Flow's own stylesheet is unlayered and would beat utility classes, so
// everything that has to differ from its default handle is inline. The jack
// is a dark bore; a cable's plug fills it with a metal face and a dark eye.
const BORE =
	"radial-gradient(circle at 50% 40%, #05070c 0 38%, oklch(0.2 0.02 262) 42%, oklch(0.3 0.02 262) 100%)";
const PLUG =
	"radial-gradient(circle at 35% 30%, #eef0f5 0%, #9aa1b0 50%, #4b5262 100%)";

export interface JackHandleProps {
	type: "source" | "target";
	position: Position;
	/** Vertical offset of a left or right jack from the card's top edge. */
	top?: number;
	/** A cable is plugged in: draw the plug face. */
	plugged: boolean;
	id?: string | undefined;
	label: string;
	connectable?: boolean;
}

/**
 * A patch jack: a React Flow handle drawn as a socket on the card's edge.
 * Its centre sits exactly on the edge, which is where cables end.
 */
export function JackHandle({
	type,
	position,
	top,
	plugged,
	id,
	label,
	connectable = true,
}: JackHandleProps) {
	const style: CSSProperties = {
		width: SIZE,
		height: SIZE,
		minWidth: SIZE,
		minHeight: SIZE,
		borderRadius: "50%",
		zIndex: 2,
		background: plugged ? PLUG : BORE,
		borderColor: "var(--color-line-control)",
		...(top !== undefined &&
		position !== Position.Bottom &&
		position !== Position.Top
			? { top }
			: {}),
	};
	return (
		<Handle
			id={id ?? null}
			type={type}
			position={position}
			isConnectable={connectable}
			aria-label={label}
			className="jack data-[connectingfrom=true]:ring-2 data-[connectingfrom=true]:ring-accent-ink data-[connecting=true]:ring-2 data-[connecting=true]:ring-accent-ink"
			style={style}
		>
			{plugged ? (
				<span
					aria-hidden
					className="pointer-events-none absolute inset-[4px] rounded-full bg-[#0e1118] shadow-[inset_0_0_0_1px_rgb(2_4_10/0.45)]"
				/>
			) : null}
		</Handle>
	);
}
