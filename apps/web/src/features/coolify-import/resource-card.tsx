import { cn } from "cn";
import { DatabaseIcon, GitBranchIcon, HardDriveIcon } from "lucide-react";
import { useId, useState } from "react";
import type {
	CoolifyPlanResource,
	CoolifyPlanRewrite,
} from "#/api/gen/types.gen";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { Badge } from "#/components/ui/badge";
import { Checkbox } from "#/components/ui/checkbox";
import { DisclosureChevron, DisclosurePanel } from "#/components/ui/disclosure";
import { Input } from "#/components/ui/input";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { Switch } from "#/components/ui/switch";
import { ServiceIcon } from "#/features/canvas/service-icon";
import { NoteAlerts } from "#/features/compose-import/parts";
import { EnvList } from "./env-list";
import {
	type Baseline,
	coolifyHostAt,
	type ImportDraft,
	parsePortField,
	patchDomain,
	patchResource,
	type ResourceDraft,
	sortNotes,
	sourceLine,
} from "./lib";

const KIND_LABEL = {
	app: "App",
	worker: "Worker",
	database: "Database",
} as const;

function iconSource(resource: CoolifyPlanResource) {
	const { source } = resource;
	if (resource.kind === "database") {
		return {
			kind: "database",
			config: { database: { engine: source.managed?.engine } },
		};
	}
	if (source.type === "git") {
		return {
			kind: resource.kind,
			sourceType: "git",
			config: { gitUrl: source.git?.url },
		};
	}
	return {
		kind: resource.kind,
		sourceType: "image",
		config: { image: source.image },
	};
}

/**
 * One Coolify application or database as it would be imported: include box,
 * what it becomes and runs from, then (opened) its name, port, domains,
 * variables, rewrites and everything that could not be carried over.
 */
