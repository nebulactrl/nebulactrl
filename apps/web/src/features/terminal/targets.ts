// Which replica a shell or the file browser attaches to, for a service whose
// containers belong to processes. The control plane lists the replicas with
// their process (empty for a service without processes) and numbers each one
// inside its process, so a replica is addressed by process and index.

/** A replica as the terminal info lists it. */
export interface TargetReplica {
	index: number;
	/** The process the replica belongs to; "" for a service without processes. */
	process: string;
	running: boolean;
	state: string;
}

/** The processes that have replicas, in the order the list gives them. */
export function replicaProcesses(
	replicas: readonly Pick<TargetReplica, "process">[],
): string[] {
	const names: string[] = [];
	for (const replica of replicas) {
		if (replica.process !== "" && !names.includes(replica.process)) {
			names.push(replica.process);
		}
	}
	return names;
}

/** The replicas of one process; every replica for "" (a service without processes). */
export function replicasOfProcess<T extends Pick<TargetReplica, "process">>(
	replicas: readonly T[],
	process: string,
): T[] {
	return replicas.filter((replica) => replica.process === process);
}

/**
 * The process a panel starts on: the one it was opened for when it has
 * replicas, else the first process that has a running replica, else the
 * first process. "" when the service has none.
 */
export function startProcess(
	replicas: readonly TargetReplica[],
	preselected: string | null,
): string {
	const names = replicaProcesses(replicas);
	if (names.length === 0) return "";
	if (preselected !== null && names.includes(preselected)) return preselected;
	return (
		names.find((name) =>
			replicas.some((r) => r.process === name && r.running),
		) ??
		names[0] ??
		""
	);
}

/**
 * The replica a panel starts on within `process`: the preselected index when
 * that replica exists there, else the first running one, else the first.
 */
export function startReplica(
	replicas: readonly TargetReplica[],
	process: string,
	preselected: number | null,
): number {
	const own = replicasOfProcess(replicas, process);
	if (preselected !== null && own.some((r) => r.index === preselected)) {
		return preselected;
	}
	return own.find((r) => r.running)?.index ?? own[0]?.index ?? 0;
}

/** "worker · replica 1" in a service with processes, "Replica 1" otherwise. */
export function targetLabel(process: string, index: number): string {
	return process === "" ? `Replica ${index}` : `${process} · replica ${index}`;
}

/** The query of the file endpoints and the socket: the process only when there is one. */
export function processQuery(process: string): Record<string, string> {
	return process === "" ? {} : { process };
}
