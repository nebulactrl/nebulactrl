import { describe, expect, test } from "bun:test";
import { makeService } from "#/features/canvas/lib/fixtures";
import {
	buildConfig,
	buildPatch,
	isDirty,
	toDraft,
	validateDraft,
} from "./draft";
import {
	byteLength,
	MAX_FILE_BYTES,
	MAX_FILES,
	sizeText,
	validateFiles,
} from "./files";

const image = makeService({ id: "s1", name: "api" });
const baseline = toDraft(image);

describe("validateDraft: image and git sources", () => {
	test("an image service needs an image", () => {
		expect(validateDraft({ ...baseline, image: "  " }, "image").image).toBe(
			"An image is required.",
		);
		expect(validateDraft(baseline, "image").image).toBeUndefined();
	});

	test("a git service needs a repository URL, with an http(s) or git scheme", () => {
		const git = { ...baseline, gitBranch: "main" };
		expect(validateDraft({ ...git, gitUrl: "" }, "git").gitUrl).toBe(
			"A repository URL is required.",
		);
		expect(
			validateDraft({ ...git, gitUrl: "git@github.com:a/b.git" }, "git").gitUrl,
		).toBe("Use an https://, http:// or git:// URL.");
		for (const url of [
			"https://x/y.git",
			"http://x/y.git",
			"git://x/y.git",
			"  https://x/y.git ",
		]) {
			expect(
				validateDraft({ ...git, gitUrl: url }, "git").gitUrl,
			).toBeUndefined();
		}
	});

	test("a git service needs a branch", () => {
		const git = { ...baseline, gitUrl: "https://x/y.git" };
		expect(validateDraft({ ...git, gitBranch: " " }, "git").gitBranch).toBe(
			"A branch is required.",
		);
	});

	test("the build context cannot leave the repository either", () => {
		const git = { ...baseline, gitUrl: "https://x/y.git", gitBranch: "main" };
		for (const buildContext of ["../up", "/abs"]) {
			expect(validateDraft({ ...git, buildContext }, "git").buildContext).toBe(
				"Use a path inside the repository: no leading / and no ..",
			);
		}
		expect(
			validateDraft({ ...git, buildContext: "apps/web" }, "git").buildContext,
		).toBeUndefined();
	});

	test("an image service does not validate git fields", () => {
		const errors = validateDraft(
			{ ...baseline, gitUrl: "nope", gitBranch: "" },
			"image",
		);
		expect(errors.gitUrl).toBeUndefined();
		expect(errors.gitBranch).toBeUndefined();
	});
});

