import { cn } from "cn";
import type {
	ComposeChoices,
	ComposePlan,
	ComposeStackComponent,
	ComposeStackPlan,
} from "#/api/gen/types.gen";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { Input } from "#/components/ui/input";
import { Switch } from "#/components/ui/switch";
import { ViolationList } from "#/features/stacks/check-panel";
import { PlanChecks } from "#/features/templates/plan-checks";
import {
	componentDomain,
	setDomain,
	setVariableChoice,
	stackSummaryText,
	stackVariableState,
	summarizeChecks,
} from "./lib";
import { Disclosure } from "./parts";

/** Under a restricted problem on a server that is not unrestricted. */
const ASK_ADMIN_HINT =
	"Ask an admin to enable Unrestricted for this server, or remove these settings from the Compose file.";

function Section({
	title,
	aside,
	children,
}: {
	title: string;
	aside?: string;
	children: React.ReactNode;
}) {
	return (
		<section aria-label={title} className="flex flex-col gap-2.5">
			<div className="flex items-baseline gap-2">
				<h3 className="type-label m-0">{title}</h3>
				{aside ? (
					<span className="font-mono text-2xs text-ink-3 tabular-nums">
						{aside}
					</span>
				) : null}
			</div>
			{children}
		</section>
	);
}

function ComponentRow({
	component,
	choices,
	onChoices,
}: {
	component: ComposeStackComponent;
	choices: ComposeChoices;
	onChoices(update: (current: ComposeChoices) => ComposeChoices): void;
}) {
	const domain = componentDomain(component, choices);
	const routable = component.port > 0;
	return (
		<li className="surface-well flex flex-col gap-2 rounded-ctl border border-line px-3 py-2.5">
			<div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
				<span className="font-medium font-mono text-[13px] text-ink-1">
					{component.name}
				</span>
				<span
					title={component.image}
					className="min-w-0 flex-1 basis-40 truncate font-mono text-[11.5px] text-ink-3"
				>
					{component.image || "no image"}
				</span>
				{(component.ports ?? []).length > 0 ? (
					<span className="font-mono text-[11.5px] text-ink-3">
						{(component.ports ?? []).map((port) => `:${port}`).join(" ")}
					</span>
				) : null}
				<Badge variant={component.action === "run" ? "default" : "warning"}>
					{component.action === "run" ? "runs" : "not started"}
				</Badge>
			</div>
			{component.action === "profile" ? (
				<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
					It has Compose profiles ({(component.profiles ?? []).join(", ")}), so
					a default deploy does not start it.
				</p>
			) : null}
			{routable ? (
				<div className="flex flex-col gap-1.5">
					<div className="flex items-center gap-3">
						<Switch
							size="sm"
							id={`domain-${component.name}`}
							aria-label={`Domain for ${component.name}`}
							checked={domain.enabled}
							onCheckedChange={(enabled) =>
								onChoices((current) =>
									setDomain(current, component.name, {
										enabled,
										host: domain.host,
									}),
								)
							}
						/>
						<label
							htmlFor={`domain-${component.name}`}
							className="cursor-pointer text-[12.5px] text-ink-1"
						>
							Public domain on port{" "}
							<span className="font-mono">{component.port}</span>
						</label>
					</div>
					{domain.enabled ? (
						<Input
							aria-label={`Hostname for ${component.name}`}
							autoComplete="off"
							spellCheck={false}
							className="h-8 font-mono text-[12px]"
							placeholder="Leave empty to generate one"
							value={domain.host}
							onChange={(event) =>
								onChoices((current) =>
									setDomain(current, component.name, {
										enabled: true,
										host: event.target.value,
									}),
								)
							}
						/>
					) : null}
				</div>
			) : (
				<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
					No HTTP port: other components reach it by name, and no domain is
					suggested.
				</p>
			)}
			{(component.notes ?? []).map((note) => (
				<p key={note} className="m-0 text-[12px] text-ink-3 leading-[1.5]">
					{note}
				</p>
			))}
		</li>
	);
}

const VARIABLE_COLUMNS =
	"grid grid-cols-[minmax(0,1fr)_auto_auto] items-center gap-x-3 sm:grid-cols-[minmax(0,1.1fr)_minmax(0,1.4fr)_64px_48px]";

function Variables({
	stack,
	choices,
	onChoices,
}: {
	stack: ComposeStackPlan;
	choices: ComposeChoices;
	onChoices(update: (current: ComposeChoices) => ComposeChoices): void;
}) {
	const variables = stack.variables ?? [];
	if (variables.length === 0) return null;
	return (
		<Disclosure title="Variables for interpolation" count={variables.length}>
			<p className="m-0 mb-2 text-[12px] text-ink-3 leading-[1.5]">
				These fill the <span className="font-mono">{"$" + "{VAR}"}</span>{" "}
				references in the Compose file. They are never written into it. Keys
				starting with DOCKER_ or COMPOSE_ are skipped, except COMPOSE_PROFILES.
			</p>
			<div className="surface-well flex max-h-80 flex-col overflow-y-auto overscroll-contain rounded-ctl border border-line">
				<div
					className={cn(
						VARIABLE_COLUMNS,
						"sticky top-0 z-10 border-line-faint border-b bg-surface-well px-3 py-1.5",
					)}
				>
					<span className="type-label">Variable</span>
					<span className="type-label hidden sm:block">Value</span>
					<span className="type-label">Secret</span>
					<span className="type-label">Skip</span>
				</div>
				{variables.map((variable) => {
					const choice = choices.variables?.[variable.key];
					const state = stackVariableState(variable, choice);
					return (
						<div
							key={variable.key}
							className={cn(
								"flex flex-col gap-1 border-line-faint border-b px-3 py-2 last:border-b-0",
								state.skipped && "opacity-70",
							)}
						>
							<div className={VARIABLE_COLUMNS}>
								<span className="truncate font-mono text-[12px] text-ink-1">
									{variable.key}
								</span>
								<span className="hidden min-w-0 truncate font-mono text-[12px] text-ink-2 sm:block">
									{state.secret ? (
										<>
											••••••<span className="sr-only"> (secret, hidden)</span>
										</>
									) : variable.value === "" ? (
										<span className="text-ink-4">empty</span>
									) : (
										variable.value
									)}
								</span>
								<span className="flex flex-col items-start gap-0.5">
									<Switch
										size="sm"
										aria-label={`Secret: ${variable.key}`}
										checked={state.secret}
										onCheckedChange={(next) =>
											onChoices((current) =>
												setVariableChoice(current, variable.key, {
													secret: next,
												}),
											)
										}
									/>
									{variable.secretGuess && choice?.secret === undefined ? (
										<span className="text-3xs text-ink-4">guessed</span>
									) : null}
								</span>
								<Switch
									size="sm"
									aria-label={`Skip: ${variable.key}`}
									checked={state.skipped}
									disabled={state.lockedByPlan}
									onCheckedChange={(next) =>
										onChoices((current) =>
											setVariableChoice(current, variable.key, { skip: next }),
										)
									}
								/>
							</div>
							{variable.note ? (
								<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
									{variable.note}
								</p>
							) : null}
						</div>
					);
				})}
			</div>
		</Disclosure>
	);
}

