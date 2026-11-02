import { describe, expect, test } from "bun:test";
import { QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import type { InstanceBackupView, UpdateView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, type Overrides, problem } from "#/test/factories";
import { testQueryClient } from "#/test/render";
import { useAdminAttention } from "./admin-attention";

const me = (instanceAdmin: boolean) =>
	make("MeBody", {
		id: "u1",
		name: "Ada",
		email: "ada@example.com",
		instanceAdmin,
		workspaceId: "w1",
		createdAt: "2026-01-01T00:00:00Z",
		principal: { type: "user", label: "Ada", scopes: [] },
	});

const update = (over: Overrides<UpdateView> = {}) =>
	make("UpdateView", {
		available: true,
		canUpdate: true,
		checkEnabled: true,
		current: "0.1.0",
		isAdmin: true,
		latest: "0.2.0",
		manualCommand: "",
		...over,
	});

const backup = (over: Overrides<InstanceBackupView> = {}) =>
	make("InstanceBackupView", {
		enabled: true,
		keyFingerprint: "abc",
		localDirEnabled: false,
		notOffSite: false,
		retentionCount: 7,
		schedule: "0 3 * * *",
		toolsAvailable: true,
		updatedAt: "2026-01-01T00:00:00Z",
		destination: { kind: "builtin" },
		...over,
	});

function setup() {
	const client = testQueryClient();
	const wrapper = ({ children }: { children: ReactNode }) => (
		<QueryClientProvider client={client}>{children}</QueryClientProvider>
	);
	return { client, ...renderHook(() => useAdminAttention(), { wrapper }) };
}

describe("useAdminAttention", () => {
	test("an admin with a newer release and an unacknowledged key has both flags", async () => {
		stubApi({
			"GET /me": { body: me(true) },
			"GET /system/update": { body: update() },
			"GET /instance/backup": { body: backup() },
		});
		const { result } = setup();
		expect(result.current).toEqual({ update: false, keyReminder: false });
		await waitFor(() =>
			expect(result.current).toEqual({ update: true, keyReminder: true }),
		);
	});

	test("an acknowledged key and an up-to-date install clear both flags", async () => {
		const api = stubApi({
			"GET /me": { body: me(true) },
			"GET /system/update": { body: update({ available: false }) },
			"GET /instance/backup": {
				body: backup({ keyAcknowledgedAt: "2026-01-02T00:00:00Z" }),
			},
		});
		const { result } = setup();
		await waitFor(() => expect(api.calls).toHaveLength(3));
		await waitFor(() =>
			expect(result.current).toEqual({ update: false, keyReminder: false }),
		);
	});

	test("an update without a known latest version is not waiting", async () => {
		const api = stubApi({
			"GET /me": { body: me(true) },
			"GET /system/update": { body: update({ latest: undefined }) },
			"GET /instance/backup": {
				body: backup({ keyAcknowledgedAt: "2026-01-02T00:00:00Z" }),
			},
		});
		const { result } = setup();
		await waitFor(() => expect(api.calls).toHaveLength(3));
		expect(result.current.update).toBe(false);
	});

	test("a non-admin gets false flags and the admin-only endpoints are never called", async () => {
		const api = stubApi({
			"GET /me": { body: me(false) },
			"GET /system/update": { body: update() },
			"GET /instance/backup": { body: backup() },
		});
		const { result, client } = setup();
		await waitFor(() => expect(api.calls).toHaveLength(1));
		await waitFor(() => expect(client.isFetching()).toBe(0));
		// Let the render that follows the profile answer run its effects.
		await act(async () => {});
		await waitFor(() => expect(client.isFetching()).toBe(0));
		expect(api.calls.map((c) => c.path)).toEqual(["/me"]);
		expect(result.current).toEqual({ update: false, keyReminder: false });
	});

	test("a failing backup read raises no key reminder", async () => {
		const api = stubApi({
			"GET /me": { body: me(true) },
			"GET /system/update": { body: update() },
			"GET /instance/backup": problem(403, {
				title: "x",
				detail: "no",
				code: "forbidden",
			}),
		});
		const { result } = setup();
		await waitFor(() => expect(result.current.update).toBe(true));
		await waitFor(() => expect(api.calls).toHaveLength(3));
		expect(result.current.keyReminder).toBe(false);
	});
});
