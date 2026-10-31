import type { HealthcheckType, Service } from "#/api/models";
import { FactLine } from "#/components/settings/fact-line";
import { MoreOptions } from "#/components/settings/more-options";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { FieldError } from "#/components/ui/field";
import { Segmented } from "#/components/ui/segmented";
import { Switch } from "#/components/ui/switch";
import { withoutPlacementWarnings } from "#/features/build-placement/model";
import { MeshPlanBlock } from "#/features/mesh/plan-block";
import { defaultProcessName, hasProcesses } from "#/features/processes/model";
import {
	type DraftProps,
	Section,
	SectionBody,
	SettingRow,
	TextRow,
} from "./fields";
import { usePlan } from "./use-plan";

const HEALTH_TYPES: ReadonlyArray<{
	value: HealthcheckType;
	label: string;
	/** One line under the control. */
	blurb: string;
	/** The longer explanation, in an info tip. */
	info?: string;
}> = [
	{
		value: "http",
		label: "HTTP",
		blurb: "A status from 200 to 399 passes.",
		info: "The agent calls it from outside the container, so the image needs no curl.",
	},
	{
		value: "tcp",
		label: "TCP",
		blurb: "Passes when a connection to the port opens.",
	},
	{
		value: "exec",
		label: "Command",
		blurb: "Passes when a command inside the container exits with 0.",
		info: "Docker runs the command inside the container and reports its exit status. The agent waits for it to turn healthy.",
	},
	{
		value: "docker",
		label: "Docker",
		blurb: "Uses the image's own HEALTHCHECK status.",
	},
	{
		value: "none",
		label: "None",
		// A warning, so it stays on the page.
		blurb:
			"Counts as healthy after 10 seconds without a restart. A broken release can go live unnoticed.",
	},
];

/** The fields of one health check as the form types them: the service's own or an additional process'. */
export interface HealthValue {
	type: HealthcheckType;
	/** argv of an exec health check as text; CMD or CMD-SHELL comes first. */
	command: string;
	path: string;
	port: string;
	intervalSeconds: string;
	timeoutSeconds: string;
	startPeriodSeconds: string;
	deadlineSeconds: string;
	successThreshold: string;
}

export type HealthErrors = Partial<Record<keyof HealthValue, string>>;

/** The timing fields, in the order they are listed. */
const TIMINGS = [
	{ key: "intervalSeconds", id: "interval", label: "Interval (s)" },
	{ key: "timeoutSeconds", id: "timeout", label: "Timeout (s)" },
	{
		key: "startPeriodSeconds",
		id: "start",
		label: "Start period (s)",
		description: "Failures during start-up are ignored",
	},
	{
		key: "deadlineSeconds",
		id: "deadline",
		label: "Deadline (s)",
		description: "Give up and keep the old release",
	},
	{
		key: "successThreshold",
		id: "success",
		label: "Success threshold",
		description: "Consecutive passes needed",
	},
] as const;

/**
 * The rows of a health check: type, then the path, command or port the type
 * needs, then the timings. The service's Health check section and each
 * additional process' editor draw the same rows over their own values.
 */
