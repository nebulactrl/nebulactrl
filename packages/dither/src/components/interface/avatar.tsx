import { cn } from "cn";
import * as React from "react";
import { useDitherFillUrl } from "../../lib/pattern";
import {
	avatarColors,
	avatarColorsFrom,
	FACE_SIZE,
	faceFor,
	faceRuns,
} from "./face";
import {
	bloomShadow,
	type DitherBloom,
	type DitherColor,
	type DitherFillVariant,
	inkCss,
} from "./fill";
import {
	Avatar,
	AvatarFallback,
	AvatarGroup,
	AvatarGroupCount,
	AvatarImage,
} from "./primitives";

export type DitherAvatarSize = "sm" | "default" | "lg" | number;

/** A presence dot in the corner; the state is added to the accessible name. */
export type DitherAvatarStatus = "online" | "away" | "busy" | "offline";

const NAMED_PX = { sm: 22, default: 32, lg: 40 } as const;

const STATUS_COLOR: Record<DitherAvatarStatus, string> = {
	online: "var(--dither-green)",
	away: "var(--dither-orange)",
	busy: "var(--dither-red)",
	offline: "var(--dither-grey)",
};

/** The pixel edge of an avatar size. */
export function avatarPx(size: DitherAvatarSize): number {
	return typeof size === "number" ? size : NAMED_PX[size];
}

function sizeProps(size: DitherAvatarSize) {
	if (typeof size !== "number") return { size, style: undefined };
	const dimension = { width: size, height: size };
	return {
		size: size <= 24 ? "sm" : size >= 38 ? "lg" : "default",
		style: dimension,
	} as const;
}

interface GroupContextValue {
	size: DitherAvatarSize | undefined;
	outline: string | undefined;
}

/** What a group hands to the avatars inside it: their size and the gap colour. */
const GroupContext = React.createContext<GroupContextValue | null>(null);

const FILL_SHAPE = {
	gradient: "square",
	dotted: "dot",
	hatched: "hatch",
	solid: "square",
} as const;

/**
 * The face itself: skin in the name's hue (or `color`), lit from the top-left
 * and shaded at the bottom with real dither, and symmetric ink features on
 * top. The dither layers keep their 2px cells in CSS px at any avatar size
 * (they sit outside the face's own 9×9 viewBox); the features are on that
 * grid, so they stay whole cells. `fill` picks how the light and shade are
 * drawn: Bayer cells, dots, hatching, or a flat translucent tint.
 */
function FaceArt({
	name,
	color,
	fill,
}: {
	name: string;
	color: string | undefined;
	fill: DitherFillVariant;
}) {
	const face = React.useMemo(() => faceFor(name), [name]);
	const colors = color ? avatarColorsFrom(color) : avatarColors(face.hue);
	const runs = React.useMemo(() => faceRuns(face.bits), [face]);
	const shape = FILL_SHAPE[fill];
	const lightHigh = useDitherFillUrl({
		tone: 0.5,
		color: colors.light,
		cell: 2,
		shape,
	});
	const lightLow = useDitherFillUrl({
		tone: 0.22,
		color: colors.light,
		cell: 2,
		shape,
	});
	const shadeLow = useDitherFillUrl({
		tone: 0.22,
		color: colors.shade,
		cell: 2,
		shape,
	});
	const shadeHigh = useDitherFillUrl({
		tone: 0.5,
		color: colors.shade,
		cell: 2,
		shape,
	});
	const flat = fill === "solid";
	return (
		<svg
			aria-hidden
			width="100%"
			height="100%"
			className="block size-full"
			data-slot="dither-avatar-face"
		>
			<rect width="100%" height="100%" style={{ fill: colors.base }} />
			<rect
				width="100%"
				height="28%"
				style={flat ? { fill: colors.light, fillOpacity: 0.4 } : undefined}
				fill={flat ? undefined : lightHigh}
			/>
			<rect
				y="28%"
				width="100%"
				height="22%"
				style={flat ? { fill: colors.light, fillOpacity: 0.18 } : undefined}
				fill={flat ? undefined : lightLow}
			/>
			<rect
				y="62%"
				width="100%"
				height="20%"
				style={flat ? { fill: colors.shade, fillOpacity: 0.22 } : undefined}
				fill={flat ? undefined : shadeLow}
			/>
			<rect
				y="82%"
				width="100%"
				height="18%"
				style={flat ? { fill: colors.shade, fillOpacity: 0.5 } : undefined}
				fill={flat ? undefined : shadeHigh}
			/>
			<svg
				aria-hidden
				viewBox={`0 0 ${FACE_SIZE} ${FACE_SIZE}`}
				width="100%"
				height="100%"
				shapeRendering="crispEdges"
			>
				{runs.map((run) => (
					<rect
						key={`${run.x}-${run.y}`}
						x={run.x}
						y={run.y}
						width={run.w}
						height={1}
						style={{ fill: colors.ink }}
					/>
				))}
			</svg>
		</svg>
	);
}

