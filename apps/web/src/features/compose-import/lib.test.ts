import { describe, expect, test } from "bun:test";
import type {
	ComposeChoices,
	ComposeNote,
	ComposePlan,
	ComposePlanService,
	ComposeRewrite,
} from "#/api/gen/types.gen";
import {
	basename,
	buildImportRequest,
	buildPlanRequest,
	buildSource,
	classifyUpload,
	convertedTargets,
	dockerOptionTexts,
	effectiveFiles,
	emptySource,
	envRowState,
	groupBinds,
	groupRewrites,
	healthSummary,
	importFailure,
	isIncluded,
	limitsText,
	mergeKnownPaths,
	notesBySeverity,
	parsePortField,
	publishedKeys,
	setConvert,
	setDomain,
	setEnvChoice,
	setImage,
	setInclude,
	setManaged,
	setPort,
	setPublish,
	setServiceGit,
	setServiceName,
	setSourceKind,
	shortImage,
	sourceChip,
	sourceReady,
	stageBlocker,
	summarizeChecks,
	summarizePlan,
	summaryText,
	uploadPath,
} from "./lib";

function service(
	over: Partial<ComposePlanService> & { key: string },
): ComposePlanService {
	return {
		binds: [],
		dependsOn: [],
		domains: [],
		env: [],
		files: [],
		include: true,
		kind: "app",
		name: over.key,
		needs: [],
		networks: [],
		notes: [],
		oneShot: false,
		port: 0,
		portHttp: false,
		ports: [],
		replicas: 1,
		slug: over.key,
		source: { type: "image", image: "nginx:1.27" },
		volumes: [],
		...over,
	};
}

function plan(over: Partial<ComposePlan> = {}): ComposePlan {
	return {
		blockers: [],
		checks: [],
		installable: true,
		missing: [],
		mode: "split",
		name: "shop",
		neededFiles: [],
		networks: [],
		notes: [],
		rewrites: [],
		services: [],
		sourceKind: "paste",
		warnings: [],
		...over,
	};
}

const COMPOSE = "services:\n  web:\n    image: nginx\n";

