import { cn } from "cn";
import { ArrowRightIcon } from "lucide-react";
import type {
	ComposeChoices,
	ComposeNeededFile,
	ComposePlan,
} from "#/api/gen/types.gen";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { Textarea } from "#/components/ui/textarea";
import { PlanChecks } from "#/features/templates/plan-checks";
import {
	groupRewrites,
	summarizeChecks,
	summarizePlan,
	summaryText,
} from "./lib";
import { Disclosure, NoteAlerts } from "./parts";
import { ServiceCard } from "./service-card";

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

const PURPOSE: Record<ComposeNeededFile["purpose"], string> = {
	env_file: "env file",
	bind: "mounted file",
};

function NeededFiles({
	files,
	contents,
	onFile,
}: {
	files: readonly ComposeNeededFile[];
	contents: Readonly<Record<string, string>>;
	onFile(path: string, content: string): void;
}) {
	return (
		<Section title="Files to add" aside={`${files.length}`}>
			<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
				The Compose file points at files that were not provided. Paste each one;
				the plan updates as you type.
			</p>
			<ul className="m-0 flex list-none flex-col gap-3 p-0">
				{files.map((file) => (
					<li key={file.path} className="flex flex-col gap-1.5">
						<div className="flex flex-wrap items-center gap-x-2 gap-y-1">
							<span className="font-mono text-[12px] text-ink-1">
								{file.path}
							</span>
							<Badge>{PURPOSE[file.purpose]}</Badge>
							<span className="text-[12px] text-ink-3">for {file.service}</span>
							{file.required ? null : <Badge variant="outline">optional</Badge>}
						</div>
						<Textarea
							aria-label={`Contents of ${file.path}`}
							spellCheck={false}
							autoComplete="off"
							value={contents[file.path] ?? ""}
							onChange={(event) => onFile(file.path, event.target.value)}
							className="min-h-20 max-h-60 overflow-y-auto overscroll-contain font-mono text-xs leading-[1.5] whitespace-pre"
						/>
					</li>
				))}
			</ul>
		</Section>
	);
}

/**
 * Step two: the plan, one card per Compose service, and everything the
 * importer changed or could not carry over. The plan behind it may be one
 * request behind the choices; `refreshing` says so quietly.
 */
