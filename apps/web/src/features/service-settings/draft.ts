import type {
	ConfigInput,
	HealthcheckInput,
	UpdateServiceInputBody,
} from "#/api/gen/types.gen";
import type { HealthcheckType, Service } from "#/api/models";
import {
	type BuildLocation,
	buildOverrideConfig,
	toBuildFields,
} from "#/features/build-placement/model";
import {
	buildProcessesInput,
	type ProcessDraft,
	processBlocker,
	processNameError,
	renameProcesses,
	toProcessDrafts,
} from "#/features/processes/draft";
import {
	buildStackInput,
	toStackFields,
	validateStackFields,
} from "#/features/stacks/draft";
import type { StackResourcesDraft } from "#/features/stacks/model";
import { quoteArg, splitCommand } from "./command-line";
import {
	buildDockerOptions,
	type DockerForm,
	type DockerPolicy,
	dockerError,
	toDockerForm,
} from "./docker-options";
import { type FileDraft, validateFiles } from "./files";
import {
	buildTailnetConfig,
	type TailnetDraft,
	type TailnetErrorKey,
	toTailnetDraft,
	validateTailnet,
} from "./tailnet-draft";

export { quoteArg, splitCommand };

/**
 * The settings form's working copy of a service. Every number is kept as the
 * text the reader typed, so an empty or half-typed field is representable;
 * `validateDraft` says what is wrong and `buildPatch` turns a valid draft
 * into the PATCH body.
 */
export interface SettingsDraft {
	name: string;
	serverId: string;
	image: string;
	gitUrl: string;
	gitBranch: string;
	dockerfilePath: string;
	buildContext: string;
	/** auto | dockerfile | railpack | starter:<lang> */
	buildType: string;
	/** Git submodules: auto (when the repository has a .gitmodules file) | on | off */
	submodules: "auto" | "on" | "off";
	/** Where this service's image is built: inherit, the app's server or a build server. */
	buildLocation: BuildLocation;
	/** A pinned build server; "" lets the control plane pick one. */
	buildServerId: string;
	/** The registry for built images; "" inherits. */
	registryId: string;
	port: string;
	/** The default process' name; blank is "web". */
	processName: string;
	/** The default process never runs two instances at once. */
	singleton: boolean;
	/** The additional processes, in the order the form lists them. */
	processes: ProcessDraft[];
	startCommand: string;
	/** Runs in a container of the new image before the new release starts. */
	preDeployCommand: string;
	preDeployTimeoutSeconds: string;
	/** Runs in a one-shot container of the new release after the swap; failure only warns. */
	postDeployCommand: string;
	postDeployTimeoutSeconds: string;
	healthType: HealthcheckType;
	/** argv of an exec health check as text; CMD or CMD-SHELL comes first. */
	healthCommand: string;
	healthPath: string;
	healthPort: string;
	intervalSeconds: string;
	timeoutSeconds: string;
	startPeriodSeconds: string;
	deadlineSeconds: string;
	successThreshold: string;
	stopGraceSeconds: string;
	imageRetention: string;
	memoryMb: string;
	cpus: string;
	/** Write-only: empty keeps the stored token. */
	gitToken: string;
	clearGitToken: boolean;
	/** Write-only: both empty keeps the stored credentials. */
	registryUsername: string;
	registryPassword: string;
	clearRegistryAuth: boolean;
	/** Files written into the container before it starts; kept whole across saves. */
	files: FileDraft[];
	/** A run-to-completion job: no port, domains or health check. */
	oneshot: boolean;
	/** Reachable over the tailnet; null when the service is not on one. */
	tailnet: TailnetDraft | null;
	/** No outbound internet: the service only joins its environment's internal network. */
	blockEgress: boolean;
	/** The allow-listed Docker options (capabilities, ulimits, tmpfs, ...). */
	docker: DockerForm;
	/** The compose file of a stack, as typed. */
	stackCompose: string;
	/** A stack's `up --wait` timeout in seconds, as typed; empty is the default. */
	stackWaitSeconds: string;
	/** A stack's per-component limits, as typed. */
	stackResources: StackResourcesDraft;
}

const text = (value: number | undefined): string =>
	value === undefined || value === 0 ? "" : String(value);

