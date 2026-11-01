import { describe, expect, test } from "bun:test";
import { makeService } from "#/features/canvas/lib/fixtures";
import { toDraft } from "./draft";
import {
	CATEGORY_IDS,
	categoriesFor,
	categoryCommands,
	categoryHasDraftFields,
	categoryOfField,
	DRAFT_KEYS,
	defaultCategory,
	editedCategories,
	firstInvalidCategory,
	invalidCategories,
	resolveCategory,
	SEARCH_ENTRIES,
	type SettingsSubject,
	searchSettings,
	stagedCategory,
	stagedCounts,
} from "./settings-nav-model";

const app: SettingsSubject = {
	kind: "app",
	sourceType: "git",
	oneshot: false,
};
const image: SettingsSubject = { ...app, sourceType: "image" };
const route: SettingsSubject = {
	kind: "route",
	sourceType: "external",
	oneshot: false,
	routeProtocol: "http",
};

const ids = (subject: SettingsSubject) =>
	categoriesFor(subject).map((category) => category.id);

const sectionsOf = (subject: SettingsSubject, id: string) =>
	categoriesFor(subject)
		.find((category) => category.id === id)
		?.sections.map((section) => section.id) ?? [];

describe("categoriesFor", () => {
	test("an app shows every category, in rail order", () => {
		expect(ids(app)).toEqual([...CATEGORY_IDS]);
		expect(ids(image)).toEqual([...CATEGORY_IDS]);
		expect([...CATEGORY_IDS]).toEqual([
			"general",
			"deploy",
			"networking",
			"resources",
			"storage",
			"operations",
			"advanced",
			"danger",
		]);
	});

	test("a worker and a database are not different from an app", () => {
		expect(ids({ ...app, kind: "worker" })).toEqual([...CATEGORY_IDS]);
		expect(ids({ ...image, kind: "database" })).toEqual([...CATEGORY_IDS]);
	});

	test("a route has no deploy, resources, storage, operations or advanced", () => {
		expect(ids(route)).toEqual(["general", "networking", "danger"]);
		expect(ids({ ...route, kind: "app" })).toEqual([
			"general",
			"networking",
			"danger",
		]);
	});

	test("a route's networking follows its protocol", () => {
		const sections = (protocol: string) =>
			sectionsOf({ ...route, routeProtocol: protocol }, "networking");
		expect(sections("http")).toEqual(["upstreams", "domains"]);
		expect(sections("tcp")).toEqual(["target", "port-and-access"]);
		expect(sections("udp")).toEqual(["target", "port-and-access"]);
	});

	test("a one-shot job drops networking, operations and the health check", () => {
		const job = { ...app, oneshot: true };
		expect(ids(job)).toEqual([
			"general",
			"deploy",
			"resources",
			"storage",
			"advanced",
			"danger",
		]);
		expect(sectionsOf(job, "deploy")).toEqual([
			"deploy",
			"deploy-hook",
			"releases",
		]);
		expect(sectionsOf(job, "resources")).toEqual(["scaling", "resources"]);
	});

	test("General holds identity, the server, the source and the build", () => {
		expect(sectionsOf(app, "general")).toEqual([
			"identity",
			"placement",
			"source",
			"build",
		]);
		// An image service has no build to place.
		expect(sectionsOf(image, "general")).toEqual([
			"identity",
			"placement",
			"source",
		]);
	});

	test("Deploy holds the commands, the processes, the health check, the deploy hook and the release facts", () => {
		expect(sectionsOf(app, "deploy")).toEqual([
			"deploy",
			"processes",
			"health-check",
			"deploy-hook",
			"releases",
		]);
		expect(sectionsOf(image, "deploy")).toEqual(sectionsOf(app, "deploy"));
	});

	test("Processes are for git and image apps and workers that keep running", () => {
		expect(sectionsOf({ ...app, kind: "worker" }, "deploy")).toContain(
			"processes",
		);
		// One build and one release for several long-lived containers: not a
		// database, a one-shot job, a route or a Compose stack.
		expect(sectionsOf({ ...image, kind: "database" }, "deploy")).not.toContain(
			"processes",
		);
		expect(sectionsOf({ ...app, oneshot: true }, "deploy")).not.toContain(
			"processes",
		);
		expect(
			sectionsOf(
				{ kind: "stack", sourceType: "compose", oneshot: false },
				"deploy",
			),
		).not.toContain("processes");
		expect(
			categoriesFor(route)
				.flatMap((category) => category.sections)
				.map((section) => section.id),
		).not.toContain("processes");
	});

	test("Networking ends with the mesh links, Operations starts with uptime", () => {
		expect(sectionsOf(app, "networking")).toEqual([
			"networking",
			"tailnet",
			"mesh",
		]);
		expect(sectionsOf(app, "operations")).toEqual([
			"uptime-monitoring",
			"tasks",
			"maintenance",
		]);
	});

	test("Resources are scaling then limits; Storage is volumes and files", () => {
		expect(sectionsOf(app, "resources")).toEqual(["scaling", "resources"]);
		expect(sectionsOf(app, "storage")).toEqual(["volumes", "files"]);
	});

	test("Docker options sit alone in Advanced, for every container service and no route", () => {
		expect(sectionsOf(app, "advanced")).toEqual(["docker-options"]);
		expect(sectionsOf({ ...image, kind: "database" }, "advanced")).toEqual([
			"docker-options",
		]);
		for (const protocol of ["http", "tcp", "udp"]) {
			const sections = categoriesFor({ ...route, routeProtocol: protocol })
				.flatMap((category) => category.sections)
				.map((section) => section.id);
			expect(sections).not.toContain("docker-options");
		}
	});

	test("every section appears under exactly one category", () => {
		for (const subject of [app, image, route]) {
			const all = categoriesFor(subject).flatMap((category) =>
				category.sections.map((section) => section.id),
			);
			expect(new Set(all).size).toBe(all.length);
		}
	});

	test("names are sentence case", () => {
		for (const category of categoriesFor(app)) {
			expect(category.label).not.toBe(category.label.toUpperCase());
			expect(category.label.slice(1)).toBe(
				category.label.slice(1).toLowerCase(),
			);
		}
	});
});

