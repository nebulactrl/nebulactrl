import { describe, expect, test } from "bun:test";
import {
	checkableServers,
	checkSummary,
	createBody,
	EMPTY_DRAFT,
	gcLine,
	hostError,
	isUsable,
	namespaceError,
	type Registry,
	type RegistryDraft,
	registryOptionText,
	repositoryRoot,
	requiredPasswords,
	secretRequiredErrors,
	selectableRegistries,
	shortImageDigest,
	updateBody,
	validateDraft,
} from "./model";

const valid: RegistryDraft = {
	...EMPTY_DRAFT,
	name: "GHCR",
	host: "ghcr.io",
	namespace: "acme",
	pushUsername: "acme-bot",
	pushPassword: "ghp_secret",
};

const registry: Registry = {
	id: "r1",
	name: "GHCR",
	kind: "external",
	host: "ghcr.io",
	namespace: "acme",
	hasPushAuth: true,
	pushUsername: "acme-bot",
	hasPullAuth: false,
	pullUsername: "",
	manageRetention: false,
	lastCheck: {},
	imageCount: 0,
	sizeBytes: 0,
	builtin: null,
	inUse: false,
	createdAt: "2026-10-27T10:00:00Z",
};

describe("hostError", () => {
	test("accepts a name, a name with a port and an address", () => {
		expect(hostError("ghcr.io")).toBeUndefined();
		expect(hostError("registry.example.com:5000")).toBeUndefined();
		expect(hostError("localhost:5000")).toBeUndefined();
		expect(hostError("192.0.2.5:5000")).toBeUndefined();
		expect(hostError(" docker.io ")).toBeUndefined();
	});

	test("refuses a scheme", () => {
		expect(hostError("https://ghcr.io")).toContain("without https://");
		expect(hostError("http://registry.local:5000")).toContain(
			"without https://",
		);
	});

	test("refuses a path and says where it goes", () => {
		expect(hostError("ghcr.io/acme")).toContain("Namespace");
	});

	test("refuses empty, spaces, capitals and a bad port", () => {
		expect(hostError("")).toBeDefined();
		expect(hostError("   ")).toBeDefined();
		expect(hostError("my registry.io")).toBeDefined();
		expect(hostError("GHCR.io")).toContain("lowercase");
		expect(hostError("ghcr.io:0")).toContain("port");
		expect(hostError("ghcr.io:99999")).toContain("port");
		expect(hostError("ghcr.io:abc")).toContain("port");
	});

	test("refuses labels that start or end with a hyphen or are empty", () => {
		expect(hostError("-ghcr.io")).toBeDefined();
		expect(hostError("ghcr-.io")).toBeDefined();
		expect(hostError("ghcr..io")).toBeDefined();
		expect(hostError("ghcr.io.")).toBeDefined();
	});
});

describe("namespaceError", () => {
	test("empty is allowed", () => {
		expect(namespaceError("")).toBeUndefined();
		expect(namespaceError("  ")).toBeUndefined();
	});

	test("accepts the OCI repository name charset", () => {
		expect(namespaceError("acme")).toBeUndefined();
		expect(namespaceError("acme/apps")).toBeUndefined();
		expect(namespaceError("my-org/team_a/v1.2")).toBeUndefined();
		expect(namespaceError("a__b")).toBeUndefined();
		expect(namespaceError("a---b")).toBeUndefined();
	});

	test("refuses capitals, spaces and punctuation", () => {
		expect(namespaceError("Acme")).toContain("lowercase");
		expect(namespaceError("acme apps")).toBeDefined();
		expect(namespaceError("acme@apps")).toBeDefined();
		expect(namespaceError("acme:latest")).toBeDefined();
	});

	test("refuses stray slashes and edge punctuation", () => {
		expect(namespaceError("/acme")).toContain("slash");
		expect(namespaceError("acme/")).toContain("slash");
		expect(namespaceError("acme//apps")).toContain("empty part");
		expect(namespaceError("-acme")).toBeDefined();
		expect(namespaceError("acme-")).toBeDefined();
		expect(namespaceError("acme/.apps")).toBeDefined();
	});
});

