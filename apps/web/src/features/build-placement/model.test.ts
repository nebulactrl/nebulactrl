import { describe, expect, test } from "bun:test";
import {
	applyRole,
	buildOverrideConfig,
	builtOnText,
	canBuildAndPush,
	concurrencyError,
	isFallback,
	isPlacementEvent,
	locationConsequence,
	locationOptions,
	type PlacementView,
	parseConcurrency,
	pinnableServers,
	pinProblem,
	placementNoticeTitle,
	placementSummary,
	ROLE_OPTIONS,
	resolveBuild,
	roleBadge,
	roleConsequence,
	serverRole,
	toBuildFields,
	toPlacementView,
	warningKind,
	withoutPlacementWarnings,
} from "./model";

describe("serverRole", () => {
	test("is read from the two flags", () => {
		expect(serverRole({ buildEnabled: false, acceptWorkloads: true })).toBe(
			"deployments",
		);
		expect(serverRole({ buildEnabled: true, acceptWorkloads: false })).toBe(
			"builds",
		);
		expect(serverRole({ buildEnabled: true, acceptWorkloads: true })).toBe(
			"both",
		);
	});

	test("a paused server that does not build is still a deployments server", () => {
		expect(serverRole({ buildEnabled: false, acceptWorkloads: false })).toBe(
			"deployments",
		);
	});
});

describe("role consequences", () => {
	test("each role says what it means in a plain sentence", () => {
		expect(ROLE_OPTIONS.map((option) => option.label)).toEqual([
			"Deployments",
			"Builds",
			"Both",
		]);
		expect(roleConsequence("builds")).toBe(
			"Builds: no apps are placed here; builds for projects that use build servers run here.",
		);
		expect(roleConsequence("deployments")).toContain("apps run here");
		expect(roleConsequence("both")).toContain("sharing");
	});
});

describe("applyRole", () => {
	test("Builds turns workloads off and builds on", () => {
		expect(
			applyRole({ buildEnabled: false, acceptWorkloads: true }, "builds"),
		).toEqual({ buildEnabled: true, acceptWorkloads: false });
	});

	test("Both turns both on", () => {
		expect(
			applyRole({ buildEnabled: true, acceptWorkloads: false }, "both"),
		).toEqual({ buildEnabled: true, acceptWorkloads: true });
	});

	test("leaving Builds for Deployments turns workloads back on", () => {
		expect(
			applyRole({ buildEnabled: true, acceptWorkloads: false }, "deployments"),
		).toEqual({ buildEnabled: false, acceptWorkloads: true });
	});

	test("a paused deployments server stays paused", () => {
		expect(
			applyRole({ buildEnabled: false, acceptWorkloads: false }, "deployments"),
		).toEqual({ buildEnabled: false, acceptWorkloads: false });
	});

	test("every role round-trips through serverRole", () => {
		for (const { value } of ROLE_OPTIONS) {
			expect(
				serverRole(
					applyRole({ buildEnabled: false, acceptWorkloads: true }, value),
				),
			).toBe(value);
		}
	});
});

describe("roleBadge", () => {
	test("only a server that builds gets one", () => {
		expect(
			roleBadge({ buildEnabled: false, acceptWorkloads: true }),
		).toBeNull();
		expect(roleBadge({ buildEnabled: true, acceptWorkloads: false })).toBe(
			"Build server",
		);
		expect(roleBadge({ buildEnabled: true, acceptWorkloads: true })).toBe(
			"Apps and builds",
		);
	});
});

describe("max concurrent builds", () => {
	test("accepts whole numbers 1 to 16", () => {
		expect(parseConcurrency("1")).toBe(1);
		expect(parseConcurrency("2")).toBe(2);
		expect(parseConcurrency("16")).toBe(16);
	});
	test("rejects the rest", () => {
		for (const text of ["", " ", "0", "17", "1.5", "two", "-1"]) {
			expect(parseConcurrency(text)).toBeNull();
			expect(concurrencyError(text)).toContain("1 to 16");
		}
		expect(concurrencyError("4")).toBeUndefined();
	});
});