export interface DitherAvatarProps
	extends Omit<
		React.ComponentProps<typeof Avatar>,
		"size" | "children" | "style" | "color"
	> {
	style?: React.CSSProperties;
	/** The person (or service): drives the face and the hue. Also the accessible name. */
	name: string;
	/** Accessible name instead of `name`, for when `name` is an id or an email. */
	label?: string | undefined;
	/** A photo wins over the generated face once it loads. */
	src?: string | undefined;
	/**
	 * `sm` / `default` / `lg`, or a diameter in px. Inside a group, the group's
	 * size. With neither, `var(--avatar-size)` (32px if unset).
	 */
	size?: DitherAvatarSize | undefined;
	/** Skin and ring colour: a palette name, a custom property or any CSS colour. Default: one of six hues picked from `name`. */
	color?: DitherColor | undefined;
	/** How the face's light and shade are drawn. Default `gradient`. */
	fill?: DitherFillVariant | undefined;
	/** A circle or a rounded square. */
	shape?: "circle" | "square" | undefined;
	/** A ring in the avatar's colour, for the current user or a selection. */
	ring?: boolean | undefined;
	/** A presence dot in the corner, added to the accessible name ("Alan Turing, busy"). */
	status?: DitherAvatarStatus | undefined;
	/** A soft glow in the avatar's colour. */
	bloom?: DitherBloom | undefined;
}

/**
 * A deterministic mirrored face from a name hash: the same name is the same
 * face on every device and in every session, with no upload needed.
 */
function DitherAvatar({
	name,
	label,
	src,
	size,
	color,
	fill = "gradient",
	shape = "circle",
	ring = false,
	status,
	bloom,
	className,
	style,
	...props
}: DitherAvatarProps) {
	const group = React.useContext(GroupContext);
	const effective = size ?? group?.size;
	const resolved =
		effective === undefined
			? {
					size: "default" as const,
					style: {
						width: "var(--avatar-size, 2rem)",
						height: "var(--avatar-size, 2rem)",
					},
				}
			: sizeProps(effective);
	const px = effective === undefined ? 32 : avatarPx(effective);
	const hue = color ? inkCss(color) : avatarColors(faceFor(name).hue).light;
	const gap = group?.outline ?? "var(--color-surface-raised)";
	const shadows: string[] = [];
	if (ring) shadows.push(`0 0 0 2px ${gap}`, `0 0 0 4px ${hue}`);
	else if (group?.outline) shadows.push(`0 0 0 2px ${group.outline}`);
	const glow = bloomShadow(hue, bloom);
	if (glow) shadows.push(glow);

	const dot = Math.max(8, Math.round(px * 0.26));
	const accessibleName = status
		? `${label ?? name}, ${status}`
		: (label ?? name);

	return (
		<Avatar
			size={resolved.size}
			role="img"
			aria-label={accessibleName}
			className={cn(shape === "square" && "rounded-[22%]", className)}
			style={{
				...resolved.style,
				...(shadows.length > 0 ? { boxShadow: shadows.join(", ") } : null),
				...style,
			}}
			{...props}
		>
			{src ? (
				<AvatarImage src={src} alt="" className="rounded-[inherit]" />
			) : null}
			<AvatarFallback className="overflow-hidden rounded-[inherit] border-0 bg-transparent p-0 shadow-none ring-1 ring-white/15 ring-inset">
				<FaceArt name={name} color={color ? hue : undefined} fill={fill} />
			</AvatarFallback>
			{status ? (
				<span
					aria-hidden
					data-slot="avatar-status"
					data-status={status}
					className={cn(
						"absolute z-10 rounded-full",
						shape === "square" ? "right-0 bottom-0" : "right-[2%] bottom-[2%]",
					)}
					style={{
						width: dot,
						height: dot,
						backgroundColor:
							status === "offline"
								? "var(--color-surface-base)"
								: STATUS_COLOR[status],
						boxShadow:
							status === "offline"
								? `inset 0 0 0 2px ${STATUS_COLOR.offline}, 0 0 0 2px ${gap}`
								: `0 0 0 2px ${gap}`,
					}}
				/>
			) : null}
		</Avatar>
	);
}