export function toDraft(service: Service): SettingsDraft {
	const config = service.config;
	const health = config.healthcheck;
	return {
		name: service.name,
		serverId: service.serverId ?? "",
		image: config.image ?? "",
		gitUrl: config.gitUrl ?? "",
		gitBranch: config.gitBranch ?? "",
		dockerfilePath: config.dockerfilePath ?? "Dockerfile",
		buildContext: config.buildContext ?? ".",
		buildType: config.buildType ?? "auto",
		submodules:
			config.submodules === "on" || config.submodules === "off"
				? config.submodules
				: "auto",
		...toBuildFields(config),
		port: text(config.port),
		startCommand: (config.startCommand ?? []).map(quoteArg).join(" "),
		preDeployCommand: (config.preDeployCommand ?? []).map(quoteArg).join(" "),
		preDeployTimeoutSeconds: text(config.preDeployTimeoutSeconds),
		postDeployCommand: (config.postDeployCommand ?? []).map(quoteArg).join(" "),
		postDeployTimeoutSeconds: text(config.postDeployTimeoutSeconds),
		healthType: health.type,
		healthCommand: (health.command ?? []).map(quoteArg).join(" "),
		healthPath: health.path ?? "",
		healthPort: text(health.port),
		intervalSeconds: String(health.intervalSeconds),
		timeoutSeconds: String(health.timeoutSeconds),
		startPeriodSeconds: String(health.startPeriodSeconds),
		deadlineSeconds: String(health.deadlineSeconds),
		successThreshold: String(health.successThreshold),
		stopGraceSeconds: String(config.stopGraceSeconds),
		imageRetention: String(config.imageRetention),
		memoryMb: text(config.memoryMb),
		cpus: text(config.cpus),
		processName: config.processName ?? "",
		singleton: config.singleton === true,
		processes: toProcessDrafts(config.processes ?? []),
		gitToken: "",
		clearGitToken: false,
		registryUsername: "",
		registryPassword: "",
		clearRegistryAuth: false,
		files: (config.files ?? []).map((file) => ({
			path: file.path,
			mode: file.mode ?? "",
			content: file.content,
		})),
		oneshot: config.oneshot === true,
		tailnet: toTailnetDraft(config.tailnet),
		blockEgress: config.blockEgress === true,
		docker: toDockerForm(config.docker),
		...toStackFields(config.stack),
	};
}

type Errors = Partial<Record<keyof SettingsDraft | TailnetErrorKey, string>>;

function integerIn(
	value: string,
	min: number,
	max: number,
	label: string,
	optional = false,
): string | undefined {
	if (value.trim() === "")
		return optional ? undefined : `${label} is required.`;
	const parsed = Number(value);
	if (!Number.isInteger(parsed)) return `${label} must be a whole number.`;
	if (parsed < min || parsed > max)
		return `${label} must be between ${min} and ${max}.`;
	return undefined;
}

function relativePath(value: string): boolean {
	return !value.startsWith("/") && !value.split("/").includes("..");
}

