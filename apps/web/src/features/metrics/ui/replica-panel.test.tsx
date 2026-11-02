import { describe, expect, test } from "bun:test";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { type ReplicaBar, replicaColor } from "../model";
import { ReplicaFilter, ReplicaList } from "./replica-panel";

const REPLICAS = [{ index: 0 }, { index: 1 }, { index: 2 }];

function filter(
	picked: number[],
	over: Partial<Parameters<typeof ReplicaFilter>[0]> = {},
) {
	const calls: ReadonlySet<number>[] = [];
	const result = render(
		<ReplicaFilter
			replicas={REPLICAS}
			picked={new Set(picked)}
			onPickedChange={(next) => calls.push(next)}
			{...over}
		/>,
	);
	return { calls, ...result };
}

describe("ReplicaFilter", () => {
	test("lists All and one chip per replica, All pressed when nothing is picked", () => {
		filter([]);
		const list = screen.getByRole("list", { name: "Replicas" });
		expect(
			within(list)
				.getAllByRole("button")
				.map((b) => b.textContent),
		).toEqual(["All", "replica 0", "replica 1", "replica 2"]);
		expect(screen.getByRole("button", { name: "All" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		expect(screen.getByRole("button", { name: "replica 1" })).toHaveAttribute(
			"aria-pressed",
			"false",
		);
	});

	test("names replicas with labelFor, e.g. a stack's components", () => {
		filter([], { labelFor: (i) => ["web", "db", "cache"][i] ?? `#${i}` });
		expect(screen.getByRole("button", { name: "db" })).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "replica 1" }),
		).not.toBeInTheDocument();
	});

	test("each chip's swatch carries that replica's colour", () => {
		filter([]);
		const swatch = screen
			.getByRole("button", { name: "replica 1" })
			.querySelector("span");
		expect((swatch as HTMLElement).style.backgroundColor).toBe(replicaColor(1));
	});

	test("clicking a chip adds that replica to the pick", async () => {
		const { calls } = filter([0]);
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "replica 2" }));
		expect([...(calls[0] ?? [])].sort()).toEqual([0, 2]);
	});

	test("clicking a picked chip removes it", async () => {
		const { calls } = filter([0, 1]);
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "replica 1" }));
		expect([...(calls[0] ?? [])]).toEqual([0]);
	});

	test("picking every replica collapses to All (an empty pick)", async () => {
		const { calls } = filter([0, 2]);
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "replica 1" }));
		expect(calls[0]?.size).toBe(0);
	});

	test("clicking All clears the pick", async () => {
		const { calls } = filter([1]);
		await userEvent.setup().click(screen.getByRole("button", { name: "All" }));
		expect(calls).toHaveLength(1);
		expect(calls[0]?.size).toBe(0);
	});

	test("picked chips show as pressed and All does not", () => {
		filter([1]);
		expect(screen.getByRole("button", { name: "replica 1" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		expect(screen.getByRole("button", { name: "All" })).toHaveAttribute(
			"aria-pressed",
			"false",
		);
	});
});

const bar = (over: Partial<ReplicaBar> = {}): ReplicaBar => ({
	index: 0,
	cpuPercent: 42.46,
	memMb: 256,
	cpuFrac: 0.4246,
	memFrac: 0.5,
	...over,
});

describe("ReplicaList", () => {
	test("one row per replica with its CPU to one decimal and memory in MiB", () => {
		render(
			<ReplicaList
				bars={[bar(), bar({ index: 1, cpuPercent: 7, memMb: 2048 })]}
				cpuHint="of 200% quota"
				memLimitMb={512}
			/>,
		);
		const rows = within(
			screen.getByRole("list", { name: "Per-replica usage now" }),
		).getAllByRole("listitem");
		expect(rows).toHaveLength(2);
		expect(rows[0]).toHaveTextContent("replica 0");
		expect(
			within(rows[0] as HTMLElement).getByText("42.5%"),
		).toBeInTheDocument();
		expect(
			within(rows[0] as HTMLElement).getByText("256 MiB"),
		).toBeInTheDocument();
		expect(rows[1]).toHaveTextContent("replica 1");
		expect(
			within(rows[1] as HTMLElement).getByText("7.0%"),
		).toBeInTheDocument();
		expect(
			within(rows[1] as HTMLElement).getByText("2 GiB"),
		).toBeInTheDocument();
	});

	test("hints say what a full bar means: the CPU quota and the memory limit", () => {
		render(
			<ReplicaList bars={[bar()]} cpuHint="of 200% quota" memLimitMb={512} />,
		);
		expect(screen.getByText("42.5%")).toHaveAttribute("title", "of 200% quota");
		expect(screen.getByText("256 MiB")).toHaveAttribute(
			"title",
			"of 512 MiB limit",
		);
	});

	test("without a memory limit the hint says no limit and no memory bar is drawn", () => {
		render(
			<ReplicaList
				bars={[bar({ memFrac: null })]}
				cpuHint="of one core = 100%"
				memLimitMb={null}
			/>,
		);
		expect(screen.getByText("256 MiB")).toHaveAttribute("title", "no limit");
		expect(screen.getByText("42.5%")).toHaveAttribute(
			"title",
			"of one core = 100%",
		);
		expect(screen.getAllByRole("progressbar")).toHaveLength(1);
	});

	test("bars carry the figure in their accessible name", () => {
		render(<ReplicaList bars={[bar()]} cpuHint="x" memLimitMb={512} />);
		expect(
			screen.getByRole("progressbar", { name: "CPU 42.5%" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("progressbar", { name: "Mem 256 MiB" }),
		).toBeInTheDocument();
	});

	test("labelFor names the rows", () => {
		render(
			<ReplicaList
				bars={[bar({ index: 1 })]}
				cpuHint="x"
				memLimitMb={null}
				labelFor={(i) => `worker · replica ${i}`}
			/>,
		);
		expect(screen.getByText("worker · replica 1")).toBeInTheDocument();
		expect(screen.queryByText("replica 1")).not.toBeInTheDocument();
	});

	test("no replicas gives an empty list", () => {
		render(<ReplicaList bars={[]} cpuHint="x" memLimitMb={null} />);
		expect(screen.queryAllByRole("listitem")).toHaveLength(0);
	});
});