export interface DitherAvatarGroupPerson {
	name: string;
	/** Accessible name instead of `name`. */
	label?: string | undefined;
	src?: string | undefined;
	color?: DitherColor | undefined;
	fill?: DitherFillVariant | undefined;
	status?: DitherAvatarStatus | undefined;
}

export interface DitherAvatarGroupProps
	extends Omit<React.ComponentProps<"div">, "children"> {
	/** The people to show. Use this or `children`. */
	people?: readonly DitherAvatarGroupPerson[] | undefined;
	/** `DitherAvatar` elements, an alternative to `people`. */
	children?: React.ReactNode;
	/** How many faces to show before the `+N` count. Default 4 for `people`, all for `children`. */
	max?: number | undefined;
	size?: DitherAvatarSize | undefined;
	/** Colour of the gap cut around each avatar: match the surface behind the group. */
	outline?: string | undefined;
	/** What the `+N` chip announces to screen readers. Default: "N more: names". */
	moreLabel?: ((count: number) => string) | undefined;
	className?: string;
}

/** The name an avatar element announces, for the `+N` chip's label. */
function elementName(node: React.ReactNode): string | undefined {
	if (!React.isValidElement<Partial<DitherAvatarProps>>(node)) return undefined;
	return node.props.label ?? node.props.name;
}

/** Overlapping faces, then a `+N` count for everyone past `max`. Lifts each face 2px on hover. */
function DitherAvatarGroup({
	people,
	children,
	max,
	size = "default",
	outline,
	moreLabel,
	className,
	...props
}: DitherAvatarGroupProps) {
	const limit = Math.max(0, max ?? (people ? 4 : Number.POSITIVE_INFINITY));
	const px = avatarPx(size);

	// Keys come from the person; a repeated name gets an occurrence suffix.
	const seen = new Map<string, number>();
	const personNodes = (people ?? []).map((person) => {
		const occurrence = seen.get(person.name) ?? 0;
		seen.set(person.name, occurrence + 1);
		const key = occurrence === 0 ? person.name : `${person.name}~${occurrence}`;
		return (
			<DitherAvatar
				key={key}
				name={person.name}
				label={person.label}
				src={person.src}
				color={person.color}
				fill={person.fill}
				status={person.status}
			/>
		);
	});
	const nodes = [...personNodes, ...React.Children.toArray(children)];
	const shown = nodes.slice(0, limit);
	const hidden = nodes.slice(shown.length);
	const names = hidden.map((node) => elementName(node) ?? "");
	const announce = moreLabel
		? moreLabel(hidden.length)
		: `${hidden.length} more: ${names.join(", ")}`;

	return (
		<GroupContext.Provider value={{ size, outline }}>
			<AvatarGroup
				role="group"
				className={cn(
					"[&>[data-slot=avatar]]:motion-safe:transition-transform motion-safe:[@media(hover:hover)]:[&>[data-slot=avatar]:hover]:-translate-y-0.5",
					className,
				)}
				{...props}
			>
				{shown}
				{hidden.length > 0 ? (
					<AvatarGroupCount
						role="img"
						aria-label={announce}
						title={names.filter(Boolean).join(", ") || undefined}
						className="font-mono font-semibold tabular-nums"
						style={{
							width: px,
							height: px,
							fontSize: Math.max(9, Math.round(px * 0.34)),
							...(outline ? { boxShadow: `0 0 0 2px ${outline}` } : null),
						}}
					>
						+{hidden.length}
					</AvatarGroupCount>
				) : null}
			</AvatarGroup>
		</GroupContext.Provider>
	);
}

export { DitherAvatar, DitherAvatarGroup };
