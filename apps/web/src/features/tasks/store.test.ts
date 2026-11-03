import { afterEach, describe, expect, test } from "bun:test";
import { act, renderHook } from "@testing-library/react";
import {
	closeRunCommand,
	closeRunHistory,
	closeTaskDialog,
	openRunCommand,
	openRunHistory,
	openTaskDialog,
	useRunCommandTarget,
	useRunHistoryTarget,
	useTaskDialogTarget,
} from "./store";

afterEach(() => {
	act(() => {
		closeRunCommand();
		closeRunHistory();
		closeTaskDialog();
	});
});

describe("run command target", () => {
	test("is closed until a service opens it, and closing clears it", () => {
		const { result } = renderHook(() => useRunCommandTarget());
		expect(result.current).toBeNull();
		act(() => openRunCommand("svc-1"));
		expect(result.current).toBe("svc-1");
		act(() => closeRunCommand());
		expect(result.current).toBeNull();
	});

	test("opening for another service switches the target", () => {
		const { result } = renderHook(() => useRunCommandTarget());
		act(() => openRunCommand("svc-1"));
		act(() => openRunCommand("svc-2"));
		expect(result.current).toBe("svc-2");
	});

	test("every mounted reader sees the change", () => {
		const a = renderHook(() => useRunCommandTarget());
		const b = renderHook(() => useRunCommandTarget());
		act(() => openRunCommand("svc-9"));
		expect(a.result.current).toBe("svc-9");
		expect(b.result.current).toBe("svc-9");
	});

	test("an unmounted reader no longer re-renders", () => {
		let renders = 0;
		const { unmount } = renderHook(() => {
			renders += 1;
			return useRunCommandTarget();
		});
		unmount();
		const before = renders;
		act(() => openRunCommand("svc-1"));
		expect(renders).toBe(before);
	});
});

describe("run history target", () => {
	test("opens for a whole service without a task id", () => {
		const { result } = renderHook(() => useRunHistoryTarget());
		expect(result.current).toBeNull();
		act(() => openRunHistory("svc-1"));
		expect(result.current).toEqual({ serviceId: "svc-1" });
		expect(result.current).not.toHaveProperty("taskId");
	});

	test("opens for one task and closes again", () => {
		const { result } = renderHook(() => useRunHistoryTarget());
		act(() => openRunHistory("svc-1", "task-1"));
		expect(result.current).toEqual({ serviceId: "svc-1", taskId: "task-1" });
		act(() => closeRunHistory());
		expect(result.current).toBeNull();
	});

	test("is independent of the run command target", () => {
		const history = renderHook(() => useRunHistoryTarget());
		const command = renderHook(() => useRunCommandTarget());
		act(() => openRunCommand("svc-1"));
		expect(history.result.current).toBeNull();
		act(() => openRunHistory("svc-2"));
		expect(command.result.current).toBe("svc-1");
	});
});

describe("task dialog target", () => {
	test("is closed until a service opens it, and closing clears it", () => {
		const { result } = renderHook(() => useTaskDialogTarget());
		expect(result.current).toBeNull();
		act(() => openTaskDialog("svc-1"));
		expect(result.current).toBe("svc-1");
		act(() => closeTaskDialog());
		expect(result.current).toBeNull();
	});

	test("is independent of the run command target", () => {
		const task = renderHook(() => useTaskDialogTarget());
		const command = renderHook(() => useRunCommandTarget());
		act(() => openTaskDialog("svc-1"));
		expect(command.result.current).toBeNull();
		act(() => openRunCommand("svc-2"));
		expect(task.result.current).toBe("svc-1");
		act(() => closeTaskDialog());
	});
});
