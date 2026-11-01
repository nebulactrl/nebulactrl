import { cn } from "cn";
import { CircleCheckIcon, CircleXIcon, TriangleAlertIcon } from "lucide-react";
import type { ReactNode } from "react";
import type { CoolifyPlan, Project } from "#/api/gen/types.gen";
import type { Server } from "#/api/models";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { CheckLabel } from "#/components/ui/check-label";
import { Input } from "#/components/ui/input";
import { Disclosure } from "#/features/compose-import/parts";
import { PlanChecks } from "#/features/templates/plan-checks";
import { FollowupList } from "./followups";
import {
	type Baseline,
	groupRewrites,
	type ImportDraft,
	includedText,
	patchRegistry,
	summarizeChecks,
	summarizePlan,
	totalsText,
	unrestrictedNeeds,
} from "./lib";
import { ProjectCard } from "./project-card";

function Section({
	title,
	aside,
	children,
}: {
	title: string;
	aside?: string;
	children: ReactNode;
}) {
	return (
		<section aria-label={title} className="flex flex-col gap-3">
			<div className="flex items-baseline gap-2">
				<h3 className="type-section-title m-0">{title}</h3>
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

/** Registries the plan found images on: pull credentials, which Coolify never exposes. */
function Registries({
	plan,
	draft,
	onDraft,
}: {
	plan: CoolifyPlan;
	draft: ImportDraft;
	onDraft(update: (current: ImportDraft) => ImportDraft): void;
}) {
	const registries = plan.registries ?? [];
	if (registries.length === 0) return null;
	return (
		<Section title="Registry credentials" aside={`${registries.length}`}>
			<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
				Coolify never exposes the credentials of a private registry. Add them
				here so the server can pull these images, or add them later. Both a
				username and a password are needed; they are sent with this request
				only.
			</p>
			<ul className="m-0 flex list-none flex-col gap-2 p-0">
				{registries.map((registry) => {
					const mine = draft.registries[registry.host];
					const hasPassword = (mine?.password ?? "") !== "";
					return (
						<li
							key={registry.host}
							className="surface-well flex flex-col gap-2 rounded-ctl border border-line px-3 py-2.5"
						>
							<div className="flex flex-wrap items-center gap-x-2 gap-y-1">
								<span className="font-mono text-[12px] text-ink-1">
									{registry.host}
								</span>
								<span className="text-[12px] text-ink-3">
									used by {(registry.services ?? []).join(", ") || "no service"}
								</span>
								<Badge
									variant={
										registry.provided || hasPassword ? "success" : "warning"
									}
								>
									{registry.provided || hasPassword
										? "credentials provided"
										: "no credentials"}
								</Badge>
							</div>
							<div className="flex flex-wrap gap-3">
								<div className="flex w-52 max-w-full flex-col gap-1">
									<label
										htmlFor={`registry-user-${registry.host}`}
										className="type-label"
									>
										Username
									</label>
									<Input
										id={`registry-user-${registry.host}`}
										autoComplete="off"
										autoCapitalize="off"
										spellCheck={false}
										className="h-7 font-mono text-[12px]"
										value={mine?.username ?? registry.username ?? ""}
										onChange={(event) =>
											onDraft((current) =>
												patchRegistry(current, registry.host, {
													username: event.target.value,
												}),
											)
										}
									/>
								</div>
								<div className="flex w-52 max-w-full flex-col gap-1">
									<label
										htmlFor={`registry-pass-${registry.host}`}
										className="type-label"
									>
										Password or token
									</label>
									<Input
										id={`registry-pass-${registry.host}`}
										type="password"
										autoComplete="off"
										autoCapitalize="off"
										spellCheck={false}
										className="h-7 font-mono text-[12px]"
										value={mine?.password ?? ""}
										onChange={(event) =>
											onDraft((current) =>
												patchRegistry(current, registry.host, {
													password: event.target.value,
												}),
											)
										}
									/>
								</div>
							</div>
						</li>
					);
				})}
			</ul>
		</Section>
	);
}

/**
 * Step two: what the plan makes of each Coolify project, with every choice
 * on the page. The plan behind it may be one request behind the choices;
 * `refreshing` says so in words.
 */
export function ReviewStep({
	plan,
	draft,
	onDraft,
	baseline,
	existingProjects,
	server,
	deploy,
	onDeploy,
	refreshing,
	problem,
	applyProblem,
	onRetry,
	onEditSource,
}: {
	plan: CoolifyPlan;
	draft: ImportDraft;
	onDraft(update: (current: ImportDraft) => ImportDraft): void;
	baseline: Baseline | undefined;
	existingProjects: readonly Project[];
	server: Server | undefined;
	deploy: boolean;
	onDeploy(next: boolean): void;
	refreshing: boolean;
	/** The latest plan request failed; the cards still show the last good plan. */
	problem: string | null;
	/** The import itself failed; a retry sends the same request under the same key. */
	applyProblem: string | null;
	onRetry(): void;
	onEditSource(): void;
}) {
	const summary = summarizePlan(plan, draft);
	const blockers = plan.blockers ?? [];
	const warnings = plan.warnings ?? [];
	const checks = summarizeChecks(plan.checks);
	const groups = groupRewrites(plan.rewrites);
	const unsupported = plan.unsupported ?? [];
	const followups = plan.followups ?? [];
	const needsOpen = unrestrictedNeeds(plan, draft);

	return (
		<div className="flex flex-col gap-6" aria-busy={refreshing}>
			<div className="flex flex-col gap-2">
				<div className="flex flex-wrap items-center gap-x-3 gap-y-1">
					<p
						className="m-0 font-medium text-[13px] text-ink-1"
						role="status"
						aria-live="polite"
					>
						{totalsText(plan.totals)}
					</p>
					{plan.coolifyVersion ? (
						<span className="font-mono text-2xs text-ink-3">
							Coolify {plan.coolifyVersion}
						</span>
					) : null}
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
				<div className="flex flex-wrap items-center gap-1.5">
					<Badge variant={blockers.length > 0 ? "destructive" : "success"}>
						{blockers.length > 0 ? (
							<CircleXIcon aria-hidden />
						) : (
							<CircleCheckIcon aria-hidden />
						)}
						{blockers.length === 0
							? "no blockers"
							: `${blockers.length} ${blockers.length === 1 ? "blocker" : "blockers"}`}
					</Badge>
					{checks.fail > 0 ? (
						<Badge variant="destructive">
							<CircleXIcon aria-hidden />
							{checks.fail}{" "}
							{checks.fail === 1 ? "check failed" : "checks failed"}
						</Badge>
					) : null}
					{warnings.length + checks.warn > 0 ? (
						<Badge variant="warning">
							<TriangleAlertIcon aria-hidden />
							{warnings.length + checks.warn}{" "}
							{warnings.length + checks.warn === 1 ? "warning" : "warnings"}
						</Badge>
					) : null}
					{summary.needValue > 0 ? (
						<Badge variant="warning">
							<TriangleAlertIcon aria-hidden />
							{summary.needValue}{" "}
							{summary.needValue === 1 ? "variable needs" : "variables need"} a
							value
						</Badge>
					) : null}
					{checks.ok > 0 ? (
						<Badge variant="success">
							<CircleCheckIcon aria-hidden />
							{checks.ok} {checks.ok === 1 ? "check passed" : "checks passed"}
						</Badge>
					) : null}
				</div>
			</div>

			{problem ? (
				<Alert variant="destructive">
					<AlertTitle>The plan could not be updated</AlertTitle>
					<AlertDescription>
						<p>{problem} The cards show the last plan that worked.</p>
						<Button variant="secondary" size="xs" onClick={onRetry}>
							Try again
						</Button>
					</AlertDescription>
				</Alert>
			) : null}

			{applyProblem ? (
				<Alert variant="destructive">
					<AlertTitle>The import failed</AlertTitle>
					<AlertDescription>
						<p>
							{applyProblem} Importing again sends the same request: if the
							first one did go through, you get its result instead of a second
							import.
						</p>
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
							Fix these in the choices below or in Coolify. The plan checks
							again after each change.
						</p>
						<Button variant="secondary" size="xs" onClick={onEditSource}>
							Back to connect
						</Button>
					</AlertDescription>
				</Alert>
			) : null}

			{warnings.length > 0 ? (
				<Alert variant="warning" role="note">
					<AlertTitle>
						{warnings.length === 1 ? "A note" : `${warnings.length} notes`}{" "}
						about this import
					</AlertTitle>
					<AlertDescription>
						<ul className="m-0 flex list-disc flex-col gap-1 pl-4">
							{warnings.map((warning) => (
								<li key={warning}>{warning}</li>
							))}
						</ul>
					</AlertDescription>
				</Alert>
			) : null}

			{needsOpen.length > 0 && server && !server.unrestricted ? (
				<Alert variant="warning" role="note">
					<AlertTitle>These need an unrestricted server</AlertTitle>
					<AlertDescription>
						<ul className="m-0 flex list-disc flex-col gap-1 pl-4 font-mono text-[12px]">
							{needsOpen.map((line) => (
								<li key={line}>{line}</li>
							))}
						</ul>
						<p>
							{server.name} is restricted and refuses these options. Go back and
							pick an unrestricted server, or leave these services out.
						</p>
						<Button variant="secondary" size="xs" onClick={onEditSource}>
							Back to connect
						</Button>
					</AlertDescription>
				</Alert>
			) : null}

			<Section
				title="Projects"
				aside={`${summary.projects} of ${(plan.projects ?? []).length} · ${includedText(summary)}`}
			>
				<ul className="m-0 flex list-none flex-col gap-3 p-0">
					{(plan.projects ?? []).map((project) => (
						<ProjectCard
							key={project.uuid}
							project={project}
							plan={plan}
							draft={draft}
							baseline={baseline}
							existingProjects={existingProjects}
							onDraft={onDraft}
						/>
					))}
				</ul>
			</Section>

			<Registries plan={plan} draft={draft} onDraft={onDraft} />

			{groups.length > 0 ? (
				<Section title="Rewritten references" aside={`${summary.rewrites}`}>
					<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
						These variables pointed at another Coolify resource by its host
						name, port or credentials. They now use a reference to the imported
						one. Turn one off with Keep the Coolify host in the resource's
						variables.
					</p>
					<div className="flex flex-col gap-3">
						{groups.map((group) => (
							<div key={group.resource} className="flex flex-col gap-1.5">
								<h4 className="m-0 font-medium text-[12px] text-ink-1">
									{group.resource}
								</h4>
								<ul className="m-0 flex list-none flex-col gap-1 p-0">
									{group.items.map((item) => (
										<li
											key={item.key}
											className="surface-well flex flex-wrap items-center gap-x-2 gap-y-1 rounded-ctl border border-line px-3 py-1.5 font-mono text-[12px]"
										>
											<span className="text-ink-1">
												{group.resource}.{item.key}
											</span>
											<span className="text-ink-4" aria-hidden>
												→
											</span>
											<span className="sr-only">becomes</span>
											<span className="break-all text-ink-2">{item.to}</span>
											<span className="text-2xs text-ink-3">({item.kind})</span>
										</li>
									))}
								</ul>
							</div>
						))}
					</div>
				</Section>
			) : null}

			{unsupported.length > 0 ? (
				<Section title="Not imported" aside={`${unsupported.length}`}>
					<ul className="m-0 flex list-none flex-col gap-2 p-0">
						{unsupported.map((item) => (
							<li
								key={`${item.kind}:${item.project ?? ""}:${item.name}`}
								className="surface-well flex flex-col gap-1 rounded-ctl border border-line px-3 py-2"
							>
								<span className="flex flex-wrap items-center gap-x-2 gap-y-1">
									<span className="font-medium text-[13px] text-ink-1">
										{item.name}
									</span>
									<Badge>{item.kind}</Badge>
									{item.project ? (
										<span className="font-mono text-2xs text-ink-3">
											{item.project}
										</span>
									) : null}
								</span>
								<span className="text-[12px] text-ink-3 leading-[1.5]">
									{item.reason}
								</span>
							</li>
						))}
					</ul>
				</Section>
			) : null}

			{followups.length > 0 ? (
				<Disclosure title="After the import" count={followups.length}>
					<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
						Data is never copied. These are the steps that stay manual; they are
						listed again when the import is done.
					</p>
					<FollowupList followups={followups} />
				</Disclosure>
			) : null}

			{checks.rows.length > 0 ? (
				<PlanChecks summary={checks} checking={refreshing} waiting={null} />
			) : null}

			<div className="flex flex-col gap-1.5 border-line-faint border-t pt-4">
				<CheckLabel
					checked={deploy}
					onCheckedChange={onDeploy}
					className="items-start text-[13px] text-ink-1"
					checkboxClassName="mt-0.5"
				>
					Deploy right away
				</CheckLabel>
				<p className="m-0 pl-6 text-[12px] text-ink-3 leading-[1.5]">
					Otherwise everything is staged and nothing is deployed. Deploy from
					the staged bar when you are ready.
				</p>
			</div>
		</div>
	);
}
