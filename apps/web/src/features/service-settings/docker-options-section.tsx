import { useQuery } from "@tanstack/react-query";
import { CheckIcon, ShieldCheckIcon } from "lucide-react";
import { type ReactNode, useMemo } from "react";
import { volumesQuery } from "#/api/hooks";
import type { Service } from "#/api/models";
import { MoreOptions } from "#/components/settings/more-options";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { FieldError } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { Switch } from "#/components/ui/switch";
import { Toggle } from "#/components/ui/toggle";
import { HostAccessGroup, useHostAccess } from "./docker-host-access";
import {
	applyHardened,
	capabilityWarnings,
	type DockerField,
	type DockerForm,
	type DockerPolicy,
	FALLBACK_POLICY,
	isHardened,
	MAX_DOCKER_DNS,
	MAX_DOCKER_EXTRA_HOSTS,
	MAX_DOCKER_GROUP_ADD,
	MAX_DOCKER_LABELS,
	MAX_DOCKER_SYSCTLS,
	MAX_DOCKER_TMPFS,
	readOnlyRootfsFileProblems,
	readOnlyRootfsWarning,
	ULIMIT_NAMES,
	type UlimitName,
	unrestrictedFields,
	validateDockerForm,
} from "./docker-options";
import { useDockerPolicy } from "./docker-policy";
import {
	AddRowButton,
	type DockerRowProps,
	issuesFor,
	patchRow,
	RowShell,
	StringList,
} from "./docker-rows";
import {
	type DraftProps,
	Section,
	SectionBody,
	SelectRow,
	SettingRow,
	TextRow,
} from "./fields";

const UNRESTRICTED_CAP_HINT =
	"Works only on an unrestricted server (see Host access below)";
const IMAGE_DEFAULT = "image-default";
const ALL_CAPABILITIES = "ALL";

/** A sub-group heading inside the section: what the rows below have in common. */
function Group({
	title,
	description,
	children,
}: {
	title: string;
	description?: ReactNode;
	children?: ReactNode;
}) {
	return (
		<SectionBody
			anchor={`docker-group-${title.toLowerCase().replace(/[^a-z0-9]+/g, "-")}`}
			className="flex flex-col gap-1 pt-4 first:pt-0"
		>
			<h4 className="type-panel-title m-0">{title}</h4>
			{description ? <p className="type-secondary m-0">{description}</p> : null}
			{children}
		</SectionBody>
	);
}

/** A row of toggle chips: capabilities to add, capabilities to drop. */
function CapabilityChips({
	label,
	caps,
	pressedCaps,
	isLocked,
	lockHint,
	onToggle,
}: {
	label: string;
	caps: readonly string[];
	pressedCaps: readonly string[];
	isLocked(cap: string): boolean;
	lockHint(cap: string): string | undefined;
	onToggle(cap: string, on: boolean): void;
}) {
	return (
		<fieldset
			aria-label={label}
			className="m-0 flex min-w-0 flex-wrap items-center gap-1.5 border-0 p-0"
		>
			{caps.map((cap) => {
				const pressed = pressedCaps.includes(cap);
				const locked = isLocked(cap);
				return (
					<Toggle
						key={cap}
						variant="outline"
						size="sm"
						className="font-mono"
						pressed={pressed}
						disabled={locked}
						title={locked ? lockHint(cap) : undefined}
						onPressedChange={(next) => onToggle(cap, next)}
					>
						{pressed ? <CheckIcon aria-hidden /> : null}
						{cap}
					</Toggle>
				);
			})}
		</fieldset>
	);
}

function HardenedRow({ form, update }: Omit<DockerRowProps, "issues">) {
	const hardened = isHardened(form);
	return (
		<SettingRow
			anchor="docker-hardened"
			label="Hardened preset"
			description="Applies the hardening options below in one step."
			info="Drops every capability, blocks new privileges, mounts the root filesystem read-only, caps the process count at 512 and adds a 64 MiB /tmp when no tmpfs exists."
		>
			<div className="flex flex-wrap items-center gap-3">
				<Button
					type="button"
					variant="secondary"
					size="sm"
					disabled={hardened}
					onClick={() => update(applyHardened(form))}
				>
					<ShieldCheckIcon aria-hidden />
					{hardened ? "Hardened" : "Apply Hardened preset"}
				</Button>
				{hardened ? (
					<span className="text-ink-3 text-xs">
						The hardening options below are on.
					</span>
				) : null}
			</div>
			<p className="type-secondary m-0">
				Hardening is opt-in and may break images that chown files at start or
				use setuid binaries. Add back only the capabilities the application
				needs.
			</p>
		</SettingRow>
	);
}

