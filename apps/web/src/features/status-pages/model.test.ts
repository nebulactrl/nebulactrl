import { describe, expect, test } from "bun:test";
import type {
	MaintenanceWindowView,
	StatusIncidentView,
} from "#/api/gen/types.gen";
import {
	addServices,
	CLOUDFLARE_LAG_NOTE,
	type ComponentRow,
	canMoveGroup,
	canMoveRow,
	compareIncidents,
	componentState,
	componentsBody,
	defaultWindow,
	desiredTargets,
	dnsNotice,
	dnsPanelMode,
	EMPTY_PAGE_FORM,
	editRow,
	emptyIncidentForm,
	emptyWindowForm,
	filterServices,
	fixProblem,
	fromLocalInput,
	groupNames,
	groupRows,
	groupWindows,
	INCIDENT_STATUSES,
	incidentPatch,
	isNewName,
	moveGroup,
	moveRow,
	NEW_NAME_NOTE,
	nextStatus,
	normalizeHostname,
	OPEN_INCIDENT_STATUSES,
	overallState,
	pageCreateBody,
	pageFixAction,
	pageFormDirty,
	pageLockInfo,
	pagePatch,
	pageProblem,
	previewBlocker,
	removeRow,
	renameGroup,
	rowForService,
	rowProblems,
	rowsDiffer,
	savePageProblem,
	sectionServices,
	slugify,
	statusRank,
	suggestedStatus,
	toggleId,
	toLocalInput,
	utcLabel,
	validateComponentUrl,
	validateIncidentForm,
	validatePageForm,
	validateSlug,
	validateUpdateBody,
	validateWindowForm,
	windowLength,
	windowPatch,
} from "./model";

const svc = (id: string, name = id, project = "shop", env = "production") => ({
	id,
	name,
	projectName: project,
	environmentName: env,
});

function row(id: string, group = "", name = id): ComponentRow {
	return { ...rowForService(svc(id, name)), groupName: group };
}

const ids = (rows: readonly ComponentRow[]) => rows.map((r) => r.serviceId);

describe("component defaults", () => {
	test("a new row takes the service's name, no group, no link", () => {
		expect(rowForService(svc("a", "api"))).toEqual({
			serviceId: "a",
			displayName: "api",
			groupName: "",
			url: "",
			serviceName: "api",
			projectName: "shop",
			environmentName: "production",
		});
	});

	test("a long service name is cut to the public name limit", () => {
		expect(rowForService(svc("a", "x".repeat(80))).displayName).toHaveLength(
			60,
		);
	});

	test("adding services skips the ones already on the page and keeps order", () => {
		const rows = addServices([row("a")], [svc("a"), svc("b"), svc("c")]);
		expect(ids(rows)).toEqual(["a", "b", "c"]);
	});

	test("a page holds at most 100 components", () => {
		const many = Array.from({ length: 105 }, (_, i) => svc(`s${i}`));
		expect(addServices([], many)).toHaveLength(100);
	});

	test("blank optional fields are left out of the request", () => {
		const rows = [
			{ ...row("a"), displayName: "  ", groupName: " Core ", url: "" },
			{ ...row("b"), displayName: "Web", url: " https://web.example.com " },
		];
		expect(componentsBody(rows)).toEqual([
			{ serviceId: "b", displayName: "Web", url: "https://web.example.com" },
			{ serviceId: "a", groupName: "Core" },
		]);
	});
});

describe("groups keep the public order", () => {
	test("ungrouped first, then groups by first appearance", () => {
		const rows = [
			row("a", "Core"),
			row("b"),
			row("c", "Edge"),
			row("d", "Core"),
		];
		expect(ids(groupRows(rows).flatMap((g) => g.rows))).toEqual([
			"b",
			"a",
			"d",
			"c",
		]);
		expect(groupNames(rows)).toEqual(["Core", "Edge"]);
	});

	test("giving a row a group moves it to the end of that group", () => {
		const rows = [row("a", "Core"), row("b", "Core"), row("c")];
		const next = editRow(rows, "c", { groupName: "Core" });
		expect(ids(next)).toEqual(["a", "b", "c"]);
		expect(next.every((r) => r.groupName === "Core")).toBe(true);
	});

	test("a new group goes last; clearing the group makes the row ungrouped", () => {
		const rows = [row("a", "Core"), row("b")];
		expect(ids(editRow(rows, "b", { groupName: "Edge" }))).toEqual(["a", "b"]);
		expect(ids(editRow(rows, "a", { groupName: "" }))).toEqual(["b", "a"]);
		expect(groupNames(editRow(rows, "a", { groupName: "" }))).toEqual([]);
	});

	test("editing a name or a link does not move the row", () => {
		const rows = [row("a", "Core"), row("b", "Core")];
		const next = editRow(rows, "a", {
			displayName: "API",
			url: "https://x.dev",
		});
		expect(ids(next)).toEqual(["a", "b"]);
		expect(next[0]?.displayName).toBe("API");
	});

	test("renaming onto an existing group merges the two", () => {
		const rows = [row("a", "Core"), row("b", "Edge"), row("c", "Core")];
		expect(groupNames(renameGroup(rows, "Edge", "Core"))).toEqual(["Core"]);
		expect(groupNames(renameGroup(rows, "Edge", "Public"))).toEqual([
			"Core",
			"Public",
		]);
	});

	test("removing the last member of a group removes the group", () => {
		const rows = [row("a", "Core"), row("b")];
		expect(groupNames(removeRow(rows, "a"))).toEqual([]);
	});
});