describe("build server pins", () => {
	const base = {
		id: "1",
		name: "build-01",
		status: "online",
		buildEnabled: true,
	};

	test("pinnableServers keeps build-enabled servers that are not revoked or pending", () => {
		const list = pinnableServers([
			base,
			{ ...base, id: "2", buildEnabled: false },
			{ ...base, id: "3", status: "revoked" },
			{ ...base, id: "4", status: "pending" },
			{ ...base, id: "5", status: "offline" },
		]);
		expect(list.map((s) => s.id)).toEqual(["1", "5"]);
	});

	test("canBuildAndPush reads the capability, unknown before facts", () => {
		expect(canBuildAndPush(base)).toBeNull();
		expect(
			canBuildAndPush({ ...base, facts: { capabilities: ["build.push"] } }),
		).toBe(true);
		expect(
			canBuildAndPush({ ...base, facts: { capabilities: ["compose"] } }),
		).toBe(false);
		expect(canBuildAndPush({ ...base, facts: {} })).toBeNull();
	});

	test("pinProblem names why a server is a poor pin", () => {
		expect(
			pinProblem({ ...base, facts: { capabilities: ["build.push"] } }),
		).toBeNull();
		expect(pinProblem({ ...base, status: "offline" })).toBe("offline");
		expect(pinProblem({ ...base, facts: { capabilities: [] } })).toBe(
			"agent needs an update",
		);
	});
});

describe("resolveBuild", () => {
	const team = { buildLocation: "target" as const, registryId: "team-reg" };

	test("falls to the team when service and project inherit", () => {
		const effective = resolveBuild({
			service: { buildLocation: "inherit" },
			project: { buildLocation: "inherit" },
			team,
		});
		expect(effective).toMatchObject({
			location: "target",
			locationFrom: "team",
			registryId: "team-reg",
			registryFrom: "team",
			buildServerId: null,
		});
	});

	test("the project beats the team, the service beats the project", () => {
		expect(
			resolveBuild({
				service: { buildLocation: "inherit" },
				project: { buildLocation: "build_server", buildServerId: "b1" },
				team,
			}),
		).toMatchObject({
			location: "build_server",
			locationFrom: "project",
			buildServerId: "b1",
		});
		expect(
			resolveBuild({
				service: { buildLocation: "target" },
				project: { buildLocation: "build_server" },
				team: { buildLocation: "build_server" },
			}),
		).toMatchObject({ location: "target", locationFrom: "service" });
	});

	test("the pin and the registry come from the nearest level that names one", () => {
		const effective = resolveBuild({
			service: { buildLocation: "inherit", buildServerId: "s-pin" },
			project: {
				buildLocation: "build_server",
				buildServerId: "p-pin",
				registryId: "p-reg",
			},
			team,
		});
		expect(effective.buildServerId).toBe("s-pin");
		expect(effective.registryId).toBe("p-reg");
		expect(effective.registryFrom).toBe("project");
	});

	test("an empty id is no id", () => {
		const effective = resolveBuild({
			service: { buildLocation: "inherit", registryId: "" },
			team: { buildLocation: "build_server", registryId: null },
		});
		expect(effective.registryId).toBeNull();
		expect(effective.registryFrom).toBeNull();
	});
});

describe("location copy", () => {
	test("a team cannot inherit", () => {
		expect(locationOptions("team").map((o) => o.value)).toEqual([
			"target",
			"build_server",
		]);
		expect(locationOptions("project").map((o) => o.value)).toEqual([
			"inherit",
			"target",
			"build_server",
		]);
	});

	test("inherit says which level decides", () => {
		const effective = resolveBuild({
			service: { buildLocation: "inherit" },
			project: { buildLocation: "inherit" },
			team: { buildLocation: "build_server" },
		});
		expect(locationConsequence("inherit", effective)).toBe(
			"Uses the team setting: builds run on a build server.",
		);
	});

	test("build server mentions the fallback", () => {
		expect(locationConsequence("build_server")).toContain("falls back");
	});
});