export function ResourceCard({
	resource,
	draft,
	rewrites,
	baseline,
	onDraft,
}: {
	resource: CoolifyPlanResource;
	draft: ResourceDraft | undefined;
	rewrites: ReadonlyMap<string, CoolifyPlanRewrite>;
	baseline: Baseline | undefined;
	onDraft(update: (current: ImportDraft) => ImportDraft): void;
}) {
	const id = useId();
	const [open, setOpen] = useState(false);
	const included = draft?.include ?? resource.include;
	const patch = (change: Parameters<typeof patchResource>[2]) =>
		onDraft((current) => patchResource(current, resource.uuid, change));
	const isDatabase = resource.kind === "database";
	const kind = draft?.kind ?? (isDatabase ? "database" : resource.kind);
	const domains = resource.domains ?? [];
	const enabledDomains = domains.filter((domain) => domain.enabled);
	const notes = sortNotes(resource.notes);
	const unsupported = resource.unsupported ?? [];
	const volumes = resource.volumes ?? [];
	const needs = resource.needsUnrestricted ?? [];
	const git = resource.source.type === "git";
	const port = draft?.port ?? resource.port;

	return (
		<li
			data-slot="coolify-resource-card"
			data-included={included}
			className="surface-well flex flex-col gap-2.5 rounded-ctl border border-line px-3 py-2.5"
		>
			<div className="flex items-start gap-3">
				<Checkbox
					aria-label={`Include ${resource.name}`}
					checked={included}
					onCheckedChange={(next) => patch({ include: next === true })}
					className="mt-2"
				/>
				<ServiceIcon service={iconSource(resource)} size={30} />
				<div className="flex min-w-0 flex-1 flex-col gap-1">
					<div className="flex flex-wrap items-center gap-x-2 gap-y-1">
						<span className="font-medium text-[13px] text-ink-1">
							{resource.name}
						</span>
						<span className="text-ink-4" aria-hidden>
							→
						</span>
						<span className="font-mono text-[12px] text-ink-2">
							{resource.slug}
						</span>
						<Badge>{KIND_LABEL[kind as keyof typeof KIND_LABEL] ?? kind}</Badge>
						{!included ? <Badge variant="outline">left out</Badge> : null}
					</div>
					<div
						className={cn(
							"flex flex-wrap items-center gap-x-3 gap-y-1 font-mono text-2xs text-ink-3",
							!included && "opacity-70",
						)}
					>
						<span className="flex min-w-0 items-center gap-1 break-all">
							{isDatabase ? (
								<DatabaseIcon aria-hidden className="size-3 shrink-0" />
							) : git ? (
								<GitBranchIcon aria-hidden className="size-3 shrink-0" />
							) : null}
							{sourceLine(resource.source)}
						</span>
						{port !== undefined && port > 0 ? (
							<span className="tabular-nums">port {port}</span>
						) : null}
						{enabledDomains.length > 0 ? (
							<span className="break-all">
								{enabledDomains.map((domain) => domain.host).join(", ")}
							</span>
						) : null}
						{volumes.length > 0 ? (
							<span className="flex items-center gap-1">
								<HardDriveIcon aria-hidden className="size-3" />
								{volumes.length} {volumes.length === 1 ? "volume" : "volumes"}
							</span>
						) : null}
					</div>
				</div>
				<button
					type="button"
					aria-expanded={open}
					aria-controls={`${id}-body`}
					onClick={() => setOpen(!open)}
					className="type-label flex shrink-0 cursor-pointer items-center gap-1 rounded-sm px-1 py-1 outline-none hover:text-ink-1 focus-visible:focus-ring"
				>
					Details
					<DisclosureChevron open={open} className="size-3.5" />
				</button>
			</div>

			{!included && resource.includeNote ? (
				<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
					{resource.includeNote}
				</p>
			) : null}

			{isDatabase && resource.source.managed ? (
				<div className="flex flex-col gap-1.5">
					{(resource.source.managed.notes ?? []).map((note) => (
						<p key={note} className="m-0 text-[12px] text-ink-3 leading-[1.5]">
							{note}
						</p>
					))}
					{resource.followup ? (
						<p className="m-0 text-[12px] text-ink-2 leading-[1.5]">
							<span className="font-medium text-ink-1">After the import: </span>
							{resource.followup}
						</p>
					) : null}
				</div>
			) : null}

			<DisclosurePanel open={open} id={`${id}-body`}>
				<div className="flex flex-col gap-4 pt-1">
					{needs.length > 0 ? (
						<Alert variant="warning" role="note">
							<AlertDescription>
								<p>
									Needs an unrestricted server:{" "}
									<span className="font-mono text-ink-1">
										{needs.join(", ")}
									</span>
									. A restricted server refuses these options.
								</p>
							</AlertDescription>
						</Alert>
					) : null}

					<div className="flex flex-wrap items-end gap-3">
						<div className="flex min-w-48 flex-1 flex-col gap-1">
							<label htmlFor={`${id}-name`} className="type-label">
								Name
							</label>
							<Input
								id={`${id}-name`}
								autoComplete="off"
								spellCheck={false}
								value={draft?.name ?? resource.name}
								onChange={(event) => patch({ name: event.target.value })}
								className="h-7"
							/>
						</div>
						{isDatabase ? (
							<div className="flex w-40 flex-col gap-1">
								<label htmlFor={`${id}-version`} className="type-label">
									Version
								</label>
								<Input
									id={`${id}-version`}
									autoComplete="off"
									spellCheck={false}
									placeholder={resource.source.managed?.version ?? ""}
									value={draft?.engineVersion ?? ""}
									onChange={(event) =>
										patch({ engineVersion: event.target.value })
									}
									className="h-7 font-mono"
								/>
							</div>
						) : (
							<>
								<div className="flex w-36 flex-col gap-1">
									<span className="type-label" id={`${id}-kind`}>
										Runs as
									</span>
									<Select
										value={
											draft?.kind ??
											(resource.kind === "worker" ? "worker" : "app")
										}
										items={[
											{ value: "app", label: "App" },
											{ value: "worker", label: "Worker (no port)" },
										]}
										onValueChange={(next) =>
											patch({ kind: next === "worker" ? "worker" : "app" })
										}
									>
										<SelectTrigger
											aria-labelledby={`${id}-kind`}
											size="sm"
											className="w-full"
										>
											<SelectValue />
										</SelectTrigger>
										<SelectContent alignItemWithTrigger={false}>
											<SelectItem value="app">App</SelectItem>
											<SelectItem value="worker">Worker (no port)</SelectItem>
										</SelectContent>
									</Select>
								</div>
								<div className="flex w-24 flex-col gap-1">
									<label htmlFor={`${id}-port`} className="type-label">
										Port
									</label>
									<Input
										id={`${id}-port`}
										inputMode="numeric"
										autoComplete="off"
										placeholder={resource.port ? String(resource.port) : "none"}
										defaultValue={
											draft?.port !== undefined ? String(draft.port) : ""
										}
										onChange={(event) => {
											const parsed = parsePortField(event.target.value);
											if (parsed === undefined || !Number.isNaN(parsed)) {
												patch({ port: parsed });
											}
										}}
										className="h-7 font-mono tabular-nums"
									/>
								</div>
							</>
						)}
					</div>

					{domains.length > 0 ? (
						<div className="flex flex-col gap-2">
							<h4 className="type-label m-0">Domains</h4>
							<ul className="m-0 flex list-none flex-col gap-2 p-0">
								{domains.map((domain, index) => {
									const key = coolifyHostAt(
										baseline,
										resource.uuid,
										index,
										domains.length,
									);
									const choice = key ? draft?.domains?.[key] : undefined;
									const enabled = choice?.enabled ?? domain.enabled;
									return (
										<li
											key={`${key ?? domain.host}${domain.path ?? ""}`}
											className="flex flex-col gap-1.5 border-line-faint border-t pt-2 first:border-t-0 first:pt-0"
										>
											<div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
												<Switch
													size="sm"
													aria-label={`Enable ${domain.host}`}
													checked={enabled}
													disabled={key === undefined}
													onCheckedChange={(next) =>
														key
															? onDraft((current) =>
																	patchDomain(current, resource.uuid, key, {
																		enabled: next,
																	}),
																)
															: undefined
													}
												/>
												<span className="break-all font-mono text-[12px] text-ink-1">
													{domain.host}
													{domain.path ?? ""}
												</span>
												<span className="font-mono text-2xs text-ink-3">
													{enabled ? "enabled" : "left out"}
												</span>
												{domain.generate ? <Badge>generated host</Badge> : null}
												{domain.conflict ? (
													<Badge variant="warning">host in use</Badge>
												) : null}
											</div>
											{key !== undefined ? (
												<div className="flex flex-col gap-1">
													<label
														htmlFor={`${id}-host-${index}`}
														className="sr-only"
													>
														Host to use instead of {key}
													</label>
													<Input
														id={`${id}-host-${index}`}
														autoComplete="off"
														autoCapitalize="off"
														spellCheck={false}
														placeholder="Use another host name"
														value={choice?.host ?? ""}
														onChange={(event) =>
															onDraft((current) =>
																patchDomain(current, resource.uuid, key, {
																	host: event.target.value,
																}),
															)
														}
														className="h-7 max-w-sm font-mono text-[12px]"
													/>
												</div>
											) : null}
											{domain.note ? (
												<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
													{domain.note}
												</p>
											) : null}
										</li>
									);
								})}
							</ul>
						</div>
					) : null}

					<EnvList
						resourceUuid={resource.uuid}
						env={resource.env ?? []}
						drafts={draft?.env}
						rewrites={rewrites}
						onDraft={onDraft}
					/>

					{notes.length > 0 ? (
						<div className="flex flex-col gap-2">
							<NoteAlerts notes={notes} ignoredTitle="Other notes" />
						</div>
					) : null}

					{unsupported.length > 0 ? (
						<div className="flex flex-col gap-1.5">
							<h4 className="type-label m-0">Not imported</h4>
							<ul className="m-0 flex list-disc flex-col gap-1 pl-4 text-[12px] text-ink-3 leading-[1.5]">
								{unsupported.map((item) => (
									<li key={item}>{item}</li>
								))}
							</ul>
						</div>
					) : null}

					{volumes.length > 0 ? (
						<div className="flex flex-col gap-1.5">
							<h4 className="type-label m-0">Volumes</h4>
							<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
								Created empty: the data is not copied.
							</p>
							<ul className="m-0 flex list-none flex-col gap-0.5 p-0 font-mono text-[12px] text-ink-2">
								{volumes.map((volume) => (
									<li key={`${volume.name}:${volume.mountPath}`}>
										{volume.name} → {volume.mountPath}
									</li>
								))}
							</ul>
						</div>
					) : null}
				</div>
			</DisclosurePanel>
		</li>
	);
}
