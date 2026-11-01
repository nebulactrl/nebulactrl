import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { PlusIcon, XIcon } from "lucide-react";
import { useMemo, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { updateRoute } from "#/api/gen/sdk.gen";
import { pendingChangesQuery, servicesQuery } from "#/api/hooks";
import {
	type SaveBarState,
	SaveBarStatus,
} from "#/components/common/save-bar-status";
import { Button } from "#/components/ui/button";
import { CheckLabel } from "#/components/ui/check-label";
import { FieldError } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Segmented } from "#/components/ui/segmented";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { AccessRulesEditor } from "#/features/access-rules/access-rules-editor";
import { checkFromErrors, cleanList } from "#/features/access-rules/rules";
import type { ServiceTabProps } from "#/features/canvas/inspector/tabs/types";
import { DangerZone } from "#/features/service-settings/danger-zone";
import {
	AddDomainForm,
	DomainList,
} from "#/features/service-settings/domain-editor/domain-list";
import {
	Section,
	SectionBody,
	SettingRow,
} from "#/features/service-settings/fields";
import { SettingsPages } from "#/features/service-settings/settings-nav";
import {
	type CategoryId,
	categoriesFor,
	defaultCategory,
	resolveCategory,
	type SettingsSubject,
	stagedCounts,
} from "#/features/service-settings/settings-nav-model";
import { fieldError, isAgentTooOld, isGenerationConflict } from "../lib/errors";
import {
	FIREWALL_NOTE,
	type L4Protocol,
	RESTART_WARNING,
	UDP_WARNING,
} from "../lib/l4";
import {
	buildRoutePatch,
	MAX_UPSTREAMS,
	type RouteForm,
	routeDirty,
	toRouteForm,
	validateRouteForm,
} from "../lib/route-form";
import { withHost } from "../lib/tailscale";
import { invalidateRoutes, serverPortsQuery } from "../queries";
import { AgentTooOldBanner } from "../ui/agent-too-old";
import { TailnetDevicePicker } from "../ui/tailnet-device-picker";

/**
 * Settings of a route service: its name, what it forwards to, who may reach a
 * TCP port, its domains and the delete. Save sends one PATCH with `If-Match`
 * set to the generation the form was opened on, like every service form;
 * changes are staged until the next deploy. There is no source, build, health
 * check, resource or volume section: a route has no container.
 */