/**
 * Step two of "Run as stack": the components the Compose file declares with
 * the domains the importer suggests, the allow-list violations (a blocker
 * until they are gone, or an admin turns Unrestricted on for the
 * server), the `.env` variables with their secret and skip toggles, and the
 * notes. The result is one staged stack, not a group of services.
 */
export function StackReviewStep({
	plan,
	choices,
	onChoices,
	onEditSource,
	refreshing,
	problem,
}: {
	plan: ComposePlan;
	choices: ComposeChoices;
	onChoices(update: (current: ComposeChoices) => ComposeChoices): void;
	onEditSource(): void;
	refreshing: boolean;
	problem: string | null;
}) {
	const stack = plan.stack;
	if (!stack) return null;
	const blockers = plan.blockers ?? [];
	const components = stack.components ?? [];
	const notes = stack.notes ?? [];
	const checks = summarizeChecks(plan.checks);

	return (
		<div className="flex flex-col gap-6" aria-busy={refreshing}>
			<div className="flex flex-wrap items-center gap-x-3 gap-y-1">
				<p
					className="m-0 font-medium text-[13px] text-ink-1"
					role="status"
					aria-live="polite"
				>
					{stackSummaryText(stack)}
				</p>
				<span className="font-mono text-2xs text-ink-3">{stack.name}</span>
				<span
					className={cn(
						"ml-auto text-2xs text-ink-3 transition-opacity duration-quick ease-out",
						refreshing ? "opacity-100" : "opacity-0",
					)}
					aria-hidden={!refreshing}
				>
					Updating
				</span>
			</div>

			{problem ? (
				<Alert variant="destructive">
					<AlertTitle>The plan could not be updated</AlertTitle>
					<AlertDescription>
						<p>{problem} The review shows the last plan that worked.</p>
					</AlertDescription>
				</Alert>
			) : null}

			{blockers.length > 0 ? (
				<Alert variant="destructive">
					<AlertTitle>This import is blocked</AlertTitle>
					<AlertDescription>
						<ul className="m-0 flex list-disc flex-col gap-1 pl-4">
							{blockers.map((blocker) => (
								<li key={blocker}>{blocker}</li>
							))}
						</ul>
						<p>
							Fix these in the Compose file. The plan checks again after each
							change.
						</p>
						<Button variant="secondary" size="xs" onClick={onEditSource}>
							Edit source
						</Button>
					</AlertDescription>
				</Alert>
			) : null}

			{(stack.violations ?? []).length > 0 ? (
				<Section title="Problems">
					<ViolationList
						result={{
							violations: stack.violations ?? [],
							unrestricted: stack.unrestricted,
						}}
						hint={ASK_ADMIN_HINT}
					/>
				</Section>
			) : null}

			{stack.composeAvailable === false ? (
				<Alert variant="warning" role="note">
					<AlertDescription>
						The server does not have the Docker Compose plugin (2.24 or newer).
						The stack can be staged, but it cannot be deployed until the plugin
						is installed.
					</AlertDescription>
				</Alert>
			) : null}

			<Section title="Components" aside={`${components.length}`}>
				<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
					The Compose file runs as it is, as one service. Ports are not
					published on the host; a domain routes to one component's port.
				</p>
				<ul className="m-0 flex list-none flex-col gap-3 p-0">
					{components.map((component) => (
						<ComponentRow
							key={component.name}
							component={component}
							choices={choices}
							onChoices={onChoices}
						/>
					))}
				</ul>
			</Section>

			<Variables stack={stack} choices={choices} onChoices={onChoices} />

			{notes.length > 0 || (plan.warnings ?? []).length > 0 ? (
				<Section title="Notes">
					{(plan.warnings ?? []).map((warning) => (
						<Alert key={warning} variant="warning" role="note">
							<AlertDescription>{warning}</AlertDescription>
						</Alert>
					))}
					<ul className="m-0 flex list-none flex-col gap-1 p-0 text-[12px] text-ink-3 leading-[1.5]">
						{notes.map((note) => (
							<li key={note}>{note}</li>
						))}
					</ul>
				</Section>
			) : null}

			{checks.rows.length > 0 ? (
				<PlanChecks summary={checks} checking={refreshing} waiting={null} />
			) : null}
		</div>
	);
}
