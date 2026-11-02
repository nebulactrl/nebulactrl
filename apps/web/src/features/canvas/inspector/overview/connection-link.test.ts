import { afterEach, beforeEach, describe, expect, spyOn, test } from "bun:test";
import { openConnectionVariables } from "./connection-link";

let frames: FrameRequestCallback[];
let clock: number;

beforeEach(() => {
	frames = [];
	clock = 5000;
	spyOn(window, "requestAnimationFrame").mockImplementation((callback) => {
		frames.push(callback);
		return frames.length;
	});
	spyOn(performance, "now").mockImplementation(() => clock);
	window.history.replaceState(null, "", "/p/a/b?tab=variables");
});

afterEach(() => {
	document.body.innerHTML = "";
	window.history.replaceState(null, "", "/");
});

/** Runs the frame that is waiting, if any. */
function nextFrame(): boolean {
	const callback = frames.shift();
	if (!callback) return false;
	callback(clock);
	return true;
}

describe("openConnectionVariables", () => {
	test("switches to the Variables tab straight away", () => {
		const tabs: string[] = [];
		openConnectionVariables((tab) => tabs.push(tab));
		expect(tabs).toEqual(["variables"]);
	});

	test("scrolls the Connection section to the top and records it in the URL once it mounts", () => {
		openConnectionVariables(() => {});
		const section = document.createElement("section");
		section.id = "connection";
		const scrolled: ScrollIntoViewOptions[] = [];
		section.scrollIntoView = (options) => {
			scrolled.push(options as ScrollIntoViewOptions);
		};
		// Not mounted on the first frame: it keeps looking.
		expect(nextFrame()).toBe(true);
		expect(scrolled).toEqual([]);
		document.body.append(section);
		clock += 16;
		expect(nextFrame()).toBe(true);
		expect(scrolled).toEqual([{ block: "start" }]);
		expect(window.location.pathname).toBe("/p/a/b");
		expect(window.location.search).toBe("?tab=variables");
		expect(window.location.hash).toBe("#connection");
		// Found: no further frames are asked for.
		expect(frames).toHaveLength(0);
	});

	test("gives up after a second without finding the section", () => {
		openConnectionVariables(() => {});
		clock += 999;
		expect(nextFrame()).toBe(true);
		expect(frames).toHaveLength(1);
		clock += 2;
		expect(nextFrame()).toBe(true);
		expect(frames).toHaveLength(0);
		expect(window.location.hash).toBe("");
	});
});
