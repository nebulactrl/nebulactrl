import { describe, expect, test } from "bun:test";
import type { ProcessConfig } from "#/api/models";
import { makeService } from "#/features/canvas/lib/fixtures";
import { newProcessDraft } from "#/features/processes/draft";
import { applyHardened, FALLBACK_POLICY } from "./docker-options";
import {
	buildConfig,
	buildPatch,
	isDirty,
	splitCommand,
	toDraft,
	validateDraft,
} from "./draft";

describe("splitCommand", () => {
	test("groups quoted arguments", () => {
		expect(splitCommand(`node server.js --name "my app" 'a b'`)).toEqual([
			"node",
			"server.js",
			"--name",
			"my app",
			"a b",
		]);
		expect(splitCommand("")).toEqual([]);
	});
});

describe("settings draft", () => {
	const service = makeService({ id: "s1", name: "web" });
	const baseline = toDraft(service);

	test("an untouched draft produces no patch", () => {
		expect(
			buildPatch(baseline, { ...baseline }, service.sourceType),
		).toBeNull();
	});

	test("a config edit sends the whole config", () => {
		const patch = buildPatch(
			baseline,
			{ ...baseline, stopGraceSeconds: "45" },
			service.sourceType,
		);
		expect(patch?.config?.stopGraceSeconds).toBe(45);
		expect(patch?.config?.healthcheck?.type).toBe(
			service.config.healthcheck.type,
		);
		expect(patch?.name).toBeUndefined();
	});

	test("write-only fields are sent only when typed or cleared", () => {
		expect(
			buildPatch(baseline, { ...baseline, gitToken: "ghp_x" }, "git")?.gitToken,
		).toBe("ghp_x");
		expect(
			buildPatch(baseline, { ...baseline, clearGitToken: true }, "git")
				?.gitToken,
		).toBe("");
		expect(
			buildPatch(baseline, { ...baseline }, "git")?.gitToken,
		).toBeUndefined();
	});

	test("validation names the bad field", () => {
		const errors = validateDraft(
			{ ...baseline, stopGraceSeconds: "0", port: "70000" },
			"image",
		);
		expect(errors.stopGraceSeconds).toBeDefined();
		expect(errors.port).toBeDefined();
		expect(validateDraft(baseline, service.sourceType)).toEqual({});
	});

	test("git paths cannot leave the repository", () => {
		const errors = validateDraft(
			{
				...baseline,
				gitUrl: "https://x/y.git",
				dockerfilePath: "../Dockerfile",
			},
			"git",
		);
		expect(errors.dockerfilePath).toBeDefined();
	});

	test("buildConfig omits an empty memory limit", () => {
		expect(buildConfig(baseline, "image").memoryMb).toBeUndefined();
	});

	test("a git service defaults to the automatic build method", () => {
		expect(baseline.buildType).toBe("auto");
		const draft = { ...baseline, gitUrl: "https://x/y.git", gitBranch: "main" };
		expect(buildConfig(draft, "git").buildType).toBe("auto");
		expect(buildConfig(draft, "image").buildType).toBeUndefined();
	});

	test("picking a build method is a config change", () => {
		const draft = { ...baseline, buildType: "railpack" };
		expect(buildPatch(baseline, draft, "git")?.config?.buildType).toBe(
			"railpack",
		);
		const starter = { ...baseline, buildType: "starter:node" };
		expect(buildPatch(baseline, starter, "git")?.config?.buildType).toBe(
			"starter:node",
		);
	});

	test("submodules are automatic by default and only on/off are sent", () => {
		expect(baseline.submodules).toBe("auto");
		const git = { ...baseline, gitUrl: "https://x/y.git", gitBranch: "main" };
		expect(buildConfig(git, "git").submodules).toBeUndefined();
		expect(buildConfig({ ...git, submodules: "on" }, "git").submodules).toBe(
			"on",
		);
		expect(buildConfig({ ...git, submodules: "off" }, "git").submodules).toBe(
			"off",
		);
		expect(
			buildConfig({ ...git, submodules: "on" }, "image").submodules,
		).toBeUndefined();
	});

	test("choosing a submodule mode is a config change", () => {
		const draft = { ...baseline, submodules: "on" as const };
		expect(buildPatch(baseline, draft, "git")?.config?.submodules).toBe("on");
		expect(buildPatch(baseline, baseline, "git")).toBeNull();
	});
});

