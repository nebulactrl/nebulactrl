import { useQuery } from "@tanstack/react-query";
import { useId, useState } from "react";
import { listGitConnectionsOptions } from "#/api/gen/@tanstack/react-query.gen";
import type { ComposeChoices, ComposePlanService } from "#/api/gen/types.gen";
import { Badge } from "#/components/ui/badge";
import { CheckLabel } from "#/components/ui/check-label";
import { Field, FieldDescription, FieldLabel } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Segmented } from "#/components/ui/segmented";
import {
	RepoPicker,
	type RepoSelection,
	usableConnections,
} from "#/features/git/repo-picker";
import { databaseLabel } from "#/features/templates/lib";
import {
	canSwitchSource,
	convertedTargets,
	dockerOptionTexts,
	domainEnabled,
	groupBinds,
	healthSummary,
	isManagedOn,
	limitsText,
	needsGit,
	parsePortField,
	portKey,
	publishedKeys,
	routablePorts,
	setConvert,
	setDomain,
	setImage,
	setManaged,
	setPort,
	setPublish,
	setServiceGit,
	setSourceKind,
} from "./lib";
import { Group, ToggleRow } from "./parts";

interface SectionProps {
	service: ComposePlanService;
	choices: ComposeChoices;
	onChoices(update: (current: ComposeChoices) => ComposeChoices): void;
}

// --- where the service comes from ---------------------------------------------

function ManagedRow({ service, choices, onChoices }: SectionProps) {
	const managed = service.managed;
	if (!managed) return null;
	const on = isManagedOn(service, choices);
	const engine = [managed.engine, managed.version].filter(Boolean).join(" ");
	if (!managed.eligible) {
		return managed.reason ? (
			<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
				Not a managed database: {managed.reason}
			</p>
		) : null;
	}
	return (
		<ToggleRow
			label={`Use a managed ${databaseLabel(engine)} database`}
			description={managed.reason}
			checked={on}
			onCheckedChange={(next) =>
				onChoices((current) => setManaged(current, service.key, next))
			}
		>
			{on ? (
				<div className="flex flex-col gap-1 rounded-ctl bg-white/3 px-2.5 py-2 text-[12px] text-ink-3 leading-[1.5]">
					{managed.database || managed.username ? (
						<span className="font-mono text-2xs text-ink-2">
							{managed.database ? `database ${managed.database}` : ""}
							{managed.database && managed.username ? " · " : ""}
							{managed.username ? `user ${managed.username}` : ""}
						</span>
					) : null}
					<span>Existing data is not copied. The database starts empty.</span>
					{(managed.notes ?? []).map((note) => (
						<span key={note}>{note}</span>
					))}
				</div>
			) : (managed.notes ?? []).length > 0 ? (
				<ul className="m-0 flex list-none flex-col gap-0.5 p-0 text-[12px] text-ink-3 leading-[1.5]">
					{(managed.notes ?? []).map((note) => (
						<li key={note}>{note}</li>
					))}
				</ul>
			) : null}
		</ToggleRow>
	);
}

type GitMode = "account" | "url";