describe("source", () => {
	test("paste needs a Compose file, git needs a repository", () => {
		const draft = emptySource();
		expect(sourceReady(draft)).toBe(false);
		expect(sourceReady({ ...draft, compose: "  \n" })).toBe(false);
		expect(sourceReady({ ...draft, compose: COMPOSE })).toBe(true);

		const git = { ...draft, mode: "git" as const };
		expect(sourceReady(git)).toBe(false);
		expect(
			sourceReady({
				...git,
				git: { ...git.git, connectionId: "c1", repo: "acme/shop" },
			}),
		).toBe(true);
		expect(
			sourceReady({ ...git, git: { ...git.git, mode: "url", url: " " } }),
		).toBe(false);
		expect(
			sourceReady({
				...git,
				git: { ...git.git, mode: "url", url: "https://github.com/a/b" },
			}),
		).toBe(true);
	});

	test("classifies picked files", () => {
		expect(classifyUpload("docker-compose.yml", false)).toBe("compose");
		expect(classifyUpload("compose.yaml", true)).toBe("compose");
		expect(classifyUpload("docker-compose.prod.yml", false)).toBe("compose");
		expect(classifyUpload(".env", false)).toBe("env");
		expect(classifyUpload("app/.env", true)).toBe("env");
		expect(classifyUpload("stack.yml", false)).toBe("compose");
		expect(classifyUpload("prometheus.yml", true)).toBe("extra");
		expect(classifyUpload("nginx.conf", false)).toBe("extra");
	});

	test("a paste body carries the text, .env, files and the group name", () => {
		const draft = {
			...emptySource(),
			compose: COMPOSE,
			env: "TAG=1\n",
			name: " Shop ",
			files: { "./app.env": "A=1" },
		};
		expect(buildSource(draft, [])).toEqual({
			compose: COMPOSE,
			env: "TAG=1\n",
			files: { "./app.env": "A=1" },
			name: "Shop",
		});
		expect(buildSource({ ...emptySource(), compose: COMPOSE }, [])).toEqual({
			compose: COMPOSE,
		});
	});

	test("a git body names the repository, never the Compose text", () => {
		const draft = {
			...emptySource(),
			mode: "git" as const,
			compose: COMPOSE,
			git: {
				mode: "account" as const,
				connectionId: "c1",
				repo: " acme/shop ",
				url: "",
				branch: "main",
				path: "deploy/compose.yml",
			},
		};
		expect(buildSource(draft, [])).toEqual({
			repo: {
				connectionId: "c1",
				repo: "acme/shop",
				branch: "main",
				path: "deploy/compose.yml",
			},
		});
		const byUrl = {
			...draft,
			git: {
				...draft.git,
				mode: "url" as const,
				url: "https://github.com/a/b",
			},
		};
		expect(buildSource(byUrl, []).repo).toEqual({
			url: "https://github.com/a/b",
			branch: "main",
			path: "deploy/compose.yml",
		});
	});

	test("uploads match a needed file by file name and keep the match", () => {
		const upload = {
			id: "u1",
			name: "nginx.conf",
			path: "./nginx.conf",
			pathEdited: false,
			content: "worker_processes 1;",
		};
		expect(uploadPath(upload, [])).toBe("./nginx.conf");
		const first = plan({
			neededFiles: [
				{
					path: "./conf/nginx.conf",
					purpose: "bind",
					required: true,
					service: "web",
				},
			],
		});
		const known = mergeKnownPaths([], first);
		expect(known).toEqual(["./conf/nginx.conf"]);
		expect(uploadPath(upload, known)).toBe("./conf/nginx.conf");

		// Once attached, the plan stops asking: the match must not flip back.
		const second = plan({
			services: [
				service({
					key: "web",
					files: [
						{
							bytes: 19,
							path: "/etc/nginx/nginx.conf",
							source: "./conf/nginx.conf",
						},
					],
				}),
			],
		});
		const still = mergeKnownPaths(known, second);
		expect(still).toContain("./conf/nginx.conf");
		expect(uploadPath(upload, still)).toBe("./conf/nginx.conf");

		// A path the reader typed always wins.
		expect(
			uploadPath({ ...upload, path: "./other", pathEdited: true }, known),
		).toBe("./other");
	});

	test("pasted text wins over an upload for the same path; empty text does not", () => {
		const upload = {
			id: "u1",
			name: "app.env",
			path: "./app.env",
			pathEdited: false,
			content: "FROM=upload",
		};
		const known = ["./app.env"];
		expect(
			effectiveFiles(
				{ uploads: [upload], files: { "./app.env": "FROM=paste" } },
				known,
			),
		).toEqual({ "./app.env": "FROM=paste" });
		expect(
			effectiveFiles({ uploads: [upload], files: { "./app.env": "" } }, known),
		).toEqual({ "./app.env": "FROM=upload" });
	});

	test("mergeKnownPaths only grows and keeps identity of order", () => {
		const a = plan({
			neededFiles: [
				{ path: "./a", purpose: "env_file", required: true, service: "x" },
			],
		});
		const known = mergeKnownPaths([], a);
		expect(mergeKnownPaths(known, undefined)).toEqual(known);
		expect(mergeKnownPaths(known, plan())).toEqual(known);
	});

	test("basename ignores directories and trailing slashes", () => {
		expect(basename("./conf/nginx.conf")).toBe("nginx.conf");
		expect(basename("conf/")).toBe("conf");
		expect(basename("file")).toBe("file");
	});
});

describe("requests", () => {
	test("plan request leaves out an unknown server; import carries it", () => {
		const draft = { ...emptySource(), compose: COMPOSE };
		const choices: ComposeChoices = {};
		expect(buildPlanRequest(draft, choices, "", [])).toEqual({
			source: { compose: COMPOSE },
			choices: {},
		});
		expect(buildPlanRequest(draft, choices, "s1", []).serverId).toBe("s1");
		const staged = buildImportRequest(draft, choices, "s1", [], false);
		expect(staged).toEqual({
			source: { compose: COMPOSE },
			choices: {},
			serverId: "s1",
		});
		expect(buildImportRequest(draft, choices, "s1", [], true).deploy).toBe(
			true,
		);
	});

	test("secret values never travel: choices carry flags, not values", () => {
		const secretValue = "hunter2-very-secret";
		const draft = {
			...emptySource(),
			compose: `services:\n  db:\n    image: postgres\n    environment:\n      PASSWORD: ${secretValue}\n`,
		};
		let choices: ComposeChoices = {};
		choices = setEnvChoice(choices, "db", "PASSWORD", { secret: true });
		choices = setEnvChoice(choices, "db", "TOKEN", { skip: true });
		choices = setEnvChoice(choices, "db", "DB_HOST", { rewrite: false });

		const body = buildImportRequest(draft, choices, "s1", [], false);
		// The choices are flags only.
		const env = body.choices.services?.db?.env ?? {};
		for (const entry of Object.values(env)) {
			expect(
				Object.keys(entry).every((k) =>
					["secret", "skip", "rewrite"].includes(k),
				),
			).toBe(true);
		}
		expect(JSON.stringify(body.choices)).not.toContain(secretValue);
		// The one place the text appears is the source the reader wrote.
		const json = JSON.stringify(body);
		expect(json.split(secretValue).length - 1).toBe(1);
		expect(body.source.compose).toContain(secretValue);
	});
});

