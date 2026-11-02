import { describe, expect, test } from "bun:test";
import { QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { stubApi } from "#/test/api";
import { testQueryClient } from "#/test/render";
import { useFileNotes } from "./use-file-notes";

function wrapper({ children }: { children: ReactNode }) {
	return (
		<QueryClientProvider client={testQueryClient()}>
			{children}
		</QueryClientProvider>
	);
}

const lines = [
	{ field: "config.port", label: "port 8080" },
	{ field: "config.cpus", label: "cpus 2", note: "was 1" },
	{ field: "scale", label: "scale 3" },
];

describe("useFileNotes", () => {
	test("adds the file note to the owned fields and keeps the others as they are", async () => {
		stubApi({
			"GET /services/:id/config-source": {
				body: {
					managed: true,
					state: "in_sync",
					fields: [
						{ field: "config.port", state: "in_sync" },
						{ field: "config.cpus", state: "in_sync" },
					],
					pending: [],
				},
			},
		});
		const { result } = renderHook(() => useFileNotes("svc_1", lines, true), {
			wrapper,
		});
		await waitFor(() =>
			expect(result.current[0]?.note).toBe("set by nebula.toml"),
		);
		expect(result.current[1]?.note).toBe("was 1 · set by nebula.toml");
		expect(result.current[2]).toEqual({ field: "scale", label: "scale 3" });
	});

	test("asks nothing while disabled and returns the lines untouched", async () => {
		const api = stubApi({});
		const { result } = renderHook(() => useFileNotes("svc_1", lines, false), {
			wrapper,
		});
		expect(result.current).toEqual(lines);
		expect(api.calls).toEqual([]);
		expect(api.unmatched).toEqual([]);
	});

	test("a service the file does not own gets no notes", async () => {
		const api = stubApi({
			"GET /services/:id/config-source": {
				body: { managed: false, fields: null, pending: null },
			},
		});
		const { result } = renderHook(() => useFileNotes("svc_1", lines, true), {
			wrapper,
		});
		await waitFor(() => expect(api.calls).toHaveLength(1));
		await act(async () => {});
		expect(result.current).toEqual(lines);
	});
});