function GitSource({ service, choices, onChoices }: SectionProps) {
	const id = useId();
	const connections = useQuery(listGitConnectionsOptions());
	const items = connections.data?.items ?? [];
	const hasAccounts = usableConnections(items).length > 0;
	const choice = choices.services?.[service.key];
	const build = service.source.build;
	const gitUrl = choice?.gitUrl ?? build?.gitUrl ?? "";
	const connectionId = choice?.connectionId ?? build?.connectionId ?? "";
	const repo = choice?.repo ?? build?.repo ?? "";
	const branch = choice?.gitBranch ?? build?.gitBranch ?? "";
	const [picked, setPicked] = useState<GitMode | null>(null);
	const mode: GitMode =
		picked ??
		(connectionId !== ""
			? "account"
			: gitUrl !== "" || !hasAccounts
				? "url"
				: "account");

	const write = (git: Parameters<typeof setServiceGit>[2]) =>
		onChoices((current) => setServiceGit(current, service.key, git));

	const selection: RepoSelection | null =
		connectionId === "" ? null : { connectionId, repo, branch, cloneUrl: null };

	return (
		<div className="flex flex-col gap-3">
			{needsGit(service) ? (
				<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
					This service is built from source. Say where the code is, or use an
					image instead.
				</p>
			) : null}
			{hasAccounts ? (
				<Segmented<GitMode>
					aria-label="Where the repository comes from"
					font="sans"
					density="regular"
					value={mode}
					onValueChange={setPicked}
					options={[
						{ value: "account", label: "Connected account" },
						{ value: "url", label: "Any URL" },
					]}
				/>
			) : null}
			{mode === "account" ? (
				<RepoPicker
					idPrefix={`${id}-repo`}
					connections={items}
					value={selection}
					onChange={(next) =>
						write(
							next
								? {
										connectionId: next.connectionId,
										repo: next.repo,
										gitBranch: next.branch,
									}
								: {},
						)
					}
				/>
			) : (
				<div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_160px]">
					<Field>
						<FieldLabel htmlFor={`${id}-url`}>Repository URL</FieldLabel>
						<Input
							id={`${id}-url`}
							spellCheck={false}
							autoComplete="off"
							className="font-mono"
							placeholder="https://github.com/acme/shop.git"
							value={gitUrl}
							onChange={(event) =>
								write({ gitUrl: event.target.value, gitBranch: branch })
							}
						/>
					</Field>
					<Field>
						<FieldLabel htmlFor={`${id}-branch`}>Branch</FieldLabel>
						<Input
							id={`${id}-branch`}
							spellCheck={false}
							autoComplete="off"
							className="font-mono"
							placeholder="main"
							value={branch}
							onChange={(event) =>
								write({ gitUrl, gitBranch: event.target.value })
							}
						/>
					</Field>
				</div>
			)}
			{build ? (
				<p className="m-0 font-mono text-2xs text-ink-3 leading-[1.5]">
					build {build.context}
					{build.dockerfile ? ` · ${build.dockerfile}` : ""}
					{build.target ? ` · target ${build.target}` : ""}
				</p>
			) : null}
			{(build?.argNames ?? []).length > 0 ? (
				<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
					Build arguments ({(build?.argNames ?? []).join(", ")}) are not
					applied. Set them as variables on the service after the import.
				</p>
			) : null}
		</div>
	);
}

/** Managed database, git repository or image: what the service runs. */
export function SourceSection(props: SectionProps) {
	const { service, choices, onChoices } = props;
	const choice = choices.services?.[service.key];
	const switchable = canSwitchSource(service, choices);
	const useImage = choice?.source === "image";
	const managed = isManagedOn(service, choices);
	const imageId = useId();
	const showManaged =
		service.managed !== undefined &&
		(service.managed.eligible || Boolean(service.managed.reason));
	if (!switchable && !showManaged) return null;
	return (
		<Group title="Source">
			{showManaged ? <ManagedRow {...props} /> : null}
			{!managed && switchable ? (
				<>
					<ToggleRow
						label="Use the image instead"
						description="Skip the build and run a published image."
						checked={useImage}
						onCheckedChange={(next) =>
							onChoices((current) =>
								setSourceKind(current, service.key, next ? "image" : undefined),
							)
						}
					/>
					{useImage ? (
						<Field>
							<FieldLabel htmlFor={imageId}>Image</FieldLabel>
							<Input
								id={imageId}
								spellCheck={false}
								autoComplete="off"
								className="font-mono"
								placeholder="ghcr.io/acme/app:1.4"
								value={choice?.image ?? service.source.image ?? ""}
								onChange={(event) =>
									onChoices((current) =>
										setImage(current, service.key, event.target.value),
									)
								}
							/>
						</Field>
					) : (
						<GitSource {...props} />
					)}
				</>
			) : null}
		</Group>
	);
}

// --- network ------------------------------------------------------------------

function PortField({ service, choices, onChoices }: SectionProps) {
	const id = useId();
	const choice = choices.services?.[service.key];
	const [draft, setDraft] = useState(
		String(choice?.port ?? (service.port > 0 ? service.port : "")),
	);
	const parsed = parsePortField(draft);
	const invalid = parsed !== undefined && Number.isNaN(parsed);
	return (
		<Field className="w-40 gap-1.5">
			<FieldLabel htmlFor={id}>Port</FieldLabel>
			<Input
				id={id}
				inputMode="numeric"
				autoComplete="off"
				aria-invalid={invalid || undefined}
				className="font-mono"
				placeholder="8080"
				value={draft}
				onChange={(event) => {
					setDraft(event.target.value);
					const next = parsePortField(event.target.value);
					if (next === undefined || !Number.isNaN(next)) {
						onChoices((current) => setPort(current, service.key, next));
					}
				}}
			/>
			<FieldDescription>
				{invalid
					? "Use a number from 0 to 65535."
					: "The container port behind the domain."}
			</FieldDescription>
		</Field>
	);
}