describe("choices", () => {
	const web = service({
		key: "web",
		port: 80,
		portHttp: true,
		ports: [
			{ port: 80, protocol: "tcp", role: "http", publish: false },
			{
				port: 5432,
				protocol: "tcp",
				role: "tcp",
				publish: false,
				listen: 5432,
			},
			{ port: 53, protocol: "udp", role: "udp", publish: true, listen: 53 },
		],
		binds: [
			{
				source: "./data",
				target: "/data",
				status: "ignored",
				canConvert: true,
			},
			{
				source: "/var/run/docker.sock",
				target: "/var/run/docker.sock",
				status: "rejected",
				reason: "Mounting the Docker socket is not allowed.",
				canConvert: false,
			},
			{
				source: "./nginx.conf",
				target: "/etc/nginx/nginx.conf",
				status: "file",
				canConvert: false,
			},
		],
	});

	test("reducers do not mutate their input", () => {
		const start: ComposeChoices = { services: { web: { include: false } } };
		const frozen = JSON.parse(JSON.stringify(start));
		setInclude(start, "web", true);
		setServiceName(start, "web", "Front");
		setPort(start, "web", 8080);
		setManaged(start, "web", true);
		setEnvChoice(start, "web", "A", { secret: true });
		setPublish(start, web, "5432/tcp", true);
		setConvert(start, web, "/data", true);
		expect(start).toEqual(frozen);
	});

	test("every toggle round-trips to the empty choice", () => {
		let c: ComposeChoices = {};
		c = setInclude(c, "web", false);
		c = setServiceName(c, "web", "Front");
		c = setPort(c, "web", 8080);
		c = setManaged(c, "web", true);
		c = setImage(c, "web", "nginx:1");
		c = setSourceKind(c, "web", "image");
		c = setDomain(c, "web", { enabled: true, host: "shop.example.com" });
		c = setEnvChoice(c, "web", "A", { secret: false });
		c = setEnvChoice(c, "web", "B", { skip: true });
		c = setEnvChoice(c, "web", "C", { rewrite: false });
		expect(JSON.parse(JSON.stringify(c))).toEqual(c);
		expect(c.services?.web).toEqual({
			include: false,
			name: "Front",
			port: 8080,
			managedDatabase: true,
			image: "nginx:1",
			source: "image",
			domain: { enabled: true, host: "shop.example.com" },
			env: { A: { secret: false }, B: { skip: true }, C: { rewrite: false } },
		});

		// Undo what can be undone: the choice object shrinks back to nothing.
		c = setServiceName(c, "web", "");
		c = setPort(c, "web", undefined);
		c = setImage(c, "web", "");
		c = setSourceKind(c, "web", undefined);
		c = setDomain(c, "web", undefined);
		c = setEnvChoice(c, "web", "A", { secret: null });
		c = setEnvChoice(c, "web", "B", { skip: false });
		c = setEnvChoice(c, "web", "C", { rewrite: true });
		expect(c.services?.web).toEqual({ include: false, managedDatabase: true });
	});

	test("an empty service choice is removed from the map", () => {
		let c = setServiceName({}, "web", "Front");
		c = setServiceName(c, "web", "");
		expect(c).toEqual({});
		c = setEnvChoice({}, "db", "A", { skip: true });
		c = setEnvChoice(c, "db", "A", { skip: false });
		expect(c).toEqual({});
	});

	test("choices of other services are untouched", () => {
		let c = setInclude({}, "web", false);
		c = setInclude(c, "db", true);
		c = setServiceName(c, "db", "Database");
		expect(c.services?.web).toEqual({ include: false });
		expect(c.services?.db).toEqual({ include: true, name: "Database" });
	});

	test("include reads the choice over the plan", () => {
		const off = service({
			key: "debug",
			include: false,
			includeNote: "profile",
		});
		expect(isIncluded(off, {})).toBe(false);
		expect(isIncluded(off, setInclude({}, "debug", true))).toBe(true);
		expect(isIncluded(web, setInclude({}, "web", false))).toBe(false);
	});

	test("publishing starts from the plan's ports and keeps the HTTP port out", () => {
		expect(publishedKeys(web, {})).toEqual(["53/udp"]);
		let c = setPublish({}, web, "5432/tcp", true);
		expect(c.services?.web?.publishPorts).toEqual(["53/udp", "5432/tcp"]);
		expect(publishedKeys(web, c)).toEqual(["53/udp", "5432/tcp"]);
		c = setPublish(c, web, "53/udp", false);
		expect(publishedKeys(web, c)).toEqual(["5432/tcp"]);
		// Turning the last one off keeps an explicit empty list: "none", not "default".
		c = setPublish(c, web, "5432/tcp", false);
		expect(c.services?.web?.publishPorts).toEqual([]);
		expect(publishedKeys(web, c)).toEqual([]);
		// Toggling twice never duplicates.
		c = setPublish(
			setPublish({}, web, "5432/tcp", true),
			web,
			"5432/tcp",
			true,
		);
		expect(c.services?.web?.publishPorts).toEqual(["53/udp", "5432/tcp"]);
	});

	test("converting a bind mount to a volume", () => {
		expect(convertedTargets(web, {})).toEqual([]);
		const c = setConvert({}, web, "/data", true);
		expect(c.services?.web?.convertBinds).toEqual(["/data"]);
		expect(convertedTargets(web, setConvert(c, web, "/data", false))).toEqual(
			[],
		);
		const converted = service({
			key: "x",
			binds: [
				{ source: "./d", target: "/d", status: "converted", canConvert: true },
			],
		});
		expect(convertedTargets(converted, {})).toEqual(["/d"]);
	});

	test("binds group into what needs a word and what can convert", () => {
		const groups = groupBinds(web.binds);
		expect(groups.attention.map((b) => b.source)).toEqual([
			"/var/run/docker.sock",
		]);
		expect(groups.convertible.map((b) => b.target)).toEqual(["/data"]);
		expect(groupBinds(null)).toEqual({ attention: [], convertible: [] });
	});

	test("domain choice keeps the host only when enabled", () => {
		expect(
			setDomain({}, "web", { enabled: true, host: " a.example.com " }),
		).toEqual({
			services: { web: { domain: { enabled: true, host: "a.example.com" } } },
		});
		expect(
			setDomain({}, "web", { enabled: false, host: "a.example.com" }),
		).toEqual({
			services: { web: { domain: { enabled: false } } },
		});
		expect(setDomain({}, "web", { enabled: true })).toEqual({
			services: { web: { domain: { enabled: true } } },
		});
	});

	test("git choice switches between URL and connection", () => {
		let c = setServiceGit({}, "api", {
			gitUrl: "https://github.com/a/b",
			gitBranch: "main",
		});
		expect(c.services?.api).toEqual({
			gitUrl: "https://github.com/a/b",
			gitBranch: "main",
		});
		c = setServiceGit(c, "api", {
			connectionId: "c1",
			repo: "a/b",
			gitBranch: "dev",
		});
		expect(c.services?.api).toEqual({
			connectionId: "c1",
			repo: "a/b",
			gitBranch: "dev",
		});
		expect(setServiceGit(c, "api", {})).toEqual({});
	});

	test("an env row reads the choice over the plan", () => {
		const row = {
			key: "DB_HOST",
			value: "db",
			origin: "environment" as const,
			rewritten: true,
			secret: false,
			secretGuess: true,
			skipped: false,
		};
		expect(envRowState(row, undefined)).toEqual({
			secret: false,
			skipped: false,
			skippedByPlan: false,
			rewritten: true,
			canKeepHosts: true,
			keepHosts: false,
		});
		const kept = envRowState({ ...row, rewritten: false }, { rewrite: false });
		expect(kept.canKeepHosts).toBe(true);
		expect(kept.keepHosts).toBe(true);
		expect(envRowState(row, { secret: true }).secret).toBe(true);
		expect(
			envRowState({ ...row, skipped: true }, undefined).skippedByPlan,
		).toBe(true);
		expect(
			envRowState({ ...row, skipped: true }, { skip: true }).skippedByPlan,
		).toBe(false);
		expect(
			envRowState({ ...row, rewritten: false }, undefined).canKeepHosts,
		).toBe(false);
	});
});

