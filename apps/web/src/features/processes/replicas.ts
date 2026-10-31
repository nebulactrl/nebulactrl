import type { Service } from "#/api/models";
import { processReplicaLabel } from "./model";

type Replica = NonNullable<
	NonNullable<Service["observed"]>["replicas"]
>[number];

/**
 * The replicas the agent reports, in the order a reader expects: by process
 * in rollout order (the default one first), then by index inside the process.
 * A service without processes keeps the agent's order, which is by index.
 */
export function orderedReplicas(
	service: Pick<Service, "observed" | "processes">,
): Replica[] {
	const replicas = [...(service.observed?.replicas ?? [])];
	if (service.processes.length === 0) return replicas;
	const rank = new Map(
		service.processes.map((process, index) => [process.name, index]),
	);
	const rankOf = (replica: Replica) =>
		rank.get(replica.process) ?? service.processes.length;
	return replicas.sort((a, b) => rankOf(a) - rankOf(b) || a.index - b.index);
}

/** "#1" for a plain service, "worker · replica 1" once it runs several processes. */
export function replicaLabel(
	replica: Pick<Replica, "process" | "index">,
): string {
	return replica.process === ""
		? `#${replica.index}`
		: processReplicaLabel(replica.process, replica.index);
}