/** What is wrong with the draft, by field. Empty means it can be saved. */
export function validateDraft(
	draft: SettingsDraft,
	sourceType: Service["sourceType"],
	dockerPolicy?: DockerPolicy,
): Errors {
	const errors: Errors = {};
	const set = (key: keyof SettingsDraft, message: string | undefined) => {
		if (message) errors[key] = message;
	};
	if (sourceType === "compose") {
		if (draft.name.trim() === "") errors.name = "Name is required.";
		set(
			"stopGraceSeconds",
			integerIn(draft.stopGraceSeconds, 1, 3600, "Stop grace period"),
		);
		Object.assign(errors, validateStackFields(draft));
		return errors;
	}
	if (draft.name.trim() === "") errors.name = "Name is required.";
	if (sourceType === "image" && draft.image.trim() === "")
		errors.image = "An image is required.";
	if (sourceType === "git") {
		if (draft.gitUrl.trim() === "")
			errors.gitUrl = "A repository URL is required.";
		else if (!/^(https?|git):\/\//.test(draft.gitUrl.trim())) {
			errors.gitUrl = "Use an https://, http:// or git:// URL.";
		}
		if (draft.gitBranch.trim() === "")
			errors.gitBranch = "A branch is required.";
		if (!relativePath(draft.dockerfilePath)) {
			errors.dockerfilePath =
				"Use a path inside the repository: no leading / and no ..";
		}
		if (!relativePath(draft.buildContext)) {
			errors.buildContext =
				"Use a path inside the repository: no leading / and no ..";
		}
	}
	set("port", integerIn(draft.port, 1, 65535, "Port", true));
	set(
		"healthPort",
		integerIn(draft.healthPort, 1, 65535, "Health check port", true),
	);
	set("intervalSeconds", integerIn(draft.intervalSeconds, 1, 3600, "Interval"));
	set("timeoutSeconds", integerIn(draft.timeoutSeconds, 1, 3600, "Timeout"));
	set(
		"startPeriodSeconds",
		integerIn(draft.startPeriodSeconds, 0, 3600, "Start period"),
	);
	set("deadlineSeconds", integerIn(draft.deadlineSeconds, 1, 3600, "Deadline"));
	set(
		"successThreshold",
		integerIn(draft.successThreshold, 1, 100, "Success threshold"),
	);
	set(
		"stopGraceSeconds",
		integerIn(draft.stopGraceSeconds, 1, 3600, "Stop grace period"),
	);
	set(
		"imageRetention",
		integerIn(draft.imageRetention, 1, 50, "Image retention"),
	);
	if (
		draft.preDeployCommand.trim() !== "" &&
		draft.preDeployTimeoutSeconds.trim() !== ""
	) {
		set(
			"preDeployTimeoutSeconds",
			integerIn(draft.preDeployTimeoutSeconds, 1, 3600, "Pre-deploy timeout"),
		);
	}
	if (
		draft.postDeployCommand.trim() !== "" &&
		draft.postDeployTimeoutSeconds.trim() !== ""
	) {
		set(
			"postDeployTimeoutSeconds",
			integerIn(draft.postDeployTimeoutSeconds, 1, 3600, "Post-deploy timeout"),
		);
	}
	set("memoryMb", integerIn(draft.memoryMb, 6, 1_048_576, "Memory", true));
	if (draft.cpus.trim() !== "") {
		const cpus = Number(draft.cpus);
		if (!Number.isFinite(cpus) || cpus <= 0 || cpus > 256) {
			errors.cpus = "CPUs must be a number between 0.01 and 256.";
		}
	}
	if (
		!draft.oneshot &&
		draft.healthType === "http" &&
		draft.port.trim() === ""
	) {
		errors.healthType =
			"An HTTP health check needs the service to have a port.";
	}
	if (draft.healthType === "exec") {
		const argv = splitCommand(draft.healthCommand);
		if (argv.length < 2 || (argv[0] !== "CMD" && argv[0] !== "CMD-SHELL")) {
			errors.healthCommand =
				'Start with CMD or CMD-SHELL, then the command: CMD-SHELL "pg_isready -U app".';
		}
	}
	if (draft.oneshot && draft.port.trim() !== "") {
		errors.port = "A one-shot job has no port.";
	}
	if (draft.processName.trim() !== "") {
		const problem = processNameError(draft.processName.trim());
		if (problem) errors.processName = problem;
	}
	if (draft.processes.length > 0) {
		const blocker = processBlocker(draft.processes, {
			defaultName: draft.processName.trim(),
			defaultPort: draft.port,
		});
		if (blocker) errors.processes = blocker;
	}
	const filesProblem = validateFiles(draft.files);
	if (filesProblem) errors.files = filesProblem;
	const dockerProblem = dockerError(draft.docker, {
		policy: dockerPolicy,
		filePaths: draft.files.map((file) => file.path),
	});
	if (dockerProblem) errors.docker = dockerProblem;
	Object.assign(
		errors,
		validateTailnet(draft.tailnet, {
			port: draft.port,
			oneshot: draft.oneshot,
		}),
	);
	return errors;
}

const int = (value: string): number => Number.parseInt(value, 10);