describe("resolveCategory", () => {
	const categories = categoriesFor(app);

	test("takes a category id", () => {
		expect(resolveCategory("danger", categories)).toBe("danger");
		expect(resolveCategory("resources", categories)).toBe("resources");
		expect(resolveCategory("advanced", categories)).toBe("advanced");
	});

	test("the old runtime category lands on Resources", () => {
		expect(resolveCategory("runtime", categories)).toBe("resources");
		expect(resolveCategory("runtime", categoriesFor(route))).toBeUndefined();
	});

	test("takes a section id and lands on its category", () => {
		expect(resolveCategory("health-check", categories)).toBe("deploy");
		expect(resolveCategory("danger-zone", categories)).toBe("danger");
		expect(resolveCategory("placement", categories)).toBe("general");
		expect(resolveCategory("docker-options", categories)).toBe("advanced");
		expect(resolveCategory("uptime-monitoring", categories)).toBe("operations");
		expect(resolveCategory("mesh", categories)).toBe("networking");
	});

	test("ignores unknown values and categories the service does not have", () => {
		expect(resolveCategory("nope", categories)).toBeUndefined();
		expect(resolveCategory(undefined, categories)).toBeUndefined();
		expect(resolveCategory("storage", categoriesFor(route))).toBeUndefined();
		// An inherited property is not an alias.
		expect(resolveCategory("constructor", categories)).toBeUndefined();
	});

	test("the default is the first category", () => {
		expect(defaultCategory(categories)).toBe("general");
		expect(defaultCategory([])).toBe("general");
	});
});