export function ReviewStep({
	plan,
	choices,
	onChoices,
	contents,
	onFile,
	onEditSource,
	refreshing,
	problem,
}: {
	plan: ComposePlan;
	choices: ComposeChoices;
	onChoices(update: (current: ComposeChoices) => ComposeChoices): void;
	/** Files by path: uploads and pasted text. */
	contents: Readonly<Record<string, string>>;
	onFile(path: string, content: string): void;
	onEditSource(): void;
	refreshing: boolean;
	/** The latest plan request failed; the cards still show the last good plan. */
	problem: string | null;
}) {
	const summary = summarizePlan(plan, choices);
	const services = plan.services ?? [];
	const blockers = plan.blockers ?? [];
	const missing = plan.missing ?? [];
	const needed = plan.neededFiles ?? [];
	const groups = groupRewrites(plan.rewrites);
	const names = new Map(services.map((service) => [service.key, service.name]));
	const networks = (plan.networks ?? []).filter((n) => n.note !== "");
	const projectWarnings = plan.warnings ?? [];
	const projectNotes = plan.notes ?? [];
	const checks = summarizeChecks(plan.checks);
	const hasProject =
		projectWarnings.length > 0 ||
		projectNotes.length > 0 ||
		networks.length > 0;

	return (
		<div className="flex flex-col gap-6" aria-busy={refreshing}>
			<div className="flex flex-wrap items-center gap-x-3 gap-y-1">
				<p
					className="m-0 font-medium text-[13px] text-ink-1"
					role="status"
					aria-live="polite"
				>
					{summaryText(summary)}
				</p>
				<span className="font-mono text-2xs text-ink-3">
					{plan.name}
					{plan.sourceKind === "repo" && plan.commit
						? ` · ${plan.commit.slice(0, 7)}`
						: ""}
				</span>
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
						<p>{problem} The cards show the last plan that worked.</p>
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
							Fix these in the Compose file or the choices below. The plan
							checks again after each change.
						</p>
						<Button variant="secondary" size="xs" onClick={onEditSource}>
							Edit source
						</Button>
					</AlertDescription>
				</Alert>
			) : null}

			{missing.length > 0 ? (
				<Alert variant="warning" role="note">
					<AlertTitle>
						{missing.length === 1
							? "A variable has no value"
							: `${missing.length} variables have no value`}
					</AlertTitle>
					<AlertDescription>
						<ul className="m-0 flex list-none flex-col gap-0.5 p-0">
							{missing.map((name) => (
								<li key={name}>
									<span className="font-mono text-ink-1">{`\${${name}}`}</span>{" "}
									has no value; an empty string was used.
								</li>
							))}
						</ul>
						<p>Add it to the .env box on the source step.</p>
						<Button variant="secondary" size="xs" onClick={onEditSource}>
							Edit .env
						</Button>
					</AlertDescription>
				</Alert>
			) : null}

			{needed.length > 0 ? (
				<NeededFiles files={needed} contents={contents} onFile={onFile} />
			) : null}

			<Section
				title="Services"
				aside={`${summary.services} of ${summary.total}`}
			>
				<ul className="m-0 flex list-none flex-col gap-3 p-0">
					{services.map((service) => (
						<ServiceCard
							key={service.key}
							service={service}
							choices={choices}
							onChoices={onChoices}
						/>
					))}
				</ul>
			</Section>

			{groups.length > 0 ? (
				<Section title="Rewritten references" aside={`${summary.rewrites}`}>
					<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
						These values pointed at other services by their Compose name. They
						now use the new services. Turn one off with Keep original host names
						in the service's variables.
					</p>
					<div className="flex flex-col gap-3">
						{groups.map((group) => (
							<div key={group.service} className="flex flex-col gap-1.5">
								<h4 className="m-0 font-medium text-[12px] text-ink-1">
									{names.get(group.service) ?? group.service}
								</h4>
								<ul className="m-0 flex list-none flex-col gap-2 p-0">
									{group.items.map((item) => (
										<li
											key={item.key}
											className="surface-well flex flex-col gap-1 rounded-ctl border border-line px-3 py-2"
										>
											<span className="font-mono text-[12px] text-ink-1">
												{group.service}.{item.key}
											</span>
											<span className="flex flex-wrap items-center gap-x-2 gap-y-1 font-mono text-[12px] text-ink-2">
												<span className="break-all">{item.from}</span>
												<ArrowRightIcon
													aria-label="becomes"
													className="size-3 shrink-0 text-ink-3"
												/>
												<span className="break-all">{item.to}</span>
											</span>
											<span className="text-[12px] text-ink-3 leading-[1.5]">
												{item.reason}
											</span>
										</li>
									))}
								</ul>
							</div>
						))}
					</div>
				</Section>
			) : null}

			{hasProject ? (
				<Section title="Project">
					{projectWarnings.map((warning) => (
						<Alert key={warning} variant="warning" role="note">
							<AlertDescription>{warning}</AlertDescription>
						</Alert>
					))}
					<NoteAlerts
						notes={projectNotes}
						ignoredTitle="Ignored project settings"
					/>
					{networks.length > 0 ? (
						<Disclosure title="Networks" count={networks.length}>
							<ul className="m-0 flex list-none flex-col gap-1.5 p-0">
								{networks.map((network) => (
									<li
										key={network.name}
										className="text-[12px] text-ink-3 leading-[1.5]"
									>
										<span className="font-mono text-ink-2">{network.name}</span>
										{network.external ? " (external)" : ""}
										{": "}
										{network.note}
									</li>
								))}
							</ul>
						</Disclosure>
					) : null}
				</Section>
			) : null}

			{checks.rows.length > 0 ? (
				<PlanChecks summary={checks} checking={refreshing} waiting={null} />
			) : null}
		</div>
	);
}
