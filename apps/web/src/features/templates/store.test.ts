import { afterEach, describe, expect, test } from "bun:test";
import { act, renderHook } from "@testing-library/react";
import { templatesActions, useTemplatesState } from "./store";
import { instance, resetTemplatesStore } from "./test-fixtures";

afterEach(() => act(() => resetTemplatesStore()));

describe("templates store", () => {
	test("opens on the gallery with clean filters", () => {
		const { result } = renderHook(() => useTemplatesState());
		act(() => {
			templatesActions.setQuery("umami");
			templatesActions.setCategory("analytics");
			templatesActions.setLicense("open");
		});
		act(() => templatesActions.open());
		expect(result.current).toMatchObject({
			open: true,
			templateId: null,
			query: "",
			category: null,
			license: null,
		});
	});

	test("opening with an id goes straight to that template's detail step", () => {
		const { result } = renderHook(() => useTemplatesState());
		act(() => templatesActions.open("umami"));
		expect(result.current.open).toBe(true);
		expect(result.current.templateId).toBe("umami");
	});

	test("select moves between detail and gallery, keeping the filters", () => {
		const { result } = renderHook(() => useTemplatesState());
		act(() => {
			templatesActions.open();
			templatesActions.setQuery("blog");
			templatesActions.setCategory("cms");
			templatesActions.setLicense("proprietary");
			templatesActions.select("ghost");
		});
		expect(result.current.templateId).toBe("ghost");
		act(() => templatesActions.select(null));
		expect(result.current).toMatchObject({
			templateId: null,
			query: "blog",
			category: "cms",
			license: "proprietary",
		});
	});

	test("close hides the dialog and leaves the step where it was", () => {
		const { result } = renderHook(() => useTemplatesState());
		act(() => templatesActions.open("umami"));
		act(() => templatesActions.close());
		expect(result.current.open).toBe(false);
		expect(result.current.templateId).toBe("umami");
	});

	test("delete and update requests are independent and cancellable", () => {
		const { result } = renderHook(() => useTemplatesState());
		const stack = instance({ id: "i1" });
		act(() => templatesActions.requestDelete(stack));
		expect(result.current.deleting).toBe(stack);
		expect(result.current.updating).toBeNull();
		act(() => templatesActions.requestUpdate(stack));
		expect(result.current.updating).toBe(stack);
		act(() => templatesActions.cancelDelete());
		expect(result.current.deleting).toBeNull();
		expect(result.current.updating).toBe(stack);
		act(() => templatesActions.cancelUpdate());
		expect(result.current.updating).toBeNull();
	});

	test("every mounted reader is told about a change, and an unmounted one is not", () => {
		const a = renderHook(() => useTemplatesState());
		const b = renderHook(() => useTemplatesState());
		act(() => templatesActions.setQuery("x"));
		expect(a.result.current.query).toBe("x");
		expect(b.result.current.query).toBe("x");
		a.unmount();
		act(() => templatesActions.setQuery("y"));
		expect(a.result.current.query).toBe("x");
		expect(b.result.current.query).toBe("y");
	});
});