describe("placementSummary", () => {
	const view: PlacementView = {
		location: "build_server",
		buildServerId: "b1",
		buildServerName: "build-01",
		registryId: "r1",
		registryHost: "ghcr.io/acme",
		platform: "linux/amd64",
		reason: "sticky",
		sticky: true,
		warnings: [],
	};

	test("names the build server, the registry and the target", () => {
		expect(placementSummary(view, { name: "web-02" })).toBe(
			"Builds on build-01 (sticky), pushes to ghcr.io/acme, deploys to web-02",
		);
	});

	test("says pinned when the choice was pinned", () => {
		expect(
			placementSummary(
				{ ...view, sticky: false, reason: "pinned" },
				{ name: "web-02" },
			),
		).toBe(
			"Builds on build-01 (pinned), pushes to ghcr.io/acme, deploys to web-02",
		);
	});

	test("leaves out the registry when there is none", () => {
		expect(
			placementSummary(
				{ ...view, registryHost: null, sticky: false, reason: "" },
				{
					name: "web-02",
				},
			),
		).toBe("Builds on build-01, deploys to web-02");
	});

	test("a build on the target reads as one server", () => {
		const onTarget: PlacementView = {
			...view,
			location: "target",
			buildServerId: null,
			buildServerName: null,
			registryHost: null,
			reason: "",
			sticky: false,
		};
		expect(placementSummary(onTarget, { name: "web-02" })).toBe(
			"Builds and deploys on web-02",
		);
	});

	test("a fallback says so", () => {
		const fallback: PlacementView = {
			location: "target",
			buildServerId: null,
			buildServerName: null,
			registryId: null,
			registryHost: null,
			platform: "linux/amd64",
			reason: "fallback: no eligible build server",
			sticky: false,
			warnings: [],
		};
		expect(isFallback(fallback)).toBe(true);
		expect(placementSummary(fallback, { name: "web-02" })).toBe(
			"Builds and deploys on web-02 (fallback)",
		);
	});
});

describe("warningKind", () => {
	test("reads the code when it is known", () => {
		expect(warningKind({ code: "emulation", message: "x" })).toBe("emulation");
		expect(warningKind({ code: "cold_cache", message: "x" })).toBe(
			"cold_cache",
		);
		expect(warningKind({ code: "fallback", message: "x" })).toBe("fallback");
	});
	test("reads the words when the code is missing", () => {
		expect(
			warningKind({
				code: "",
				message: "No build server available; building on web-02",
			}),
		).toBe("fallback");
		expect(
			warningKind({
				code: "",
				message: "linux/arm64 is built under emulation (QEMU), slow",
			}),
		).toBe("emulation");
		expect(
			warningKind({
				code: "",
				message: "This build server has a cold cache for the project",
			}),
		).toBe("cold_cache");
		expect(warningKind({ code: "", message: "The server is offline" })).toBe(
			"other",
		);
	});
});

describe("toPlacementView", () => {
	test("is null without a placement or with an unknown location", () => {
		expect(toPlacementView(null)).toBeNull();
		expect(toPlacementView(undefined)).toBeNull();
		expect(toPlacementView({ location: "elsewhere" })).toBeNull();
	});

	test("maps the wire shape, blanking empty ids", () => {
		const view = toPlacementView({
			location: "build_server",
			buildServerId: "b1",
			buildServerName: "build-01",
			registryId: "",
			registryHost: "ghcr.io/acme",
			platform: "linux/arm64",
			reason: "sticky",
			sticky: true,
			warnings: [{ code: "cold_cache", message: "Cold cache" }],
		});
		expect(view?.registryId).toBeNull();
		expect(view?.registryHost).toBe("ghcr.io/acme");
		expect(view?.sticky).toBe(true);
		expect(view?.warnings).toEqual([
			{ code: "cold_cache", message: "Cold cache" },
		]);
	});

	test("takes known warnings from the plan's plain list when the placement has none", () => {
		const view = toPlacementView({ location: "target" }, [
			"No build server available; building on web-02",
			"The server is offline",
		]);
		expect(view?.warnings).toHaveLength(1);
		expect(view?.warnings[0]?.message).toContain("No build server");
	});
});

