import { afterEach, describe, expect, it } from "bun:test";
import { act, renderHook } from "@testing-library/react";
import type { ServiceLink } from "#/api/models";
import { linkActions, useLinkState } from "./store";
import { useLinkReview } from "./use-link-review";

const link = { id: "l1", fromServiceId: "a", toServiceId: "b" } as ServiceLink;

afterEach(() => {
	linkActions.closeReferences();
});

describe("link store", () => {
	it("starts with no references sheet open", () => {
		const { result } = renderHook(() => useLinkState());
		expect(result.current.references).toBeNull();
	});

	it("opens and closes the references sheet for every subscriber", () => {
		const first = renderHook(() => useLinkState());
		const second = renderHook(() => useLinkState());
		act(() => linkActions.openReferences(link));
		expect(first.result.current.references).toBe(link);
		expect(second.result.current.references).toBe(link);
		act(() => linkActions.closeReferences());
		expect(first.result.current.references).toBeNull();
		expect(second.result.current.references).toBeNull();
	});

	it("stops notifying an unmounted subscriber", () => {
		let renders = 0;
		const { unmount } = renderHook(() => {
			renders += 1;
			return useLinkState();
		});
		unmount();
		const before = renders;
		act(() => linkActions.openReferences(link));
		expect(renders).toBe(before);
	});

	it("ignores a request while no host is mounted", () => {
		expect(() => linkActions.request("a", "b")).not.toThrow();
	});

	it("hands a request to the registered host, with its anchor", () => {
		const seen: unknown[][] = [];
		const unregister = linkActions.registerReviewer((...args) =>
			seen.push(args),
		);
		linkActions.request("a", "b");
		linkActions.request("c", "d", { x: 1, y: 2 });
		expect(seen).toEqual([
			["a", "b", undefined],
			["c", "d", { x: 1, y: 2 }],
		]);
		unregister();
		linkActions.request("e", "f");
		expect(seen).toHaveLength(2);
	});

	it("keeps the newer host when an older one unregisters late", () => {
		const older: string[] = [];
		const newer: string[] = [];
		const unregisterOlder = linkActions.registerReviewer((from) =>
			older.push(from),
		);
		const unregisterNewer = linkActions.registerReviewer((from) =>
			newer.push(from),
		);
		unregisterOlder();
		linkActions.request("x", "y");
		expect(older).toEqual([]);
		expect(newer).toEqual(["x"]);
		unregisterNewer();
	});
});

describe("useLinkReview", () => {
	it("routes the canvas's calls into the store, with a stable api", () => {
		const { result, rerender } = renderHook(() => useLinkReview());
		const first = result.current;
		rerender();
		expect(result.current).toBe(first);

		const seen: string[] = [];
		const unregister = linkActions.registerReviewer((from, to) =>
			seen.push(`${from}>${to}`),
		);
		result.current.request("a", "b");
		expect(seen).toEqual(["a>b"]);
		unregister();

		const state = renderHook(() => useLinkState());
		act(() => result.current.openReferences(link));
		expect(state.result.current.references).toBe(link);
	});
});