describe("validateDraft: commands and limits", () => {
	const withCommands = {
		...baseline,
		preDeployCommand: "node migrate.js",
		postDeployCommand: "node warm.js",
	};

	test("a pre- or post-deploy timeout must be whole seconds up to an hour", () => {
		const errors = validateDraft(
			{
				...withCommands,
				preDeployTimeoutSeconds: "0",
				postDeployTimeoutSeconds: "3601",
			},
			"image",
		);
		expect(errors.preDeployTimeoutSeconds).toBe(
			"Pre-deploy timeout must be between 1 and 3600.",
		);
		expect(errors.postDeployTimeoutSeconds).toBe(
			"Post-deploy timeout must be between 1 and 3600.",
		);
		const fractional = validateDraft(
			{ ...withCommands, preDeployTimeoutSeconds: "1.5" },
			"image",
		);
		expect(fractional.preDeployTimeoutSeconds).toBe(
			"Pre-deploy timeout must be a whole number.",
		);
	});

	test("a timeout without a command is not judged, and an empty one is the default", () => {
		expect(
			validateDraft({ ...baseline, preDeployTimeoutSeconds: "0" }, "image")
				.preDeployTimeoutSeconds,
		).toBeUndefined();
		expect(
			validateDraft(
				{
					...withCommands,
					preDeployTimeoutSeconds: "",
					postDeployTimeoutSeconds: "",
				},
				"image",
			).preDeployTimeoutSeconds,
		).toBeUndefined();
	});

	test("CPUs must be a positive number up to 256", () => {
		const message = "CPUs must be a number between 0.01 and 256.";
		for (const cpus of ["abc", "0", "-1", "257"]) {
			expect(validateDraft({ ...baseline, cpus }, "image").cpus).toBe(message);
		}
		for (const cpus of ["", "0.5", "256"]) {
			expect(
				validateDraft({ ...baseline, cpus }, "image").cpus,
			).toBeUndefined();
		}
	});

	test("memory has a floor of 6 MB", () => {
		expect(
			validateDraft({ ...baseline, memoryMb: "5" }, "image").memoryMb,
		).toBe("Memory must be between 6 and 1048576.");
		expect(
			validateDraft({ ...baseline, memoryMb: "" }, "image").memoryMb,
		).toBeUndefined();
	});

	test("an HTTP health check needs a port, a one-shot job none", () => {
		const http = { ...baseline, healthType: "http" as const, port: "" };
		expect(validateDraft(http, "image").healthType).toBe(
			"An HTTP health check needs the service to have a port.",
		);
		expect(
			validateDraft({ ...http, oneshot: true }, "image").healthType,
		).toBeUndefined();
		expect(
			validateDraft({ ...baseline, oneshot: true, port: "80" }, "image").port,
		).toBe("A one-shot job has no port.");
	});

	test("an exec health check starts with CMD or CMD-SHELL and a command", () => {
		const exec = { ...baseline, healthType: "exec" as const };
		for (const healthCommand of ["", "CMD", "pg_isready -U app", "CMD-SHELL"]) {
			expect(
				validateDraft({ ...exec, healthCommand }, "image").healthCommand,
			).toBeDefined();
		}
		expect(
			validateDraft(
				{ ...exec, healthCommand: 'CMD-SHELL "pg_isready -U app"' },
				"image",
			).healthCommand,
		).toBeUndefined();
	});

	test("an invalid process name is reported", () => {
		expect(
			validateDraft({ ...baseline, processName: "Bad Name!" }, "image")
				.processName,
		).toBeDefined();
	});
});

describe("validateDraft: a compose stack", () => {
	const stack = {
		...baseline,
		stackCompose: "services:\n  web:\n    image: nginx\n",
	};

	test("needs a name and a compose file, and ignores image and git fields", () => {
		const errors = validateDraft(
			{ ...stack, name: " ", stackCompose: "  ", image: "", gitUrl: "nope" },
			"compose",
		);
		expect(errors.name).toBe("Name is required.");
		expect(errors.stackCompose).toBe("A Compose file is required.");
		expect(errors.image).toBeUndefined();
		expect(errors.gitUrl).toBeUndefined();
		expect(errors.port).toBeUndefined();
	});

	test("judges the stop grace period and the wait timeout", () => {
		const errors = validateDraft(
			{ ...stack, stopGraceSeconds: "0", stackWaitSeconds: "abc" },
			"compose",
		);
		expect(errors.stopGraceSeconds).toBe(
			"Stop grace period must be between 1 and 3600.",
		);
		expect(errors.stackWaitSeconds).toBeDefined();
	});

	test("a valid stack draft has no errors", () => {
		expect(validateDraft(stack, "compose")).toEqual({});
	});
});