describe("categoryOfField", () => {
	test("every draft field maps to a category the rail has", () => {
		const draft = toDraft(makeService({ id: "s1", name: "web" }));
		const keys = Object.keys(draft);
		for (const key of keys) {
			expect(DRAFT_KEYS as readonly string[]).toContain(key);
			expect(CATEGORY_IDS).toContain(categoryOfField(key));
		}
	});

	test("every category that holds a form field says so, and the others act at once", () => {
		const withFields = CATEGORY_IDS.filter(categoryHasDraftFields);
		expect(withFields).toEqual([
			"general",
			"deploy",
			"networking",
			"resources",
			"storage",
			"advanced",
		]);
	});

	test("sorts draft fields and their errors", () => {
		expect(categoryOfField("image")).toBe("general");
		expect(categoryOfField("dockerfilePath")).toBe("general");
		expect(categoryOfField("submodules")).toBe("general");
		expect(categoryOfField("buildContext")).toBe("general");
		expect(categoryOfField("serverId")).toBe("general");
		expect(categoryOfField("stopGraceSeconds")).toBe("deploy");
		expect(categoryOfField("imageRetention")).toBe("deploy");
		expect(categoryOfField("preDeployTimeoutSeconds")).toBe("deploy");
		expect(categoryOfField("startCommand")).toBe("deploy");
		expect(categoryOfField("healthType")).toBe("deploy");
		expect(categoryOfField("intervalSeconds")).toBe("deploy");
		expect(categoryOfField("port")).toBe("networking");
		expect(categoryOfField("tailnetHostname")).toBe("networking");
		expect(categoryOfField("tailnet")).toBe("networking");
		expect(categoryOfField("memoryMb")).toBe("resources");
		expect(categoryOfField("cpus")).toBe("resources");
		expect(categoryOfField("docker")).toBe("advanced");
		expect(categoryOfField("files")).toBe("storage");
	});

	test("the process fields belong to Deploy, and their errors too", () => {
		expect(categoryOfField("processes")).toBe("deploy");
		expect(categoryOfField("processName")).toBe("deploy");
		expect(categoryOfField("singleton")).toBe("deploy");
		expect(invalidCategories(["processes"])).toEqual(new Set(["deploy"]));
	});

	test("finds the category to land on after a failed save", () => {
		const categories = categoriesFor(app);
		expect(firstInvalidCategory(["memoryMb", "port"], categories)).toBe(
			"networking",
		);
		expect(firstInvalidCategory([], categories)).toBeUndefined();
		expect(invalidCategories(["cpus", "files"])).toEqual(
			new Set(["resources", "storage"]),
		);
	});
});

describe("editedCategories", () => {
	const service = makeService({ id: "s1", name: "web" });
	const baseline = toDraft(service);

	test("an untouched draft edits nothing", () => {
		expect(editedCategories(baseline, { ...baseline }, "image").size).toBe(0);
	});

	test("marks only the category of the field that changed", () => {
		const draft = { ...baseline, memoryMb: "512" };
		expect([...editedCategories(baseline, draft, "image")]).toEqual([
			"resources",
		]);
	});

	test("marks several categories at once", () => {
		const draft = { ...baseline, name: "api", port: "8080" };
		const edited = editedCategories(baseline, draft, "image");
		expect(edited.has("general")).toBe(true);
		expect(edited.has("networking")).toBe(true);
		expect(edited.has("resources")).toBe(false);
	});

	test("a change the patch ignores is not an edit", () => {
		const draft = { ...baseline, name: `${baseline.name}  ` };
		expect(editedCategories(baseline, draft, "image").size).toBe(0);
	});

	test("write-only fields count once typed", () => {
		const draft = { ...baseline, gitToken: "ghp_x" };
		expect(editedCategories(baseline, draft, "git").has("general")).toBe(true);
	});
});

describe("staged changes", () => {
	test("map field prefixes to categories", () => {
		expect(stagedCategory("name")).toBe("general");
		expect(stagedCategory("config.image")).toBe("general");
		expect(stagedCategory("config.dockerfilePath")).toBe("general");
		expect(stagedCategory("source")).toBe("general");
		expect(stagedCategory("server")).toBe("general");
		expect(stagedCategory("config.startCommand")).toBe("deploy");
		expect(stagedCategory("config.preDeployCommand")).toBe("deploy");
		expect(stagedCategory("config.postDeployCommand")).toBe("deploy");
		expect(stagedCategory("config.stopGraceSeconds")).toBe("deploy");
		expect(stagedCategory("config.imageRetention")).toBe("deploy");
		expect(stagedCategory("config.healthcheck.path")).toBe("deploy");
		expect(stagedCategory("config.port")).toBe("networking");
		expect(stagedCategory("config.blockEgress")).toBe("networking");
		expect(stagedCategory("config.tailnet.enabled")).toBe("networking");
		expect(stagedCategory("domains.api.example.com")).toBe("networking");
		expect(stagedCategory("networks.shared")).toBe("networking");
		expect(stagedCategory("config.memoryMb")).toBe("resources");
		expect(stagedCategory("config.cpus")).toBe("resources");
		expect(stagedCategory("config.docker.capAdd")).toBe("advanced");
		expect(stagedCategory("volumes.data")).toBe("storage");
		expect(stagedCategory("config.files")).toBe("storage");
		expect(stagedCategory("config.processes.worker")).toBe("deploy");
		expect(stagedCategory("config.processes.worker.startCommand")).toBe(
			"deploy",
		);
		expect(stagedCategory("config.processes.worker.healthcheck.path")).toBe(
			"deploy",
		);
		expect(stagedCategory("config.processName")).toBe("deploy");
		expect(stagedCategory("config.singleton")).toBe("deploy");
	});

	test("variables and a first deployment are not settings", () => {
		expect(stagedCategory("variables.API_KEY")).toBeNull();
		expect(stagedCategory("deployment")).toBeNull();
	});

	test("counts per category", () => {
		expect(
			stagedCounts([
				"config.image",
				"config.port",
				"domains.a.example.com",
				"variables.A",
				"deployment",
				"volumes.data",
				"config.stopGraceSeconds",
				"server",
			]),
		).toEqual({
			general: 2,
			deploy: 1,
			networking: 2,
			resources: 0,
			storage: 1,
			operations: 0,
			advanced: 0,
			danger: 0,
		});
	});
});