// PATCH replaces the whole config, so a form that does not carry a field
// back would silently delete it on the next save (template services bring
// files, a one-shot flag and an exec health command).
describe("template config round-trip", () => {
	const files = [
		{ path: "/etc/app/config.toml", mode: "0444", content: 'a = 1\nb = "x"\n' },
		{ path: "/etc/app/plain.conf", content: "" },
	];
	const exec = ["CMD-SHELL", 'pg_isready -U "app user" || exit 1'];
	const service = makeService({
		id: "tpl",
		config: {
			image: "postgres:17@sha256:abc",
			healthcheck: {
				type: "exec",
				command: exec,
				intervalSeconds: 5,
				timeoutSeconds: 3,
				startPeriodSeconds: 10,
				deadlineSeconds: 180,
				successThreshold: 1,
			},
			stopGraceSeconds: 30,
			imageRetention: 5,
			files,
			oneshot: false,
			processes: [],
		},
	});

	test("an untouched service saves back files and the exec command unchanged", () => {
		const draft = toDraft(service);
		const config = buildConfig(draft, service.sourceType);
		expect(config.files).toEqual(files);
		expect(config.healthcheck?.type).toBe("exec");
		expect(config.healthcheck?.command).toEqual(exec);
		expect(config.oneshot).toBeUndefined();
		expect(buildPatch(draft, { ...draft }, service.sourceType)).toBeNull();
	});

	test("editing another field still sends files and the command", () => {
		const draft = toDraft(service);
		const patch = buildPatch(
			draft,
			{ ...draft, stopGraceSeconds: "60" },
			service.sourceType,
		);
		expect(patch?.config?.stopGraceSeconds).toBe(60);
		expect(patch?.config?.files).toHaveLength(2);
		expect(patch?.config?.healthcheck?.command).toEqual(exec);
	});

	test("a one-shot job keeps its flag", () => {
		const oneshot = makeService({
			config: {
				image: "migrate:1",
				healthcheck: {
					type: "none",
					intervalSeconds: 2,
					timeoutSeconds: 3,
					startPeriodSeconds: 0,
					deadlineSeconds: 900,
					successThreshold: 2,
				},
				stopGraceSeconds: 30,
				imageRetention: 5,
				oneshot: true,
				processes: [],
			},
		});
		const draft = toDraft(oneshot);
		expect(draft.oneshot).toBe(true);
		expect(buildConfig(draft, "image").oneshot).toBe(true);
		expect(buildConfig(draft, "image").port).toBeUndefined();
		expect(validateDraft(draft, "image")).toEqual({});
	});

	test("the command text survives quotes and backslashes", () => {
		const argv = ["CMD", "sh", "-c", 'echo "a b" \\ c', "it's", ""];
		const tricky = makeService({
			config: {
				...service.config,
				healthcheck: { ...service.config.healthcheck, command: argv },
			},
		});
		const draft = toDraft(tricky);
		expect(splitCommand(draft.healthCommand)).toEqual(argv);
		expect(buildConfig(draft, "image").healthcheck?.command).toEqual(argv);
	});

	test("a command is dropped once the check is no longer exec", () => {
		const draft = { ...toDraft(service), healthType: "http" as const };
		expect(buildConfig(draft, "image").healthcheck?.command).toBeUndefined();
	});

	test("exec needs CMD or CMD-SHELL and a command", () => {
		const draft = toDraft(service);
		for (const bad of ["", "CMD", "pg_isready -U app", "cmd pg_isready"]) {
			expect(
				validateDraft({ ...draft, healthCommand: bad }, "image").healthCommand,
			).toBeDefined();
		}
		expect(
			validateDraft({ ...draft, healthCommand: "CMD true" }, "image")
				.healthCommand,
		).toBeUndefined();
	});

	test("files are checked like the control plane checks them", () => {
		const draft = toDraft(service);
		const withFile = (file: {
			path: string;
			mode?: string;
			content?: string;
		}) =>
			validateDraft(
				{
					...draft,
					files: [{ mode: "", content: "", ...file }],
				},
				"image",
			).files;
		expect(withFile({ path: "/etc/ok.conf", mode: "0644" })).toBeUndefined();
		expect(withFile({ path: "etc/rel.conf" })).toBeDefined();
		expect(withFile({ path: "/etc/../x" })).toBeDefined();
		expect(withFile({ path: "/etc/dir/" })).toBeDefined();
		expect(withFile({ path: "/etc/ok.conf", mode: "999" })).toBeDefined();
		expect(
			withFile({ path: "/etc/big", content: "x".repeat(64 * 1024 + 1) }),
		).toBeDefined();
		expect(
			withFile({ path: "/etc/big", content: "é".repeat(33 * 1024) }),
		).toBeDefined();

		const same = { path: "/a", mode: "", content: "" };
		expect(
			validateDraft({ ...draft, files: [same, same] }, "image").files,
		).toBeDefined();
		const many = Array.from({ length: 17 }, (_, index) => ({
			path: `/f${index}`,
			mode: "",
			content: "",
		}));
		expect(
			validateDraft({ ...draft, files: many }, "image").files,
		).toBeDefined();
	});

	test("a one-shot job cannot keep a port", () => {
		const draft = { ...toDraft(service), oneshot: true, port: "80" };
		expect(validateDraft(draft, "image").port).toBeDefined();
	});
});

