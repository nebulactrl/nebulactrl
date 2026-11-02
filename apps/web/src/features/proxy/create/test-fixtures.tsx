// Builders for the proxy create dialogs' tests: the default scene of the
// typed factories (src/test/factories.ts), with the proxy-v2 server and the
// route-capable services these screens expect.

import type { ReactNode } from "react";
import type * as Gen from "#/api/gen/types.gen";
import { type Environment, type Server, toServer } from "#/api/models";
import { Dialog, DialogContent } from "#/components/ui/dialog";
import { make, type Overrides } from "#/test/factories";

/** An online server whose agent speaks proxy v2, as the API sends it; `over` states what a test is about. */
export function serverView(
	over: Overrides<Gen.Server> & { id: string },
): Gen.Server {
	return make("Server", {
		name: over.id,
		lastSeenAt: null,
		protocolVersion: 2,
		serviceCount: 0,
		controlPlane: false,
		composeVersion: "",
		buildMaxConcurrent: 1,
		...over,
		proxy: { status: "ok", v2: true, ...over.proxy },
	});
}

/** The same server as the component model. */
export function server(over: Overrides<Gen.Server> & { id: string }): Server {
	return toServer(serverView(over));
}

export const production: Environment = make("Environment", {
	id: "env-prod",
	projectId: "proj-1",
	name: "production",
	slug: "production",
	isProduction: true,
	createdAt: "2026-01-01T00:00:00Z",
});

export const staging: Environment = make("Environment", {
	id: "env-stg",
	projectId: "proj-1",
	name: "staging",
	slug: "staging",
	isProduction: false,
	createdAt: "2026-01-01T00:00:00Z",
});

/** A service as the API sends it (`ServiceView`), placed on `serverId` when given. */
export function serviceView(over: {
	id: string;
	name: string;
	serverId?: string;
	serverName?: string;
	kind?: Gen.ServiceView["kind"];
	port?: number;
}) {
	const serverId = over.serverId ?? "";
	return make("ServiceView", {
		id: over.id,
		environmentId: production.id,
		projectId: production.projectId,
		name: over.name,
		slug: over.name,
		kind: over.kind ?? "app",
		config: { healthcheck: { type: "none" }, port: over.port },
		serverId,
		server: serverId
			? {
					id: serverId,
					name: over.serverName ?? serverId,
					status: "online",
					lastSeenAt: null,
				}
			: null,
		generation: 1,
		appliedGeneration: 0,
		createdAt: "2026-01-01T00:00:00Z",
		updatedAt: "2026-01-01T00:00:00Z",
	});
}

/** The open dialog the create forms render inside (their parts need a Dialog root). */
export function DialogFrame({ children }: { children: ReactNode }) {
	return (
		<Dialog open>
			<DialogContent width={540}>{children}</DialogContent>
		</Dialog>
	);
}