describe("searchSettings", () => {
	const top = (query: string, subject: SettingsSubject = app) =>
		searchSettings(query, subject)[0]?.entry.label;

	test("an empty query finds nothing", () => {
		expect(searchSettings("", app)).toEqual([]);
		expect(searchSettings("   ", app)).toEqual([]);
	});

	test("changing the source is found by what people call it, except on a stack or route", () => {
		const labels = (query: string, subject: SettingsSubject) =>
			searchSettings(query, subject).map((result) => result.entry.label);
		expect(top("change source")).toBe("Change source");
		expect(top("change source", image)).toBe("Change source");
		expect(labels("switch to git", image)).toContain("Change source");
		expect(labels("docker image", app)).toContain("Change source");
		expect(searchSettings("change source", app)[0]?.sectionLabel).toBe(
			"Source",
		);
		const stack: SettingsSubject = {
			kind: "stack",
			sourceType: "compose",
			oneshot: false,
		};
		expect(labels("change source", stack)).not.toContain("Change source");
		expect(labels("change source", route)).not.toContain("Change source");
	});

	test("a label beats a keyword", () => {
		expect(top("port")).toBe("Port");
		expect(top("memory")).toBe("Memory");
		expect(top("dockerfile")).toBe("Dockerfile path");
	});

	test("an exact label beats a longer one that starts the same", () => {
		const labels = searchSettings("health check", app).map(
			(result) => result.entry.label,
		);
		expect(labels[0]).toBe("Health check");
		expect(labels).toContain("Health check path");
	});

	test("finds a field by a keyword", () => {
		expect(top("ram")).toBe("Memory");
		expect(top("cron")).toBe("Tasks");
		expect(top("tailscale")).toBe("Reachable over your tailnet");
		expect(top("migrate")).toBe("Pre-deploy command");
		expect(top("uptime")).toBe("Uptime monitoring");
		expect(top("outage")).toBe("Failed checks before an outage");
	});

	test("every word must match", () => {
		expect(searchSettings("memory dockerfile", app)).toEqual([]);
		expect(top("stop grace")).toBe("Stop grace period");
	});

	test("ignores case and punctuation", () => {
		expect(top("PRE-DEPLOY")).toBe("Pre-deploy command");
		expect(top("  Pre deploy ")).toBe("Pre-deploy command");
	});

	test("a result carries the category to switch to", () => {
		const [first] = searchSettings("memory", app);
		expect(first?.category).toBe("resources");
		expect(first?.categoryLabel).toBe("Resources");
		expect(first?.sectionLabel).toBe("Limits");
		expect(searchSettings("delete", app)[0]?.category).toBe("danger");
	});

	test("a moved field is found where it lives now", () => {
		const where = (query: string, subject: SettingsSubject = app) => {
			const [first] = searchSettings(query, subject);
			return [first?.category, first?.entry.section];
		};
		expect(where("server")).toEqual(["general", "placement"]);
		expect(where("dockerfile path")).toEqual(["general", "build"]);
		expect(where("submodules")).toEqual(["general", "build"]);
		expect(where("build context")).toEqual(["general", "build"]);
		expect(where("health check")).toEqual(["deploy", "health-check"]);
		expect(where("success threshold")).toEqual(["deploy", "health-check"]);
		expect(where("strategy")).toEqual(["deploy", "releases"]);
		expect(where("images kept")).toEqual(["deploy", "releases"]);
		expect(where("uptime monitoring")).toEqual([
			"operations",
			"uptime-monitoring",
		]);
		expect(where("capabilities")).toEqual(["advanced", "docker-options"]);
		expect(where("scaling")).toEqual(["resources", "scaling"]);
	});

	test("only fields the service has are searched", () => {
		expect(top("dockerfile", image)).not.toBe("Dockerfile path");
		expect(top("registry", image)).toBe("Registry login");
		// A git service has a registry too, for built images; an image service pulls with its own login.
		expect(top("registry", app)).toBe("Build registry");
		expect(searchSettings("memory", route)).toEqual([]);
		expect(top("upstreams", route)).toBe("Upstreams");
		expect(searchSettings("tasks", { ...app, oneshot: true })).toEqual([]);
	});

	test("the deploy hook is in Deploy for apps and stacks, and found by what people call it", () => {
		expect(top("webhook", app)).toBe("Deploy hook");
		expect(top("github actions", app)).toBe("Deploy hook");
		expect(searchSettings("webhook", app)[0]?.category).toBe("deploy");
		expect(searchSettings("webhook", route)).toEqual([]);
	});

	test("finds the processes by what people call them", () => {
		expect(top("processes")).toBe("Processes");
		expect(top("sidekiq")).toBe("Processes");
		expect(top("scheduler")).toBe("Singleton");
		expect(searchSettings("sidekiq", app)[0]?.category).toBe("deploy");
		expect(searchSettings("sidekiq", route)).toEqual([]);
		expect(searchSettings("sidekiq", { ...app, oneshot: true })).toEqual([]);
		expect(top("add process")).toBe("Add process");
	});

	test("ties keep page order and the list is capped", () => {
		const results = searchSettings("s", app, 5);
		expect(results.length).toBeLessThanOrEqual(5);
		const scores = results.map((result) => result.score);
		expect(scores).toEqual([...scores].sort((a, b) => b - a));
	});

	test("every entry belongs to a section some service shows", () => {
		const shown = new Set(
			[
				app,
				image,
				route,
				{ ...route, routeProtocol: "tcp" },
				{ kind: "stack", sourceType: "compose", oneshot: false },
			].flatMap((s) =>
				categoriesFor(s).flatMap((c) => c.sections.map((x) => x.id)),
			),
		);
		for (const candidate of SEARCH_ENTRIES) {
			expect(shown.has(candidate.section)).toBe(true);
		}
	});
});

