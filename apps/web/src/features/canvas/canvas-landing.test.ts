import { afterEach, describe, expect, it, jest, mock, test } from "bun:test";
import type { CanvasDocument } from "#/api/models";
import {
	fitWhenMeasured,
	type Landing,
	landOnCanvas,
	registerLanding,
} from "./canvas-landing";

const landing: Landing = {
	document: { layout: { nodes: {}, groups: [] }, version: 7 } as CanvasDocument,
	groupId: "g-1",
	serviceIds: ["a", "b"],
};

describe("landOnCanvas", () => {
	test("reports false when no canvas is mounted", () => {
		expect(landOnCanvas(landing)).toBe(false);
	});

	test("hands the landing to the mounted canvas", () => {
		const received: Landing[] = [];
		const release = registerLanding((next) => received.push(next));
		expect(landOnCanvas(landing)).toBe(true);
		expect(received).toEqual([landing]);
		release();
	});

	test("letting go stops landings", () => {
		const received: Landing[] = [];
		registerLanding((next) => received.push(next))();
		expect(landOnCanvas(landing)).toBe(false);
		expect(received).toEqual([]);
	});

	test("the newest canvas takes the landing, and an older one's release leaves it registered", () => {
		const first: Landing[] = [];
		const second: Landing[] = [];
		const releaseFirst = registerLanding((next) => first.push(next));
		const releaseSecond = registerLanding((next) => second.push(next));
		releaseFirst();
		expect(landOnCanvas(landing)).toBe(true);
		expect(first).toEqual([]);
		expect(second).toEqual([landing]);
		releaseSecond();
		expect(landOnCanvas(landing)).toBe(false);
	});
});

describe("fitWhenMeasured", () => {
	afterEach(() => {
		jest.restoreAllMocks();
	});

	const options = (id: string, duration: number) => ({
		nodes: [{ id }],
		padding: 0.25,
		maxZoom: 1,
		duration,
	});

	it("fits at once when the node is already measured", () => {
		const fitView = mock(() => undefined);
		fitWhenMeasured(
			{ getInternalNode: () => ({ measured: { width: 280 } }), fitView },
			"group:g-1",
			300,
		);
		expect(fitView).toHaveBeenCalledTimes(1);
		expect(fitView).toHaveBeenCalledWith(options("group:g-1", 300));
	});

	it("waits a frame at a time until the node has a width, then fits", () => {
		const frames: FrameRequestCallback[] = [];
		jest
			.spyOn(window, "requestAnimationFrame")
			.mockImplementation((callback) => frames.push(callback));
		const measured: { width?: number } = {};
		const fitView = mock(() => undefined);
		const flow = { getInternalNode: () => ({ measured }), fitView };

		fitWhenMeasured(flow, "g", 0);
		expect(fitView).not.toHaveBeenCalled();
		expect(frames).toHaveLength(1);

		frames.shift()?.(0);
		expect(fitView).not.toHaveBeenCalled();
		expect(frames).toHaveLength(1);

		measured.width = 100;
		frames.shift()?.(0);
		expect(fitView).toHaveBeenCalledWith(options("g", 0));
		expect(frames).toHaveLength(0);
	});

	it("waits for a node React Flow has not created yet", () => {
		const frames: FrameRequestCallback[] = [];
		jest
			.spyOn(window, "requestAnimationFrame")
			.mockImplementation((callback) => frames.push(callback));
		let node: { measured: { width?: number } } | undefined;
		const fitView = mock(() => undefined);
		fitWhenMeasured({ getInternalNode: () => node, fitView }, "g", 5);
		node = { measured: { width: 10 } };
		frames.shift()?.(0);
		expect(fitView).toHaveBeenCalledWith(options("g", 5));
	});

	it("gives up after the frame budget when the node never measures", () => {
		const frames: FrameRequestCallback[] = [];
		jest
			.spyOn(window, "requestAnimationFrame")
			.mockImplementation((callback) => frames.push(callback));
		const fitView = mock(() => undefined);
		fitWhenMeasured(
			{ getInternalNode: () => ({ measured: {} }), fitView },
			"g",
			0,
			3,
		);
		let ran = 0;
		while (frames.length > 0) {
			frames.shift()?.(0);
			ran += 1;
		}
		expect(ran).toBe(3);
		expect(fitView).not.toHaveBeenCalled();
	});
});
