import { describe, expect, test } from "bun:test";
import type { Service } from "#/api/models";
import { orderedReplicas, replicaLabel } from "./replicas";

type Replica = NonNullable<
	NonNullable<Service["observed"]>["replicas"]
>[number];

function replica(process: string, index: number): Replica {
	return {
		component: "",
		containerId: `c-${process}-${index}`,
		health: "healthy",
		index,
		process,
		restartCount: 0,
		startedAt: null,
		state: "running",
	};
}

function source(
	replicas: Replica[] | null | undefined,
	processNames: string[],
): Pick<Service, "observed" | "processes"> {
	return {
		observed:
			replicas === undefined
				? null
				: ({ replicas } as unknown as Service["observed"]),
		processes: processNames.map((name) => ({ name })) as Service["processes"],
	};
}

const keys = (replicas: Replica[]) =>
	replicas.map((r) => `${r.process}#${r.index}`);

describe("orderedReplicas", () => {
	test("a service without processes keeps the order the agent reported", () => {
		const reported = [replica("", 2), replica("", 0), replica("", 1)];
		expect(keys(orderedReplicas(source(reported, [])))).toEqual([
			"#2",
			"#0",
			"#1",
		]);
	});

	test("orders by process in rollout order, then by index inside the process", () => {
		const reported = [
			replica("worker", 1),
			replica("web", 1),
			replica("clock", 0),
			replica("worker", 0),
			replica("web", 0),
		];
		expect(
			keys(orderedReplicas(source(reported, ["web", "worker", "clock"]))),
		).toEqual(["web#0", "web#1", "worker#0", "worker#1", "clock#0"]);
	});

	test("replicas of an unknown process sort after the known ones", () => {
		const reported = [replica("ghost", 0), replica("web", 0), replica("", 0)];
		expect(keys(orderedReplicas(source(reported, ["web"])))).toEqual([
			"web#0",
			"ghost#0",
			"#0",
		]);
	});

	test("does not reorder the service's own list", () => {
		const reported = [replica("b", 0), replica("a", 0)];
		const service = source(reported, ["a", "b"]);
		orderedReplicas(service);
		expect(keys(service.observed?.replicas ?? [])).toEqual(["b#0", "a#0"]);
	});

	test("nothing observed yields no replicas", () => {
		expect(orderedReplicas(source(undefined, ["web"]))).toEqual([]);
		expect(orderedReplicas(source(undefined, []))).toEqual([]);
	});
});

describe("replicaLabel", () => {
	test("a plain service numbers its replicas", () => {
		expect(replicaLabel({ process: "", index: 3 })).toBe("#3");
	});

	test("a replica of a named process says which", () => {
		expect(replicaLabel({ process: "worker", index: 1 })).toBe(
			"worker · replica 1",
		);
	});
});
