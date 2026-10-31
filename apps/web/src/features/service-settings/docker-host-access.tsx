import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { CheckIcon } from "lucide-react";
import { serversQuery } from "#/api/hooks";
import type { Server } from "#/api/models";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { FieldError } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Segmented } from "#/components/ui/segmented";
import { Switch } from "#/components/ui/switch";
import { Toggle } from "#/components/ui/toggle";
import {
	clearHostAccess,
	type DockerForm,
	type DockerPolicy,
	FALLBACK_POLICY,
	type GpuMode,
	HOST_ACCESS_WARNINGS,
	MAX_DOCKER_DEVICES,
	MAX_DOCKER_GPUS,
	unrestrictedFields,
} from "./docker-options";
import {
	AddRowButton,
	type DockerRowProps,
	issuesFor,
	patchRow,
	RowShell,
} from "./docker-rows";
import { SectionBody, SettingRow, TextRow } from "./fields";

/**
 * Whether the service's current target server allows the host-access options:
 * `unrestricted` (an instance admin switched it on), `restricted`, or `none`
 * when no server is chosen (or it is still loading, or no longer listed).
 */
export type HostAccess =
	| { kind: "unrestricted"; server: Server }
	| { kind: "restricted"; server: Server }
	| { kind: "none"; loading: boolean };

export function useHostAccess(serverId: string): HostAccess {
	const servers = useQuery(serversQuery());
	if (serverId === "") return { kind: "none", loading: false };
	const server = servers.data?.find((item) => item.id === serverId);
	if (!server) return { kind: "none", loading: servers.isPending };
	return { kind: server.unrestricted ? "unrestricted" : "restricted", server };
}

const GPU_OPTIONS: readonly { value: GpuMode; label: string }[] = [
	{ value: "none", label: "None" },
	{ value: "all", label: "All" },
	{ value: "count", label: "Count" },
];

function ServerLink({ access }: { access: HostAccess }) {
	if (access.kind === "none") return null;
	return (
		<>
			{" "}
			<Link
				to="/servers/$serverId/settings/$section"
				params={{ serverId: access.server.id, section: "danger" }}
				className="text-accent-ink underline"
			>
				Open {access.server.name}
			</Link>
			.
		</>
	);
}

/** The words above the group: what it needs, and what is wrong right now. */
function HostAccessNotice({
	access,
	policy,
	form,
	update,
}: DockerRowProps & {
	access: HostAccess;
	policy: DockerPolicy | undefined;
}) {
	const needing = unrestrictedFields(form, policy);
	if (access.kind === "unrestricted") {
		return (
			<Alert variant="warning">
				<AlertTitle>Host access is on for {access.server.name}</AlertTitle>
				<AlertDescription>
					Privileged mode, host devices and the other options here can give the
					container control of the host. Use them only with images you trust.
				</AlertDescription>
			</Alert>
		);
	}
	return (
		<>
			<p className="type-secondary m-0">
				These options work only on a server an instance admin made unrestricted
				(Servers, the server, Unrestricted).
				{access.kind === "restricted" ? (
					<>
						{" "}
						{access.server.name} is not.
						<ServerLink access={access} />
					</>
				) : (
					" Choose a server under General first."
				)}
			</p>
			{needing.length > 0 ? (
				<Alert variant="destructive">
					<AlertTitle>The next deploy will fail</AlertTitle>
					<AlertDescription>
						<p className="m-0">
							Host access options are set, but{" "}
							{access.kind === "restricted"
								? `${access.server.name} is not unrestricted`
								: "no unrestricted server is chosen"}
							. Remove them, or move the service to an unrestricted server.
						</p>
						<div className="mt-2">
							<Button
								type="button"
								variant="danger"
								size="xs"
								onClick={() => update(clearHostAccess(form, policy))}
							>
								Remove host access options
							</Button>
						</div>
					</AlertDescription>
				</Alert>
			) : null}
		</>
	);
}

function SwitchRow({
	anchor,
	id,
	label,
	description,
	text,
	warning,
	checked,
	disabled,
	onChange,
}: {
	anchor: string;
	id: string;
	label: string;
	description: string;
	text: string;
	warning: string;
	checked: boolean;
	disabled: boolean;
	onChange(next: boolean): void;
}) {
	return (
		<SettingRow
			anchor={anchor}
			label={label}
			htmlFor={id}
			description={description}
		>
			<div className="flex items-center gap-3">
				<Switch
					id={id}
					checked={checked}
					disabled={disabled}
					onCheckedChange={onChange}
				/>
				<span className="text-[13px] text-ink-2">{text}</span>
			</div>
			{checked ? (
				<Alert variant="warning">
					<AlertDescription>{warning}</AlertDescription>
				</Alert>
			) : null}
		</SettingRow>
	);
}