describe("moving rows and groups", () => {
	const rows = [row("a"), row("b", "Core"), row("c", "Core"), row("d", "Edge")];

	test("a row moves within its own group only", () => {
		expect(ids(moveRow(rows, "c", -1))).toEqual(["a", "c", "b", "d"]);
		expect(ids(moveRow(rows, "b", 1))).toEqual(["a", "c", "b", "d"]);
		expect(canMoveRow(rows, "b", -1)).toBe(false);
		expect(canMoveRow(rows, "c", 1)).toBe(false);
		expect(canMoveRow(rows, "b", 1)).toBe(true);
	});

	test("the edge of a group is a no-op", () => {
		expect(ids(moveRow(rows, "b", -1))).toEqual(ids(rows));
		expect(ids(moveRow(rows, "a", 1))).toEqual(ids(rows));
		expect(ids(moveRow(rows, "missing", 1))).toEqual(ids(rows));
	});

	test("a group moves as one block past its neighbour group", () => {
		expect(ids(moveGroup(rows, "Edge", -1))).toEqual(["a", "d", "b", "c"]);
		expect(ids(moveGroup(rows, "Core", 1))).toEqual(["a", "d", "b", "c"]);
		expect(canMoveGroup(rows, "Core", -1)).toBe(false);
		expect(canMoveGroup(rows, "Edge", -1)).toBe(true);
		expect(canMoveGroup(rows, "Edge", 1)).toBe(false);
	});

	test("the ungrouped run never moves", () => {
		expect(ids(moveGroup(rows, "", 1))).toEqual(ids(rows));
		expect(canMoveGroup(rows, "", 1)).toBe(false);
	});

	test("rowsDiffer notices order, names, groups and links, not whitespace", () => {
		expect(rowsDiffer(rows, [...rows])).toBe(false);
		expect(rowsDiffer(rows, moveRow(rows, "c", -1))).toBe(true);
		expect(rowsDiffer([row("a")], [{ ...row("a"), displayName: " a " }])).toBe(
			false,
		);
		expect(
			rowsDiffer([row("a")], [{ ...row("a"), url: "https://x.dev" }]),
		).toBe(true);
		expect(rowsDiffer([row("a")], [])).toBe(true);
	});
});

describe("component validation", () => {
	test("a link is blank or an absolute https URL", () => {
		expect(validateComponentUrl("")).toBeNull();
		expect(validateComponentUrl("  ")).toBeNull();
		expect(validateComponentUrl("https://example.com/docs?a=1")).toBeNull();
		expect(validateComponentUrl("HTTPS://Example.com")).toBeNull();
		expect(validateComponentUrl("http://example.com")).toMatch(/https/);
		expect(validateComponentUrl("example.com")).toMatch(/https/);
		expect(validateComponentUrl("https://")).toMatch(/https/);
		expect(validateComponentUrl("javascript:alert(1)")).toMatch(/https/);
		expect(validateComponentUrl("https://a b.com")).toMatch(/spaces/);
		expect(validateComponentUrl("https://me:pw@example.com")).toMatch(
			/user name/,
		);
		expect(validateComponentUrl(`https://e.com/${"a".repeat(300)}`)).toMatch(
			/300/,
		);
	});

	test("problems are reported per row", () => {
		const rows = [
			{ ...row("a"), url: "http://x" },
			{ ...row("b"), displayName: "n".repeat(61) },
			row("c"),
		];
		const problems = rowProblems(rows);
		expect(Object.keys(problems)).toEqual(["a", "b"]);
		expect(problems.a?.url).toBeDefined();
		expect(problems.b?.displayName).toMatch(/60/);
	});
});