// PATCH replaces the whole config, so the tailnet must survive every save.
describe("tailnet in the settings draft", () => {
	const onTailnet = makeService({
		id: "s-tailnet",
		config: {
			...makeService().config,
			tailnet: {
				enabled: true,
				hostname: "web-prod",
				web: true,
				tcp: [{ listen: 5432, target: 5432 }],
				hidePublic: true,
			},
		},
	});

	test("a service that is not on a tailnet sends no tailnet", () => {
		const draft = toDraft(makeService());
		expect(draft.tailnet).toBeNull();
		expect(buildConfig(draft, "image")).not.toHaveProperty("tailnet");
		expect(validateDraft(draft, "image")).toEqual({});
	});

	test("a disabled draft sends no tailnet and is never invalid", () => {
		const draft = toDraft(onTailnet);
		const off = draft.tailnet && { ...draft.tailnet, enabled: false };
		expect(off).not.toBeNull();
		const config = buildConfig({ ...draft, tailnet: off }, "image");
		expect(config).not.toHaveProperty("tailnet");
	});

	test("an untouched service saves its tailnet back unchanged", () => {
		const draft = toDraft(onTailnet);
		expect(buildConfig(draft, "image").tailnet).toEqual(
			onTailnet.config.tailnet,
		);
		const patch = buildPatch(
			draft,
			{ ...draft, stopGraceSeconds: "60" },
			"image",
		);
		expect(patch?.config?.tailnet).toEqual(onTailnet.config.tailnet);
		expect(buildPatch(draft, { ...draft }, "image")).toBeNull();
	});

	test("turning the tailnet on or off is a config change", () => {
		const plain = toDraft(makeService());
		const on = toDraft(onTailnet);
		expect(
			buildPatch(plain, { ...plain, tailnet: on.tailnet }, "image")?.config
				?.tailnet?.enabled,
		).toBe(true);
		const patch = buildPatch(on, { ...on, tailnet: null }, "image");
		expect(patch?.config).toBeDefined();
		expect(patch?.config).not.toHaveProperty("tailnet");
	});

	test("tailnet problems are reported under their own keys", () => {
		const draft = toDraft(onTailnet);
		const errors = validateDraft(
			{
				...draft,
				tailnet: draft.tailnet && {
					...draft.tailnet,
					hostname: "Not_Valid",
					extraTags: ["nebula"],
				},
			},
			"image",
		);
		expect(errors.tailnetHostname).toBeDefined();
		expect(errors.tailnetTags).toBeDefined();
		expect(errors.port).toBeUndefined();
	});

	test("the web exposure follows the port field", () => {
		const draft = toDraft(onTailnet);
		expect(
			validateDraft({ ...draft, port: "" }, "image").tailnetWeb,
		).toBeDefined();
	});
});

