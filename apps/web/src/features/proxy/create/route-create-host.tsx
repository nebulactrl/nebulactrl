import { useRef } from "react";
import type { Server } from "#/api/models";
import { useProjectContext } from "#/components/shell/use-project-context";
import { Dialog, DialogContent } from "#/components/ui/dialog";
import { useServers } from "#/features/canvas/data";
import {
	type RouteCreateRequest,
	routeCreate,
	useRouteCreateRequest,
} from "../create-store";
import { ExternalForm } from "./external-form";
import { PortForm } from "./port-form";

const NO_SERVERS: Server[] = [];

/**
 * Mounted once beside the create dialogs: shows the External service or the
 * TCP/UDP port dialog for whatever `routeCreate.open` asked for, in the
 * environment on screen.
 */
export function RouteCreateHost() {
	const environment = useProjectContext()?.environment;
	const servers = useServers();
	const request = useRouteCreateRequest();
	// Keep the last request while the dialog animates closed.
	const last = useRef<RouteCreateRequest>({ mode: "external" });
	if (request !== null) last.current = request;
	if (!environment) return null;
	const shown = last.current;
	return (
		<Dialog
			open={request !== null}
			onOpenChange={(next) => {
				if (!next) routeCreate.close();
			}}
		>
			<DialogContent width={540}>
				{request !== null ? (
					shown.mode === "external" ? (
						<ExternalForm
							key="external"
							environment={environment}
							servers={servers.data ?? NO_SERVERS}
							position={shown.position}
						/>
					) : (
						<PortForm
							key="port"
							environment={environment}
							servers={servers.data ?? NO_SERVERS}
							request={shown}
						/>
					)
				) : null}
			</DialogContent>
		</Dialog>
	);
}
