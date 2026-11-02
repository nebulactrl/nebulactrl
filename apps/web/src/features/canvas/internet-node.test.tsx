import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import { ReactFlow, ReactFlowProvider } from "@xyflow/react";
import { CanvasUiContext, DIMMED_OPACITY } from "./canvas-context";
import { InternetNode } from "./internet-node";
import type { InternetSummary } from "./lib/internet";

const nodeTypes = { internet: InternetNode };

function show(
	summary: InternetSummary,
	matchIds: ReadonlySet<string> | null = null,
) {
	const ui = {
		matchIds,
		tick: 0,
		toggleGroup() {},
		renameGroup() {},
		ungroup() {},
		renamingGroupId: null,
		finishRenameRequest() {},
	};
	return render(
		<div style={{ width: 800, height: 600 }}>
			<CanvasUiContext.Provider value={ui}>
				<ReactFlowProvider>
					<ReactFlow
						nodes={[
							{
								id: "internet:public",
								type: "internet",
								position: { x: 0, y: 0 },
								data: { summary },
							},
						]}
						edges={[]}
						nodeTypes={nodeTypes}
					/>
				</ReactFlowProvider>
			</CanvasUiContext.Provider>
		</div>,
	);
}

describe("InternetNode", () => {
	test("names the Internet and counts its public domains", () => {
		show({ domains: ["a.example.com", "b.example.com"], serviceIds: ["s1"] });
		expect(screen.getByText("Internet")).toBeInTheDocument();
		expect(screen.getByText("2 public domains")).toBeInTheDocument();
	});

	test("a single domain is singular", () => {
		show({ domains: ["a.example.com"], serviceIds: ["s1"] });
		expect(screen.getByText("1 public domain")).toBeInTheDocument();
	});

	test("shows the first two hostnames as chips and folds the rest into '+N more'", () => {
		show({
			domains: [
				"a.example.com",
				"b.example.com",
				"c.example.com",
				"d.example.com",
			],
			serviceIds: ["s1", "s2"],
		});
		expect(screen.getByText("a.example.com")).toBeInTheDocument();
		expect(screen.getByText("b.example.com")).toBeInTheDocument();
		expect(screen.queryByText("c.example.com")).not.toBeInTheDocument();
		expect(screen.getByText("+2 more")).toBeInTheDocument();
	});

	test("no fold line when every hostname fits", () => {
		show({ domains: ["a.example.com", "b.example.com"], serviceIds: ["s1"] });
		expect(screen.queryByText(/more$/)).not.toBeInTheDocument();
	});

	test("has the Internet entry jack at its bottom edge", () => {
		show({ domains: ["a.example.com"], serviceIds: ["s1"] });
		expect(screen.getByLabelText("Internet entry")).toBeInTheDocument();
	});

	test("is full strength when nothing is filtered", () => {
		const { container } = show({
			domains: ["a.example.com"],
			serviceIds: ["s1"],
		});
		expect(
			container.querySelector<HTMLElement>("[data-internet-node]")?.style
				.opacity,
		).toBe("1");
	});

	test("dims when none of the services it feeds match the filter", () => {
		const { container } = show(
			{ domains: ["a.example.com"], serviceIds: ["s1"] },
			new Set(["other"]),
		);
		expect(
			container.querySelector<HTMLElement>("[data-internet-node]")?.style
				.opacity,
		).toBe(String(DIMMED_OPACITY));
	});

	test("stays lit while one service it feeds matches", () => {
		const { container } = show(
			{ domains: ["a", "b"], serviceIds: ["s1", "s2"] },
			new Set(["s2"]),
		);
		expect(
			container.querySelector<HTMLElement>("[data-internet-node]")?.style
				.opacity,
		).toBe("1");
	});
});
