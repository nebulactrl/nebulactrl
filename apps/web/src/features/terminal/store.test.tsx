import { afterEach, describe, expect, test } from "bun:test";
import { act, renderHook } from "@testing-library/react";
import {
	closeFiles,
	closeHostShell,
	closeShell,
	openFiles,
	openHostShell,
	openShell,
	useFilesProcess,
	useFilesReplica,
	useFilesTarget,
	useHostShellTarget,
	useShellProcess,
	useShellReplica,
	useShellTarget,
} from "./store";

afterEach(() => {
	act(() => {
		closeShell();
		closeFiles();
		closeHostShell();
	});
});

function shellState() {
	return renderHook(() => ({
		target: useShellTarget(),
		replica: useShellReplica(),
		process: useShellProcess(),
	}));
}

function filesState() {
	return renderHook(() => ({
		target: useFilesTarget(),
		replica: useFilesReplica(),
		process: useFilesProcess(),
	}));
}

describe("terminal panel store", () => {
	test("is closed until opened", () => {
		const { result } = shellState();
		expect(result.current).toEqual({
			target: null,
			replica: null,
			process: null,
		});
	});

	test("openShell without options targets the service with no preselection", () => {
		const { result } = shellState();
		act(() => openShell("svc-1"));
		expect(result.current).toEqual({
			target: "svc-1",
			replica: null,
			process: null,
		});
	});

	test("openShell keeps the process and a replica index of 0", () => {
		const { result } = shellState();
		act(() => openShell("svc-1", { process: "worker", replica: 0 }));
		expect(result.current).toEqual({
			target: "svc-1",
			replica: 0,
			process: "worker",
		});
	});

	test("opening another service replaces the whole target", () => {
		const { result } = shellState();
		act(() => openShell("svc-1", { process: "worker", replica: 2 }));
		act(() => openShell("svc-2"));
		expect(result.current).toEqual({
			target: "svc-2",
			replica: null,
			process: null,
		});
	});

	test("closeShell clears the target, replica and process", () => {
		const { result } = shellState();
		act(() => openShell("svc-1", { process: "worker", replica: 1 }));
		act(() => closeShell());
		expect(result.current).toEqual({
			target: null,
			replica: null,
			process: null,
		});
	});
});

describe("file browser store", () => {
	test("opens and closes independently of the terminal", () => {
		const shell = shellState();
		const files = filesState();
		act(() => openFiles("svc-9", { process: "web", replica: 3 }));
		expect(files.result.current).toEqual({
			target: "svc-9",
			replica: 3,
			process: "web",
		});
		expect(shell.result.current.target).toBeNull();

		act(() => openShell("svc-1"));
		act(() => closeFiles());
		expect(files.result.current.target).toBeNull();
		expect(shell.result.current.target).toBe("svc-1");
	});

	test("openFiles without options leaves replica and process unset", () => {
		const { result } = filesState();
		act(() => openFiles("svc-1"));
		expect(result.current).toEqual({
			target: "svc-1",
			replica: null,
			process: null,
		});
	});
});

describe("host shell store", () => {
	test("holds the server being opened and clears on close", () => {
		const { result } = renderHook(() => useHostShellTarget());
		expect(result.current).toBeNull();
		act(() => openHostShell("srv-1"));
		expect(result.current).toBe("srv-1");
		act(() => closeHostShell());
		expect(result.current).toBeNull();
	});
});
