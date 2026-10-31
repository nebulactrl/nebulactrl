import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { errorCode } from "#/api/client-config";
import { getServiceMeshOptions } from "#/api/gen/@tanstack/react-query.gen";
import type { Service } from "#/api/models";
import { DocsLink } from "#/components/docs/docs-link";
import { Skeleton } from "#/components/ui/skeleton";
import {
	grantSourceLabel,
	purposeLabel,
	routeState,
	stateChip,
} from "#/features/mesh/model";
import { StateLabel } from "#/features/mesh/state-label";
import { Section, SectionBody } from "./fields";

const ITEM = "flex flex-wrap items-center gap-x-3 gap-y-0.5 text-[13px]";

/**
 * One list of mesh links: a heading that says what the list is, then its
 * links, or one muted line when it is empty. Not a setting, so it has no row
 * and no control; `anchor` is what the settings search jumps to.
 */
function MeshList({
	anchor,
	title,
	empty,
	children,
}: {
	anchor: string;
	title: string;
	empty: string;
	children: ReactNode[];
}) {
	return (
		<div data-anchor={anchor} className="flex flex-col gap-1.5">
			<h4 className="m-0 font-medium text-[13px] text-ink-1">{title}</h4>
			{children.length === 0 ? (
				<p className="type-secondary m-0">{empty}</p>
			) : (
				<ul aria-label={title} className="flex flex-col gap-1.5">
					{children}
				</ul>
			)}
		</div>
	);
}

/**
 * What this service does over the mesh, as three read-only lists: the ports of
 * it that services on other servers reach, the services it reaches on other
 * servers (as `<name>:<port>`, the address it dials), and who reaches it.
 * Links and devices are added by deploying, by drawing a cable, or by adding
 * a replica, never from here.
 */
export function MeshSection({ service }: { service: Service }) {
	const query = useQuery({
		...getServiceMeshOptions({ path: { serviceId: service.id } }),
		retry: false,
	});
	const data = query.data;
	const exposures = data?.exposures ?? [];
	const routes = data?.routes ?? [];
	const consumers = data?.consumers ?? [];

	let body: ReactNode;
	if (query.isPending) {
		body = <Skeleton className="h-16 rounded-card" role="status" />;
	} else if (!data) {
		body =
			errorCode(query.error) === "mesh_not_configured" ? (
				<p className="type-secondary">
					No tailnet is connected, so this service stays on its server.{" "}
					<Link
						to="/settings/$section"
						params={{ section: "server-mesh" }}
						className="text-accent-ink underline"
					>
						Open Team settings → Server mesh
					</Link>
				</p>
			) : (
				<p className="type-secondary">
					The mesh status did not load. Nothing was changed.
				</p>
			);
	} else if (
		exposures.length === 0 &&
		routes.length === 0 &&
		consumers.length === 0
	) {
		body = (
			<p className="type-secondary">
				This service does not use the mesh. It does when a service on another
				server reaches it, or it reaches one: by a variable reference or a link
				on the canvas.
			</p>
		);
	} else {
		body = (
			<div className="flex flex-col gap-4">
				<MeshList
					anchor="mesh-exposures"
					title="Shared with other servers"
					empty="Not shared."
				>
					{exposures.map((exposure) => (
						<li key={exposure.id} className={ITEM}>
							<code className="font-mono text-ink-1">
								{exposure.alias}:{exposure.meshPort}
							</code>
							<span className="text-ink-3">
								container port {exposure.containerPort} ·{" "}
								{purposeLabel(exposure.purpose).toLowerCase()} · on{" "}
								{exposure.server.name}
							</span>
						</li>
					))}
				</MeshList>
				<MeshList
					anchor="mesh-routes"
					title="Reaches over the mesh"
					empty="Nothing on another server."
				>
					{routes.map((route) => (
						<li key={`${route.providerId}:${route.port}`} className={ITEM}>
							<code className="font-mono text-ink-1">
								{route.port > 0 ? `${route.alias}:${route.port}` : route.alias}
							</code>
							<span className="text-ink-3">
								{route.provider} on {route.server.name} ·{" "}
								{grantSourceLabel(route.source)}
								{route.port === 0 ? " · no port shared yet" : ""}
								{route.notExposable ? " · cannot be shared" : ""}
							</span>
							<StateLabel chip={stateChip(routeState(route.state))} />
						</li>
					))}
				</MeshList>
				<MeshList anchor="mesh-consumers" title="Reached by" empty="Nobody.">
					{consumers.map((consumer) => (
						<li key={consumer.serviceId} className={ITEM}>
							<span className="font-medium text-ink-1">{consumer.slug}</span>
							<span className="text-ink-3">
								on {consumer.server.name} · {grantSourceLabel(consumer.source)}
							</span>
							<StateLabel chip={stateChip(routeState(consumer.state))} />
						</li>
					))}
				</MeshList>
			</div>
		);
	}

	return (
		<Section
			title="Mesh"
			description={
				<>
					Links to services on other servers, over your tailnet.{" "}
					<DocsLink to="operations/mesh" />
				</>
			}
		>
			<SectionBody>{body}</SectionBody>
		</Section>
	);
}