describe("deployment timeline helpers", () => {
	test("builtOnText names both servers, only when they differ", () => {
		expect(builtOnText("build-01", "web-02")).toBe(
			"Built on build-01, deployed on web-02",
		);
		expect(builtOnText("web-02", "web-02")).toBeNull();
		expect(builtOnText(null, "web-02")).toBeNull();
		expect(builtOnText("build-01", null)).toBeNull();
	});

	test("placement events are not steps", () => {
		expect(isPlacementEvent({ step: "placement" })).toBe(true);
		expect(isPlacementEvent({ step: "Placement" })).toBe(true);
		expect(isPlacementEvent({ step: "push" })).toBe(false);
	});
});

describe("service build override", () => {
	test("a config without the fields is Inherit with nothing set", () => {
		expect(toBuildFields({})).toEqual({
			buildLocation: "inherit",
			buildServerId: "",
			registryId: "",
		});
		expect(toBuildFields({ buildLocation: "elsewhere" }).buildLocation).toBe(
			"inherit",
		);
	});

	test("reads a stored override", () => {
		expect(
			toBuildFields({
				buildLocation: "build_server",
				buildServerId: "b1",
				registryId: "r1",
			}),
		).toEqual({
			buildLocation: "build_server",
			buildServerId: "b1",
			registryId: "r1",
		});
	});

	test("Inherit with nothing set adds nothing to the config", () => {
		expect(
			buildOverrideConfig({
				buildLocation: "inherit",
				buildServerId: "",
				registryId: "",
			}),
		).toEqual({});
	});

	test("a build server override carries its pin and registry", () => {
		expect(
			buildOverrideConfig({
				buildLocation: "build_server",
				buildServerId: "b1",
				registryId: "r1",
			}),
		).toEqual({
			buildLocation: "build_server",
			buildServerId: "b1",
			registryId: "r1",
		});
	});

	test("a pin is dropped when the service builds on its own server", () => {
		expect(
			buildOverrideConfig({
				buildLocation: "target",
				buildServerId: "b1",
				registryId: "",
			}),
		).toEqual({ buildLocation: "target" });
	});

	test("a pin survives Inherit, which may resolve to a build server", () => {
		expect(
			buildOverrideConfig({
				buildLocation: "inherit",
				buildServerId: "b1",
				registryId: "",
			}),
		).toEqual({ buildServerId: "b1" });
	});

	test("an override round-trips through the form", () => {
		const config = {
			buildLocation: "build_server",
			buildServerId: "b1",
			registryId: "r1",
		};
		expect(buildOverrideConfig(toBuildFields(config))).toEqual(config);
	});
});

describe("withoutPlacementWarnings", () => {
	const placement = toPlacementView({ location: "target" }, [
		"No build server available; building on web-02",
	]);

	test("leaves the others and drops the ones the placement shows", () => {
		expect(
			withoutPlacementWarnings(
				[
					"No build server available; building on web-02",
					"The server is offline",
				],
				placement,
			),
		).toEqual(["The server is offline"]);
	});

	test("keeps everything when there is no placement", () => {
		expect(withoutPlacementWarnings(["a", "b"], null)).toEqual(["a", "b"]);
	});
});

describe("placementNoticeTitle", () => {
	test("names a fallback, a queue wait and anything else", () => {
		expect(
			placementNoticeTitle("No build server available; building on web-02"),
		).toBe("Built on the app's server instead");
		expect(placementNoticeTitle("Waiting for a build slot on build-01")).toBe(
			"Waiting for a build slot",
		);
		expect(placementNoticeTitle("Building on build-01")).toBe(
			"Build placement",
		);
	});
});