describe("a Compose stack's settings", () => {
	const stack = { kind: "stack", sourceType: "compose", oneshot: false };
	const ids = (subject = stack) =>
		categoriesFor(subject).map((c) => [c.id, c.sections.map((s) => s.id)]);

	test("shows the compose file, deploy, limits, server, networking, uptime and the danger zone", () => {
		expect(ids()).toEqual([
			["general", ["identity", "placement", "compose"]],
			["deploy", ["stack-deploy", "deploy-hook"]],
			["networking", ["stack-networking"]],
			["resources", ["stack-limits"]],
			["operations", ["uptime-monitoring"]],
			["danger", ["danger-zone"]],
		]);
	});

	test("hides what the compose file or the control plane owns", () => {
		const shown = new Set(
			ids().flatMap(([, sections]) => sections as string[]),
		);
		for (const hidden of [
			"source",
			"deploy",
			"releases",
			"health-check",
			"scaling",
			"resources",
			"docker-options",
			"volumes",
			"files",
			"tasks",
			"maintenance",
			"tailnet",
			"networking",
		]) {
			expect(shown.has(hidden)).toBe(false);
		}
	});

	test("search finds the stack's fields and none of the app-only ones", () => {
		expect(searchSettings("yaml", stack)[0]?.entry.label).toBe("Compose file");
		expect(searchSettings("wait", stack)[0]?.entry.label).toBe("Wait timeout");
		expect(searchSettings("memory", stack)[0]?.entry.label).toBe(
			"Component limits",
		);
		expect(searchSettings("one-shot", stack)).toEqual([]);
		expect(searchSettings("dockerfile", stack)).toEqual([]);
		expect(searchSettings("tcp", stack)).toEqual([]);
	});

	test("staged compose changes land in General, the wait in Deploy, limits in Resources", () => {
		expect(stagedCategory("config.stack.compose")).toBe("general");
		expect(stagedCategory("config.stack.waitSeconds")).toBe("deploy");
		expect(stagedCategory("config.stack.resources")).toBe("resources");
	});
});