describe("reading a plan", () => {
	const rewrite = (
		service: string,
		key: string,
		extra: Partial<ComposeRewrite> = {},
	): ComposeRewrite => ({
		service,
		key,
		from: "db",
		to: `\${{db.HOST}}`,
		kind: "host",
		reason: "Points at the db service",
		target: "db",
		...extra,
	});

	test("rewrites group per service in order of appearance", () => {
		const groups = groupRewrites([
			rewrite("api", "DB_HOST"),
			rewrite("worker", "DB_HOST"),
			rewrite("api", "REDIS_URL"),
		]);
		expect(groups.map((g) => [g.service, g.items.map((i) => i.key)])).toEqual([
			["api", ["DB_HOST", "REDIS_URL"]],
			["worker", ["DB_HOST"]],
		]);
		expect(groupRewrites(null)).toEqual([]);
	});

	test("notes split by severity", () => {
		const notes: ComposeNote[] = [
			{ key: "privileged", severity: "danger", message: "a" },
			{ key: "ulimits", severity: "info", message: "b" },
			{ key: "cap_add", severity: "warning", message: "c" },
			{ key: "x", severity: "info", message: "d" },
		];
		const by = notesBySeverity(notes);
		expect(by.danger.map((n) => n.key)).toEqual(["privileged"]);
		expect(by.warning.map((n) => n.key)).toEqual(["cap_add"]);
		expect(by.info.map((n) => n.key)).toEqual(["ulimits", "x"]);
		expect(notesBySeverity(undefined)).toEqual({
			danger: [],
			warning: [],
			info: [],
		});
	});

	test("summary counts included services and warnings", () => {
		const p = plan({
			services: [
				service({
					key: "web",
					notes: [
						{ key: "privileged", severity: "danger", message: "x" },
						{ key: "i", severity: "info", message: "y" },
					],
				}),
				service({
					key: "debug",
					include: false,
					notes: [{ key: "w", severity: "warning", message: "z" }],
				}),
			],
			rewrites: [rewrite("web", "A"), rewrite("web", "B")],
			warnings: ["one"],
			notes: [{ key: "version", severity: "warning", message: "v" }],
			blockers: ["x"],
		});
		expect(summarizePlan(p)).toEqual({
			services: 1,
			total: 2,
			rewrites: 2,
			warnings: 3,
			blockers: 1,
		});
		// Including the gated service adds its warning.
		expect(summarizePlan(p, setInclude({}, "debug", true))).toMatchObject({
			services: 2,
			warnings: 4,
		});
		expect(summaryText(summarizePlan(p))).toBe(
			"1 service, 2 rewritten references, 3 warnings",
		);
		expect(
			summaryText({
				services: 2,
				total: 2,
				rewrites: 1,
				warnings: 1,
				blockers: 0,
			}),
		).toBe("2 services, 1 rewritten reference, 1 warning");
	});

	test("checks list failures, then warnings, then passes, and keep every row", () => {
		const summary = summarizeChecks([
			{ id: "secrets", status: "ok", message: "a" },
			{ id: "server", status: "warn", message: "b" },
			{ id: "domain", status: "fail", message: "c" },
			{ id: "proxy-restart", status: "warn", message: "d" },
		]);
		expect(summary.rows.map((r) => r.id)).toEqual([
			"domain",
			"server",
			"proxy-restart",
			"secrets",
		]);
		expect([summary.fail, summary.warn, summary.ok]).toEqual([1, 2, 1]);
		expect(summarizeChecks(null).rows).toEqual([]);
	});

	test("the source chip names what the service runs", () => {
		expect(sourceChip(service({ key: "web" }))).toEqual({
			kind: "image",
			text: "nginx:1.27",
		});
		expect(
			sourceChip(
				service({
					key: "api",
					source: { type: "git", build: { argNames: [], context: "." } },
				}),
			),
		).toEqual({ kind: "git", text: "Build from git" });
		const db = service({
			key: "db",
			kind: "database",
			source: { type: "image", image: "postgres:16" },
			managed: {
				eligible: true,
				enabled: false,
				engine: "postgres",
				version: "16",
				notes: [],
			},
		});
		expect(sourceChip(db).kind).toBe("image");
		expect(sourceChip(db, setManaged({}, "db", true))).toEqual({
			kind: "managed",
			text: "Managed PostgreSQL 16",
		});
		// Switching a build to its image shows the image the reader typed.
		const built = service({
			key: "api",
			source: { type: "git", build: { argNames: [], context: "." } },
		});
		let c = setSourceKind({}, "api", "image");
		c = setImage(c, "api", "ghcr.io/acme/api:2");
		expect(sourceChip(built, c)).toEqual({ kind: "image", text: "acme/api:2" });
	});

	test("short image drops the registry and the digest", () => {
		expect(shortImage("ghcr.io/acme/app:1.4")).toBe("acme/app:1.4");
		expect(shortImage("postgres:16@sha256:abc")).toBe("postgres:16");
		expect(shortImage("registry.example.com/x:1")).toBe(
			"registry.example.com/x:1",
		);
	});

	test("health and limits read in one line", () => {
		expect(healthSummary(undefined)).toBeNull();
		expect(healthSummary({ type: "none", fromCompose: true })).toBeNull();
		expect(
			healthSummary({
				type: "cmd",
				command: ["pg_isready", "-U", "app"],
				intervalSeconds: 10,
				fromCompose: true,
			}),
		).toBe("cmd pg_isready -U app every 10s");
		expect(limitsText(service({ key: "a" }))).toBeNull();
		expect(
			limitsText(service({ key: "a", memoryMb: 512, cpus: 0.5, replicas: 2 })),
		).toBe("512 MB · 0.5 CPU · 2 replicas");
		expect(limitsText(service({ key: "a", memoryMb: 1536 }))).toBe("1.5 GB");
	});

	test("port field", () => {
		expect(parsePortField("")).toBeUndefined();
		expect(parsePortField(" 8080 ")).toBe(8080);
		expect(Number.isNaN(parsePortField("80a"))).toBe(true);
		expect(Number.isNaN(parsePortField("70000"))).toBe(true);
	});

	test("Stage says why it is not ready", () => {
		const ok = plan();
		expect(
			stageBlocker({
				plan: undefined,
				included: 0,
				serverId: "s",
				fresh: true,
			}),
		).toBe("Review the plan first.");
		expect(
			stageBlocker({ plan: ok, included: 1, serverId: "", fresh: true }),
		).toBe("Choose a server.");
		expect(
			stageBlocker({ plan: ok, included: 1, serverId: "s", fresh: false }),
		).toBe("Checking the plan.");
		expect(
			stageBlocker({ plan: ok, included: 0, serverId: "s", fresh: true }),
		).toBe("Include at least one service.");
		expect(
			stageBlocker({
				plan: plan({
					installable: false,
					blockers: ["The file has no services."],
				}),
				included: 1,
				serverId: "s",
				fresh: true,
			}),
		).toBe("The file has no services.");
		expect(
			stageBlocker({ plan: ok, included: 1, serverId: "s", fresh: true }),
		).toBeNull();
	});
});

