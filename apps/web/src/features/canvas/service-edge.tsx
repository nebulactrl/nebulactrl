import {
	type ConnectionLineComponentProps,
	type Edge,
	EdgeLabelRenderer,
	type EdgeProps,
	Position,
} from "@xyflow/react";
import { useReducedMotion } from "motion/react";
import { memo, useState } from "react";
import type { ServiceLink } from "#/api/models";
import { useLinkReview } from "#/features/links";
import { MeshEdgeBadge, useMeshEdge } from "#/features/mesh/canvas-mesh";
import {
	type CableSide,
	cable,
	jackCentre,
	MANUAL_DASH,
	STAGED_DASH,
} from "./lib/cable";

/** How a cable is drawn: variable-referenced, hand-drawn only, or the derived Internet one. */
export type LinkKind = "reference" | "manual" | "internet";

export type LinkEdgeData = {
	kind: LinkKind;
	/** The stored link, for review; null for the derived Internet cable. */
	link: ServiceLink | null;
	/** Variable keys on the source service that reference the target. */
	references: string[];
	/** Every reference that makes this cable is staged: it goes live when the source deploys. */
	staged: boolean;
	/** The target is deploying: a bead of light runs down the cable. */
	inFlight: boolean;
};

export type LinkFlowEdge = Edge<LinkEdgeData, "link">;

const SIDE: Record<Position, CableSide> = {
	[Position.Left]: "left",
	[Position.Right]: "right",
	[Position.Top]: "top",
	[Position.Bottom]: "bottom",
};

const DASHED = MANUAL_DASH;
const STAGED = STAGED_DASH;

const ACTIVE_BODY =
	"color-mix(in oklab, var(--color-line-control), var(--color-accent-ink) 60%)";

function Layers({
	d,
	dash,
	body,
}: {
	d: string;
	dash: string | undefined;
	body?: string | undefined;
}) {
	return (
		<>
			<path d={d} className="cable-shadow" strokeDasharray={dash} />
			<path
				d={d}
				className="cable-body"
				strokeDasharray={dash}
				{...(body ? { style: { stroke: body } } : {})}
			/>
			<path d={d} className="cable-hi" strokeDasharray={dash} />
		</>
	);
}

/**
 * A link between two services as a sagging patch cable: shadow under, body,
 * a highlight over. Links are presentation only; clicking one reviews which
 * variables reference the target, and neither adding nor removing one ever
 * touches a deployment.
 */
function ServiceEdgeImpl({
	sourceX,
	sourceY,
	targetX,
	targetY,
	sourcePosition,
	targetPosition,
	id,
	data,
	style,
}: EdgeProps<LinkFlowEdge>) {
	const reduced = useReducedMotion() ?? false;
	const [hovered, setHovered] = useState(false);
	const { openReferences } = useLinkReview();
	const { d, mid } = cable(
		jackCentre({ x: sourceX, y: sourceY, side: SIDE[sourcePosition] }),
		jackCentre({ x: targetX, y: targetY, side: SIDE[targetPosition] }),
	);
	const kind = data?.kind ?? "reference";
	const link = data?.link ?? null;
	const references = data?.references ?? [];
	const inFlight = data?.inFlight === true;
	const staged = data?.staged === true;
	const dash = kind === "manual" ? DASHED : staged ? STAGED : undefined;
	const labelled = kind !== "internet";
	const mesh = useMeshEdge(id);

	return (
		<>
			<g style={style} data-cable={kind}>
				<Layers d={d} dash={dash} body={inFlight ? ACTIVE_BODY : undefined} />
				{inFlight && !reduced ? (
					<>
						{/* The glow is a wide, faint twin of the bead: a CSS filter on an animated path repaints a blurred region of the whole cable every frame. */}
						<path d={d} className="cable-bead-glow" />
						<path d={d} className="cable-bead" />
					</>
				) : null}
				{/*
				 * The cable's hit area for the pointer. It is no tab stop and not a
				 * button for assistive tech: Tab goes through the cards first, and a
				 * card's menu (Shift+F10) lists its cables ("Review link to …"). The
				 * cable's own name is the edge's `ariaLabel`, built from service names.
				 */}
				<path
					d={d}
					fill="none"
					stroke="transparent"
					strokeWidth={18}
					className="react-flow__edge-interaction"
					aria-hidden
					style={{
						pointerEvents: "stroke",
						cursor: link ? "pointer" : "default",
					}}
					onPointerEnter={(event) => {
						// A touch pointer fires enter on tap and would latch the highlight.
						if (event.pointerType === "mouse") setHovered(true);
					}}
					onPointerLeave={() => setHovered(false)}
					onClick={(event) => {
						if (!link) return;
						event.stopPropagation();
						openReferences(link);
					}}
				/>
			</g>
			{mesh && !hovered ? (
				<EdgeLabelRenderer>
					<div
						className="pointer-events-none absolute"
						style={{
							transform: `translate(-50%, -50%) translate(${mid.x}px, ${mid.y}px)`,
						}}
					>
						<MeshEdgeBadge info={mesh} />
					</div>
				</EdgeLabelRenderer>
			) : null}
			{hovered && labelled ? (
				<EdgeLabelRenderer>
					<div
						role="tooltip"
						className="surface-pop pointer-events-none absolute z-10 max-w-[260px] rounded-tip px-2 py-1.5 text-center text-xs leading-[1.4]"
						style={{
							transform: `translate(-50%, -50%) translate(${mid.x}px, ${mid.y}px)`,
						}}
					>
						{references.length > 0 ? (
							<>
								<span className="text-ink-3">Variables </span>
								<span className="font-mono text-ink-1">
									{references.join(", ")}
								</span>
								{staged ? (
									<div className="mt-0.5 text-[11px] text-ink-3">
										Staged: live after the next deploy
									</div>
								) : null}
							</>
						) : (
							<span className="text-ink-2">
								Linked by hand. No variable references it yet.
							</span>
						)}
						{link ? (
							<div className="mt-0.5 text-[11px] text-ink-3">
								Click to review
							</div>
						) : null}
						{mesh ? (
							<div className="mt-0.5 text-[11px] text-ink-3">
								Over the mesh: {mesh.label}
								{mesh.devicesAdded > 0
									? ` · adds ${mesh.devicesAdded} ${mesh.devicesAdded === 1 ? "device" : "devices"} on deploy`
									: ""}
							</div>
						) : null}
					</div>
				</EdgeLabelRenderer>
			) : null}
		</>
	);
}

export const ServiceEdge = memo(ServiceEdgeImpl);

/** The cable that follows the pointer while a jack is dragged. */
export function ConnectionCable({
	fromX,
	fromY,
	toX,
	toY,
	fromPosition,
	toPosition,
}: ConnectionLineComponentProps) {
	const { d } = cable(
		{ x: fromX, y: fromY, side: SIDE[fromPosition] },
		{ x: toX, y: toY, side: SIDE[toPosition] },
	);
	return (
		<g>
			<path d={d} className="cable-shadow" />
			<path
				d={d}
				className="cable-body"
				style={{ stroke: "var(--color-accent-ink)" }}
			/>
			<circle
				cx={toX}
				cy={toY}
				r={6}
				fill="var(--color-accent-ink)"
				stroke="rgb(2 4 10 / 0.9)"
				strokeWidth={1.3}
			/>
		</g>
	);
}
