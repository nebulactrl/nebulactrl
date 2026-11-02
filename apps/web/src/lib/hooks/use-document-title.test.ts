import { afterEach, describe, expect, test } from "bun:test";
import { renderHook } from "@testing-library/react";
import { useDocumentTitle } from "./use-document-title";

afterEach(() => {
	document.title = "";
});

describe("useDocumentTitle", () => {
	test("puts the most specific part first and the app name last", () => {
		renderHook(() => useDocumentTitle(["web", "Shop"]));
		expect(document.title).toBe("web · Shop · NebulaCtrl");
	});

	test("skips parts that are still loading", () => {
		renderHook(() => useDocumentTitle([undefined, "", "Shop"]));
		expect(document.title).toBe("Shop · NebulaCtrl");
	});

	test("with no parts the title is the app name", () => {
		renderHook(() => useDocumentTitle([]));
		expect(document.title).toBe("NebulaCtrl");
	});

	test("follows its parts as they arrive", () => {
		const { rerender } = renderHook(
			({ name }: { name: string | undefined }) => useDocumentTitle([name]),
			{ initialProps: { name: undefined as string | undefined } },
		);
		expect(document.title).toBe("NebulaCtrl");
		rerender({ name: "Billing" });
		expect(document.title).toBe("Billing · NebulaCtrl");
	});

	test("unmounting restores the title it replaced", () => {
		document.title = "Before";
		const { unmount } = renderHook(() => useDocumentTitle(["Servers"]));
		expect(document.title).toBe("Servers · NebulaCtrl");
		unmount();
		expect(document.title).toBe("Before");
	});
});