export function HealthFields({
	value,
	errors,
	onChange,
	idPrefix,
	portHint,
	portPlaceholder,
	subject = "",
	marks = false,
	inlineTimings = false,
	error,
}: {
	value: HealthValue;
	errors: HealthErrors;
	onChange<K extends keyof HealthValue>(key: K, next: HealthValue[K]): void;
	/** Control ids are `<idPrefix>-<field>` (`set-health-path`). */
	idPrefix: string;
	/** What the port defaults to: "service port" or "process port". */
	portHint: string;
	portPlaceholder: string;
	/** Names whose check this is, for the type control's label: " of worker". */
	subject?: string;
	/** Marks the rows the project file owns (the service's own check only). */
	marks?: boolean;
	/** Shows the timings in place instead of behind "More options" (the caller already collapses them). */
	inlineTimings?: boolean;
	/** A problem with the check as a whole, shown under the type. */
	error?: string | undefined;
}) {
	const current = HEALTH_TYPES.find((type) => type.value === value.type);
	const probes = value.type === "http" || value.type === "tcp";
	const checked = value.type !== "none";
	const mark = (name: string) =>
		marks ? { field: `config.healthcheck.${name}` } : {};
	const moreInvalid = TIMINGS.some(({ key }) => Boolean(errors[key]));
	const timings = TIMINGS.map((timing) => (
		<TextRow
			key={timing.key}
			id={`${idPrefix}-${timing.id}`}
			{...mark(timing.key)}
			label={timing.label}
			{...("description" in timing ? { description: timing.description } : {})}
			type="number"
			narrow
			value={value[timing.key]}
			error={errors[timing.key]}
			onChange={(next) => onChange(timing.key, next)}
		/>
	));
	return (
		<>
			<SettingRow
				label="Type"
				description={current?.blurb}
				info={current?.info}
				{...mark("type")}
			>
				<Segmented<HealthcheckType>
					aria-label={`Health check type${subject}`}
					className="self-start"
					font="sans"
					density="regular"
					value={value.type}
					onValueChange={(next) => onChange("type", next)}
					options={HEALTH_TYPES.map((type) => ({
						value: type.value,
						label: type.label,
					}))}
				/>
				<FieldError>{errors.type ?? error}</FieldError>
			</SettingRow>
			{value.type === "http" ? (
				<TextRow
					id={`${idPrefix}-path`}
					{...mark("path")}
					label="Path"
					mono
					value={value.path}
					placeholder="/"
					onChange={(next) => onChange("path", next)}
				/>
			) : null}
			{value.type === "exec" ? (
				<TextRow
					id={`${idPrefix}-command`}
					{...mark("command")}
					label="Command"
					description="Start with CMD (arguments follow) or CMD-SHELL (one shell string)."
					info="Quote arguments that contain spaces."
					mono
					value={value.command}
					error={errors.command}
					placeholder='CMD-SHELL "pg_isready -U app"'
					onChange={(next) => onChange("command", next)}
				/>
			) : null}
			{probes ? (
				<TextRow
					id={`${idPrefix}-port`}
					{...mark("port")}
					label="Port"
					description={`Defaults to the ${portHint}`}
					type="number"
					narrow
					value={value.port}
					error={errors.port}
					placeholder={portPlaceholder}
					onChange={(next) => onChange("port", next)}
				/>
			) : null}
			{checked ? (
				inlineTimings ? (
					timings
				) : (
					<MoreOptions count={TIMINGS.length} invalid={moreInvalid}>
						{timings}
					</MoreOptions>
				)
			) : null}
		</>
	);
}

export function HealthSection({ draft, errors, set }: DraftProps) {
	const value: HealthValue = {
		type: draft.healthType,
		command: draft.healthCommand,
		path: draft.healthPath,
		port: draft.healthPort,
		intervalSeconds: draft.intervalSeconds,
		timeoutSeconds: draft.timeoutSeconds,
		startPeriodSeconds: draft.startPeriodSeconds,
		deadlineSeconds: draft.deadlineSeconds,
		successThreshold: draft.successThreshold,
	};
	const fieldErrors: HealthErrors = {
		...(errors.healthType ? { type: errors.healthType } : {}),
		...(errors.healthCommand ? { command: errors.healthCommand } : {}),
		...(errors.healthPort ? { port: errors.healthPort } : {}),
		...(errors.intervalSeconds
			? { intervalSeconds: errors.intervalSeconds }
			: {}),
		...(errors.timeoutSeconds ? { timeoutSeconds: errors.timeoutSeconds } : {}),
		...(errors.startPeriodSeconds
			? { startPeriodSeconds: errors.startPeriodSeconds }
			: {}),
		...(errors.deadlineSeconds
			? { deadlineSeconds: errors.deadlineSeconds }
			: {}),
		...(errors.successThreshold
			? { successThreshold: errors.successThreshold }
			: {}),
	};
	return (
		<Section
			title="Health check"
			description="A release goes live only once this passes."
		>
			<HealthFields
				value={value}
				errors={fieldErrors}
				idPrefix="set-health"
				portHint="service port"
				portPlaceholder={draft.port || "service port"}
				marks
				onChange={(key, next) => {
					switch (key) {
						case "type":
							set("healthType", next as HealthcheckType);
							break;
						case "command":
							set("healthCommand", next);
							break;
						case "path":
							set("healthPath", next);
							break;
						case "port":
							set("healthPort", next);
							break;
						case "intervalSeconds":
							set("intervalSeconds", next);
							break;
						case "timeoutSeconds":
							set("timeoutSeconds", next);
							break;
						case "startPeriodSeconds":
							set("startPeriodSeconds", next);
							break;
						case "deadlineSeconds":
							set("deadlineSeconds", next);
							break;
						case "successThreshold":
							set("successThreshold", next);
							break;
					}
				}}
			/>
		</Section>
	);
}

