import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import {
	CheckIcon,
	ExternalLinkIcon,
	GlobeIcon,
	NetworkIcon,
	PlusIcon,
	RefreshCwIcon,
	XIcon,
} from "lucide-react";
import { type ReactNode, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { reprovisionServiceTailnet } from "#/api/gen/sdk.gen";
import { invalidateServices } from "#/api/invalidate";
import type { Service } from "#/api/models";
import { useConfirm } from "#/components/common/confirm-dialog";
import { CopyButton } from "#/components/common/copy-button";
import { Relative } from "#/components/common/relative";
import { useProjectContext } from "#/components/shell/use-project-context";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Badge } from "#/components/ui/badge";
import { Button, buttonVariants } from "#/components/ui/button";
import { CheckLabel } from "#/components/ui/check-label";
import { FieldError } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Spinner } from "#/components/ui/spinner";
import { StatusIcon } from "#/components/ui/status-icon";
import { Switch } from "#/components/ui/switch";
import { toast } from "#/components/ui/toast";
import { Toggle } from "#/components/ui/toggle";
import { tailscaleQuery } from "#/features/proxy/queries";
import { useIsAdmin } from "#/features/proxy/use-admin";
import {
	type DraftProps,
	Section,
	SectionBody,
	SettingRow,
	TextRow,
} from "./fields";
import { tailnetDifferences, tailnetStateInfo, trimFqdn } from "./tailnet-chip";
import {
	defaultTailnetHostname,
	emptyTailnetDraft,
	MAX_TCP_FORWARDS,
	normalizeHostname,
	normalizeTags,
	type TailnetDraft,
	type TailnetPortDraft,
	TCP_PRESETS,
	tagPolicy,
	tagsNotAllowed,
	validateTcpRows,
	withTcpPreset,
} from "./tailnet-draft";
import { tailnetErrorMessage } from "./tailnet-errors";

const FUNNEL_WARNING =
	"Anyone on the internet can reach this service through Tailscale's relays. This bypasses NebulaCtrl's proxy: no access logs or traffic analytics, bandwidth limits apply, and the hostname is public. Requires the funnel attribute for your tags in your tailnet policy.";

const REPROVISION_WARNING =
	"Creates a new device with a new tailnet IP. The old device is deleted.";

function SettingsLink({ children }: { children: ReactNode }) {
	return (
		<Link
			to="/settings/$section"
			params={{ section: "integrations" }}
			className="text-accent-ink underline"
		>
			{children}
		</Link>
	);
}

/** A label, a switch and the words for its state: never the switch alone. */
function SwitchRow({
	id,
	label,
	description,
	checked,
	onChange,
	disabled = false,
	cue,
	error,
}: {
	id: string;
	label: ReactNode;
	description: ReactNode;
	checked: boolean;
	onChange(checked: boolean): void;
	disabled?: boolean;
	cue: string;
	error?: string | undefined;
}) {
	return (
		<SettingRow label={label} htmlFor={id} description={description}>
			<div className="flex items-center gap-3">
				<Switch
					id={id}
					checked={checked}
					disabled={disabled}
					onCheckedChange={onChange}
				/>
				<span className="text-[13px] text-ink-2">{cue}</span>
			</div>
			<FieldError>{error}</FieldError>
		</SettingRow>
	);
}