function CapDropRow({
	form,
	issues,
	update,
	policy,
	pending,
}: DockerRowProps & { policy: DockerPolicy | undefined; pending: boolean }) {
	const own = issuesFor(issues, "capDrop");
	const known = policy?.knownCapabilities ?? [];
	const all = form.capDrop.includes(ALL_CAPABILITIES);
	const options = [
		ALL_CAPABILITIES,
		...known,
		...form.capDrop.filter(
			(cap) => cap !== ALL_CAPABILITIES && !known.includes(cap),
		),
	];
	return (
		<SettingRow
			anchor="docker-cap-drop"
			label="Drop capabilities"
			description="Linux capabilities removed before the added ones are applied."
			info="Drop ALL and add back only what the application needs."
		>
			{pending && known.length === 0 ? (
				<p className="type-secondary">Loading the capability names…</p>
			) : (
				<CapabilityChips
					label="Dropped capabilities"
					caps={options}
					pressedCaps={form.capDrop}
					isLocked={(cap) =>
						!form.capDrop.includes(cap) &&
						(form.capAdd.includes(cap) || (all && cap !== ALL_CAPABILITIES))
					}
					lockHint={(cap) =>
						form.capAdd.includes(cap)
							? `${cap} is added under Capabilities`
							: "ALL already drops everything"
					}
					onToggle={(cap, on) =>
						update({
							capDrop: on
								? [...form.capDrop, cap].sort()
								: form.capDrop.filter((c) => c !== cap),
						})
					}
				/>
			)}
			<FieldError>{own.listWide}</FieldError>
		</SettingRow>
	);
}

function SwitchRow({
	anchor,
	id,
	label,
	description,
	text,
	checked,
	onChange,
}: {
	anchor?: string;
	id: string;
	label: string;
	description: string;
	text: string;
	checked: boolean;
	onChange(next: boolean): void;
}) {
	return (
		<SettingRow
			{...(anchor ? { anchor } : {})}
			label={label}
			htmlFor={id}
			description={description}
		>
			<div className="flex items-center gap-3">
				<Switch id={id} checked={checked} onCheckedChange={onChange} />
				<span className="text-[13px] text-ink-2">{text}</span>
			</div>
		</SettingRow>
	);
}

function CapabilitiesRow({
	form,
	issues,
	update,
	policy,
	pending,
	failed,
	unrestricted,
}: DockerRowProps & {
	policy: DockerPolicy | undefined;
	pending: boolean;
	failed: boolean;
	/** The service's server is unrestricted: NET_ADMIN and SYS_ADMIN may be added. */
	unrestricted: boolean;
}) {
	const own = issuesFor(issues, "capAdd");
	const safe = policy?.safeCapabilities ?? [];
	const needing =
		policy?.unrestrictedCapabilities ??
		FALLBACK_POLICY.unrestrictedCapabilities;
	const known = new Set([...safe, ...needing]);
	// A capability the draft carries that the policy no longer lists stays
	// visible, so it can be taken off.
	const stray = form.capAdd.filter((cap) => !known.has(cap));
	const warnings = capabilityWarnings(form, policy);
	const lockedUnrestricted = needing.filter(
		(cap) => !unrestricted && !form.capAdd.includes(cap),
	);

	return (
		<SettingRow
			anchor="docker-capabilities"
			label="Add capabilities"
			description="Linux capabilities added to the container, on top of Docker's defaults."
		>
			{pending ? (
				<p className="type-secondary">Loading the allowed capabilities…</p>
			) : failed && form.capAdd.length === 0 ? (
				<p className="type-secondary">
					Could not load the allowed capabilities. Reload to try again.
				</p>
			) : (
				<CapabilityChips
					label="Capabilities"
					caps={[...safe, ...needing, ...stray]}
					pressedCaps={form.capAdd}
					isLocked={(cap) => lockedUnrestricted.includes(cap)}
					lockHint={() => UNRESTRICTED_CAP_HINT}
					onToggle={(cap, on) =>
						update({
							capAdd: on
								? [...form.capAdd, cap].sort()
								: form.capAdd.filter((c) => c !== cap),
						})
					}
				/>
			)}
			{lockedUnrestricted.length > 0 ? (
				<p className="text-ink-3 text-xs">
					<span className="font-mono">{lockedUnrestricted.join(", ")}</span>:{" "}
					{UNRESTRICTED_CAP_HINT}.
				</p>
			) : null}
			{warnings.map((message) => (
				<Alert key={message} variant="warning">
					<AlertDescription>{message}</AlertDescription>
				</Alert>
			))}
			<FieldError>{own.listWide}</FieldError>
		</SettingRow>
	);
}

