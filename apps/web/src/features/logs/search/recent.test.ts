import { afterEach, describe, expect, spyOn, test } from "bun:test";
import { loadRecent, pushRecent, saveRecent } from "./recent";

const KEY = "nebulactrl.logs.recent-queries";

afterEach(() => localStorage.clear());

describe("pushRecent", () => {
	test("puts the newest query first", () => {
		expect(pushRecent(["a", "b"], "c")).toEqual(["c", "a", "b"]);
	});

	test("moves an earlier copy to the front instead of duplicating it", () => {
		expect(pushRecent(["a", "b", "c"], "c")).toEqual(["c", "a", "b"]);
	});

	test("trims the query and ignores a blank one", () => {
		expect(pushRecent(["a"], "  b  ")).toEqual(["b", "a"]);
		expect(pushRecent(["a"], "   ")).toEqual(["a"]);
	});

	test("keeps only the newest ten", () => {
		const list = Array.from({ length: 10 }, (_, i) => `q${i}`);
		const next = pushRecent(list, "new");
		expect(next).toHaveLength(10);
		expect(next[0]).toBe("new");
		expect(next).not.toContain("q9");
	});

	test("never mutates the list it was given", () => {
		const list = ["a"];
		pushRecent(list, "b");
		pushRecent(list, " ");
		expect(list).toEqual(["a"]);
	});
});

describe("loadRecent / saveRecent", () => {
	test("a saved list loads back", () => {
		saveRecent(["level:error", "timeout"]);
		expect(loadRecent()).toEqual(["level:error", "timeout"]);
	});

	test("nothing saved is an empty list", () => {
		expect(loadRecent()).toEqual([]);
	});

	test("unreadable JSON is an empty list", () => {
		localStorage.setItem(KEY, "{not json");
		expect(loadRecent()).toEqual([]);
	});

	test("a stored value that is not a list is an empty list", () => {
		localStorage.setItem(KEY, JSON.stringify({ a: 1 }));
		expect(loadRecent()).toEqual([]);
	});

	test("non-string entries are dropped and at most ten are read", () => {
		const stored = [
			1,
			"a",
			null,
			...Array.from({ length: 12 }, (_, i) => `q${i}`),
		];
		localStorage.setItem(KEY, JSON.stringify(stored));
		const loaded = loadRecent();
		expect(loaded).toHaveLength(10);
		expect(loaded.slice(0, 3)).toEqual(["a", "q0", "q1"]);
	});

	test("a storage that refuses writes does not throw", () => {
		const spy = spyOn(Storage.prototype, "setItem").mockImplementation(() => {
			throw new Error("quota");
		});
		try {
			expect(() => saveRecent(["a"])).not.toThrow();
		} finally {
			spy.mockRestore();
		}
	});

	test("a storage that refuses reads gives an empty list", () => {
		const spy = spyOn(Storage.prototype, "getItem").mockImplementation(() => {
			throw new Error("denied");
		});
		try {
			expect(loadRecent()).toEqual([]);
		} finally {
			spy.mockRestore();
		}
	});
});