/** The node as the last deployment left it: state, name, addresses, re-provision. */
function StatusRow({
	service,
	onReprovision,
	reprovisioning,
}: {
	service: Service;
	onReprovision(): void;
	reprovisioning: boolean;
}) {
	const view = service.tailnet;
	if (!view) return null;
	const info = tailnetStateInfo(view.state);
	const fqdn = trimFqdn(view.fqdn);
	const ips = view.ips ?? [];
	const needsAttention = view.state === "needs_attention";
	return (
		<section
			aria-label="Tailnet status"
			className="surface-well flex flex-col gap-2 rounded-ctl px-3 py-2.5"
		>
			<div className="flex flex-wrap items-center gap-2">
				<StatusIcon kind={info.kind} label="" />
				<span className="font-medium text-ink-1 text-sm">
					{info.label}
					{info.kind === "deploying" ? "…" : ""}
				</span>
				{view.funnel ? (
					<Badge tone="warn">
						<GlobeIcon aria-hidden />
						Public via Funnel
					</Badge>
				) : null}
				{view.hidePublic ? <Badge>Tailnet only</Badge> : null}
				{view.observedAt ? (
					<span className="text-ink-3 text-xs">
						reported <Relative iso={view.observedAt} />
					</span>
				) : null}
			</div>
			{fqdn ? (
				<div className="flex flex-wrap items-center gap-2">
					<code className="min-w-0 truncate font-mono text-ink-1 text-xs">
						{fqdn}
					</code>
					<CopyButton
						value={fqdn}
						label="Copy tailnet name"
						variant="ghost"
						size="icon-xs"
					/>
					{view.state === "online" && view.url !== "" ? (
						<a
							href={view.url}
							target="_blank"
							rel="noreferrer"
							className={buttonVariants({ variant: "secondary", size: "xs" })}
						>
							<ExternalLinkIcon aria-hidden />
							Open
						</a>
					) : null}
				</div>
			) : null}
			{ips.length > 0 ? (
				<div className="flex items-center gap-2">
					<code className="min-w-0 flex-1 truncate font-mono text-ink-2 text-xs">
						{ips.join("  ")}
					</code>
					<CopyButton
						value={ips.join(" ")}
						label="Copy tailnet addresses"
						variant="ghost"
						size="icon-xs"
					/>
				</div>
			) : null}
			{view.message !== "" ? (
				<p
					className={
						needsAttention
							? "text-destructive-text text-xs"
							: "text-ink-3 text-xs"
					}
				>
					{view.message}
				</p>
			) : null}
			{needsAttention || view.needsReprovision ? (
				<div>
					<Button
						variant="secondary"
						size="sm"
						disabled={reprovisioning}
						onClick={onReprovision}
					>
						{reprovisioning ? <Spinner /> : <RefreshCwIcon aria-hidden />}
						Re-provision
					</Button>
				</div>
			) : null}
		</section>
	);
}

/** One `listen → target` row of the TCP list. */
function TcpRow({
	index,
	row,
	problem,
	showTls,
	onChange,
	onRemove,
}: {
	index: number;
	row: TailnetPortDraft;
	problem: string | undefined;
	showTls: boolean;
	onChange(row: TailnetPortDraft): void;
	onRemove(): void;
}) {
	const n = index + 1;
	return (
		<div className="flex flex-wrap items-center gap-2">
			<Input
				aria-label={`TCP port ${n}: tailnet port`}
				aria-invalid={problem !== undefined}
				inputMode="numeric"
				autoComplete="off"
				spellCheck={false}
				placeholder="5432"
				className="w-24 font-mono"
				value={row.listen}
				onChange={(event) => onChange({ ...row, listen: event.target.value })}
			/>
			<span aria-hidden className="text-ink-3">
				→
			</span>
			<span className="sr-only">forwards to service port</span>
			<Input
				aria-label={`TCP port ${n}: service port`}
				aria-invalid={problem !== undefined}
				inputMode="numeric"
				autoComplete="off"
				spellCheck={false}
				placeholder={row.listen.trim() === "" ? "same" : row.listen.trim()}
				className="w-24 font-mono"
				value={row.target}
				onChange={(event) => onChange({ ...row, target: event.target.value })}
			/>
			{showTls ? (
				<CheckLabel
					checked={row.tls}
					onCheckedChange={(tls) => onChange({ ...row, tls })}
					className="w-auto"
					labelClassName="text-[13px] text-ink-2"
				>
					TLS
				</CheckLabel>
			) : null}
			<Button
				variant="ghost"
				size="icon-sm"
				aria-label={`Remove TCP port ${n}`}
				onClick={onRemove}
			>
				<XIcon aria-hidden />
			</Button>
		</div>
	);
}

/**
 * Per-service tailnet: the service gets its own Tailscale or Headscale node
 * that serves its web port and any raw TCP ports. Everything here is staged
 * like any other setting; the node is created, changed or deleted on Deploy.
 */