export function RouteSettingsTab({
	service,
	section,
	onSectionChange,
}: ServiceTabProps) {
	const queryClient = useQueryClient();
	const route = service.config.route;
	const protocol: "http" | L4Protocol =
		route?.protocol === "tcp" || route?.protocol === "udp"
			? route.protocol
			: "http";
	// `null` means untouched: the form follows the service as it refetches.
	const [edited, setEdited] = useState<RouteForm | null>(null);
	const baseline = useMemo(() => toRouteForm(service), [service]);
	const form = edited ?? baseline;
	const set = (patch: Partial<RouteForm>) => setEdited({ ...form, ...patch });

	const siblings = useQuery(servicesQuery(service.environmentId));
	const nameOf = (id: string) => siblings.data?.find((s) => s.id === id)?.name;
	const ports = useQuery({
		...serverPortsQuery(service.serverId ?? ""),
		enabled: service.serverId !== null && protocol !== "http",
	});

	const errors = useMemo(
		() =>
			validateRouteForm(form, protocol, {
				taken: ports.data?.items ?? [],
				selfServiceId: service.id,
			}),
		[form, protocol, ports.data, service.id],
	);
	const invalid = Object.keys(errors).length > 0;
	const dirty = edited !== null && routeDirty(service, edited, protocol);

	const save = useMutation({
		mutationFn: async () => {
			await updateRoute({
				path: { routeId: service.id },
				headers: { "If-Match": `"${service.generation}"` },
				body: buildRoutePatch(service, form, protocol),
			});
		},
		onSuccess: async () => {
			setEdited(null);
			await invalidateRoutes(queryClient);
			toast.success("Route saved", "Staged until the next deploy.");
		},
		onError: (error) => {
			// A lost race: the global handler toasted and refetched.
			if (isGenerationConflict(error)) {
				setEdited(null);
				return;
			}
			// A problem with the port or an address lives under Networking.
			if (
				fieldError(error, "listenPort") !== undefined ||
				fieldError(error, "upstream") !== undefined
			) {
				onSectionChange("networking");
			}
		},
	});

	const saveFailed = save.isError && !isGenerationConflict(save.error);
	const tooOld = isAgentTooOld(save.error);
	const portError = errors.listenPort ?? fieldError(save.error, "listenPort");
	const upstreamServer = fieldError(save.error, "upstream");
	const barState: SaveBarState =
		saveFailed && !tooOld
			? "failed"
			: dirty
				? invalid
					? "invalid"
					: "dirty"
				: "saved";
	const newPort =
		protocol !== "http" &&
		form.listenPort.trim() !== baseline.listenPort &&
		errors.listenPort === undefined;

	const setRow = (index: number, address: string) =>
		set({
			upstreams: form.upstreams.map((row, i) =>
				i === index && row.kind === "address" ? { ...row, address } : row,
			),
		});

	const subject = useMemo<SettingsSubject>(
		() => ({
			kind: service.kind,
			sourceType: service.sourceType,
			oneshot: false,
			routeProtocol: protocol,
		}),
		[service.kind, service.sourceType, protocol],
	);
	const categories = useMemo(() => categoriesFor(subject), [subject]);
	const active =
		resolveCategory(section, categories) ?? defaultCategory(categories);
	// The name is General; every other field of the form is Networking.
	const editedIds = useMemo(() => {
		const ids = new Set<CategoryId>();
		if (edited === null) return ids;
		if (routeDirty(service, { ...baseline, name: edited.name }, protocol)) {
			ids.add("general");
		}
		if (routeDirty(service, { ...edited, name: baseline.name }, protocol)) {
			ids.add("networking");
		}
		return ids;
	}, [service, baseline, edited, protocol]);
	const invalidIds = useMemo(() => {
		const ids = new Set<CategoryId>();
		for (const key of Object.keys(errors)) {
			ids.add(key === "name" ? "general" : "networking");
		}
		return ids;
	}, [errors]);
	const firstProblem: CategoryId | undefined = invalidIds.has("general")
		? "general"
		: invalidIds.has("networking")
			? "networking"
			: undefined;
	const stagedChanges = useQuery({
		...pendingChangesQuery(service.id),
		enabled: service.pendingChangeCounts.settings > 0,
	});
	const staged = useMemo(
		() =>
			stagedCounts(
				service.pendingChangeCounts.settings > 0
					? (stagedChanges.data ?? []).map((change) => change.field)
					: [],
			),
		[service.pendingChangeCounts.settings, stagedChanges.data],
	);

	return (
		<SettingsPages
			serviceId={service.id}
			subject={subject}
			categories={categories}
			active={active}
			onSelect={onSectionChange}
			edited={editedIds}
			invalid={invalidIds}
			staged={staged}
			banner={
				tooOld ? (
					<AgentTooOldBanner
						{...(service.server ? { serverName: service.server.name } : {})}
					/>
				) : null
			}
			footer={
				<div
					className="surface-float sticky bottom-0 z-10 flex items-center gap-3 rounded-none border-line border-x-0 border-b-0 px-4 py-2.5"
					aria-live="polite"
				>
					<SaveBarStatus state={barState}>
						{saveFailed && !tooOld
							? errorMessage(save.error)
							: dirty
								? invalid
									? "Fix the highlighted fields to save."
									: "Unsaved changes"
								: "All changes saved"}
					</SaveBarStatus>
					{firstProblem && firstProblem !== active ? (
						<Button
							variant="ghost"
							onClick={() => onSectionChange(firstProblem)}
						>
							Show in {categories.find((c) => c.id === firstProblem)?.label}
						</Button>
					) : null}
					<Button
						variant="secondary"
						disabled={!dirty || save.isPending}
						onClick={() => setEdited(null)}
					>
						Discard
					</Button>
					<Button
						variant="primary"
						disabled={!dirty || invalid || save.isPending}
						onClick={() => save.mutate()}
					>
						{save.isPending ? <Spinner /> : null}
						Save changes
					</Button>
				</div>
			}
		>
			<Section title="Identity" hidden={active !== "general"}>
				<SettingRow label="Name" htmlFor="set-name">
					<Input
						id="set-name"
						autoComplete="off"
						className="max-w-[360px]"
						aria-invalid={errors.name !== undefined}
						value={form.name}
						onChange={(event) => set({ name: event.target.value })}
					/>
					<FieldError>{errors.name}</FieldError>
				</SettingRow>
			</Section>

			<Section
				hidden={active !== "networking"}
				title={protocol === "http" ? "Upstreams" : "Target"}
				description={
					protocol === "http"
						? "Where the proxy forwards requests. More than one address load-balances between them."
						: "Where the connections go."
				}
			>
				<SectionBody>
					<ul aria-label="Upstreams" className="flex flex-col gap-1.5">
						{form.upstreams.map((row, index) => {
							const problem = errors[`upstreams.${index}`];
							return (
								// biome-ignore lint/suspicious/noArrayIndexKey: rows are positional inputs
								<li key={index} className="flex flex-col gap-1">
									<div className="flex items-center gap-1.5">
										{row.kind === "address" ? (
											<>
												<Input
													aria-label={`Upstream address ${index + 1}`}
													autoComplete="off"
													spellCheck={false}
													className="font-mono"
													placeholder="192.168.1.20:8123"
													aria-invalid={problem !== undefined}
													value={row.address}
													onChange={(event) =>
														setRow(index, event.target.value)
													}
												/>
												{form.upstreams.length === 1 && service.serverId ? (
													<TailnetDevicePicker
														serverId={service.serverId}
														onPick={(host) =>
															setRow(index, withHost(row.address, host))
														}
													/>
												) : null}
											</>
										) : (
											<>
												<span className="surface-well min-w-0 flex-1 truncate rounded-ctl px-2.5 py-1.5 text-[13px] text-ink-1">
													{nameOf(row.serviceId) ?? "Managed service"}
												</span>
												<Input
													aria-label={`Port of ${nameOf(row.serviceId) ?? "the service"}`}
													inputMode="numeric"
													className="w-24 font-mono"
													aria-invalid={problem !== undefined}
													value={row.port}
													onChange={(event) =>
														set({
															upstreams: form.upstreams.map((r, i) =>
																i === index && r.kind === "service"
																	? { ...r, port: event.target.value }
																	: r,
															),
														})
													}
												/>
											</>
										)}
										{form.upstreams.length > 1 ? (
											<Button
												type="button"
												variant="ghost"
												size="icon-sm"
												aria-label={`Remove upstream ${index + 1}`}
												onClick={() =>
													set({
														upstreams: form.upstreams.filter(
															(_, i) => i !== index,
														),
													})
												}
											>
												<XIcon aria-hidden />
											</Button>
										) : null}
									</div>
									{problem ? <FieldError>{problem}</FieldError> : null}
								</li>
							);
						})}
					</ul>
					{errors.upstreams ? (
						<FieldError>{errors.upstreams}</FieldError>
					) : null}
					{upstreamServer ? <FieldError>{upstreamServer}</FieldError> : null}
					{form.upstreams.length < MAX_UPSTREAMS ? (
						<Button
							type="button"
							variant="secondary"
							size="xs"
							className="mt-2"
							onClick={() =>
								set({
									upstreams: [
										...form.upstreams,
										{ kind: "address", address: "" },
									],
								})
							}
						>
							<PlusIcon aria-hidden />
							Add upstream
						</Button>
					) : null}
				</SectionBody>
				{protocol === "http" ? (
					<>
						<SettingRow
							label="Protocol to the service"
							description="https verifies the upstream certificate unless you turn that off."
						>
							<Segmented
								aria-label="Scheme"
								font="sans"
								value={form.scheme}
								onValueChange={(scheme) => set({ scheme })}
								options={[
									{ value: "http", label: "HTTP" },
									{ value: "https", label: "HTTPS" },
								]}
							/>
							{form.scheme === "https" ? (
								<CheckLabel
									className="mt-2 text-sm"
									checked={form.insecureSkipVerify}
									onCheckedChange={(insecureSkipVerify) =>
										set({ insecureSkipVerify })
									}
								>
									Skip TLS verification
								</CheckLabel>
							) : null}
							{form.scheme === "https" && form.insecureSkipVerify ? (
								<p className="mt-1 text-warning-text text-xs">
									The proxy accepts any certificate from this service, including
									a forged one.
								</p>
							) : null}
						</SettingRow>
						<SettingRow
							label="Health path"
							htmlFor="route-set-health"
							description="The proxy stops sending traffic to an address that fails it. Empty turns the check off."
						>
							<Input
								id="route-set-health"
								autoComplete="off"
								spellCheck={false}
								className="max-w-[240px] font-mono"
								placeholder="/health"
								aria-invalid={errors.healthPath !== undefined}
								value={form.healthPath}
								onChange={(event) => set({ healthPath: event.target.value })}
							/>
							<FieldError>{errors.healthPath}</FieldError>
						</SettingRow>
						<SettingRow
							label="Host header"
							description="Send the visitor's hostname on to the service, or the address's own."
						>
							<CheckLabel
								className="text-sm"
								checked={form.passHost}
								onCheckedChange={(passHost) => set({ passHost })}
							>
								Pass the original Host header
							</CheckLabel>
						</SettingRow>
						<SettingRow
							label="After deploy"
							description="NebulaCtrl checks that the route answers. Turn it off for a service that legitimately answers 5xx."
						>
							<CheckLabel
								className="text-sm"
								checked={form.verifySkip}
								onCheckedChange={(verifySkip) => set({ verifySkip })}
							>
								Skip the post-deploy check
							</CheckLabel>
						</SettingRow>
					</>
				) : null}
			</Section>

			{protocol !== "http" ? (
				<Section
					hidden={active !== "networking"}
					title="Port and access"
					description="The port is published on the server's proxy."
				>
					<SettingRow
						label="Port on the server"
						htmlFor="route-set-port"
						description="1024 to 65535."
					>
						<Input
							id="route-set-port"
							inputMode="numeric"
							className="max-w-[160px] font-mono"
							aria-invalid={portError !== undefined}
							value={form.listenPort}
							onChange={(event) => set({ listenPort: event.target.value })}
						/>
						<FieldError>{portError}</FieldError>
						{newPort ? (
							<p className="mt-1 text-warning-text text-xs">
								{RESTART_WARNING}
							</p>
						) : null}
					</SettingRow>
					{protocol === "tcp" ? (
						<SettingRow
							label="Who may connect"
							description="Networks that may open connections. Denied networks are refused first."
						>
							<AccessRulesEditor
								allow={form.cidrs}
								deny={form.denyCidrs}
								check={checkFromErrors(errors, { allow: "access" })}
								allowKey="cidrs"
								allowRequired={!form.openToInternet}
								allowEmptyHint="Add an allowed network, or open the port to the internet below."
								onAllowChange={(cidrs) =>
									set({
										cidrs,
										...(cleanList(cidrs).length > 0
											? { openToInternet: false }
											: {}),
									})
								}
								onDenyChange={(denyCidrs) => set({ denyCidrs })}
							/>
							<CheckLabel
								className="mt-2 items-start gap-2.5 text-sm"
								checkboxClassName="mt-0.5"
								checked={
									form.openToInternet && cleanList(form.cidrs).length === 0
								}
								disabled={cleanList(form.cidrs).length > 0}
								onCheckedChange={(openToInternet) => set({ openToInternet })}
							>
								<span className="flex flex-col gap-0.5">
									<span className="text-ink-1">Open to the internet</span>
									<span className="text-ink-3 text-xs">
										Anyone who can reach the server can connect, except the
										denied networks.
									</span>
								</span>
							</CheckLabel>
						</SettingRow>
					) : (
						<SettingRow label="Who may connect">
							<p className="text-warning-text text-xs leading-[1.5]">
								{UDP_WARNING}
							</p>
						</SettingRow>
					)}
					{protocol === "tcp" ? (
						<SettingRow
							label="PROXY protocol"
							description="Tell the service the visitor's real address. The service must expect it."
						>
							<Segmented
								aria-label="PROXY protocol"
								font="sans"
								value={String(form.proxyProtocol)}
								onValueChange={(value) =>
									set({ proxyProtocol: Number(value) as 0 | 1 | 2 })
								}
								options={[
									{ value: "0", label: "Off" },
									{ value: "1", label: "v1" },
									{ value: "2", label: "v2" },
								]}
							/>
						</SettingRow>
					) : null}
					<SectionBody>
						<p className="text-ink-3 text-xs leading-[1.5]">{FIREWALL_NOTE}</p>
					</SectionBody>
				</Section>
			) : (
				<Section
					hidden={active !== "networking"}
					title="Domains"
					description="Hostnames this route answers for. Each has its own path, redirect, access, headers, limits and TLS."
				>
					<SectionBody>
						{service.domains.length === 0 ? (
							<p className="type-secondary mb-3">
								No hostname yet. Add one to give the route a public address.
							</p>
						) : (
							<div className="mb-3">
								<DomainList service={service} />
							</div>
						)}
						<AddDomainForm service={service} disabled={false} />
					</SectionBody>
				</Section>
			)}

			{active === "danger" ? <DangerZone service={service} /> : null}
		</SettingsPages>
	);
}