describe("block outbound internet", () => {
	const open = makeService({ id: "s1", name: "web" });
	const blocked = makeService({
		id: "s2",
		name: "db",
		config: { ...open.config, blockEgress: true },
	});

	test("the draft follows the config", () => {
		expect(toDraft(open).blockEgress).toBe(false);
		expect(toDraft(blocked).blockEgress).toBe(true);
	});

	test("toggling it stages a config change that says so either way", () => {
		const baseline = toDraft(open);
		const on = buildPatch(
			baseline,
			{ ...baseline, blockEgress: true },
			"image",
		);
		expect(on?.config?.blockEgress).toBe(true);

		const was = toDraft(blocked);
		const off = buildPatch(was, { ...was, blockEgress: false }, "image");
		expect(off?.config?.blockEgress).toBe(false);
	});
});

describe("post-deploy command", () => {
	const service = makeService({ id: "s1", name: "web" });
	const baseline = toDraft(service);

	test("an empty command sends none and the timeout is ignored", () => {
		expect(baseline.postDeployCommand).toBe("");
		const patch = buildPatch(
			baseline,
			{ ...baseline, stopGraceSeconds: "45", postDeployTimeoutSeconds: "90" },
			"image",
		);
		expect(patch?.config?.postDeployCommand).toBeUndefined();
		expect(patch?.config?.postDeployTimeoutSeconds).toBeUndefined();
	});

	test("a command is split into argv with its timeout and stages a config change", () => {
		const patch = buildPatch(
			baseline,
			{
				...baseline,
				postDeployCommand: `rake cache:warm --scope "all sites"`,
				postDeployTimeoutSeconds: "90",
			},
			"image",
		);
		expect(patch?.config?.postDeployCommand).toEqual([
			"rake",
			"cache:warm",
			"--scope",
			"all sites",
		]);
		expect(patch?.config?.postDeployTimeoutSeconds).toBe(90);
	});

	test("the draft round-trips a saved command", () => {
		const saved = makeService({
			id: "s2",
			name: "api",
			config: {
				...service.config,
				postDeployCommand: ["rake", "cache:warm", "a b"],
				postDeployTimeoutSeconds: 120,
			},
		});
		const draft = toDraft(saved);
		expect(draft.postDeployCommand).toBe(`rake cache:warm "a b"`);
		expect(draft.postDeployTimeoutSeconds).toBe("120");
	});

	test("the timeout must be 1 to 3600 seconds", () => {
		const bad = {
			...baseline,
			postDeployCommand: "rake x",
			postDeployTimeoutSeconds: "4000",
		};
		expect(validateDraft(bad, "image").postDeployTimeoutSeconds).toBeDefined();
		expect(
			validateDraft({ ...bad, postDeployTimeoutSeconds: "60" }, "image")
				.postDeployTimeoutSeconds,
		).toBeUndefined();
	});
});

