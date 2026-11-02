import type {
	PromotePlan,
	ReplicaMemberView,
	ReplicaPlan,
	ReplicationView,
	Server,
} from "#/api/gen/types.gen";
import type { Service } from "#/api/models";
import { make, type Overrides } from "#/test/factories";

/** The fields the replica screens read; the rest of a Service is irrelevant to them. */
export function postgresService(overrides: Partial<Service> = {}): Service {
	return {
		id: "svc-primary",
		name: "Orders DB",
		slug: "orders-db",
		serverId: "srv-a",
		server: null,
		config: { database: { engine: "postgres" } },
		...overrides,
	} as Service;
}

export function member(
	overrides: Partial<ReplicaMemberView> = {},
): ReplicaMemberView {
	return {
		id: "svc-r1",
		slug: "orders-db-replica",
		state: "streaming",
		message: "",
		serverName: "eu-2",
		...overrides,
	};
}

export function replication(
	overrides: Partial<ReplicationView> = {},
): ReplicationView {
	return {
		role: "standalone",
		canPromote: false,
		rebuildable: false,
		replicas: [],
		...overrides,
	};
}

/** A wire server as `GET /servers` returns it: online, accepting workloads, empty. */
export function wireServer(
	id: string,
	name: string,
	overrides: Overrides<Server> = {},
): Server {
	return make("Server", {
		id,
		name,
		status: "online",
		drainState: "none",
		acceptWorkloads: true,
		serviceCount: 0,
		...overrides,
	});
}

export function serversPage(...servers: Server[]) {
	return make("PageServer", { items: servers });
}

export function replicaPlan(overrides: Partial<ReplicaPlan> = {}): ReplicaPlan {
	return {
		serverId: "srv-b",
		serverName: "eu-2",
		sameServer: false,
		devicesAdded: 0,
		links: [],
		blockers: [],
		warnings: [],
		...overrides,
	};
}

export function promotePlan(overrides: Partial<PromotePlan> = {}): PromotePlan {
	return {
		mode: "switchover",
		oldPrimary: { id: "svc-primary", slug: "orders-db" },
		replica: { id: "svc-r1", slug: "orders-db-replica" },
		consumers: [],
		rebuildReplicas: [],
		schedulesMoving: 0,
		blockers: [],
		warnings: [],
		...overrides,
	};
}