describe("service picker", () => {
	const services = [
		svc("1", "api", "shop", "production"),
		svc("2", "web", "shop", "staging"),
		svc("3", "api", "blog", "production"),
	];

	test("every word has to match the name, project or environment", () => {
		expect(filterServices(services, "").map((s) => s.id)).toEqual([
			"1",
			"2",
			"3",
		]);
		expect(filterServices(services, "api blog").map((s) => s.id)).toEqual([
			"3",
		]);
		expect(filterServices(services, "STAGING").map((s) => s.id)).toEqual(["2"]);
		expect(filterServices(services, "nothing")).toEqual([]);
	});

	test("services sit under project and environment headings", () => {
		expect(sectionServices(services).map((s) => s.label)).toEqual([
			"shop · production",
			"shop · staging",
			"blog · production",
		]);
	});
});

describe("page form", () => {
	const saved = {
		...EMPTY_PAGE_FORM,
		title: "Acme status",
		slug: "acme",
		hostname: "status.acme.dev",
	};

	test("a hostname is checked with the control plane's rules", () => {
		expect(validatePageForm(saved, "edit")).toEqual({});
		expect(validatePageForm({ ...saved, hostname: "" }, "edit")).toEqual({});
		expect(
			validatePageForm({ ...saved, hostname: "https://x.dev" }, "edit")
				.hostname,
		).toMatch(/hostname only/);
		expect(
			validatePageForm({ ...saved, hostname: "localhost" }, "edit").hostname,
		).toMatch(/two parts/);
		expect(
			validatePageForm({ ...saved, hostname: "10.0.0.1" }, "edit").hostname,
		).toMatch(/IP/);
	});

	test("a slug is lowercase words with single hyphens; blank only on create", () => {
		expect(validateSlug("acme-2")).toBeNull();
		expect(validateSlug("Acme")).not.toBeNull();
		expect(validateSlug("a--b")).not.toBeNull();
		expect(validateSlug("-a")).not.toBeNull();
		expect(validateSlug("a".repeat(51))).toMatch(/50/);
		expect(validatePageForm({ ...saved, slug: "" }, "create")).toEqual({});
		expect(validatePageForm({ ...saved, slug: "" }, "edit").slug).toBeDefined();
	});

	test("title is required and limited", () => {
		expect(
			validatePageForm({ ...saved, title: " " }, "edit").title,
		).toBeDefined();
		expect(
			validatePageForm({ ...saved, title: "t".repeat(101) }, "edit").title,
		).toMatch(/100/);
		expect(
			validatePageForm({ ...saved, footer: "f".repeat(501) }, "edit").footer,
		).toMatch(/500/);
	});

	test("a slug is derived from a title the way the control plane does", () => {
		expect(slugify("API status")).toBe("api-status");
		expect(slugify("  Acme -- Prod!  ")).toBe("acme-prod");
		expect(slugify("!!!")).toBe("status");
		expect(slugify("x".repeat(80))).toHaveLength(50);
	});

	test("hostnames are compared as stored", () => {
		expect(normalizeHostname(" Status.Acme.Dev. ")).toBe("status.acme.dev");
		expect(
			pageFormDirty({ ...saved, hostname: "STATUS.acme.dev." }, saved),
		).toBe(false);
		expect(pageFormDirty({ ...saved, theme: "dark" }, saved)).toBe(true);
	});

	test("a patch carries only what changed; an empty hostname removes it", () => {
		expect(pagePatch(saved, saved)).toEqual({});
		expect(
			pagePatch(
				{
					...saved,
					title: " New ",
					footer: "Hi",
					showUptime: false,
					hostname: "",
				},
				saved,
			),
		).toEqual({ title: "New", footer: "Hi", showUptime: false, hostname: "" });
	});

	test("create sends a slug only when one was typed", () => {
		const body = pageCreateBody({ ...EMPTY_PAGE_FORM, title: " Acme " });
		expect(body).toEqual({ title: "Acme", theme: "auto", showUptime: true });
	});

	test("create sends the chosen project and omits none", () => {
		const form = { ...EMPTY_PAGE_FORM, title: "Acme" };
		expect(pageCreateBody(form, "p1")).toEqual({
			title: "Acme",
			theme: "auto",
			showUptime: true,
			fromProjectId: "p1",
		});
		expect(pageCreateBody(form, "")).not.toHaveProperty("fromProjectId");
		expect(pageCreateBody(form)).not.toHaveProperty("fromProjectId");
	});

	test("an unknown project reads at the picker", () => {
		expect(pageProblem("unknown_project")?.field).toBe("project");
	});

	test("the publish refusals read at the publish switch", () => {
		expect(pageProblem("publish_needs_hostname")).toEqual({
			field: "publish",
			message: "Add a hostname before publishing the page.",
		});
		expect(pageProblem("publish_needs_components")?.field).toBe("publish");
		expect(pageProblem("hostname_conflict")?.field).toBe("hostname");
		expect(pageProblem("hostname_reserved")?.field).toBe("hostname");
		expect(pageProblem("slug_conflict")?.field).toBe("slug");
		expect(pageProblem("something_else")).toBeNull();
		expect(pageProblem(undefined)).toBeNull();
	});

	test("taking the hostname or components off a published page says to unpublish", () => {
		expect(savePageProblem("publish_needs_components", true)?.message).toMatch(
			/Unpublish it first/,
		);
		expect(savePageProblem("publish_needs_hostname", true)).toMatchObject({
			field: "hostname",
		});
		expect(savePageProblem("publish_needs_components", false)?.message).toMatch(
			/Add at least one component/,
		);
		expect(savePageProblem("slug_conflict", true)?.field).toBe("slug");
		expect(savePageProblem(undefined, false)).toBeNull();
	});
});