const DOMAIN_SOURCE: Record<string, string> = {
	traefik: "from Traefik labels",
	suggested: "suggested",
	choice: "your choice",
};

function DomainRow({ service, choices, onChoices }: SectionProps) {
	const id = useId();
	const choice = choices.services?.[service.key];
	const enabled = domainEnabled(service, choices);
	const domains = service.domains ?? [];
	const host = choice?.domain?.host ?? "";
	return (
		<ToggleRow
			label="Public domain"
			description={
				domains.length === 0 && !enabled
					? "No domain yet. Turn it on to get a generated hostname."
					: "Routes traffic from the internet to this service."
			}
			checked={enabled}
			onCheckedChange={(next) =>
				onChoices((current) =>
					setDomain(current, service.key, {
						enabled: next,
						...(next && host !== "" ? { host } : {}),
					}),
				)
			}
		>
			{domains.length > 0 ? (
				<ul className="m-0 flex list-none flex-col gap-1 p-0">
					{domains.map((domain) => (
						<li
							key={`${domain.host ?? "generated"}:${domain.source}`}
							className="flex flex-wrap items-center gap-x-2 gap-y-1"
						>
							<span className="min-w-0 truncate font-mono text-[12px] text-ink-1">
								{domain.host ? domain.host : "Generated hostname"}
							</span>
							<Badge>{DOMAIN_SOURCE[domain.source] ?? domain.source}</Badge>
						</li>
					))}
				</ul>
			) : null}
			{enabled ? (
				<Field className="gap-1.5">
					<FieldLabel htmlFor={id}>Custom host (optional)</FieldLabel>
					<Input
						id={id}
						spellCheck={false}
						autoComplete="off"
						className="font-mono"
						placeholder="shop.example.com"
						value={host}
						onChange={(event) =>
							onChoices((current) =>
								setDomain(current, service.key, {
									enabled: true,
									host: event.target.value,
								}),
							)
						}
					/>
				</Field>
			) : null}
		</ToggleRow>
	);
}

function PublishedPorts({ service, choices, onChoices }: SectionProps) {
	const ports = routablePorts(service);
	if (ports.length === 0) return null;
	const published = publishedKeys(service, choices);
	return (
		<div className="flex flex-col gap-2">
			{ports.map((port) => {
				const key = portKey(port);
				const on = published.includes(key);
				return (
					<ToggleRow
						key={key}
						label={
							<>
								Publish <span className="font-mono">{key}</span> through a{" "}
								{port.protocol.toUpperCase()} route
							</>
						}
						description={
							<>
								{port.hostPort
									? `The Compose file publishes host port ${port.hostPort}. `
									: ""}
								{on && port.listen
									? `Public port ${port.listen}.`
									: "Off by default: it opens a port on the server."}
							</>
						}
						checked={on}
						onCheckedChange={(next) =>
							onChoices((current) => setPublish(current, service, key, next))
						}
					/>
				);
			})}
		</div>
	);
}

export function NetworkSection(props: SectionProps) {
	const { service } = props;
	const hasPort = service.kind === "app" || service.port > 0;
	const routable = routablePorts(service).length > 0;
	if (!hasPort && !routable && (service.domains ?? []).length === 0)
		return null;
	return (
		<Group title="Network">
			{hasPort ? <PortField {...props} /> : null}
			{hasPort ? <DomainRow {...props} /> : null}
			<PublishedPorts {...props} />
		</Group>
	);
}

// --- storage ------------------------------------------------------------------

const VOLUME_FROM: Record<string, string> = {
	volume: "named volume",
	anonymous: "anonymous",
	bind: "from a bind mount",
	managed: "managed",
};

const BIND_STATUS: Record<string, string> = {
	rejected: "Not imported",
	missing: "Missing",
	ignored: "Ignored",
	file: "File",
	converted: "Named volume",
};