describe("docker options in the draft", () => {
	const plain = makeService({ id: "d1", name: "web" });
	const base = toDraft(plain);
	const withDocker = makeService({
		id: "d2",
		name: "web",
		config: {
			...plain.config,
			docker: {
				capAdd: ["CHOWN"],
				ulimits: [{ name: "nofile", soft: 1024, hard: 4096 }],
				labels: { team: "payments" },
				init: true,
			},
		},
	});

	test("a service without docker options sends no docker key", () => {
		expect(base.docker.capAdd).toEqual([]);
		expect("docker" in buildConfig(base, "image")).toBe(false);
		expect(buildPatch(base, { ...base }, "image")).toBeNull();
	});

	test("saved options round-trip without looking edited", () => {
		const draft = toDraft(withDocker);
		expect(buildConfig(draft, "image").docker).toEqual(
			withDocker.config.docker,
		);
		expect(buildPatch(draft, { ...draft }, "image")).toBeNull();
	});

	test("an edit stages the whole config with the docker block", () => {
		const patch = buildPatch(
			base,
			{ ...base, docker: { ...base.docker, init: true, capAdd: ["chown"] } },
			"image",
		);
		expect(patch?.config?.docker).toEqual({ capAdd: ["CHOWN"], init: true });
		expect(patch?.config?.healthcheck?.type).toBe(
			plain.config.healthcheck.type,
		);
	});

	test("clearing every option drops the docker key", () => {
		const draft = toDraft(withDocker);
		const cleared = { ...draft, docker: toDraft(plain).docker };
		const patch = buildPatch(draft, cleared, "image");
		expect(patch?.config).toBeDefined();
		expect(patch?.config && "docker" in patch.config).toBe(false);
	});

	test("reordering rows is not an edit", () => {
		const draft = toDraft(withDocker);
		const shuffled = {
			...draft,
			docker: {
				...draft.docker,
				labels: [{ key: "a", value: "1" }, ...draft.docker.labels],
			},
		};
		const once = buildPatch(draft, shuffled, "image");
		expect(once?.config?.docker?.labels).toEqual({ a: "1", team: "payments" });
		const reversed = {
			...shuffled,
			docker: {
				...shuffled.docker,
				labels: [...shuffled.docker.labels].reverse(),
			},
		};
		expect(buildPatch(shuffled, reversed, "image")).toBeNull();
	});

	test("a bad option blocks saving and names the docker field", () => {
		const draft = {
			...base,
			docker: {
				...base.docker,
				ulimits: [{ name: "nofile" as const, soft: "9", hard: "1" }],
			},
		};
		expect(validateDraft(draft, "image").docker).toContain(
			"Soft must not exceed hard",
		);
		expect(validateDraft(base, "image").docker).toBeUndefined();
	});

	test("a tmpfs on a file mount is refused", () => {
		const draft = {
			...base,
			files: [{ path: "/etc/app.conf", mode: "", content: "x" }],
			docker: {
				...base.docker,
				tmpfs: [{ path: "/etc/app.conf", sizeMb: "", mode: "" }],
			},
		};
		expect(validateDraft(draft, "image").docker).toContain("file mount");
	});

	test("the policy decides which capabilities may be added or dropped", () => {
		const policy = {
			...FALLBACK_POLICY,
			safeCapabilities: ["CHOWN"],
			knownCapabilities: ["CHOWN", "NET_RAW"],
			reservedLabels: ["traefik."],
			stopSignals: ["SIGTERM"],
		};
		const add = (capAdd: string[]) => ({
			...base,
			docker: { ...base.docker, capAdd },
		});
		expect(validateDraft(add(["MAC_ADMIN"]), "image", policy).docker).toContain(
			"not an allowed capability",
		);
		// The ones that need an unrestricted server are well-formed: the server decides.
		expect(
			validateDraft(add(["NET_ADMIN"]), "image", policy).docker,
		).toBeUndefined();
		const drop = {
			...base,
			docker: { ...base.docker, capDrop: ["WHATEVER"] },
		};
		expect(validateDraft(drop, "image", policy).docker).toContain(
			"not a Linux capability",
		);
		// Until the policy loads, the control plane is the judge.
		expect(validateDraft(add(["MAC_ADMIN"]), "image").docker).toBeUndefined();
		expect(validateDraft(drop, "image").docker).toBeUndefined();
	});

	test("the new options reach the config and back without a phantom change", () => {
		const docker = {
			capDrop: ["ALL"],
			noNewPrivileges: true,
			readOnlyRootfs: true,
			pidsLimit: 512,
			hostname: "web",
			dns: ["1.1.1.1", "9.9.9.9"],
			sysctls: { "net.core.somaxconn": "1024" },
			hostSysctls: { "vm.max_map_count": 262144 },
			user: "1000:1000",
			groupAdd: ["109"],
			tmpfs: [{ path: "/tmp", sizeMb: 64 }],
			devices: [{ hostPath: "/dev/dri/renderD128", permissions: "rw" }],
			gpus: { count: -1, driver: "nvidia" as const },
			runtime: "runsc",
			privileged: true,
		};
		const saved = makeService({
			id: "d3",
			name: "api",
			config: { ...plain.config, docker },
		});
		const draft = toDraft(saved);
		expect(buildConfig(draft, "image").docker).toEqual(docker);
		expect(buildPatch(draft, { ...draft }, saved.sourceType)).toBeNull();
		const patch = buildPatch(
			base,
			{ ...base, docker: applyHardened(base.docker) },
			"image",
		);
		expect(patch?.config?.docker).toEqual({
			capDrop: ["ALL"],
			noNewPrivileges: true,
			readOnlyRootfs: true,
			pidsLimit: 512,
			tmpfs: [{ path: "/tmp", sizeMb: 64 }],
		});
	});
});

