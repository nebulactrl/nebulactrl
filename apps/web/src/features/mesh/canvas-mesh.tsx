import { useQuery } from "@tanstack/react-query";
import { NetworkIcon } from "lucide-react";
import { createContext, type ReactNode, useContext, useMemo } from "react";
import { getEnvironmentMeshOptions } from "#/api/gen/@tanstack/react-query.gen";
import { StatusIcon } from "#/components/ui/status-icon";
import {
	buildCanvasMesh,
	type CanvasLinkInput,
	type CanvasMesh,
	type CanvasServiceInput,
	crossServerLinks,
	EMPTY_CANVAS_MESH,
	type MeshEdgeInfo,
	stateChip,
	viaMeshTitle,
} from "./model";

const MeshCanvasContext = createContext<CanvasMesh>(EMPTY_CANVAS_MESH);

/**
 * Gives the canvas's cards and cables the mesh facts they draw: which cables
 * run between servers (and so carry a badge), and which services reach another
 * through the mesh (a "via mesh" chip). The environment's mesh is read only
 * while a cable crosses servers, so an environment on one server never asks.
 */
export function MeshCanvasProvider({
	environmentId,
	services,
	links,
	children,
}: {
	environmentId: string;
	services: readonly CanvasServiceInput[];
	links: readonly CanvasLinkInput[];
	children: ReactNode;
}) {
	const cross = useMemo(
		() => crossServerLinks(links, services),
		[links, services],
	);
	const mesh = useQuery({
		...getEnvironmentMeshOptions({ path: { environmentId } }),
		enabled: cross.length > 0,
		retry: false,
	});
	const value = useMemo(
		() => buildCanvasMesh(cross, services, mesh.data),
		[cross, services, mesh.data],
	);
	return (
		<MeshCanvasContext.Provider value={value}>
			{children}
		</MeshCanvasContext.Provider>
	);
}

/** The mesh badge of one cable, or undefined when it stays on one server. */
export function useMeshEdge(linkId: string): MeshEdgeInfo | undefined {
	return useContext(MeshCanvasContext).edges.get(linkId);
}

const CHIP =
	"inline-flex h-5 min-w-0 max-w-[110px] items-center gap-[5px] rounded-md border border-line-faint bg-white/[0.045] px-[7px] font-mono text-[11px] text-ink-3 leading-none";

/**
 * "via mesh" on a card whose service reaches another through the mesh. Not
 * interactive, like the other chips; the tooltip and the screen reader text
 * name the providers, the servers and the link's state. A link that is not
 * online shows its status icon too.
 */
export function MeshViaChip({ serviceId }: { serviceId: string }) {
	const info = useContext(MeshCanvasContext).via.get(serviceId);
	if (!info) return null;
	const title = viaMeshTitle(info);
	const chip = stateChip(info.state);
	return (
		<span data-slot="mesh-via-chip" title={title} className={CHIP}>
			<NetworkIcon aria-hidden className="size-3 shrink-0" />
			<span className="truncate">via mesh</span>
			{info.state === "online" ? null : (
				<StatusIcon kind={chip.kind} label="" className="size-3" />
			)}
			<span className="sr-only">{title}</span>
		</span>
	);
}

/**
 * The badge at the middle of a cable between two servers: the mesh, the link's
 * state in words and the devices it holds. A cable that crosses servers is
 * badged even before its link exists ("Not connected yet"), because the link
 * and its device are added by an explicit action, never by drawing the cable.
 */
export function MeshEdgeBadge({ info }: { info: MeshEdgeInfo }) {
	const chip = stateChip(info.state);
	const word = info.state === "pending" ? "Not connected yet" : chip.label;
	return (
		<span
			data-slot="mesh-edge-badge"
			className="surface-float pointer-events-none inline-flex h-5 items-center gap-1.5 rounded-md px-1.5 font-medium text-[11px] text-ink-2 leading-none"
		>
			<NetworkIcon aria-hidden className="size-3 shrink-0 text-ink-3" />
			Mesh
			<StatusIcon kind={chip.kind} label="" className="size-3" />
			<span className="text-ink-3">{word}</span>
		</span>
	);
}