export function StorageSection({ service, choices, onChoices }: SectionProps) {
	const volumes = service.volumes ?? [];
	const files = service.files ?? [];
	const binds = groupBinds(service.binds);
	if (
		volumes.length === 0 &&
		files.length === 0 &&
		binds.attention.length === 0 &&
		binds.convertible.length === 0
	) {
		return null;
	}
	const converted = convertedTargets(service, choices);
	return (
		<Group title="Storage">
			{volumes.length > 0 ? (
				<ul className="m-0 flex list-none flex-col gap-1 p-0">
					{volumes.map((volume) => (
						<li
							key={`${volume.name}:${volume.path}`}
							className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[12px]"
						>
							<span className="font-mono text-ink-1">{volume.name}</span>
							<span className="font-mono text-ink-3">{volume.path}</span>
							<Badge>{VOLUME_FROM[volume.from] ?? volume.from}</Badge>
							{volume.note ? (
								<span className="text-ink-3">{volume.note}</span>
							) : null}
						</li>
					))}
				</ul>
			) : null}
			{files.length > 0 ? (
				<ul className="m-0 flex list-none flex-col gap-1 p-0">
					{files.map((file) => (
						<li
							key={file.path}
							className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[12px]"
						>
							<span className="font-mono text-ink-1">{file.path}</span>
							<span className="font-mono text-2xs text-ink-3 tabular-nums">
								{file.bytes} B{file.mode ? ` · ${file.mode}` : ""}
							</span>
							<Badge>config file from {file.source}</Badge>
						</li>
					))}
				</ul>
			) : null}
			{binds.attention.map((bind) => (
				<div
					key={`${bind.source}:${bind.target}`}
					className="flex flex-col gap-0.5 rounded-ctl border border-line bg-white/3 px-2.5 py-2"
				>
					<div className="flex flex-wrap items-center gap-x-2 gap-y-1">
						<Badge variant="warning">
							{BIND_STATUS[bind.status] ?? bind.status}
						</Badge>
						<span className="font-mono text-[12px] text-ink-1">
							{bind.source}
						</span>
						<span className="font-mono text-[12px] text-ink-3">
							to {bind.target}
						</span>
					</div>
					{bind.reason ? (
						<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
							{bind.reason}
						</p>
					) : null}
				</div>
			))}
			{binds.convertible.map((bind) => (
				<div
					key={`${bind.source}:${bind.target}`}
					className="flex flex-col gap-1 rounded-ctl border border-line bg-white/3 px-2.5 py-2"
				>
					<div className="flex flex-wrap items-center gap-x-2 gap-y-1">
						<span className="font-mono text-[12px] text-ink-1">
							{bind.source}
						</span>
						<span className="font-mono text-[12px] text-ink-3">
							to {bind.target}
						</span>
					</div>
					{bind.reason ? (
						<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
							{bind.reason}
						</p>
					) : null}
					<CheckLabel
						checked={converted.includes(bind.target)}
						onCheckedChange={(next) =>
							onChoices((current) =>
								setConvert(current, service, bind.target, next),
							)
						}
						className="text-[12px] text-ink-1"
					>
						Use a named volume instead
					</CheckLabel>
					<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
						Files already in {bind.source} are not copied; the volume starts
						empty.
					</p>
				</div>
			))}
		</Group>
	);
}

// --- runtime ------------------------------------------------------------------

export function RuntimeSection({ service }: { service: ComposePlanService }) {
	const health = healthSummary(service.health);
	const limits = limitsText(service);
	const options = dockerOptionTexts(service.docker);
	const command = (service.command ?? []).join(" ");
	const rows: Array<[string, string]> = [];
	if (command !== "") rows.push(["Command", command]);
	if (health) {
		rows.push([
			service.health?.fromCompose ? "Health check" : "Health check (added)",
			health,
		]);
	}
	if (limits) rows.push(["Limits", limits]);
	if (service.stopGraceSeconds) {
		rows.push(["Stop grace", `${service.stopGraceSeconds}s`]);
	}
	if ((service.dependsOn ?? []).length > 0) {
		rows.push(["Starts after", (service.dependsOn ?? []).join(", ")]);
	}
	if (rows.length === 0 && options.length === 0 && !service.replicasNote) {
		return null;
	}
	return (
		<Group title="Runtime">
			{rows.length > 0 ? (
				<dl className="m-0 grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1 text-[12px]">
					{rows.map(([label, value]) => (
						<div key={label} className="contents">
							<dt className="text-ink-3">{label}</dt>
							<dd className="m-0 min-w-0 break-words font-mono text-ink-2">
								{value}
							</dd>
						</div>
					))}
				</dl>
			) : null}
			{service.replicasNote ? (
				<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
					{service.replicasNote}
				</p>
			) : null}
			{options.length > 0 ? (
				<div className="flex flex-col gap-1">
					<span className="text-[12px] text-ink-3">Container options</span>
					<ul className="m-0 flex list-none flex-wrap gap-1.5 p-0">
						{options.map((option) => (
							<li key={option}>
								<Badge>{option}</Badge>
							</li>
						))}
					</ul>
				</div>
			) : null}
		</Group>
	);
}