describe("the DNS notice", () => {
	const finding = (
		status: "ok" | "mismatch" | "missing" | "unknown",
		extra: { proxied?: boolean; cloudflareRecord?: boolean } = {},
	) => ({ status, proxied: false, cloudflareRecord: false, ...extra });

	test("a proxied hostname that resolves to Cloudflare is ready", () => {
		const n = dnsNotice(finding("ok", { proxied: true }), true);
		expect(n.title).toBe("Resolves through Cloudflare");
		expect(n.variant).toBe("success");
		expect(dnsNotice(finding("ok"), false).summary).toBe(
			"The hostname points at this control plane.",
		);
	});

	test("a hostname without records tells the user to create them only when DNS is not managed", () => {
		const manual = dnsNotice(finding("missing"), false);
		expect(manual.variant).toBe("warning");
		expect(manual.summary).toBe(
			"The hostname has no records yet. Create the ones below at your DNS provider.",
		);
		const managed = dnsNotice(finding("missing"), true);
		expect(managed.summary).not.toMatch(/DNS provider|Create the ones below/);
		expect(dnsNotice(finding("mismatch"), true).summary).not.toMatch(
			/DNS provider|ones below/,
		);
	});

	test("the check of a page in a connected zone never tells to create records", () => {
		// Every zone state gets the Cloudflare panel, whose check is the managed one.
		for (const status of [
			"managed",
			"present",
			"conflict",
			"wrong_target",
			"missing",
		]) {
			expect(dnsPanelMode({ configured: true, status })).toBe("cloudflare");
		}
		for (const status of ["ok", "mismatch", "missing", "unknown"] as const) {
			for (const cloudflareRecord of [false, true]) {
				for (const proxied of [false, true]) {
					const n = dnsNotice(
						finding(status, { cloudflareRecord, proxied }),
						true,
					);
					expect(`${n.title} ${n.summary}`).not.toMatch(
						/DNS provider|ones below|A record|Create the/,
					);
				}
			}
		}
	});

	test("when Cloudflare has the record a lagging lookup is a neutral note", () => {
		for (const status of ["missing", "mismatch"] as const) {
			const n = dnsNotice(finding(status, { cloudflareRecord: true }), true);
			expect(n.variant).toBe("info");
			expect(n.title).toBe("Cloudflare has the record");
			expect(n.summary).toBe(CLOUDFLARE_LAG_NOTE);
			expect(n.showMessage).toBe(false);
		}
		expect(CLOUDFLARE_LAG_NOTE).toBe(
			"Cloudflare has the record. Some DNS caches can take up to 30 minutes to see a new name.",
		);
	});

	test("a new name adds the heads-up under an ok check only", () => {
		expect(dnsNotice(finding("ok"), true, true).headsUp).toBe(true);
		expect(
			dnsNotice(finding("ok", { proxied: true }), false, true).headsUp,
		).toBe(true);
		expect(dnsNotice(finding("ok"), true, false).headsUp).toBe(false);
		expect(dnsNotice(finding("missing"), false, true).headsUp).toBe(false);
		expect(dnsNotice(finding("unknown"), false, true).headsUp).toBe(false);
	});

	test("the neutral Cloudflare note and the heads-up are one line", () => {
		const n = dnsNotice(
			finding("missing", { cloudflareRecord: true }),
			true,
			true,
		);
		expect(n.headsUp).toBe(false);
		expect(n.summary).toBe(`Cloudflare has the record. ${NEW_NAME_NOTE}`);
		expect(NEW_NAME_NOTE).toBe(
			"New names can take up to 30 minutes to reach every DNS resolver. If it doesn't open on your device yet, your network's DNS may still remember it as missing; it clears by itself.",
		);
		// An old name keeps the shorter neutral note.
		expect(
			dnsNotice(finding("missing", { cloudflareRecord: true }), true, false)
				.summary,
		).toBe(CLOUDFLARE_LAG_NOTE);
	});

	test("a check that could not tell says to check again", () => {
		const n = dnsNotice(finding("unknown", { cloudflareRecord: true }), true);
		expect(n.variant).toBe("info");
		expect(n.summary).toMatch(/check again/);
	});
});