export function TailnetSection({
	service,
	draft,
	errors,
	set,
}: DraftProps & { service: Service }) {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const isAdmin = useIsAdmin();
	const envSlug = useProjectContext()?.environment?.slug;
	const integration = useQuery({
		...tailscaleQuery(),
		enabled: isAdmin,
		retry: false,
	});
	const [advancedOpen, setAdvancedOpen] = useState(
		draft.tailnet?.funnel === true,
	);
	const [tagText, setTagText] = useState("");

	const tailnet = draft.tailnet?.enabled ? draft.tailnet : null;
	const update = (patch: Partial<TailnetDraft>) => {
		if (tailnet) set("tailnet", { ...tailnet, ...patch });
	};

	const connected = integration.data?.configured === true;
	const headscale = connected
		? integration.data?.provider === "headscale"
		: service.tailnet?.provider === "headscale";
	const providerKnown = connected || service.tailnet !== null;
	const policy = tagPolicy(integration.data);
	const hasPort = Number.parseInt(draft.port, 10) > 0;
	const defaultHostname = defaultTailnetHostname(service.slug, envSlug ?? "");
	const effectiveHostname =
		normalizeHostname(tailnet?.hostname ?? "") || defaultHostname;
	const rowProblems = tailnet ? validateTcpRows(tailnet.tcp, tailnet.web) : [];
	const notAllowed = policy
		? tagsNotAllowed(tailnet?.extraTags ?? [], policy.allowed)
		: [];
	const differences = tailnetDifferences(
		service.config.tailnet,
		service.tailnet,
	);

	const reprovision = useMutation({
		mutationFn: async () => {
			await reprovisionServiceTailnet({
				path: { serviceId: service.id },
				headers: { "Idempotency-Key": crypto.randomUUID() },
			});
		},
		onSuccess: async () => {
			await invalidateServices(queryClient);
			toast.success(
				"Re-provisioning",
				"A new device is being created; the status updates here.",
			);
		},
		onError: (error) =>
			toast.error(
				"Could not re-provision",
				tailnetErrorMessage(error) ?? errorMessage(error),
			),
	});

	const askReprovision = () =>
		confirm({
			title: "Re-provision the tailnet device?",
			body: REPROVISION_WARNING,
			cta: "Re-provision",
			danger: true,
			hold: false,
			onConfirm: async () => {
				await reprovision.mutateAsync().catch(() => undefined);
			},
		});

	const toggleFunnel = async (next: boolean) => {
		if (!next) {
			update({ funnel: false });
			return;
		}
		const confirmed = await confirm({
			title: "Publish this service on the internet with Funnel?",
			body: FUNNEL_WARNING,
			cta: "Turn on Funnel",
			danger: true,
			typeToConfirm: effectiveHostname,
		});
		if (confirmed) update({ funnel: true });
	};

	const toggleTailnet = (on: boolean) => {
		if (on) {
			set(
				"tailnet",
				draft.tailnet
					? { ...draft.tailnet, enabled: true }
					: emptyTailnetDraft(hasPort),
			);
		} else if (draft.tailnet) {
			set("tailnet", { ...draft.tailnet, enabled: false });
		}
	};

	const addTag = () => {
		const added = normalizeTags([...(tailnet?.extraTags ?? []), tagText]);
		update({ extraTags: added });
		setTagText("");
	};

	const removesDevice = !tailnet && service.tailnet !== null;
	const deviceName = service.tailnet
		? trimFqdn(service.tailnet.fqdn) || service.tailnet.hostname
		: "";

	return (
		<Section
			title="Tailnet"
			description="Give this service its own device on your Tailscale or Headscale network, so teammates and machines on the tailnet can reach it privately."
			aside={
				tailnet?.funnel ? (
					<Badge tone="warn">
						<GlobeIcon aria-hidden />
						Public via Funnel
					</Badge>
				) : null
			}
		>
			<SwitchRow
				id="set-tailnet"
				label="Reachable over your tailnet"
				description="Saving only stages this. Nothing contacts Tailscale until you Deploy."
				checked={tailnet !== null}
				disabled={draft.oneshot}
				onChange={toggleTailnet}
				cue={
					tailnet
						? "On: this service gets its own tailnet device"
						: "Off: not on a tailnet"
				}
				error={errors.tailnet}
			/>
			{tailnet || service.tailnet || differences.length > 0 ? (
				<SectionBody className="flex flex-col gap-3">
					{removesDevice ? (
						<Alert variant="warning">
							<AlertTitle>Turning this off removes the device</AlertTitle>
							<AlertDescription>
								Removes device {deviceName} from your tailnet. Re-enabling
								creates a new device with a new tailnet IP.
							</AlertDescription>
						</Alert>
					) : null}
					{tailnet && integration.isSuccess && !connected ? (
						<Alert variant="warning">
							<AlertTitle>Tailscale or Headscale is not connected</AlertTitle>
							<AlertDescription>
								Connect it in{" "}
								<SettingsLink>Team settings → Integrations</SettingsLink> before
								you deploy. You can stage this change now.
							</AlertDescription>
						</Alert>
					) : null}
					{tailnet && !integration.isSuccess && !integration.isFetching ? (
						<p className="type-secondary">
							Cannot check the integration from here (it needs an
							administrator). You can still stage this; the control plane checks
							it on Deploy.
						</p>
					) : null}
					<StatusRow
						service={service}
						onReprovision={() => void askReprovision()}
						reprovisioning={reprovision.isPending}
					/>
					{differences.length > 0 ? (
						<p className="flex items-start gap-1.5 text-ink-3 text-xs">
							<NetworkIcon aria-hidden className="mt-0.5 size-3.5 shrink-0" />
							<span>
								Staged, applies on the next Deploy: {differences.join("; ")}.
							</span>
						</p>
					) : null}
				</SectionBody>
			) : null}

			{tailnet ? (
				<>
					<TextRow
						id="set-tailnet-hostname"
						label="Hostname"
						description={
							headscale
								? "The device's name on your Headscale tailnet. Empty uses the default."
								: "Becomes <hostname>.<tailnet>.ts.net. Hostnames appear in public Certificate Transparency logs when HTTPS is on."
						}
						mono
						value={tailnet.hostname}
						error={errors.tailnetHostname}
						placeholder={
							envSlug ? defaultHostname : `${defaultHostname}-<environment>`
						}
						onChange={(hostname) => update({ hostname })}
					/>

					<SwitchRow
						id="set-tailnet-web"
						label="Web"
						description={
							<>
								{!providerKnown
									? "HTTPS on 443 with Tailscale, HTTP on 80 with Headscale"
									: headscale
										? "HTTP on 80"
										: "HTTPS on 443"}{" "}
								→ the service's port {hasPort ? draft.port.trim() : "(none)"}.
								{headscale
									? " Headscale does not provide HTTPS certificates."
									: ""}
								{!hasPort
									? " Set a port in Networking first: the web exposure needs an HTTP port."
									: ""}
							</>
						}
						checked={tailnet.web}
						disabled={!hasPort && !tailnet.web}
						onChange={(web) => update(web ? { web } : { web, funnel: false })}
						cue={tailnet.web ? "On: web exposure" : "Off: no web exposure"}
						error={errors.tailnetWeb}
					/>

					<SettingRow
						anchor="tailnet-tcp"
						label="TCP ports"
						description="Raw TCP over WireGuard, for databases and game servers: the tailnet port, then the service port it forwards to."
					>
						{tailnet.tcp.map((row, index) => (
							<TcpRow
								// biome-ignore lint/suspicious/noArrayIndexKey: rows are positional inputs without identity
								key={index}
								index={index}
								row={row}
								problem={
									row.listen.trim() === "" && row.target.trim() === ""
										? undefined
										: rowProblems[index]
								}
								showTls={!headscale}
								onChange={(next) =>
									update({
										tcp: tailnet.tcp.map((existing, i) =>
											i === index ? next : existing,
										),
									})
								}
								onRemove={() =>
									update({ tcp: tailnet.tcp.filter((_, i) => i !== index) })
								}
							/>
						))}
						<div className="flex flex-wrap items-center gap-1.5">
							<Button
								variant="secondary"
								size="xs"
								disabled={tailnet.tcp.length >= MAX_TCP_FORWARDS}
								onClick={() =>
									update({
										tcp: [
											...tailnet.tcp,
											{ listen: "", target: "", tls: false },
										],
									})
								}
							>
								<PlusIcon aria-hidden />
								Add port
							</Button>
							{TCP_PRESETS.map((preset) => {
								const listed = tailnet.tcp.some(
									(row) => row.listen.trim() === String(preset.port),
								);
								return (
									<Button
										key={preset.port}
										variant="secondary"
										size="xs"
										disabled={listed}
										title={listed ? "Already listed" : undefined}
										onClick={() =>
											update({ tcp: withTcpPreset(tailnet.tcp, preset.port) })
										}
									>
										Expose {preset.label} {preset.port}
									</Button>
								);
							})}
						</div>
						{headscale ? null : (
							<p className="text-ink-3 text-xs">
								TLS terminates with the device's certificate; it needs HTTPS
								certificates enabled on your tailnet.
							</p>
						)}
						<FieldError>{errors.tailnetTcp}</FieldError>
					</SettingRow>

					<SettingRow
						label="Extra tags"
						description={
							policy && policy.defaults.length > 0
								? `Every device already carries ${policy.defaults.join(", ")}.`
								: "Added to the integration's default tags."
						}
					>
						<div className="flex flex-wrap items-center gap-1.5">
							{policy
								? policy.allowed
										.filter((tag) => !policy.defaults.includes(tag))
										.map((tag) => (
											<Toggle
												key={tag}
												variant="outline"
												size="sm"
												className="font-mono"
												pressed={tailnet.extraTags.includes(tag)}
												onPressedChange={(pressed) =>
													update({
														extraTags: pressed
															? normalizeTags([...tailnet.extraTags, tag])
															: tailnet.extraTags.filter((t) => t !== tag),
													})
												}
											>
												{tailnet.extraTags.includes(tag) ? (
													<CheckIcon aria-hidden />
												) : null}
												{tag}
											</Toggle>
										))
								: null}
							{tailnet.extraTags
								.filter((tag) => !policy?.allowed.includes(tag))
								.map((tag) => (
									<Badge
										key={tag}
										variant="outline"
										className="gap-1 pr-1 font-mono"
									>
										{tag}
										<button
											type="button"
											aria-label={`Remove ${tag}`}
											className="rounded-sm outline-none hover:text-ink-1 focus-visible:focus-ring"
											onClick={() =>
												update({
													extraTags: tailnet.extraTags.filter((t) => t !== tag),
												})
											}
										>
											<XIcon aria-hidden />
										</button>
									</Badge>
								))}
						</div>
						{policy ? (
							policy.allowed.every((tag) => policy.defaults.includes(tag)) ? (
								<p className="text-ink-3 text-xs">
									The integration allows no extra tags. Add some under Allowed
									tags in{" "}
									<SettingsLink>Team settings → Integrations</SettingsLink>.
								</p>
							) : null
						) : (
							<div className="flex items-center gap-2">
								<Input
									aria-label="Add an extra tag"
									autoComplete="off"
									spellCheck={false}
									placeholder="tag:name"
									className="max-w-[200px] font-mono"
									value={tagText}
									onChange={(event) => setTagText(event.target.value)}
									onKeyDown={(event) => {
										if (event.key === "Enter" && tagText.trim() !== "") {
											event.preventDefault();
											addTag();
										}
									}}
								/>
								<Button
									variant="secondary"
									size="xs"
									disabled={tagText.trim() === ""}
									onClick={addTag}
								>
									<PlusIcon aria-hidden />
									Add tag
								</Button>
							</div>
						)}
						{notAllowed.length > 0 ? (
							<Alert variant="warning">
								<AlertDescription>
									{notAllowed.join(", ")}{" "}
									{notAllowed.length === 1 ? "is" : "are"} not allowed by the
									integration. Remove {notAllowed.length === 1 ? "it" : "them"}{" "}
									or allow {notAllowed.length === 1 ? "it" : "them"} in{" "}
									<SettingsLink>Team settings → Integrations</SettingsLink>.
								</AlertDescription>
							</Alert>
						) : null}
						<FieldError>{errors.tailnetTags}</FieldError>
					</SettingRow>

					<SwitchRow
						id="set-tailnet-only"
						label="Tailnet only"
						description="Public domains are paused while the service is on the tailnet."
						checked={tailnet.hidePublic}
						onChange={(hidePublic) => update({ hidePublic })}
						cue={
							tailnet.hidePublic
								? "On: public domains are not routed"
								: "Off: public domains keep working"
						}
					/>

					<SectionBody>
						<details
							open={advancedOpen}
							onToggle={(event) => setAdvancedOpen(event.currentTarget.open)}
						>
							<summary className="cursor-pointer select-none rounded-sm text-ink-2 text-sm outline-none hover:text-ink-1 focus-visible:focus-ring">
								Advanced
							</summary>
							<div className="pt-2">
								<SwitchRow
									id="set-tailnet-funnel"
									label={
										<span className="flex flex-wrap items-center gap-2">
											Funnel
											{tailnet.funnel ? (
												<Badge tone="warn">
													<GlobeIcon aria-hidden />
													Public via Funnel
												</Badge>
											) : null}
										</span>
									}
									description={
										headscale
											? "Funnel is not available with Headscale: it has no way to publish a service on the internet."
											: !tailnet.web
												? "Funnel publishes the web exposure. Turn the web exposure on first."
												: "Publishes the web exposure on the public internet through Tailscale's relays."
									}
									checked={tailnet.funnel}
									disabled={headscale || (!tailnet.web && !tailnet.funnel)}
									onChange={(next) => void toggleFunnel(next)}
									cue={
										tailnet.funnel
											? "On: anyone on the internet can reach it"
											: "Off: only your tailnet"
									}
									error={errors.tailnetFunnel}
								/>
							</div>
						</details>
					</SectionBody>
				</>
			) : null}
		</Section>
	);
}