describe("buildConfig / buildPatch", () => {
	const stack = {
		...baseline,
		stackCompose: "services:\n  web:\n    image: nginx\n",
		stackWaitSeconds: "120",
		blockEgress: true,
	};

	test("a stack's config is the compose project, the stop grace and the egress flag only", () => {
		expect(buildConfig(stack, "compose")).toEqual({
			stopGraceSeconds: 30,
			blockEgress: true,
			stack: { compose: stack.stackCompose, waitSeconds: 120 },
		});
	});

	test("an empty wait timeout is left out of a stack's config", () => {
		expect(
			buildConfig({ ...stack, stackWaitSeconds: "" }, "compose").stack,
		).toEqual({
			compose: stack.stackCompose,
		});
	});

	test("pre- and post-deploy commands carry their timeouts only when typed", () => {
		const both = buildConfig(
			{
				...baseline,
				preDeployCommand: 'node "my migrate.js"',
				preDeployTimeoutSeconds: "90",
				postDeployCommand: "node warm.js",
				postDeployTimeoutSeconds: "",
			},
			"image",
		);
		expect(both.preDeployCommand).toEqual(["node", "my migrate.js"]);
		expect(both.preDeployTimeoutSeconds).toBe(90);
		expect(both.postDeployCommand).toEqual(["node", "warm.js"]);
		expect(both.postDeployTimeoutSeconds).toBeUndefined();
		// A timeout with no command to time out is not sent.
		const orphan = buildConfig(
			{ ...baseline, preDeployTimeoutSeconds: "90" },
			"image",
		);
		expect(orphan.preDeployCommand).toBeUndefined();
		expect(orphan.preDeployTimeoutSeconds).toBeUndefined();
	});

	test("a post-deploy timeout alone does not change the config", () => {
		expect(
			buildPatch(
				baseline,
				{ ...baseline, postDeployTimeoutSeconds: "50" },
				"image",
			),
		).toBeNull();
	});

	test("optional fields appear in the config only when set", () => {
		const config = buildConfig(
			{
				...baseline,
				cpus: "1.5",
				memoryMb: "512",
				processName: " web ",
				singleton: true,
				startCommand: "node server.js",
				healthPath: " /up ",
				healthPort: "8081",
				oneshot: true,
				port: "",
			},
			"image",
		);
		expect(config.cpus).toBe(1.5);
		expect(config.memoryMb).toBe(512);
		expect(config.processName).toBe("web");
		expect(config.singleton).toBe(true);
		expect(config.startCommand).toEqual(["node", "server.js"]);
		expect(config.healthcheck?.path).toBe("/up");
		expect(config.healthcheck?.port).toBe(8081);
		expect(config.oneshot).toBe(true);
		expect(config.port).toBeUndefined();
	});

	test("a git service sends trimmed fields and falls back to defaults", () => {
		const config = buildConfig(
			{
				...baseline,
				gitUrl: " https://x/y.git ",
				gitBranch: " main ",
				dockerfilePath: "  ",
				buildContext: "",
			},
			"git",
		);
		expect(config.gitUrl).toBe("https://x/y.git");
		expect(config.gitBranch).toBe("main");
		expect(config.dockerfilePath).toBe("Dockerfile");
		expect(config.buildContext).toBe(".");
		expect(config.image).toBeUndefined();
		expect(
			buildConfig(
				{ ...baseline, gitUrl: "https://x/y.git", gitBranch: " " },
				"git",
			).gitBranch,
		).toBeUndefined();
	});

	test("a changed server is sent, an emptied one is not", () => {
		expect(
			buildPatch(baseline, { ...baseline, serverId: "server-2" }, "image"),
		).toEqual({
			serverId: "server-2",
		});
		expect(
			buildPatch(baseline, { ...baseline, serverId: "" }, "image"),
		).toBeNull();
	});

	test("clearing registry credentials sends empty ones; typing sends both fields", () => {
		expect(
			buildPatch(baseline, { ...baseline, clearRegistryAuth: true }, "image"),
		).toEqual({
			registryAuth: { username: "", password: "" },
		});
		expect(
			buildPatch(baseline, { ...baseline, registryUsername: "bot" }, "image"),
		).toEqual({
			registryAuth: { username: "bot", password: "" },
		});
		expect(
			buildPatch(
				baseline,
				{ ...baseline, registryPassword: "s3cret" },
				"image",
			),
		).toEqual({
			registryAuth: { username: "", password: "s3cret" },
		});
		// Clearing wins over anything typed alongside it.
		expect(
			buildPatch(
				baseline,
				{ ...baseline, clearRegistryAuth: true, registryUsername: "bot" },
				"image",
			)?.registryAuth,
		).toEqual({ username: "", password: "" });
	});

	test("clearing the git token wins over a typed one", () => {
		expect(
			buildPatch(
				baseline,
				{ ...baseline, clearGitToken: true, gitToken: "x" },
				"git",
			)?.gitToken,
		).toBe("");
	});

	test("a renamed service sends its trimmed name; a padded unchanged one sends nothing", () => {
		expect(
			buildPatch(baseline, { ...baseline, name: "  api-2 " }, "image"),
		).toEqual({ name: "api-2" });
		expect(
			buildPatch(baseline, { ...baseline, name: "api  " }, "image"),
		).toBeNull();
	});
});