describe("a new name", () => {
	const now = Date.parse("2026-10-30T12:00:00Z");
	const ago = (minutes: number) =>
		new Date(now - minutes * 60_000).toISOString();

	test("is new for 30 minutes after it changed, then not", () => {
		expect(isNewName(ago(0), now)).toBe(true);
		expect(isNewName(ago(10), now)).toBe(true);
		expect(isNewName(ago(29.9), now)).toBe(true);
		expect(isNewName(ago(30), now)).toBe(false);
		expect(isNewName(ago(24 * 60), now)).toBe(false);
	});

	test("is never new without a usable timestamp", () => {
		expect(isNewName(null, now)).toBe(false);
		expect(isNewName(undefined, now)).toBe(false);
		expect(isNewName("", now)).toBe(false);
		expect(isNewName("not a date", now)).toBe(false);
	});

	test("tolerates a minute of clock skew, not more", () => {
		expect(isNewName(ago(-0.5), now)).toBe(true);
		expect(isNewName(ago(-5), now)).toBe(false);
	});
});

describe("the Cloudflare DNS panel", () => {
	const base = {
		configured: true,
		status: "managed",
		error: "",
		records: [] as {
			type: string;
			content: string;
			proxied: boolean;
			owned: boolean;
		}[],
		effectiveProxied: false,
		desiredRecords: [
			{ type: "A", content: "203.0.113.10", proxied: false },
			{ type: "AAAA", content: "2001:db8::1", proxied: false },
		],
	};

	test("the panel is Cloudflare only for a hostname in a connected zone", () => {
		for (const status of [
			"managed",
			"present",
			"conflict",
			"wrong_target",
			"missing",
		]) {
			expect(dnsPanelMode({ configured: true, status })).toBe("cloudflare");
		}
		expect(dnsPanelMode({ configured: true, status: "not_in_zone" })).toBe(
			"manual",
		);
		expect(dnsPanelMode({ configured: true, status: "" })).toBe("manual");
		expect(dnsPanelMode({ configured: false, status: "managed" })).toBe(
			"manual",
		);
		expect(dnsPanelMode(undefined)).toBe("manual");
		expect(dnsPanelMode(null)).toBe("manual");
	});

	test("a locked page reads locked, with the pull state", () => {
		expect(
			pageLockInfo({ enabled: true, optIn: true, aop: "on", reason: "" }),
		).toEqual({
			label: "Locked",
			kind: "live",
			reason: "",
			aop: "Authenticated Origin Pulls: on",
		});
		expect(
			pageLockInfo({
				enabled: true,
				optIn: true,
				aop: "unavailable",
				reason: "",
			}).aop,
		).toBe("Authenticated Origin Pulls: unavailable");
	});

	test("an unlocked page says why, with a tone", () => {
		const lock = (reason: string) =>
			pageLockInfo({ enabled: false, optIn: true, aop: "off", reason });
		expect(lock("opted_out")).toMatchObject({ label: "Open", kind: "warning" });
		expect(lock("not_published")).toMatchObject({
			label: "Pending",
			kind: "pending",
		});
		expect(lock("not_published").reason).toMatch(/published/);
		expect(lock("pending")).toMatchObject({ label: "Pending" });
		expect(lock("not_proxied")).toMatchObject({
			label: "Not locked",
			kind: "stopped",
		});
		expect(lock("not_in_zone").reason).toMatch(/zone/);
		expect(lock("not_proxied").aop).toBeNull();
		expect(lock("something_new").reason).not.toBe("");
	});

	test("desired records read as their addresses", () => {
		expect(desiredTargets(base.desiredRecords)).toBe(
			"203.0.113.10, 2001:db8::1",
		);
		expect(desiredTargets([])).toBe("the server");
		expect(desiredTargets(null)).toBe("the server");
	});

	test("the fix: nothing when right, create or fix otherwise", () => {
		expect(pageFixAction(base)).toEqual({ kind: "none" });
		expect(pageFixAction({ ...base, status: "missing" })).toEqual({
			kind: "fix",
			label: "Create records",
		});
		expect(pageFixAction({ ...base, status: "wrong_target" })).toEqual({
			kind: "fix",
			label: "Fix DNS",
		});
		expect(pageFixAction({ ...base, error: "boom" })).toEqual({
			kind: "fix",
			label: "Retry sync",
		});
	});

	test("the fix: a right record the team created can be adopted", () => {
		expect(pageFixAction({ ...base, status: "present" })).toMatchObject({
			kind: "adopt",
			label: "Adopt record",
		});
	});

	test("the fix: a conflict offers the one replace action, listing what changes", () => {
		const action = pageFixAction({
			...base,
			status: "conflict",
			records: [
				{ type: "A", content: "198.51.100.7", proxied: false, owned: false },
			],
			effectiveProxied: true,
		});
		expect(action).toMatchObject({
			kind: "adopt",
			label: "Replace with NebulaCtrl's record",
		});
		if (action.kind !== "adopt") throw new Error("expected adopt");
		expect(action.confirm).toContain("A 198.51.100.7 (DNS only)");
		expect(action.confirm).toContain("A 203.0.113.10 (Proxied)");
		expect(action.confirm).toContain("AAAA 2001:db8::1 (Proxied)");
	});

	test("the fix: no zone offers nothing", () => {
		expect(pageFixAction({ ...base, configured: false })).toEqual({
			kind: "none",
		});
		expect(pageFixAction({ ...base, status: "not_in_zone" })).toEqual({
			kind: "none",
		});
	});

	test("refused fixes read as sentences", () => {
		expect(fixProblem("adoption_required")).toMatch(/Replace it with/);
		expect(fixProblem("dns_conflict")).toBeNull();
		expect(fixProblem("not_configured")).toMatch(/not connected/);
		expect(fixProblem("not_in_zone")).toMatch(/zone/);
		expect(fixProblem("other")).toBeNull();
		expect(fixProblem(undefined)).toBeNull();
	});
});

