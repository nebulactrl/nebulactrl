import { describe, expect, test } from "bun:test";
import { capturePointer, holdSelection, releasePointer } from "./touch";

describe("holdSelection", () => {
	test("suppresses selection while the gesture runs and the release restores the element", () => {
		const element = document.createElement("div");
		const release = holdSelection(element);
		expect(element.style.getPropertyValue("user-select")).toBe("none");
		release();
		expect(element.style.getPropertyValue("user-select")).toBe("");
	});
});

describe("capturePointer", () => {
	test("captures the pointer on the element", () => {
		const captured: number[] = [];
		const element = document.createElement("div");
		element.setPointerCapture = (id) => {
			captured.push(id);
		};
		capturePointer(element, 7);
		expect(captured).toEqual([7]);
	});

	test("a pointer that is already gone (WebKit NotFoundError) does not throw", () => {
		const element = document.createElement("div");
		element.setPointerCapture = () => {
			throw new DOMException("gone", "NotFoundError");
		};
		expect(() => capturePointer(element, 1)).not.toThrow();
	});
});

describe("releasePointer", () => {
	test("releases a capture the element holds", () => {
		const released: number[] = [];
		const element = document.createElement("div");
		element.hasPointerCapture = () => true;
		element.releasePointerCapture = (id) => {
			released.push(id);
		};
		releasePointer(element, 3);
		expect(released).toEqual([3]);
	});

	test("leaves an element that holds no capture alone", () => {
		const released: number[] = [];
		const element = document.createElement("div");
		element.hasPointerCapture = () => false;
		element.releasePointerCapture = (id) => {
			released.push(id);
		};
		releasePointer(element, 3);
		expect(released).toEqual([]);
	});

	test("a stale pointer does not throw", () => {
		const element = document.createElement("div");
		element.hasPointerCapture = () => {
			throw new DOMException("stale", "NotFoundError");
		};
		expect(() => releasePointer(element, 3)).not.toThrow();
	});
});