describe("validateDraft", () => {
	test("a complete draft is valid", () => {
		expect(validateDraft(valid, { editing: false })).toEqual({});
	});

	test("an empty draft names every required field", () => {
		const errors = validateDraft(EMPTY_DRAFT, { editing: false });
		expect(Object.keys(errors).sort()).toEqual([
			"host",
			"name",
			"pushPassword",
			"pushUsername",
		]);
	});

	test("a namespace is optional", () => {
		expect(
			validateDraft({ ...valid, namespace: "" }, { editing: false }),
		).toEqual({});
	});

	test("editing keeps stored credentials when the password is empty", () => {
		const edit = { ...valid, pushPassword: "" };
		expect(validateDraft(edit, { editing: true })).toEqual({});
		expect(validateDraft(edit, { editing: false }).pushPassword).toBeDefined();
	});

	test("a pull credential needs a user name and a token, unless one is stored", () => {
		const pull = { ...valid, pullEnabled: true };
		const created = validateDraft(pull, { editing: false });
		expect(created.pullUsername).toBeDefined();
		expect(created.pullPassword).toBeDefined();
		const kept = validateDraft(
			{ ...pull, pullUsername: "reader" },
			{ editing: true, hadPull: true },
		);
		expect(kept).toEqual({});
		const added = validateDraft(
			{ ...pull, pullUsername: "reader" },
			{ editing: true, hadPull: false },
		);
		expect(added.pullPassword).toBeDefined();
	});

	test("a scheme in the host is reported on the host", () => {
		const errors = validateDraft(
			{ ...valid, host: "https://ghcr.io" },
			{ editing: false },
		);
		expect(Object.keys(errors)).toEqual(["host"]);
	});

	test("a host change needs the passwords typed again", () => {
		const edit = { ...valid, host: "registry.example.com", pushPassword: "" };
		const errors = validateDraft(edit, {
			editing: true,
			reenter: requiredPasswords(edit, registry),
		});
		expect(Object.keys(errors)).toEqual(["pushPassword"]);
		const typed = { ...edit, pushPassword: "new" };
		expect(
			validateDraft(typed, {
				editing: true,
				reenter: requiredPasswords(typed, registry),
			}),
		).toEqual({});
	});

	test("a host change also needs the pull password when one is stored and kept", () => {
		const stored = { ...registry, hasPullAuth: true, pullUsername: "reader" };
		const edit = {
			...valid,
			host: "registry.example.com",
			pullEnabled: true,
			pullUsername: "reader",
		};
		const errors = validateDraft(edit, {
			editing: true,
			hadPull: true,
			reenter: requiredPasswords(edit, stored),
		});
		expect(errors.pullPassword).toBeDefined();
		expect(errors.pushPassword).toBeUndefined();
	});

	test("the same host keeps both passwords optional", () => {
		const stored = { ...registry, hasPullAuth: true, pullUsername: "reader" };
		const edit = {
			...valid,
			pushPassword: "",
			pullEnabled: true,
			pullUsername: "reader",
		};
		expect(
			validateDraft(edit, {
				editing: true,
				hadPull: true,
				reenter: requiredPasswords(edit, stored),
			}),
		).toEqual({});
	});
});

