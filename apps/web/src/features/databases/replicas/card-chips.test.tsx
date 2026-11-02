import { describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { ReplicaChip } from "./card-chips";
import { member, postgresService, replication } from "./replicas.fixture";

const MIB = 1024 * 1024;
const ago = (ms: number) => new Date(Date.now() - ms).toISOString();
const REPLICATION = "GET /services/:id/database/replication";

function chip(container: HTMLElement) {
	return container.querySelector<HTMLElement>('[data-slot="replica-chip"]');
}

describe("ReplicaChip", () => {
	test("a primary counts its replicas", async () => {
		const api = stubApi({
			[REPLICATION]: {
				body: replication({
					role: "primary",
					replicas: [member({ id: "a" }), member({ id: "b", slug: "r2" })],
				}),
			},
		});
		const { container } = await renderWithProviders(
			<ReplicaChip service={postgresService()} />,
		);
		expect(await screen.findByText("Primary · 2 replicas")).toBeInTheDocument();
		expect(chip(container)).toHaveAttribute("data-kind", "primary");
		expect(api.calls[0]?.path).toBe(
			"/services/svc-primary/database/replication",
		);
	});

	test("one replica reads in the singular", async () => {
		stubApi({
			[REPLICATION]: {
				body: replication({ role: "primary", replicas: [member()] }),
			},
		});
		await renderWithProviders(<ReplicaChip service={postgresService()} />);
		expect(await screen.findByText("Primary · 1 replica")).toBeInTheDocument();
	});

	test("a primary warns when one of its replicas is broken", async () => {
		stubApi({
			[REPLICATION]: {
				body: replication({
					role: "primary",
					replicas: [
						member({ id: "ok" }),
						member({ id: "bad", state: "needs_rebuild" }),
					],
				}),
			},
		});
		const { container } = await renderWithProviders(
			<ReplicaChip service={postgresService()} />,
		);
		await screen.findByText("Primary · 2 replicas");
		expect(chip(container)?.getAttribute("title")).toContain(
			"A replica needs attention: open the Replicas panel.",
		);
	});

	test("a primary whose replicas are all healthy carries no warning", async () => {
		stubApi({
			[REPLICATION]: {
				body: replication({
					role: "primary",
					replicas: [member({ observedAt: ago(1_000) })],
				}),
			},
		});
		const { container } = await renderWithProviders(
			<ReplicaChip service={postgresService()} />,
		);
		await screen.findByText("Primary · 1 replica");
		expect(chip(container)?.getAttribute("title")).toBe(
			"The writer of a read-replica group.",
		);
	});

	test("a replica names its primary and its lag", async () => {
		stubApi({
			[REPLICATION]: {
				body: replication({
					role: "replica",
					primary: { id: "svc-p", slug: "orders-db" },
					self: {
						state: "streaming",
						message: "",
						lagBytes: 2 * MIB,
						observedAt: ago(1_000),
					},
				}),
			},
		});
		const { container } = await renderWithProviders(
			<ReplicaChip service={postgresService({ id: "svc-r" })} />,
		);
		expect(await screen.findByText("Replica of orders-db")).toBeInTheDocument();
		expect(screen.getByText("· 2 MiB")).toBeInTheDocument();
		expect(chip(container)).toHaveAttribute("data-kind", "replica");
		expect(chip(container)?.getAttribute("title")).toBe(
			"Replica of orders-db: streaming",
		);
	});

	test("a replica with no lag says it is in sync", async () => {
		stubApi({
			[REPLICATION]: {
				body: replication({
					role: "replica",
					primary: { id: "svc-p", slug: "orders-db" },
					self: {
						state: "streaming",
						message: "",
						lagBytes: 0,
						observedAt: ago(1_000),
					},
				}),
			},
		});
		await renderWithProviders(<ReplicaChip service={postgresService()} />);
		expect(await screen.findByText("· in sync")).toBeInTheDocument();
	});

	test("a replica that is not streaming shows no lag", async () => {
		stubApi({
			[REPLICATION]: {
				body: replication({
					role: "replica",
					primary: { id: "svc-p", slug: "orders-db" },
					self: {
						state: "lagging",
						message: "",
						lagBytes: 300 * MIB,
						observedAt: ago(1_000),
					},
				}),
			},
		});
		const { container } = await renderWithProviders(
			<ReplicaChip service={postgresService()} />,
		);
		await screen.findByText("Replica of orders-db");
		expect(screen.queryByText(/MiB/)).not.toBeInTheDocument();
		expect(chip(container)?.getAttribute("title")).toBe(
			"Replica of orders-db: lagging",
		);
	});

	test("a reading older than a minute is shown as unknown, not as a fact", async () => {
		stubApi({
			[REPLICATION]: {
				body: replication({
					role: "replica",
					primary: { id: "svc-p", slug: "orders-db" },
					self: {
						state: "streaming",
						message: "",
						lagBytes: 5 * MIB,
						observedAt: ago(5 * 60_000),
					},
				}),
			},
		});
		const { container } = await renderWithProviders(
			<ReplicaChip service={postgresService()} />,
		);
		await screen.findByText("Replica of orders-db");
		expect(chip(container)?.getAttribute("title")).toBe(
			"Replica of orders-db: unknown",
		);
		expect(screen.queryByText(/MiB/)).not.toBeInTheDocument();
	});

	test("a fenced old primary says it is fenced", async () => {
		stubApi({ [REPLICATION]: { body: replication({ role: "fenced" }) } });
		const { container } = await renderWithProviders(
			<ReplicaChip service={postgresService()} />,
		);
		expect(await screen.findByText("Fenced")).toBeInTheDocument();
		expect(chip(container)).toHaveAttribute("data-kind", "fenced");
		expect(chip(container)?.getAttribute("title")).toContain(
			"a replica was promoted in its place",
		);
	});

	test("a standalone database draws nothing", async () => {
		const api = stubApi({ [REPLICATION]: { body: replication() } });
		const { container } = await renderWithProviders(
			<ReplicaChip service={postgresService()} />,
		);
		await waitFor(() => expect(api.calls).toHaveLength(1));
		expect(chip(container)).toBeNull();
	});

	test("a database that is not managed Postgres draws nothing and asks nothing", async () => {
		const api = stubApi({
			[REPLICATION]: { body: replication({ role: "primary" }) },
		});
		const { container } = await renderWithProviders(
			<ReplicaChip
				service={postgresService({
					config: { database: { engine: "mysql" } },
				} as never)}
			/>,
		);
		expect(chip(container)).toBeNull();
		expect(api.calls).toEqual([]);
	});

	test("a failed replication request draws nothing", async () => {
		stubApi({
			[REPLICATION]: {
				status: 500,
				body: { status: 500, title: "x", detail: "boom", code: "internal" },
			},
		});
		const { container, queryClient } = await renderWithProviders(
			<ReplicaChip service={postgresService()} />,
		);
		await waitFor(() =>
			expect(queryClient.getQueryCache().getAll()[0]?.state.status).toBe(
				"error",
			),
		);
		expect(chip(container)).toBeNull();
	});
});