describe("previewBlocker", () => {
	test("a page without a hostname cannot be previewed", () => {
		const reason = previewBlocker({ hostname: null, published: false });
		expect(reason).toContain("Add a hostname");
	});

	test("a draft with a hostname says to publish it", () => {
		const reason = previewBlocker({
			hostname: "status.acme.com",
			published: false,
		});
		expect(reason).toContain("Publish the page");
		expect(reason).toContain("status.acme.com");
	});

	test("a published page with a hostname has nothing in the way", () => {
		expect(
			previewBlocker({ hostname: "status.acme.com", published: true }),
		).toBeNull();
	});
});

describe("incident status order", () => {
	test("statuses run investigating, identified, monitoring, resolved", () => {
		expect([...INCIDENT_STATUSES]).toEqual([
			"investigating",
			"identified",
			"monitoring",
			"resolved",
		]);
		expect(INCIDENT_STATUSES.map(statusRank)).toEqual([0, 1, 2, 3]);
	});

	test("the open statuses are the ones before resolved", () => {
		expect([...OPEN_INCIDENT_STATUSES]).toEqual([
			"investigating",
			"identified",
			"monitoring",
		]);
	});

	test("the next status follows the order and stops at resolved", () => {
		expect(nextStatus("investigating")).toBe("identified");
		expect(nextStatus("identified")).toBe("monitoring");
		expect(nextStatus("monitoring")).toBe("resolved");
		expect(nextStatus("resolved")).toBe("resolved");
	});

	test("an update on a resolved incident suggests reopening it", () => {
		expect(suggestedStatus("investigating")).toBe("identified");
		expect(suggestedStatus("monitoring")).toBe("resolved");
		expect(suggestedStatus("resolved")).toBe("investigating");
	});

	const incident = (
		over: Partial<StatusIncidentView> & { id: string },
	): StatusIncidentView => ({
		createdAt: "2026-10-01T00:00:00Z",
		impact: "minor",
		pageIds: [],
		resolvedAt: null,
		serviceIds: [],
		status: "investigating",
		title: over.id,
		updatedAt: "2026-10-01T00:00:00Z",
		updates: [],
		...over,
	});

	test("open incidents sort before resolved, worst impact first, then newest", () => {
		const list = [
			incident({
				id: "resolved-new",
				status: "resolved",
				resolvedAt: "2026-10-05T00:00:00Z",
			}),
			incident({ id: "minor-old", updatedAt: "2026-10-01T00:00:00Z" }),
			incident({ id: "critical", impact: "critical" }),
			incident({ id: "minor-new", updatedAt: "2026-10-03T00:00:00Z" }),
			incident({
				id: "resolved-old",
				status: "resolved",
				resolvedAt: "2026-10-02T00:00:00Z",
			}),
		];
		expect(list.sort(compareIncidents).map((i) => i.id)).toEqual([
			"critical",
			"minor-new",
			"minor-old",
			"resolved-new",
			"resolved-old",
		]);
	});

	test("a new incident starts minor and investigating", () => {
		const form = emptyIncidentForm(["p1"]);
		expect(form).toMatchObject({
			impact: "minor",
			status: "investigating",
			pageIds: ["p1"],
			serviceIds: [],
		});
	});

	test("the first update is required on create, not on edit", () => {
		const form = { ...emptyIncidentForm(), title: "API errors" };
		expect(validateIncidentForm(form, "create").body).toBeDefined();
		expect(validateIncidentForm(form, "edit")).toEqual({});
		expect(
			validateIncidentForm({ ...form, title: "" }, "edit").title,
		).toBeDefined();
		expect(validateUpdateBody("x".repeat(4001))).toMatch(/4000/);
		expect(validateUpdateBody("We are on it.")).toBeNull();
	});

	test("a patch carries only what changed; page order does not matter", () => {
		const current = incident({
			id: "i",
			title: "API errors",
			pageIds: ["p1", "p2"],
			serviceIds: ["s1"],
		});
		const form = {
			...emptyIncidentForm(),
			title: "API errors",
			impact: "minor" as const,
			pageIds: ["p2", "p1"],
			serviceIds: ["s1"],
		};
		expect(incidentPatch(form, current)).toEqual({});
		expect(
			incidentPatch(
				{ ...form, impact: "major", pageIds: [], serviceIds: ["s1", "s2"] },
				current,
			),
		).toEqual({ impact: "major", pageIds: [], serviceIds: ["s1", "s2"] });
	});

	test("toggling an id keeps the others in order", () => {
		expect(toggleId(["a", "b"], "c", true)).toEqual(["a", "b", "c"]);
		expect(toggleId(["a", "b"], "a", false)).toEqual(["b"]);
		expect(toggleId(["a", "b"], "b", true)).toEqual(["a", "b"]);
	});
});