function DevicesRow({
	form,
	issues,
	update,
	disabled,
}: DockerRowProps & { disabled: boolean }) {
	const own = issuesFor(issues, "devices");
	const showWarning = form.devices.some(
		(row) => row.hostPath.trim() !== "" || row.containerPath.trim() !== "",
	);
	return (
		<SettingRow
			anchor="docker-devices"
			label="Devices"
			description="Host device nodes under /dev mapped into the container."
			info="Like /dev/dri/renderD128 for hardware video. Permissions are a subset of rwm; empty means all three."
		>
			{form.devices.length > 0 ? (
				<ul aria-label="Devices" className="flex flex-col gap-1.5">
					{form.devices.map((row, index) => (
						<RowShell
							// biome-ignore lint/suspicious/noArrayIndexKey: rows are positional inputs without identity
							key={index}
							label="device"
							index={index}
							error={own.row(index)}
							disabled={disabled}
							onRemove={() =>
								update({ devices: form.devices.filter((_, i) => i !== index) })
							}
						>
							<Input
								aria-label={`Device ${index + 1} host path`}
								autoComplete="off"
								spellCheck={false}
								className="font-mono"
								placeholder="/dev/dri/renderD128"
								disabled={disabled}
								aria-invalid={own.row(index) !== undefined}
								value={row.hostPath}
								onChange={(event) =>
									update({
										devices: patchRow(form.devices, index, {
											hostPath: event.target.value,
										}),
									})
								}
							/>
							<Input
								aria-label={`Device ${index + 1} container path`}
								autoComplete="off"
								spellCheck={false}
								className="font-mono"
								placeholder="same path"
								disabled={disabled}
								aria-invalid={own.row(index) !== undefined}
								value={row.containerPath}
								onChange={(event) =>
									update({
										devices: patchRow(form.devices, index, {
											containerPath: event.target.value,
										}),
									})
								}
							/>
							<Input
								aria-label={`Device ${index + 1} permissions`}
								autoComplete="off"
								spellCheck={false}
								className="max-w-[80px] font-mono"
								placeholder="rwm"
								disabled={disabled}
								aria-invalid={own.row(index) !== undefined}
								value={row.permissions}
								onChange={(event) =>
									update({
										devices: patchRow(form.devices, index, {
											permissions: event.target.value,
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
				disabled={disabled || form.devices.length >= MAX_DOCKER_DEVICES}
				onClick={() =>
					update({
						devices: [
							...form.devices,
							{ hostPath: "", containerPath: "", permissions: "" },
						],
					})
				}
			>
				Add device
			</AddRowButton>
			{showWarning ? (
				<Alert variant="warning">
					<AlertDescription>{HOST_ACCESS_WARNINGS.hardware}</AlertDescription>
				</Alert>
			) : null}
		</SettingRow>
	);
}

function GpusRow({
	form,
	issues,
	update,
	policy,
	disabled,
}: DockerRowProps & { policy: DockerPolicy | undefined; disabled: boolean }) {
	const own = issuesFor(issues, "gpus");
	const gpus = form.gpus;
	const capabilities =
		policy?.gpuCapabilities ?? FALLBACK_POLICY.gpuCapabilities;
	const patch = (next: Partial<DockerForm["gpus"]>) =>
		update({ gpus: { ...gpus, ...next } });
	return (
		<SettingRow
			anchor="docker-gpus"
			label="GPUs"
			description="NVIDIA GPUs through the NVIDIA Container Toolkit, which must be installed on the server."
		>
			<div className="flex flex-wrap items-center gap-2">
				<Segmented
					aria-label="GPUs"
					font="sans"
					density="regular"
					value={gpus.mode}
					options={GPU_OPTIONS.map((option) => ({ ...option, disabled }))}
					onValueChange={(mode) => patch({ mode })}
				/>
				{gpus.mode === "count" ? (
					<Input
						aria-label="Number of GPUs"
						autoComplete="off"
						spellCheck={false}
						inputMode="numeric"
						className="max-w-[88px] font-mono"
						placeholder={`1-${MAX_DOCKER_GPUS}`}
						disabled={disabled}
						aria-invalid={own.listWide !== undefined}
						value={gpus.count}
						onChange={(event) => patch({ count: event.target.value })}
					/>
				) : null}
			</div>
			{gpus.mode === "none" ? null : (
				<fieldset
					aria-label="GPU capabilities"
					className="m-0 flex min-w-0 flex-wrap items-center gap-1.5 border-0 p-0"
				>
					{capabilities.map((cap) => {
						const pressed = gpus.capabilities.includes(cap);
						return (
							<Toggle
								key={cap}
								variant="outline"
								size="sm"
								className="font-mono"
								pressed={pressed}
								disabled={disabled}
								onPressedChange={(next) =>
									patch({
										capabilities: next
											? [...gpus.capabilities, cap].sort()
											: gpus.capabilities.filter((c) => c !== cap),
									})
								}
							>
								{pressed ? <CheckIcon aria-hidden /> : null}
								{cap}
							</Toggle>
						);
					})}
				</fieldset>
			)}
			{gpus.mode === "none" ? null : (
				<p className="type-secondary m-0">
					No capability selected means the toolkit's default.
				</p>
			)}
			<FieldError>{own.listWide}</FieldError>
			{gpus.mode === "none" ? null : (
				<Alert variant="warning">
					<AlertDescription>{HOST_ACCESS_WARNINGS.hardware}</AlertDescription>
				</Alert>
			)}
		</SettingRow>
	);
}

/**
 * The "Host access" group: options that work only on a server an instance
 * admin made Unrestricted. Collapsed under the service's Docker options unless
 * the current server is unrestricted; the controls are disabled unless it is,
 * and the words above say why.
 */
export function HostAccessGroup({
	form,
	issues,
	update,
	policy,
	access,
}: DockerRowProps & {
	policy: DockerPolicy | undefined;
	access: HostAccess;
}) {
	const disabled = access.kind !== "unrestricted";
	const runtime = issuesFor(issues, "runtime");
	return (
		<>
			<SectionBody
				anchor="docker-host-access"
				className="flex flex-col gap-2 py-2"
			>
				<HostAccessNotice
					access={access}
					policy={policy}
					form={form}
					issues={issues}
					update={update}
				/>
			</SectionBody>
			<DevicesRow
				form={form}
				issues={issues}
				update={update}
				disabled={disabled}
			/>
			<GpusRow
				form={form}
				issues={issues}
				update={update}
				policy={policy}
				disabled={disabled}
			/>
			<TextRow
				id="set-docker-runtime"
				label="Runtime"
				description="An OCI runtime from the server's Docker daemon, like runsc (gVisor)."
				info="Empty means the daemon's default."
				mono
				disabled={disabled}
				value={form.runtime}
				placeholder="runsc"
				error={runtime.listWide}
				onChange={(value) => update({ runtime: value })}
			/>
			<SwitchRow
				anchor="docker-privileged"
				id="docker-privileged"
				label="Privileged"
				description="All capabilities, all devices and no seccomp or AppArmor profile."
				text="Run in privileged mode"
				warning={HOST_ACCESS_WARNINGS.privileged}
				checked={form.privileged}
				disabled={disabled}
				onChange={(privileged) => update({ privileged })}
			/>
			<SwitchRow
				anchor="docker-pid-host"
				id="docker-pid-host"
				label="Host PID namespace"
				description="Share the server's process namespace, like docker run --pid host."
				text="Share the host's PID namespace"
				warning={HOST_ACCESS_WARNINGS.pidHost}
				checked={form.pidHost}
				disabled={disabled}
				onChange={(pidHost) => update({ pidHost })}
			/>
			<SwitchRow
				anchor="docker-seccomp"
				id="docker-seccomp"
				label="Seccomp"
				description="Removes Docker's default syscall filter."
				text="Run without a seccomp profile"
				warning={HOST_ACCESS_WARNINGS.unconfined}
				checked={form.seccompUnconfined}
				disabled={disabled}
				onChange={(seccompUnconfined) => update({ seccompUnconfined })}
			/>
			<SwitchRow
				anchor="docker-apparmor"
				id="docker-apparmor"
				label="AppArmor"
				description="Removes Docker's default AppArmor profile."
				text="Run without an AppArmor profile"
				warning={HOST_ACCESS_WARNINGS.unconfined}
				checked={form.apparmorUnconfined}
				disabled={disabled}
				onChange={(apparmorUnconfined) => update({ apparmorUnconfined })}
			/>
		</>
	);
}