/** The full config the PATCH replaces: every field from the draft. */
export function buildConfig(
	draft: SettingsDraft,
	sourceType: Service["sourceType"],
): ConfigInput {
	if (sourceType === "compose") {
		// A stack's config is the compose project, the stop grace and the egress flag;
		// its health checks, port, image and options live in the compose file.
		return {
			stopGraceSeconds: int(draft.stopGraceSeconds),
			blockEgress: draft.blockEgress,
			stack: buildStackInput(draft),
		};
	}
	const health: HealthcheckInput = {
		type: draft.healthType,
		intervalSeconds: int(draft.intervalSeconds),
		timeoutSeconds: int(draft.timeoutSeconds),
		startPeriodSeconds: int(draft.startPeriodSeconds),
		deadlineSeconds: int(draft.deadlineSeconds),
		successThreshold: int(draft.successThreshold),
	};
	if (draft.healthPath.trim() !== "") health.path = draft.healthPath.trim();
	if (draft.healthPort.trim() !== "") health.port = int(draft.healthPort);
	if (draft.healthType === "exec") {
		health.command = splitCommand(draft.healthCommand);
	}
	const config: ConfigInput = {
		healthcheck: health,
		stopGraceSeconds: int(draft.stopGraceSeconds),
		imageRetention: int(draft.imageRetention),
	};
	if (sourceType === "image") config.image = draft.image.trim();
	else {
		config.gitUrl = draft.gitUrl.trim();
		if (draft.gitBranch.trim() !== "")
			config.gitBranch = draft.gitBranch.trim();
		config.dockerfilePath = draft.dockerfilePath.trim() || "Dockerfile";
		config.buildContext = draft.buildContext.trim() || ".";
		config.buildType = draft.buildType || "auto";
		// auto is the unset value; PATCH replaces the whole config.
		if (draft.submodules !== "auto") config.submodules = draft.submodules;
		Object.assign(config, buildOverrideConfig(draft));
	}
	if (draft.files.length > 0) {
		config.files = draft.files.map((file) => ({
			path: file.path,
			...(file.mode !== "" ? { mode: file.mode } : {}),
			content: file.content,
		}));
	}
	if (draft.oneshot) config.oneshot = true;
	const tailnet = buildTailnetConfig(draft.tailnet);
	if (tailnet) config.tailnet = tailnet;
	config.blockEgress = draft.blockEgress;
	const docker = buildDockerOptions(draft.docker);
	if (docker) config.docker = docker;
	if (draft.port.trim() !== "") config.port = int(draft.port);
	const command = splitCommand(draft.startCommand);
	if (command.length > 0) config.startCommand = command;
	const preDeploy = splitCommand(draft.preDeployCommand);
	if (preDeploy.length > 0) {
		config.preDeployCommand = preDeploy;
		if (draft.preDeployTimeoutSeconds.trim() !== "")
			config.preDeployTimeoutSeconds = int(draft.preDeployTimeoutSeconds);
	}
	const postDeploy = splitCommand(draft.postDeployCommand);
	if (postDeploy.length > 0) {
		config.postDeployCommand = postDeploy;
		if (draft.postDeployTimeoutSeconds.trim() !== "")
			config.postDeployTimeoutSeconds = int(draft.postDeployTimeoutSeconds);
	}
	if (draft.memoryMb.trim() !== "") config.memoryMb = int(draft.memoryMb);
	if (draft.cpus.trim() !== "") config.cpus = Number(draft.cpus);
	if (draft.processName.trim() !== "")
		config.processName = draft.processName.trim();
	if (draft.singleton) config.singleton = true;
	const processes = buildProcessesInput(draft.processes);
	if (processes) config.processes = processes;
	return config;
}

/**
 * The PATCH body for what changed between `baseline` and `draft`, or null
 * when nothing did. The config is a full replace, so it is sent whole
 * whenever any config field differs.
 */
export function buildPatch(
	baseline: SettingsDraft,
	draft: SettingsDraft,
	sourceType: Service["sourceType"],
): UpdateServiceInputBody | null {
	const body: UpdateServiceInputBody = {};
	if (draft.name.trim() !== baseline.name) body.name = draft.name.trim();
	if (draft.serverId !== baseline.serverId && draft.serverId !== "")
		body.serverId = draft.serverId;
	const before = JSON.stringify(buildConfig(baseline, sourceType));
	const after = JSON.stringify(buildConfig(draft, sourceType));
	if (before !== after) body.config = buildConfig(draft, sourceType);
	const renames = renameProcesses(draft.processes);
	if (renames) body.renameProcesses = renames;
	if (draft.clearGitToken) body.gitToken = "";
	else if (draft.gitToken !== "") body.gitToken = draft.gitToken;
	if (draft.clearRegistryAuth)
		body.registryAuth = { username: "", password: "" };
	else if (draft.registryUsername !== "" || draft.registryPassword !== "") {
		body.registryAuth = {
			username: draft.registryUsername,
			password: draft.registryPassword,
		};
	}
	return Object.keys(body).length === 0 ? null : body;
}

export function isDirty(
	baseline: SettingsDraft,
	draft: SettingsDraft,
	sourceType: Service["sourceType"],
): boolean {
	return buildPatch(baseline, draft, sourceType) !== null;
}