describe("maintenance windows", () => {
	const now = new Date("2026-10-29T12:30:00Z");

	test("local input and instant round-trip", () => {
		const iso = "2026-07-15T08:45:00.000Z";
		expect(fromLocalInput(toLocalInput(iso))).toBe(iso);
		expect(fromLocalInput("")).toBeNull();
		expect(fromLocalInput("2026-10-29")).toBeNull();
		expect(toLocalInput("nope")).toBe("");
	});

	test("the UTC reading is explicit", () => {
		expect(utcLabel("2026-10-29T14:05:59Z")).toBe("2026-10-29 14:05 UTC");
		expect(utcLabel("nope")).toBe("");
	});

	test("a new window starts at the next full hour and lasts an hour", () => {
		const { startsAt, endsAt } = defaultWindow(now);
		const start = new Date(fromLocalInput(startsAt) ?? "");
		const end = new Date(fromLocalInput(endsAt) ?? "");
		expect(start.getTime()).toBeGreaterThan(now.getTime());
		expect(start.getTime() - now.getTime()).toBeLessThanOrEqual(60 * 60_000);
		expect(start.getMinutes()).toBe(0);
		expect(end.getTime() - start.getTime()).toBe(60 * 60_000);
	});

	test("the end has to come after the start and still be ahead", () => {
		const form = {
			...emptyWindowForm(now),
			title: "Database upgrade",
		};
		expect(validateWindowForm(form, now)).toEqual({});
		expect(
			validateWindowForm({ ...form, endsAt: form.startsAt }, now).endsAt,
		).toMatch(/after the start/);
		expect(
			validateWindowForm({ ...form, endsAt: "2000-01-01T00:00" }, now).endsAt,
		).toMatch(/after the start/);
		const past = {
			...form,
			startsAt: "2000-01-01T00:00",
			endsAt: "2000-01-01T01:00",
		};
		expect(validateWindowForm(past, now).endsAt).toMatch(/future/);
		expect(
			validateWindowForm({ ...form, startsAt: "" }, now).startsAt,
		).toBeDefined();
		expect(
			validateWindowForm({ ...form, title: " " }, now).title,
		).toBeDefined();
	});

	test("a window that already started may keep its start", () => {
		const start = new Date(now.getTime() - 30 * 60_000);
		const end = new Date(now.getTime() + 30 * 60_000);
		const form = {
			...emptyWindowForm(now),
			title: "Running",
			startsAt: toLocalInput(start.toISOString()),
			endsAt: toLocalInput(end.toISOString()),
		};
		expect(validateWindowForm(form, now)).toEqual({});
	});

	const window = (
		over: Partial<MaintenanceWindowView> & { id: string },
	): MaintenanceWindowView => ({
		canceledAt: null,
		createdAt: "2026-10-01T00:00:00Z",
		description: "",
		endsAt: "2026-11-01T02:00:00Z",
		pageIds: [],
		phase: "upcoming",
		serviceIds: [],
		startsAt: "2026-11-01T01:00:00Z",
		title: over.id,
		updatedAt: "2026-10-01T00:00:00Z",
		...over,
	});

	test("windows split into active, upcoming and past, each in its own order", () => {
		const groups = groupWindows([
			window({ id: "later", startsAt: "2026-12-01T00:00:00Z" }),
			window({ id: "sooner", startsAt: "2026-11-01T00:00:00Z" }),
			window({ id: "now", phase: "active" }),
			window({
				id: "ended-old",
				phase: "ended",
				startsAt: "2026-09-01T00:00:00Z",
			}),
			window({
				id: "ended-new",
				phase: "ended",
				startsAt: "2026-10-10T00:00:00Z",
			}),
			window({
				id: "canceled",
				phase: "canceled",
				startsAt: "2026-10-01T00:00:00Z",
			}),
		]);
		expect(groups.active.map((w) => w.id)).toEqual(["now"]);
		expect(groups.upcoming.map((w) => w.id)).toEqual(["sooner", "later"]);
		expect(groups.past.map((w) => w.id)).toEqual([
			"ended-new",
			"canceled",
			"ended-old",
		]);
	});

	test("an unchanged form patches nothing; a moved end patches only the end", () => {
		const current = window({
			id: "w",
			pageIds: ["p"],
			serviceIds: ["s"],
			description: "Upgrade",
		});
		const form = { ...emptyWindowForm(now), ...windowFormOf(current) };
		expect(windowPatch(form, current)).toEqual({});
		const later = new Date("2026-11-01T03:00:00Z").toISOString();
		expect(
			windowPatch({ ...form, endsAt: toLocalInput(later) }, current),
		).toEqual({ endsAt: later });
	});

	test("lengths read in days, hours and minutes", () => {
		expect(windowLength("2026-11-01T01:00:00Z", "2026-11-01T02:30:00Z")).toBe(
			"1 h 30 min",
		);
		expect(windowLength("2026-11-01T01:00:00Z", "2026-11-01T01:45:00Z")).toBe(
			"45 min",
		);
		expect(windowLength("2026-11-01T01:00:00Z", "2026-11-03T02:00:00Z")).toBe(
			"2 days 1 h",
		);
		expect(windowLength("2026-11-01T01:00:00Z", "2026-11-01T01:00:00Z")).toBe(
			"",
		);
	});
});