describe("the Build section", () => {
	test("a git service has it between Source and the end of General", () => {
		expect(sectionsOf(app, "general")).toEqual([
			"identity",
			"placement",
			"source",
			"build",
		]);
	});

	test("an image service has no build to place", () => {
		expect(sectionsOf(image, "general")).not.toContain("build");
	});

	test("the override fields live in General", () => {
		for (const field of ["buildLocation", "buildServerId", "registryId"]) {
			expect(categoryOfField(field)).toBe("general");
		}
	});

	test("search finds it by what people call it", () => {
		expect(searchSettings("build server", app)[0]?.sectionLabel).toBe("Build");
		expect(searchSettings("ghcr", app)[0]?.entry.label).toBe("Build registry");
		expect(searchSettings("build server", image)).toEqual([]);
	});
});

describe("categoryCommands", () => {
	const keywords = (subject: SettingsSubject, id: string) =>
		categoryCommands(subject).find((command) => command.category === id)
			?.keywords ?? [];

	test("one command per category a service has, in rail order", () => {
		expect(categoryCommands(app).map((command) => command.category)).toEqual([
			...CATEGORY_IDS,
		]);
		expect(categoryCommands(route).map((command) => command.category)).toEqual([
			"general",
			"networking",
			"danger",
		]);
		expect(categoryCommands(app).map((command) => command.label)).toContain(
			"Deploy",
		);
	});

	test("a category is found by the sections, fields and keywords inside it", () => {
		expect(keywords(app, "deploy")).toContain("health check");
		expect(keywords(app, "deploy")).toContain("webhook");
		expect(keywords(app, "general")).toContain("dockerfile path");
		expect(keywords(app, "general")).toContain("server");
		expect(keywords(app, "resources")).toContain("ram");
		expect(keywords(app, "operations")).toContain("cron");
		expect(keywords(app, "advanced")).toContain("capabilities");
		expect(keywords(app, "danger")).toContain("delete service");
	});

	test("the old Runtime name still finds Resources", () => {
		expect(keywords(app, "resources")).toContain("runtime");
		expect(keywords(app, "deploy")).not.toContain("runtime");
	});

	test("only what the service has is offered", () => {
		expect(keywords(image, "general")).not.toContain("dockerfile path");
		expect(keywords({ ...app, oneshot: true }, "deploy")).not.toContain(
			"health check",
		);
	});

	test("a command does not repeat its own name as a keyword", () => {
		for (const command of categoryCommands(app)) {
			expect(command.keywords).not.toContain(command.label.toLowerCase());
		}
	});
});

describe("search ranking and keywords", () => {
	const labels = (query: string, subject: SettingsSubject = app) =>
		searchSettings(query, subject).map((result) => result.entry.label);

	test("'scale' finds Scaling first, ahead of fields that only mention tailscale", () => {
		expect(labels("scale")[0]).toBe("Scaling");
		expect(labels("scale")).toContain("Tailnet hostname");
		expect(labels("scale").indexOf("Scaling")).toBeLessThan(
			labels("scale").indexOf("Tailnet hostname"),
		);
	});

	test("a word finds the same word in another form", () => {
		expect(labels("scaled")[0]).toBe("Scaling");
		expect(labels("limits")).toContain("Memory");
	});

	test("'restart' finds where a release stops and starts", () => {
		expect(labels("restart")).toEqual(
			expect.arrayContaining(["Stop grace period", "Strategy", "Deploy hook"]),
		);
	});

	test("'secret' finds the credentials and the hook URL", () => {
		expect(labels("secret", image)).toEqual(
			expect.arrayContaining(["Registry login", "Deploy hook"]),
		);
		expect(labels("secret")).toContain("Access token");
	});

	test("'env' finds the env-file mount", () => {
		expect(labels("env")).toContain("Files");
	});

	test("'cloudflare' finds the domain editor's DNS, for an app, a route and a stack", () => {
		const stack: SettingsSubject = {
			kind: "stack",
			sourceType: "compose",
			oneshot: false,
		};
		expect(labels("cloudflare")[0]).toBe("Cloudflare DNS");
		expect(labels("cloudflare", route)[0]).toBe("Cloudflare DNS");
		expect(labels("cloudflare", stack)[0]).toBe("Cloudflare DNS");
		expect(searchSettings("cloudflare", app)[0]?.category).toBe("networking");
	});
});