describe("requiredPasswords", () => {
	const stored = { ...registry, hasPullAuth: true, pullUsername: "reader" };
	const pulling = { ...valid, pullEnabled: true, pullUsername: "reader" };

	test("nothing is required when the host is unchanged, or on create", () => {
		expect(requiredPasswords(valid, registry)).toEqual({
			push: false,
			pull: false,
		});
		expect(requiredPasswords(pulling, stored)).toEqual({
			push: false,
			pull: false,
		});
		expect(requiredPasswords(valid, null)).toEqual({
			push: false,
			pull: false,
		});
	});

	test("a changed host requires the push password", () => {
		expect(
			requiredPasswords({ ...valid, host: "registry.example.com" }, registry),
		).toEqual({ push: true, pull: false });
	});

	test("surrounding spaces are not a host change", () => {
		expect(
			requiredPasswords({ ...valid, host: " ghcr.io " }, registry),
		).toEqual({ push: false, pull: false });
	});

	test("the pull password is required only when a pull credential is stored and kept", () => {
		const moved = { host: "registry.example.com" };
		expect(requiredPasswords({ ...pulling, ...moved }, stored)).toEqual({
			push: true,
			pull: true,
		});
		// Clearing the pull credential needs no pull password.
		expect(
			requiredPasswords({ ...pulling, ...moved, pullEnabled: false }, stored),
		).toEqual({ push: true, pull: false });
		// Adding one where none is stored is a new credential, not a re-entry.
		expect(requiredPasswords({ ...pulling, ...moved }, registry)).toEqual({
			push: true,
			pull: false,
		});
	});
});

describe("secretRequiredErrors", () => {
	test("points at the push password, and the pull one when it is needed", () => {
		const moved = { ...valid, host: "registry.example.com" };
		expect(Object.keys(secretRequiredErrors(moved, registry))).toEqual([
			"pushPassword",
		]);
		const stored = { ...registry, hasPullAuth: true, pullUsername: "reader" };
		expect(
			Object.keys(
				secretRequiredErrors({ ...moved, pullEnabled: true }, stored),
			).sort(),
		).toEqual(["pullPassword", "pushPassword"]);
	});
});

describe("createBody", () => {
	test("trims and leaves out the pull credential when it is off", () => {
		const body = createBody({
			...valid,
			name: " GHCR ",
			host: " ghcr.io ",
			namespace: "/acme/",
			pullUsername: "ignored",
			pullPassword: "ignored",
		});
		expect(body).toEqual({
			name: "GHCR",
			host: "ghcr.io",
			namespace: "acme",
			pushUsername: "acme-bot",
			pushPassword: "ghp_secret",
		});
	});

	test("sends the pull credential when it is on", () => {
		const body = createBody({
			...valid,
			pullEnabled: true,
			pullUsername: "reader",
			pullPassword: "ro",
		});
		expect(body.pullUsername).toBe("reader");
		expect(body.pullPassword).toBe("ro");
	});
});

describe("updateBody", () => {
	test("sends only what changed and never an empty password", () => {
		expect(updateBody({ ...valid, pushPassword: "" }, registry)).toEqual({});
		expect(
			updateBody({ ...valid, name: "Registry", pushPassword: "new" }, registry),
		).toEqual({ name: "Registry", pushPassword: "new" });
	});

	test("turning the pull credential off clears the stored one", () => {
		const stored = { ...registry, hasPullAuth: true, pullUsername: "reader" };
		expect(updateBody({ ...valid, pullEnabled: false }, stored).clearPull).toBe(
			true,
		);
		expect(
			updateBody({ ...valid, pushPassword: "", pullEnabled: false }, registry),
		).toEqual({});
	});
});

describe("repositoryRoot", () => {
	test("joins host and namespace, or is the bare host", () => {
		expect(repositoryRoot("ghcr.io", "acme")).toBe("ghcr.io/acme");
		expect(repositoryRoot("ghcr.io", "")).toBe("ghcr.io");
		expect(repositoryRoot(" ghcr.io ", "/acme/apps/")).toBe(
			"ghcr.io/acme/apps",
		);
	});
});

