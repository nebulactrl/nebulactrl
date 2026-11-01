import { describe, expect, test } from "bun:test";
import { ApiError } from "#/api/client-config";
import { makeService } from "#/features/canvas/lib/fixtures";
import {
	authUsersChanged,
	buildAuthBody,
	toAuthRows,
	validateAuthRows,
} from "./auth-users";
import { groupServerErrors } from "./errors";
import { exposuresOf } from "./l4";
import { mergeEdits, setGroupEdit, toForm } from "./options";
import {
	planHighlights,
	planIsDisruptive,
	TAILNET_DELETE_WARNING,
} from "./proxy-plan";
import { withHost } from "./tailscale";

describe("per-group edits", () => {
	test("an edit lives under its group and never leaks into another", () => {
		const base = toForm({});
		const edits = setGroupEdit({}, base, "limits", { compress: true });
		expect(mergeEdits(base, edits).compress).toBe(true);
		expect(mergeEdits(base, edits).noindex).toBe(false);
		expect(Object.keys(edits)).toEqual(["limits"]);
	});
	test("edits to two groups merge; a refetched base shows through untouched groups", () => {
		const base = toForm({});
		let edits = setGroupEdit({}, base, "limits", { compress: true });
		edits = setGroupEdit(edits, base, "headers", { noindex: true });
		const changedBase = toForm({ securityHeaders: "strict" });
		const form = mergeEdits(changedBase, edits);
		expect(form.compress).toBe(true);
		expect(form.noindex).toBe(true);
		// the headers group was edited, so its own fields come from the edit
		expect(form.securityHeaders).toBe("");
		expect(
			mergeEdits(changedBase, { limits: edits.limits ?? {} }).securityHeaders,
		).toBe("strict");
	});
});

describe("basic-auth users", () => {
	test("existing users stay without a password; a typed one is sent", () => {
		const rows = toAuthRows(["alice", "bob"]);
		expect(authUsersChanged(["alice", "bob"], rows)).toBe(false);
		expect(buildAuthBody(rows)).toEqual([
			{ username: "alice" },
			{ username: "bob" },
		]);
		const changed = rows.map((r) =>
			r.username === "bob" ? { ...r, password: "s3cret" } : r,
		);
		expect(authUsersChanged(["alice", "bob"], changed)).toBe(true);
		expect(buildAuthBody(changed)[1]).toEqual({
			username: "bob",
			password: "s3cret",
		});
	});
	test("removing and adding users is a change", () => {
		expect(authUsersChanged(["alice", "bob"], toAuthRows(["alice"]))).toBe(
			true,
		);
		expect(
			authUsersChanged(
				["alice"],
				[
					...toAuthRows(["alice"]),
					{ username: "c", password: "x", existing: false },
				],
			),
		).toBe(true);
	});
	test("a new user needs a valid unique name and a password", () => {
		const errors = validateAuthRows([
			...toAuthRows(["alice"]),
			{ username: "alice", password: "", existing: false },
			{ username: "bad name", password: "x", existing: false },
		]);
		expect(errors["users.1.username"]).toBeDefined();
		expect(errors["users.1.password"]).toBeDefined();
		expect(errors["users.2.username"]).toBeDefined();
		expect(errors["users.0.username"]).toBeUndefined();
	});
});

describe("groupServerErrors", () => {
	test("basic_auth_needs_users lands on the access group's switch", () => {
		const error = new ApiError(
			422,
			"basic_auth_needs_users",
			"add users first",
		);
		expect(groupServerErrors(error, "access").fields.basicAuth).toBe(
			"add users first",
		);
		expect(groupServerErrors(error, "headers").fields).toEqual({});
	});
	test("invalid_options points at the redirect rule it names", () => {
		const error = new ApiError(
			422,
			"invalid_options",
			"redirect 2: invalid regex: x",
		);
		expect(
			groupServerErrors(error, "redirects").fields["redirects.1.regex"],
		).toBeDefined();
		expect(groupServerErrors(error, "access").general).toBeUndefined();
	});
	test("certificate errors belong to TLS; agent_too_old and 412 are not inline", () => {
		const cert = new ApiError(422, "certificate_mismatch", "does not cover");
		expect(groupServerErrors(cert, "tls").fields.certificate).toBe(
			"does not cover",
		);
		expect(
			groupServerErrors(new ApiError(422, "agent_too_old", "old"), "tls"),
		).toEqual({
			fields: {},
			general: undefined,
		});
		expect(
			groupServerErrors(new ApiError(412, "generation_conflict", "x"), "tls")
				.general,
		).toBeUndefined();
		expect(
			groupServerErrors(new ApiError(500, "boom", "bad"), "tls").general,
		).toBe("bad");
	});
});

describe("proxy plan highlights", () => {
	const base = { diff: [], reasons: [], restartRequired: false, warnings: [] };
	test("a managed -> off change warns that the device is deleted", () => {
		const plan = {
			...base,
			diff: [{ field: "tailnet.mode", from: "managed", to: "off" }],
		};
		const h = planHighlights(plan);
		expect(h.deletesTailnetDevice).toBe(true);
		expect(h.warnings).toContain(TAILNET_DELETE_WARNING);
		expect(planIsDisruptive(plan)).toBe(true);
	});
	test("the backend's own device warning is not doubled", () => {
		const h = planHighlights({
			...base,
			diff: [
				{ field: "tailnet.provision", from: "-", to: "re-provision requested" },
			],
			warnings: ["The Tailscale device will be deleted and re-created."],
		});
		expect(h.warnings).toHaveLength(1);
	});
	test("restart reasons only show when a restart is required", () => {
		expect(
			planHighlights({ ...base, reasons: ["new port"], restartRequired: false })
				.restart,
		).toEqual([]);
		expect(
			planHighlights({ ...base, reasons: ["new port"], restartRequired: true })
				.restart,
		).toEqual(["new port"]);
		expect(
			planHighlights({ ...base, diff: [{ field: "a", from: "", to: "b" }] })
				.changes,
		).toEqual(["a: - → b"]);
	});
});

describe("exposures and device host", () => {
	const route = (id: string, config: object) =>
		makeService({
			id,
			name: id,
			kind: "route",
			sourceType: "external",
			config: { ...makeService().config, route: config as never },
		});
	test("exposuresOf lists the L4 routes targeting a service, by port", () => {
		const list = [
			route("b", {
				protocol: "udp",
				listenPort: 30000,
				upstreams: [{ serviceId: "svc", port: 1 }],
			}),
			route("a", {
				protocol: "tcp",
				listenPort: 25565,
				allowCidrs: ["10.0.0.0/8"],
				upstreams: [{ serviceId: "svc", port: 25565 }],
			}),
			route("c", {
				protocol: "http",
				upstreams: [{ serviceId: "svc", port: 80 }],
			}),
			route("d", {
				protocol: "tcp",
				listenPort: 1,
				upstreams: [{ serviceId: "other", port: 1 }],
			}),
			makeService({ id: "svc" }),
		];
		const found = exposuresOf("svc", list);
		expect(found.map((e) => e.listenPort)).toEqual([25565, 30000]);
		expect(found[0]?.access).toBe("Allow 10.0.0.0/8");
		expect(found[1]?.access).toContain("UDP");
	});
	test("withHost keeps a typed port", () => {
		expect(withHost("old:8123", "100.64.0.2")).toBe("100.64.0.2:8123");
		expect(withHost("", "100.64.0.2")).toBe("100.64.0.2:");
	});
});