describe("processes in the settings draft", () => {
	const plain = makeService({ id: "p0", name: "app" });
	const check = {
		type: "none" as const,
		intervalSeconds: 2,
		timeoutSeconds: 3,
		startPeriodSeconds: 0,
		deadlineSeconds: 120,
		successThreshold: 2,
	};
	const worker: ProcessConfig = {
		name: "worker",
		startCommand: ["bin/jobs", "--queue", "default"],
		healthcheck: check,
		stopGraceSeconds: 90,
	};
	const saved = makeService({
		id: "p1",
		name: "app",
		config: {
			...plain.config,
			processName: "app",
			singleton: true,
			processes: [worker],
		},
	});
	const baseline = toDraft(saved);

	test("the draft carries the default process' name, its singleton flag and each process", () => {
		expect(baseline.processName).toBe("app");
		expect(baseline.singleton).toBe(true);
		expect(baseline.processes).toHaveLength(1);
		expect(baseline.processes[0]?.command).toBe("bin/jobs --queue default");
		expect(baseline.processes[0]?.originalName).toBe("worker");
		const bare = toDraft(plain);
		expect(bare.processName).toBe("");
		expect(bare.singleton).toBe(false);
		expect(bare.processes).toEqual([]);
	});

	test("a saved service round-trips through buildConfig without looking edited", () => {
		const config = buildConfig(baseline, "image");
		expect(config.processName).toBe("app");
		expect(config.singleton).toBe(true);
		// The default check of a portless process is left to the control plane.
		expect(config.processes).toEqual([
			{
				name: "worker",
				startCommand: ["bin/jobs", "--queue", "default"],
				stopGraceSeconds: 90,
			},
		]);
		expect(buildPatch(baseline, { ...baseline }, "image")).toBeNull();
		expect(isDirty(baseline, { ...baseline }, "image")).toBe(false);
	});

	test("a customized process health check is sent", () => {
		const [row] = baseline.processes;
		if (!row) throw new Error("expected a process row");
		const config = buildConfig(
			{
				...baseline,
				processes: [
					{
						...row,
						health: {
							...row.health,
							custom: true,
							type: "exec",
							command: 'CMD-SHELL "pgrep jobs"',
						},
					},
				],
			},
			"image",
		);
		expect(config.processes?.[0]?.healthcheck).toEqual({
			...check,
			type: "exec",
			command: ["CMD-SHELL", "pgrep jobs"],
		});
	});

	test("a service without processes sends none of the process keys", () => {
		const config = buildConfig(toDraft(plain), "image");
		expect(config.processName).toBeUndefined();
		expect(config.singleton).toBeUndefined();
		expect(config.processes).toBeUndefined();
	});

	test("a blank name and a false singleton are not sent", () => {
		const config = buildConfig(
			{ ...baseline, processName: "  ", singleton: false },
			"image",
		);
		expect(config.processName).toBeUndefined();
		expect(config.singleton).toBeUndefined();
	});

	test("renaming a process sends the new list and the rename", () => {
		const [row] = baseline.processes;
		if (!row) throw new Error("expected a process row");
		const patch = buildPatch(
			baseline,
			{ ...baseline, processes: [{ ...row, name: "jobs" }] },
			"image",
		);
		expect(patch?.renameProcesses).toEqual({ worker: "jobs" });
		expect(patch?.config?.processes?.map((p) => p.name)).toEqual(["jobs"]);
	});

	test("an ordinary edit of a process sends no rename", () => {
		const [row] = baseline.processes;
		if (!row) throw new Error("expected a process row");
		const patch = buildPatch(
			baseline,
			{ ...baseline, processes: [{ ...row, port: "9000" }] },
			"image",
		);
		expect(patch?.renameProcesses).toBeUndefined();
		expect(patch?.config?.processes?.[0]?.port).toBe(9000);
	});

	test("adding and removing a process are config changes", () => {
		const added = newProcessDraft("worker-2");
		added.command = "bin/mail";
		const withAdded = {
			...baseline,
			processes: [...baseline.processes, added],
		};
		expect(isDirty(baseline, withAdded, "image")).toBe(true);
		const patch = buildPatch(baseline, withAdded, "image");
		expect(patch?.config?.processes?.map((p) => p.name)).toEqual([
			"worker",
			"worker-2",
		]);
		expect(patch?.renameProcesses).toBeUndefined();
		// The config is a full replace: removing the last one leaves none.
		const removed = buildPatch(
			baseline,
			{ ...baseline, processes: [] },
			"image",
		);
		expect(removed?.config).toBeDefined();
		expect(removed?.config?.processes).toBeUndefined();
	});

	test("turning the default process' singleton or name on is a config change", () => {
		const bare = toDraft(plain);
		expect(
			buildPatch(bare, { ...bare, singleton: true }, "image")?.config
				?.singleton,
		).toBe(true);
		expect(
			buildPatch(bare, { ...bare, processName: "api" }, "image")?.config
				?.processName,
		).toBe("api");
	});

	test("a valid set of processes is valid", () => {
		expect(validateDraft(baseline, "image")).toEqual({});
	});

	test("validation blocks a duplicate name, naming the process", () => {
		const [row] = baseline.processes;
		if (!row) throw new Error("expected a process row");
		const twin = { ...row, key: "twin", originalName: null };
		const errors = validateDraft(
			{ ...baseline, processes: [row, twin] },
			"image",
		);
		expect(errors.processes).toContain("worker");
	});

	test("validation blocks a process without a start command", () => {
		const added = newProcessDraft("queue");
		const errors = validateDraft(
			{ ...baseline, processes: [...baseline.processes, added] },
			"image",
		);
		expect(errors.processes).toContain("start command");
	});

	test("validation blocks a port another process already uses", () => {
		const [row] = baseline.processes;
		if (!row) throw new Error("expected a process row");
		const errors = validateDraft(
			{
				...baseline,
				port: "3000",
				processes: [{ ...row, port: "3000" }],
			},
			"image",
		);
		expect(errors.processes).toContain("3000");
	});

	test("validation blocks the default process' name as a process name", () => {
		const [row] = baseline.processes;
		if (!row) throw new Error("expected a process row");
		const errors = validateDraft(
			{ ...baseline, processes: [{ ...row, name: "app" }] },
			"image",
		);
		expect(errors.processes).toContain("default process");
	});

	test("a blank default process name is fine, a malformed one is not", () => {
		expect(
			validateDraft({ ...baseline, processName: "" }, "image").processName,
		).toBeUndefined();
		expect(
			validateDraft({ ...baseline, processName: "Bad_Name" }, "image")
				.processName,
		).toBeDefined();
	});

	test("the singleton flag alone never blocks saving", () => {
		const bare = toDraft(plain);
		expect(validateDraft({ ...bare, singleton: true }, "image")).toEqual({});
	});
});