/**
 * What the next deployment will do, as sentences from the real plan: the
 * strategy and why, then every warning and blocker as an alert that stays on
 * the page. The `strategy` anchor is on the strategy line, in every state, so
 * the settings search always has somewhere to land.
 */
function ReleaseFacts({ service }: { service: Service }) {
	const plan = usePlan(service);
	if (plan.isPending) {
		return <FactLine id="strategy">Planning the next deploy…</FactLine>;
	}
	if (plan.isError) {
		return (
			<FactLine id="strategy">
				No plan yet:{" "}
				{plan.error instanceof Error
					? plan.error.message
					: "the plan could not be made"}
			</FactLine>
		);
	}
	const rolling = plan.data.strategy === "rolling";
	return (
		<>
			<FactLine id="strategy">
				Next deploy:{" "}
				<span className="font-medium text-ink-1">
					{rolling ? "Rolling" : "Recreate"}
				</span>
				.{" "}
				{rolling
					? "The new container starts before the old one stops."
					: "The old container stops first, so there is a short gap."}
			</FactLine>
			{plan.data.reasons.length > 0 ? (
				<FactLine>Why: {plan.data.reasons.join(" · ")}</FactLine>
			) : null}
			{withoutPlacementWarnings(plan.data.warnings, plan.data.placement).map(
				(warning) => (
					<Alert key={warning} variant="warning">
						<AlertDescription>{warning}</AlertDescription>
					</Alert>
				),
			)}
			<MeshPlanBlock mesh={plan.data.mesh} />
			{plan.data.blocking.map((block) => (
				<Alert key={block.code} variant="destructive">
					<AlertDescription>{block.message}</AlertDescription>
				</Alert>
			))}
		</>
	);
}