function windowFormOf(w: MaintenanceWindowView) {
	return {
		title: w.title,
		description: w.description,
		startsAt: toLocalInput(w.startsAt),
		endsAt: toLocalInput(w.endsAt),
		pageIds: w.pageIds ?? [],
		serviceIds: w.serviceIds ?? [],
	};
}

describe("overall state", () => {
	test("monitor states map onto the public vocabulary", () => {
		expect(componentState("up")).toBe("operational");
		expect(componentState("degraded")).toBe("degraded");
		expect(componentState("down")).toBe("major_outage");
		expect(componentState("maintenance")).toBe("maintenance");
		expect(componentState("paused")).toBe("unknown");
		expect(componentState(undefined)).toBe("unknown");
	});

	test("some components down is a partial outage, all of them a major one", () => {
		expect(overallState(["operational", "major_outage"], [], false)).toBe(
			"partial_outage",
		);
		expect(overallState(["major_outage", "major_outage"], [], false)).toBe(
			"major_outage",
		);
		expect(overallState(["operational", "degraded"], [], false)).toBe(
			"degraded",
		);
	});

	test("open incidents raise the state by their impact", () => {
		expect(overallState(["operational"], ["minor"], false)).toBe("degraded");
		expect(overallState(["operational"], ["major"], false)).toBe(
			"partial_outage",
		);
		expect(overallState(["operational"], ["critical", "minor"], false)).toBe(
			"major_outage",
		);
		expect(overallState(["operational"], ["none"], false)).toBe("operational");
	});

	test("maintenance shows only when nothing is worse", () => {
		expect(overallState(["operational"], [], true)).toBe("maintenance");
		expect(overallState(["maintenance"], [], false)).toBe("maintenance");
		expect(overallState(["maintenance", "degraded"], [], true)).toBe(
			"degraded",
		);
	});

	test("a page with no data is unknown, and unknown components do not worsen it", () => {
		expect(overallState(["unknown"], [], false)).toBe("unknown");
		expect(overallState([], [], false)).toBe("unknown");
		expect(overallState(["unknown", "operational"], [], false)).toBe(
			"operational",
		);
	});
});