describe("checkSummary", () => {
	const at = "2026-10-27T10:00:00Z";
	test("reads a pass and the split results", () => {
		expect(
			checkSummary({ ok: true, push: true, pull: true, error: "", at }),
		).toBe("Push and pull work");
		expect(
			checkSummary({ ok: false, push: true, pull: false, error: "x", at }),
		).toBe("Push works, pull failed");
		expect(
			checkSummary({ ok: false, push: false, pull: true, error: "x", at }),
		).toBe("Pull works, push failed");
	});
	test("falls to the registry's own error, then a plain sentence", () => {
		expect(
			checkSummary({ ok: false, push: false, pull: false, error: "401", at }),
		).toBe("401");
		expect(
			checkSummary({ ok: false, push: false, pull: false, error: "", at }),
		).toBe("Could not reach the registry");
	});
});

describe("checkableServers", () => {
	test("keeps online servers, build servers first", () => {
		const servers = [
			{ id: "1", name: "web-02", status: "online", buildEnabled: false },
			{ id: "2", name: "off", status: "offline", buildEnabled: true },
			{ id: "3", name: "build-01", status: "online", buildEnabled: true },
			{ id: "4", name: "web-01", status: "online", buildEnabled: false },
		];
		expect(checkableServers(servers).map((s) => s.name)).toEqual([
			"build-01",
			"web-01",
			"web-02",
		]);
	});
});

describe("the built-in registry", () => {
	const now = Date.parse("2026-10-27T12:00:00Z");
	const base = {
		status: "ready" as const,
		statusMessage: "",
		gcState: "idle" as const,
	};

	test("gcLine says when cleanup last ran", () => {
		expect(gcLine({ ...base, lastGcAt: null }, now)).toBe("No cleanup yet");
		expect(gcLine({ ...base, lastGcAt: "2026-10-27T11:59:40Z" }, now)).toBe(
			"Last cleanup just now",
		);
		expect(gcLine({ ...base, lastGcAt: "2026-10-27T11:30:00Z" }, now)).toBe(
			"Last cleanup 30 min ago",
		);
		expect(gcLine({ ...base, lastGcAt: "2026-10-27T06:00:00Z" }, now)).toBe(
			"Last cleanup 6 h ago",
		);
		expect(gcLine({ ...base, lastGcAt: "2026-10-20T12:00:00Z" }, now)).toBe(
			"Last cleanup 7 days ago",
		);
	});

	test("gcLine names a cleanup in progress", () => {
		expect(
			gcLine({ ...base, gcState: "collecting", lastGcAt: null }, now),
		).toBe("Cleaning up now");
		expect(
			gcLine({ ...base, gcState: "readonly", lastGcAt: null }, now),
		).toContain("Read-only");
	});

	test("only a ready built-in registry, or an external one, is usable", () => {
		expect(isUsable(registry)).toBe(true);
		expect(
			isUsable({
				...registry,
				kind: "builtin",
				builtin: { ...base, lastGcAt: null },
			}),
		).toBe(true);
		expect(
			isUsable({
				...registry,
				kind: "builtin",
				builtin: { ...base, status: "unavailable", lastGcAt: null },
			}),
		).toBe(false);
	});

	test("selectableRegistries puts usable ones first and labels the built-in one without its address", () => {
		const down: Registry = {
			...registry,
			id: "r2",
			name: "Aaa",
			kind: "builtin",
			host: "127.0.0.1:18500",
			namespace: "",
			builtin: { ...base, status: "unavailable", lastGcAt: null },
		};
		const list = selectableRegistries([down, registry]);
		expect(list.map((r) => r.id)).toEqual(["r1", "r2"]);
		expect(registryOptionText(registry)).toBe("GHCR (ghcr.io/acme)");
		expect(registryOptionText(down)).toBe("Aaa (built-in)");
	});
});

describe("shortImageDigest", () => {
	test("cuts a sha256 digest to twelve hex characters", () => {
		const full = `sha256:${"0a1b2c3d4e5f".repeat(5)}0123`;
		expect(shortImageDigest(full)).toBe("sha256:0a1b2c3d4e5f");
	});
	test("leaves anything else alone", () => {
		expect(shortImageDigest("sha256:abc")).toBe("sha256:abc");
		expect(shortImageDigest("")).toBe("");
	});
});