describe("isDirty", () => {
	test("is true exactly when a patch would be sent", () => {
		expect(isDirty(baseline, baseline, "image")).toBe(false);
		expect(isDirty(baseline, { ...baseline, name: "other" }, "image")).toBe(
			true,
		);
		expect(isDirty(baseline, { ...baseline, port: "81" }, "image")).toBe(true);
		expect(isDirty(baseline, { ...baseline, name: "api " }, "image")).toBe(
			false,
		);
	});
});

describe("toDraft", () => {
	test("falls back to defaults for what the service leaves unset", () => {
		const { port: _port, ...withoutPort } = image.config;
		const service = makeService({
			serverId: null,
			config: { ...withoutPort, submodules: "weird" as never },
		});
		const draft = toDraft(service);
		expect(draft.serverId).toBe("");
		expect(draft.port).toBe("");
		expect(draft.submodules).toBe("auto");
		expect(draft.dockerfilePath).toBe("Dockerfile");
		expect(draft.buildContext).toBe(".");
		expect(draft.buildType).toBe("auto");
		expect(draft.files).toEqual([]);
	});

	test("quotes command arguments with spaces so they split back the same", () => {
		const service = makeService({
			config: {
				...image.config,
				startCommand: ["node", "my app.js", "--flag"],
			},
		});
		const draft = toDraft(service);
		expect(draft.startCommand).toBe('node "my app.js" --flag');
		expect(buildConfig(draft, "image").startCommand).toEqual([
			"node",
			"my app.js",
			"--flag",
		]);
	});
});

describe("files helpers", () => {
	test("byteLength counts UTF-8 bytes, not characters", () => {
		expect(byteLength("abc")).toBe(3);
		expect(byteLength("é")).toBe(2);
		expect(byteLength("€")).toBe(3);
	});

	test("sizeText reads a size at a glance", () => {
		expect(sizeText(0)).toBe("0 B");
		expect(sizeText(1023)).toBe("1023 B");
		expect(sizeText(1024)).toBe("1.0 KiB");
		expect(sizeText(1536)).toBe("1.5 KiB");
		expect(sizeText(10 * 1024)).toBe("10 KiB");
		expect(sizeText(64 * 1024)).toBe("64 KiB");
	});

	test("the files of a service may hold 256 KiB in total, though each may be 64 KiB", () => {
		const big = "x".repeat(MAX_FILE_BYTES);
		const file = (n: number) => ({
			path: `/etc/f${n}`,
			mode: "",
			content: big,
		});
		expect(validateFiles([file(1), file(2), file(3), file(4)])).toBeUndefined();
		expect(
			validateFiles([
				file(1),
				file(2),
				file(3),
				file(4),
				{ path: "/etc/f5", mode: "", content: "y" },
			]),
		).toBe("Files hold at most 256 KiB in total.");
	});

	test("there is a cap on the number of files, and the first problem names its file", () => {
		const many = Array.from({ length: MAX_FILES + 1 }, (_, n) => ({
			path: `/etc/f${n}`,
			mode: "",
			content: "",
		}));
		expect(validateFiles(many)).toBe("At most 16 files.");
		expect(
			validateFiles([
				{ path: "/etc/ok", mode: "0644", content: "" },
				{ path: "relative", mode: "", content: "" },
			]),
		).toBe(
			"relative: Use a clean absolute path like /etc/app/config.toml (no .. and no trailing /).",
		);
	});
});