function UlimitsRow({ form, issues, update }: DockerRowProps) {
	const own = issuesFor(issues, "ulimits");
	const items = ULIMIT_NAMES.map((name) => ({ value: name, label: name }));
	const unused = ULIMIT_NAMES.find(
		(name) => !form.ulimits.some((row) => row.name === name),
	);
	return (
		<SettingRow
			anchor="docker-ulimits"
			label="Ulimits"
			description="Open files (nofile) and locked memory (memlock), soft and hard."
			info="-1 or unlimited means no limit. Cap processes with the process limit under Hardening."
		>
			{form.ulimits.length > 0 ? (
				<ul aria-label="Ulimits" className="flex flex-col gap-1.5">
					{form.ulimits.map((row, index) => (
						<RowShell
							// biome-ignore lint/suspicious/noArrayIndexKey: rows are positional inputs without identity
							key={index}
							label="ulimit"
							index={index}
							error={own.row(index)}
							onRemove={() =>
								update({ ulimits: form.ulimits.filter((_, i) => i !== index) })
							}
						>
							<Select
								value={row.name}
								items={items}
								onValueChange={(name) =>
									update({
										ulimits: patchRow(form.ulimits, index, {
											name: name as UlimitName,
										}),
									})
								}
							>
								<SelectTrigger
									aria-label={`Ulimit ${index + 1} resource`}
									className="w-[120px] shrink-0 font-mono"
								>
									<SelectValue />
								</SelectTrigger>
								<SelectContent alignItemWithTrigger={false}>
									{ULIMIT_NAMES.map((name) => (
										<SelectItem key={name} value={name}>
											{name}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
							<Input
								aria-label={`Ulimit ${index + 1} soft limit`}
								autoComplete="off"
								spellCheck={false}
								inputMode="numeric"
								className="max-w-[140px] font-mono"
								placeholder="soft"
								aria-invalid={own.row(index) !== undefined}
								value={row.soft}
								onChange={(event) =>
									update({
										ulimits: patchRow(form.ulimits, index, {
											soft: event.target.value,
										}),
									})
								}
							/>
							<Input
								aria-label={`Ulimit ${index + 1} hard limit`}
								autoComplete="off"
								spellCheck={false}
								inputMode="numeric"
								className="max-w-[140px] font-mono"
								placeholder="hard"
								aria-invalid={own.row(index) !== undefined}
								value={row.hard}
								onChange={(event) =>
									update({
										ulimits: patchRow(form.ulimits, index, {
											hard: event.target.value,
										}),
									})
								}
							/>
						</RowShell>
					))}
				</ul>
			) : null}
			<FieldError>{own.listWide}</FieldError>
			<AddRowButton
				disabled={unused === undefined}
				onClick={() =>
					unused &&
					update({
						ulimits: [...form.ulimits, { name: unused, soft: "", hard: "" }],
					})
				}
			>
				Add ulimit
			</AddRowButton>
		</SettingRow>
	);
}

function ExtraHostsRow({ form, issues, update }: DockerRowProps) {
	const own = issuesFor(issues, "extraHosts");
	return (
		<SettingRow
			anchor="docker-extra-hosts"
			label="Extra hosts"
			description="Names added to the container's hosts file."
			info="host-gateway points at the server itself."
		>
			{form.extraHosts.length > 0 ? (
				<ul aria-label="Extra hosts" className="flex flex-col gap-1.5">
					{form.extraHosts.map((row, index) => (
						<RowShell
							// biome-ignore lint/suspicious/noArrayIndexKey: rows are positional inputs without identity
							key={index}
							label="extra host"
							index={index}
							error={own.row(index)}
							onRemove={() =>
								update({
									extraHosts: form.extraHosts.filter((_, i) => i !== index),
								})
							}
						>
							<Input
								aria-label={`Extra host ${index + 1} name`}
								autoComplete="off"
								spellCheck={false}
								className="max-w-[220px] font-mono"
								placeholder="db.internal"
								aria-invalid={own.row(index) !== undefined}
								value={row.name}
								onChange={(event) =>
									update({
										extraHosts: patchRow(form.extraHosts, index, {
											name: event.target.value,
										}),
									})
								}
							/>
							<Input
								aria-label={`Extra host ${index + 1} address`}
								autoComplete="off"
								spellCheck={false}
								className="font-mono"
								placeholder="10.0.0.5 or host-gateway"
								aria-invalid={own.row(index) !== undefined}
								value={row.address}
								onChange={(event) =>
									update({
										extraHosts: patchRow(form.extraHosts, index, {
											address: event.target.value,
										}),
									})
								}
							/>
						</RowShell>
					))}
				</ul>
			) : null}
			<FieldError>{own.listWide}</FieldError>
			<AddRowButton
				disabled={form.extraHosts.length >= MAX_DOCKER_EXTRA_HOSTS}
				onClick={() =>
					update({
						extraHosts: [...form.extraHosts, { name: "", address: "" }],
					})
				}
			>
				Add host
			</AddRowButton>
		</SettingRow>
	);
}

function LabelsRow({
	form,
	issues,
	reserved,
	update,
}: DockerRowProps & { reserved: readonly string[] }) {
	const own = issuesFor(issues, "labels");
	return (
		<SettingRow
			anchor="docker-labels"
			label="Labels"
			description={`Docker labels on the container, up to ${MAX_DOCKER_LABELS}.`}
			info={
				<>
					Labels starting with{" "}
					<span className="font-mono">
						{reserved.map((prefix) => `${prefix}*`).join(", ")}
					</span>{" "}
					are managed by NebulaCtrl.
				</>
			}
		>
			{form.labels.length > 0 ? (
				<ul aria-label="Labels" className="flex flex-col gap-1.5">
					{form.labels.map((row, index) => (
						<RowShell
							// biome-ignore lint/suspicious/noArrayIndexKey: rows are positional inputs without identity
							key={index}
							label="label"
							index={index}
							error={own.row(index)}
							onRemove={() =>
								update({ labels: form.labels.filter((_, i) => i !== index) })
							}
						>
							<Input
								aria-label={`Label ${index + 1} key`}
								autoComplete="off"
								spellCheck={false}
								className="max-w-[240px] font-mono"
								placeholder="com.example.team"
								aria-invalid={own.row(index) !== undefined}
								value={row.key}
								onChange={(event) =>
									update({
										labels: patchRow(form.labels, index, {
											key: event.target.value,
										}),
									})
								}
							/>
							<Input
								aria-label={`Label ${index + 1} value`}
								autoComplete="off"
								spellCheck={false}
								className="font-mono"
								placeholder="value"
								aria-invalid={own.row(index) !== undefined}
								value={row.value}
								onChange={(event) =>
									update({
										labels: patchRow(form.labels, index, {
											value: event.target.value,
										}),
									})
								}
							/>
						</RowShell>
					))}
				</ul>
			) : null}
			<FieldError>{own.listWide}</FieldError>
			<AddRowButton
				disabled={form.labels.length >= MAX_DOCKER_LABELS}
				onClick={() =>
					update({ labels: [...form.labels, { key: "", value: "" }] })
				}
			>
				Add label
			</AddRowButton>
		</SettingRow>
	);
}

function TmpfsRows({ form, issues, update }: DockerRowProps) {
	const own = issuesFor(issues, "tmpfs");
	return (
		<SettingRow
			anchor="docker-tmpfs"
			label="tmpfs mounts"
			description="In-memory filesystems at a path. They start empty on every start."
			info="Size is in MiB (default 64); mode is octal, like 1777."
		>
			{form.tmpfs.length > 0 ? (
				<ul aria-label="tmpfs mounts" className="flex flex-col gap-1.5">
					{form.tmpfs.map((row, index) => (
						<RowShell
							// biome-ignore lint/suspicious/noArrayIndexKey: rows are positional inputs without identity
							key={index}
							label="tmpfs mount"
							index={index}
							error={own.row(index)}
							onRemove={() =>
								update({ tmpfs: form.tmpfs.filter((_, i) => i !== index) })
							}
						>
							<Input
								aria-label={`tmpfs ${index + 1} path`}
								autoComplete="off"
								spellCheck={false}
								className="font-mono"
								placeholder="/var/cache/app"
								aria-invalid={own.row(index) !== undefined}
								value={row.path}
								onChange={(event) =>
									update({
										tmpfs: patchRow(form.tmpfs, index, {
											path: event.target.value,
										}),
									})
								}
							/>
							<Input
								aria-label={`tmpfs ${index + 1} size in MiB`}
								autoComplete="off"
								spellCheck={false}
								inputMode="numeric"
								className="max-w-[96px] font-mono"
								placeholder="64"
								aria-invalid={own.row(index) !== undefined}
								value={row.sizeMb}
								onChange={(event) =>
									update({
										tmpfs: patchRow(form.tmpfs, index, {
											sizeMb: event.target.value,
										}),
									})
								}
							/>
							<Input
								aria-label={`tmpfs ${index + 1} mode`}
								autoComplete="off"
								spellCheck={false}
								className="max-w-[88px] font-mono"
								placeholder="1777"
								aria-invalid={own.row(index) !== undefined}
								value={row.mode}
								onChange={(event) =>
									update({
										tmpfs: patchRow(form.tmpfs, index, {
											mode: event.target.value,
										}),
									})
								}
							/>
						</RowShell>
					))}
				</ul>
			) : null}
			<FieldError>{own.listWide}</FieldError>
			<AddRowButton
				disabled={form.tmpfs.length >= MAX_DOCKER_TMPFS}
				onClick={() =>
					update({
						tmpfs: [...form.tmpfs, { path: "", sizeMb: "", mode: "" }],
					})
				}
			>
				Add tmpfs
			</AddRowButton>
		</SettingRow>
	);
}

/** Rows of a kernel parameter (picked from the catalog) and a number. */
function KernelRows({
	anchor,
	label,
	description,
	info,
	field,
	rowLabel,
	keys,
	rows,
	issues,
	max,
	loading,
	keyPlaceholder,
	valuePlaceholder,
	addLabel,
	onChange,
}: {
	anchor: string;
	label: string;
	description: ReactNode;
	info?: ReactNode;
	field: DockerField;
	/** Singular, for "Remove sysctl 2". */
	rowLabel: string;
	/** The catalog's keys. */
	keys: readonly string[];
	rows: readonly { key: string; value: string }[];
	issues: DockerRowProps["issues"];
	max: number;
	loading: boolean;
	keyPlaceholder: string;
	valuePlaceholder: string;
	addLabel: string;
	onChange(rows: { key: string; value: string }[]): void;
}) {
	const own = issuesFor(issues, field);
	const used = new Set(rows.map((row) => row.key));
	return (
		<SettingRow
			anchor={anchor}
			label={label}
			description={description}
			info={info}
		>
			{rows.length > 0 ? (
				<ul aria-label={label} className="flex flex-col gap-1.5">
					{rows.map((row, index) => {
						const items = [
							...keys,
							...(row.key !== "" && !keys.includes(row.key) ? [row.key] : []),
						].map((key) => ({ value: key, label: key }));
						return (
							<RowShell
								// biome-ignore lint/suspicious/noArrayIndexKey: rows are positional inputs without identity
								key={index}
								label={rowLabel}
								index={index}
								error={own.row(index)}
								onRemove={() => onChange(rows.filter((_, i) => i !== index))}
							>
								<Select
									value={row.key === "" ? null : row.key}
									items={items}
									onValueChange={(key) =>
										onChange(patchRow(rows, index, { key: String(key ?? "") }))
									}
								>
									<SelectTrigger
										aria-label={`${label} ${index + 1} key`}
										aria-invalid={own.row(index) !== undefined}
										className="w-[260px] max-w-full shrink-0 font-mono"
									>
										<SelectValue placeholder={keyPlaceholder} />
									</SelectTrigger>
									<SelectContent alignItemWithTrigger={false}>
										{items.map((item) => (
											<SelectItem
												key={item.value}
												value={item.value}
												disabled={
													item.value !== row.key && used.has(item.value)
												}
											>
												<span className="font-mono">{item.label}</span>
											</SelectItem>
										))}
									</SelectContent>
								</Select>
								<Input
									aria-label={`${label} ${index + 1} value`}
									autoComplete="off"
									spellCheck={false}
									className="max-w-[200px] font-mono"
									placeholder={valuePlaceholder}
									aria-invalid={own.row(index) !== undefined}
									value={row.value}
									onChange={(event) =>
										onChange(
											patchRow(rows, index, { value: event.target.value }),
										)
									}
								/>
							</RowShell>
						);
					})}
				</ul>
			) : null}
			{keys.length === 0 && rows.length === 0 ? (
				<p className="type-secondary m-0">
					{loading
						? "Loading the supported keys…"
						: "Could not load the supported keys. Reload to try again."}
				</p>
			) : null}
			<FieldError>{own.listWide}</FieldError>
			<AddRowButton
				disabled={keys.length === 0 || rows.length >= max}
				onClick={() => onChange([...rows, { key: "", value: "" }])}
			>
				{addLabel}
			</AddRowButton>
		</SettingRow>
	);
}

/**
 * The typed Docker options of a service, in groups: hardening, container
 * basics, identity, networking, kernel and, last, the host-access options that
 * work only on a server an instance admin made Unrestricted. Release config like
 * the rest: staged on Save, applied on the next Deploy. `serverError` is what
 * the control plane said about them on a failed Save.
 */
export function DockerOptionsSection({
	draft,
	set,
	service,
	serverError,
}: DraftProps & { service: Service; serverError?: string | undefined }) {
	const { policy, pending, failed } = useDockerPolicy();
	const access = useHostAccess(draft.serverId);
	const volumes = useQuery(volumesQuery(service.id));
	const form = draft.docker;
	const issues = useMemo(
		() =>
			validateDockerForm(form, {
				policy,
				filePaths: draft.files.map((file) => file.path),
			}),
		[form, policy, draft.files],
	);
	// Only once the volumes are known: a half-loaded list would warn about everything.
	const unmountedFiles = useMemo(
		() =>
			volumes.data
				? readOnlyRootfsFileProblems(
						form,
						draft.files.map((file) => file.path),
						volumes.data.map((volume) => volume.mountPath),
					)
				: [],
		[form, draft.files, volumes.data],
	);
	const update = (patch: Partial<DockerForm>) =>
		set("docker", { ...form, ...patch });
	const rowProps: DockerRowProps = { form, issues, update };
	const stopSignals = policy?.stopSignals ?? FALLBACK_POLICY.stopSignals;
	const pids = issuesFor(issues, "pidsLimit");
	const hostname = issuesFor(issues, "hostname");
	const user = issuesFor(issues, "user");
	const shm = issuesFor(issues, "shmSizeMb");
	const signal = issuesFor(issues, "stopSignal");
	const entry = issuesFor(issues, "entrypoint");
	const rootfsWarning = readOnlyRootfsWarning(form);

	return (
		<Section
			title="Docker options"
			description="Container hardening and runtime options."
		>
			{serverError ? (
				<SectionBody>
					<Alert variant="destructive">
						<AlertDescription>{serverError}</AlertDescription>
					</Alert>
				</SectionBody>
			) : null}

			<Group
				title="Hardening"
				description="Shrink what a compromised container can do."
			/>
			<HardenedRow form={form} update={update} />
			<CapDropRow {...rowProps} policy={policy} pending={pending && !failed} />
			<SwitchRow
				anchor="docker-no-new-privileges"
				id="docker-no-new-privileges"
				label="No new privileges"
				description="Setuid helpers such as sudo stop working inside the container."
				text="Block privilege escalation"
				checked={form.noNewPrivileges}
				onChange={(noNewPrivileges) => update({ noNewPrivileges })}
			/>
			<SwitchRow
				anchor="docker-read-only-rootfs"
				id="docker-read-only-rootfs"
				label="Read-only root"
				description="Mounts the root filesystem read-only; writable paths are volumes and tmpfs mounts."
				text="Mount the root filesystem read-only"
				checked={form.readOnlyRootfs}
				onChange={(readOnlyRootfs) => update({ readOnlyRootfs })}
			/>
			{rootfsWarning || unmountedFiles.length > 0 ? (
				<SectionBody className="flex flex-col gap-2 py-1.5">
					{rootfsWarning ? (
						<Alert variant="warning">
							<AlertDescription>{rootfsWarning}</AlertDescription>
						</Alert>
					) : null}
					{unmountedFiles.length > 0 ? (
						<Alert variant="warning">
							<AlertDescription>
								Saving fails while the root is read-only: the config{" "}
								{unmountedFiles.length === 1 ? "file" : "files"}{" "}
								<span className="font-mono">{unmountedFiles.join(", ")}</span>{" "}
								{unmountedFiles.length === 1 ? "is" : "are"} written into the
								root filesystem. Put each on a volume mount (a tmpfs does not
								work), or turn the read-only root off.
							</AlertDescription>
						</Alert>
					) : null}
				</SectionBody>
			) : null}
			<TextRow
				id="set-docker-pids-limit"
				label="Process limit"
				description="Caps the processes and threads of the container. 0 or empty means no limit."
				info="A fork bomb stops at the cap."
				type="number"
				narrow
				mono
				value={form.pidsLimit}
				error={pids.listWide}
				placeholder="512"
				onChange={(pidsLimit) => update({ pidsLimit })}
			/>

			<Group title="Container" />
			<CapabilitiesRow
				{...rowProps}
				policy={policy}
				pending={pending && !failed}
				failed={failed}
				unrestricted={access.kind === "unrestricted"}
			/>
			<UlimitsRow {...rowProps} />
			<TextRow
				id="set-docker-shm"
				label="Shared memory"
				description="Size of /dev/shm in MiB."
				info="Browsers and some databases need more than Docker's default of 64 MiB."
				type="number"
				narrow
				mono
				value={form.shmSizeMb}
				error={shm.listWide}
				placeholder="64"
				onChange={(shmSizeMb) => update({ shmSizeMb })}
			/>
			<LabelsRow
				{...rowProps}
				reserved={policy?.reservedLabels ?? FALLBACK_POLICY.reservedLabels}
			/>
			<SwitchRow
				anchor="docker-init"
				id="docker-init"
				label="Init process"
				description="Reaps zombie processes and forwards signals when the app does not."
				text="Run an init process (tini) as PID 1"
				checked={form.init}
				onChange={(init) => update({ init })}
			/>
			<SelectRow
				id="set-docker-stop-signal"
				label="Stop signal"
				description="Sent first when the container stops."
				info="The stop grace period then ends it with SIGKILL."
				value={form.stopSignal === "" ? IMAGE_DEFAULT : form.stopSignal}
				options={[
					{
						value: IMAGE_DEFAULT,
						label: "Image default",
						text: "Image default",
					},
					...stopSignals.map((name) => ({
						value: name,
						label: <span className="font-mono">{name}</span>,
						text: name,
					})),
				]}
				error={signal.listWide}
				onChange={(next) =>
					update({ stopSignal: next === IMAGE_DEFAULT ? "" : next })
				}
			/>
			<TextRow
				id="set-docker-entrypoint"
				label="Entrypoint"
				description="Replaces the image's entrypoint, like docker run --entrypoint."
				info="The image's default command is dropped, so the start command is passed to it as arguments. Quote arguments with spaces."
				mono
				value={form.entrypoint}
				placeholder="(image default)  e.g. /app/entry.sh --verbose"
				error={entry.listWide}
				onChange={(entrypoint) => update({ entrypoint })}
			/>
			<TmpfsRows {...rowProps} />

			<Group
				title="Identity"
				description="Who the process runs as, and the name it sees."
			/>
			<TextRow
				id="set-docker-hostname"
				label="Hostname"
				description="One label of lower-case letters, digits and dashes. Empty uses the service slug."
				info="Up to 63 characters. The slug survives every deploy."
				mono
				value={form.hostname}
				placeholder={service.slug}
				error={hostname.listWide}
				onChange={(value) => update({ hostname: value })}
			/>
			<TextRow
				id="set-docker-user"
				label="User"
				description="Runs the container as uid[:gid]. Empty keeps the image's user."
				info="Numbers or names, like 1000:1000 or app."
				mono
				narrow
				value={form.user}
				placeholder="1000:1000"
				error={user.listWide}
				onChange={(value) => update({ user: value })}
			/>
			<SettingRow
				anchor="docker-group-add"
				label="Supplementary groups"
				description={`Numeric group IDs added to the process, up to ${MAX_DOCKER_GROUP_ADD}.`}
				info="Like the server's render group for a device."
			>
				<StringList
					label="Supplementary groups"
					rowLabel="group ID"
					values={form.groupAdd}
					issues={issuesFor(issues, "groupAdd")}
					max={MAX_DOCKER_GROUP_ADD}
					placeholder="109"
					addLabel="Add group"
					inputMode="numeric"
					onChange={(groupAdd) => update({ groupAdd })}
				/>
			</SettingRow>

			<Group
				title="Networking"
				description="Name resolution inside the container."
			/>
			<SettingRow
				anchor="docker-dns"
				label="DNS servers"
				description={`Resolver IP addresses, IPv4 or IPv6, up to ${MAX_DOCKER_DNS}.`}
				info="Hostnames are not accepted. Empty uses the server's resolvers."
			>
				<StringList
					label="DNS servers"
					rowLabel="DNS server"
					values={form.dns}
					issues={issuesFor(issues, "dns")}
					max={MAX_DOCKER_DNS}
					placeholder="1.1.1.1"
					addLabel="Add DNS server"
					onChange={(dns) => update({ dns })}
				/>
			</SettingRow>
			<ExtraHostsRow {...rowProps} />

			<Group
				title="Kernel"
				description="Kernel parameters, for the container and for the host it runs on."
			/>
			<KernelRows
				anchor="docker-sysctls"
				label="Sysctls"
				description={`Namespaced kernel parameters of the container, up to ${MAX_DOCKER_SYSCTLS}.`}
				info="From a curated list. A value is a number, or numbers separated by single spaces."
				field="sysctls"
				rowLabel="sysctl"
				keys={policy?.allowedSysctls ?? []}
				rows={form.sysctls}
				issues={issues}
				max={MAX_DOCKER_SYSCTLS}
				loading={pending}
				keyPlaceholder="Choose a sysctl"
				valuePlaceholder="1024"
				addLabel="Add sysctl"
				onChange={(sysctls) => update({ sysctls })}
			/>
			<KernelRows
				anchor="docker-host-sysctls"
				label="Host requirements"
				description="Kernel parameters the application needs on the server, as minimum values."
				info="Like vm.max_map_count for Elasticsearch. The container cannot set these; the deployment plan warns when the server has less."
				field="hostSysctls"
				rowLabel="host requirement"
				keys={policy?.hostSysctlKeys ?? []}
				rows={form.hostSysctls}
				issues={issues}
				max={policy?.hostSysctlKeys.length ?? 0}
				loading={pending}
				keyPlaceholder="Choose a parameter"
				valuePlaceholder="262144"
				addLabel="Add requirement"
				onChange={(hostSysctls) => update({ hostSysctls })}
			/>

			<MoreOptions
				label="Host access"
				defaultOpen={access.kind === "unrestricted"}
				invalid={unrestrictedFields(form, policy).length > 0}
			>
				<SectionBody>
					<p className="type-secondary m-0">
						Unrestricted servers only: options that can hand the container the
						host. NebulaCtrl never runs services on the host network, with the
						Docker socket or with host bind mounts.
					</p>
				</SectionBody>
				<HostAccessGroup {...rowProps} policy={policy} access={access} />
			</MoreOptions>
		</Section>
	);
}