describe("container options", () => {
	test("each option reads as one phrase and nothing prints when absent", () => {
		expect(dockerOptionTexts(undefined)).toEqual([]);
		expect(dockerOptionTexts({})).toEqual([]);
		expect(
			dockerOptionTexts({
				capAdd: ["NET_BIND_SERVICE"],
				ulimits: [{ name: "nofile", soft: 1024, hard: 4096 }],
				shmSizeMb: 256,
				extraHosts: ["host.docker.internal:host-gateway"],
				init: true,
				stopSignal: "SIGINT",
				entrypoint: ["/entry.sh", "serve"],
				tmpfs: [{ path: "/tmp", sizeMb: 64 }],
			}),
		).toEqual([
			"cap_add NET_BIND_SERVICE",
			"ulimit nofile 1024:4096",
			"shm 256 MB",
			"extra hosts host.docker.internal:host-gateway",
			"init",
			"stop signal SIGINT",
			"entrypoint /entry.sh serve",
			"tmpfs /tmp 64 MB",
		]);
	});
});

describe("import failures", () => {
	test("blockers from the details are named", () => {
		const failure = importFailure({
			status: 422,
			code: "import_blocked",
			message: "blocked",
			details: { blockers: ["No services.", "Port clash."] },
		});
		expect(failure.body).toBe("No services. Port clash. Nothing was staged.");
		expect(failure.replan).toBe(true);
	});

	test("a conflict replans, a 403 does not", () => {
		expect(
			importFailure({ status: 409, code: "import_conflict", message: "Taken." })
				.replan,
		).toBe(true);
		const denied = importFailure({
			status: 403,
			code: "forbidden",
			message: "Needs deployments:write.",
		});
		expect(denied.replan).toBe(false);
		expect(denied.body).toBe("Needs deployments:write.");
	});
});