/** The commands a release runs: start, before it goes live, after it is live. */
export function CommandsSection({
	service,
	draft,
	errors,
	set,
}: DraftProps & { service: Service }) {
	const timeouts =
		(draft.preDeployCommand.trim() !== "" ? 1 : 0) +
		(draft.postDeployCommand.trim() !== "" ? 1 : 0);
	// With more than one process the start command and port are the default process' own.
	const several = hasProcesses(service) || draft.processes.length > 0;
	return (
		<Section
			title={
				several
					? `Default process (${defaultProcessName({ processName: draft.processName.trim() })})`
					: "Commands"
			}
			description={
				several
					? "What runs the default process, and around the release."
					: "What runs to start a release, and around it."
			}
		>
			<TextRow
				id="set-command"
				field="config.startCommand"
				label="Start command"
				description="Overrides the image's default. Quote arguments with spaces."
				mono
				value={draft.startCommand}
				placeholder="(image default)"
				onChange={(value) => set("startCommand", value)}
			/>
			<SettingRow
				label="Never run two instances at once"
				htmlFor="set-singleton"
				description="Stops the old container before starting the new one. Use it for schedulers."
				field="config.singleton"
			>
				<Switch
					id="set-singleton"
					checked={draft.singleton}
					onCheckedChange={(checked) => set("singleton", checked)}
				/>
			</SettingRow>
			<TextRow
				id="set-predeploy"
				field="config.preDeployCommand"
				label="Pre-deploy command"
				description="Runs before the new release starts, for migrations. If it fails, the deploy stops."
				info="Runs once per deploy in a container of the new image. The current release keeps serving when it fails. Not run for rollbacks."
				mono
				value={draft.preDeployCommand}
				placeholder="(none)  e.g. rake db:migrate"
				error={errors.preDeployCommand}
				onChange={(value) => set("preDeployCommand", value)}
			/>
			<TextRow
				id="set-postdeploy"
				field="config.postDeployCommand"
				label="Post-deploy command"
				description="Runs once the new release is live, for a cache warm-up or a reindex."
				info="Runs once per deploy in a one-shot container of the new image. A failure only warns: the deploy still succeeds, with a warning in its timeline and a notification. Not run for rollbacks."
				mono
				value={draft.postDeployCommand}
				placeholder="(none)  e.g. rake cache:warm"
				error={errors.postDeployCommand}
				onChange={(value) => set("postDeployCommand", value)}
			/>
			{timeouts > 0 ? (
				<MoreOptions
					count={timeouts}
					invalid={Boolean(
						errors.preDeployTimeoutSeconds || errors.postDeployTimeoutSeconds,
					)}
				>
					{draft.preDeployCommand.trim() !== "" ? (
						<TextRow
							id="set-predeploy-timeout"
							field="config.preDeployTimeoutSeconds"
							label="Pre-deploy timeout (s)"
							description="The deploy fails after this long (default 600)"
							type="number"
							narrow
							value={draft.preDeployTimeoutSeconds}
							placeholder="600"
							error={errors.preDeployTimeoutSeconds}
							onChange={(value) => set("preDeployTimeoutSeconds", value)}
						/>
					) : null}
					{draft.postDeployCommand.trim() !== "" ? (
						<TextRow
							id="set-postdeploy-timeout"
							field="config.postDeployTimeoutSeconds"
							label="Post-deploy timeout (s)"
							description="A warning is raised after this long (default 600)"
							type="number"
							narrow
							value={draft.postDeployTimeoutSeconds}
							placeholder="600"
							error={errors.postDeployTimeoutSeconds}
							onChange={(value) => set("postDeployTimeoutSeconds", value)}
						/>
					) : null}
				</MoreOptions>
			) : null}
		</Section>
	);
}

/** How the next deploy replaces the running release: the plan's strategy as a fact, and the rarely changed limits. */
export function ReleasesSection({
	service,
	draft,
	errors,
	set,
}: DraftProps & { service: Service }) {
	return (
		<Section
			title="Releases"
			description="How a new release replaces the running one."
		>
			<SectionBody className="flex flex-col gap-2">
				<ReleaseFacts service={service} />
			</SectionBody>
			<MoreOptions
				count={2}
				invalid={Boolean(errors.stopGraceSeconds || errors.imageRetention)}
			>
				<TextRow
					id="set-stop-grace"
					field="config.stopGraceSeconds"
					label="Stop grace period (s)"
					description="Time the old container gets to finish before it is killed"
					type="number"
					narrow
					value={draft.stopGraceSeconds}
					error={errors.stopGraceSeconds}
					onChange={(value) => set("stopGraceSeconds", value)}
				/>
				<TextRow
					id="set-retention"
					field="config.imageRetention"
					label="Images kept"
					description="Built images retained per service for rollback"
					type="number"
					narrow
					value={draft.imageRetention}
					error={errors.imageRetention}
					onChange={(value) => set("imageRetention", value)}
				/>
			</MoreOptions>
		</Section>
	);
}
