import {
	type UseQueryResult,
	useMutation,
	useQuery,
	useQueryClient,
} from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useId, useMemo, useState } from "react";
import { errorCode, errorMessage } from "#/api/client-config";
import {
	getServiceAutoBanOptions,
	getServiceAutoBanQueryKey,
	getServiceFirewallOptions,
	listServiceBansOptions,
} from "#/api/gen/@tanstack/react-query.gen";
import { setServiceAutoBan, unbanServiceAddress } from "#/api/gen/sdk.gen";
import type {
	AutoBanView,
	BanView,
	ListServiceBansError,
	PageBanView,
} from "#/api/gen/types.gen";
import type { Service } from "#/api/models";
import { useConfirm } from "#/components/common/confirm-dialog";
import { Relative } from "#/components/common/relative";
import { SaveBarStatus } from "#/components/common/save-bar-status";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
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
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { Switch } from "#/components/ui/switch";
import {
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "#/components/ui/table";
import { toast } from "#/components/ui/toast";
import { StringList } from "#/features/service-settings/docker-rows";
import {
	Section,
	SectionBody,
	SettingRow,
} from "#/features/service-settings/fields";
import { absoluteTime } from "#/lib/format";
import {
	AUTOBAN_DURATION_SECONDS,
	AUTOBAN_THRESHOLD,
	AUTOBAN_UNITS,
	AUTOBAN_WINDOW_SECONDS,
	type AutoBanDraft,
	type AutoBanField,
	type AutoBanFieldErrors,
	type AutoBanRefusal,
	type AutoBanSpan,
	type AutoBanUnit,
	autobanBody,
	autobanDraftOf,
	autobanRefusal,
	autobanUnchanged,
	banEndReason,
	banRuleNames,
	hasAutoBanErrors,
	listPhrase,
	MAX_AUTOBAN_ALLOW,
	ruleNameIndex,
	spanPhrase,
	validateAutoBan,
} from "./model";

type BansQuery = UseQueryResult<PageBanView, ListServiceBansError>;

/** How long a queued unban keeps the list refreshing before it gives up. */
const UNBAN_WATCH_MS = 30_000;
const UNBAN_POLL_MS = 2_000;

/**
 * Settings → Firewall → Auto-ban. An address that keeps tripping the firewall
 * is banned for a while. Like the rules it is operational: Save changes the
 * edge guard at once, nothing is staged, and deploys keep it.
 */
export function AutoBanSection({ service }: { service: Service }) {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const path = { serviceId: service.id };
	const query = useQuery(getServiceAutoBanOptions({ path }));
	// The rule names a ban's counts are shown under: the same query the rules
	// above use, so it costs no request.
	const firewall = useQuery(getServiceFirewallOptions({ path }));
	const names = useMemo(() => ruleNameIndex(firewall.data), [firewall.data]);

	// Unbans the agent has not confirmed yet, by ban id: the list refreshes
	// until they are gone from it.
	const [unbanning, setUnbanning] = useState<ReadonlySet<string>>(new Set());
	const active = useQuery({
		...listServiceBansOptions({ path, query: { state: "active", limit: 200 } }),
		refetchInterval: unbanning.size > 0 ? UNBAN_POLL_MS : false,
	});
	const ended = useQuery(
		listServiceBansOptions({ path, query: { state: "ended", limit: 20 } }),
	);
	const activeItems = active.data?.items;
	useEffect(() => {
		if (!activeItems) return;
		setUnbanning((current) => {
			const live = new Set(activeItems.map((ban) => ban.id));
			const next = new Set([...current].filter((id) => live.has(id)));
			return next.size === current.size ? current : next;
		});
	}, [activeItems]);

	// `null` means untouched: the card follows the server as it refetches.
	const [edited, setEdited] = useState<AutoBanDraft | null>(null);
	const [refusal, setRefusal] = useState<AutoBanRefusal | null>(null);
	const live = query.data?.policy;
	const draft = useMemo(
		() => edited ?? (live ? autobanDraftOf(live) : null),
		[edited, live],
	);
	const errors = useMemo(
		() => (draft ? validateAutoBan(draft) : null),
		[draft],
	);
	const invalid = errors !== null && hasAutoBanErrors(errors);
	const dirty =
		edited !== null && live !== undefined && !autobanUnchanged(edited, live);

	const refreshBans = () =>
		queryClient.invalidateQueries({
			queryKey: [{ _id: "listServiceBans", path }],
		});

	const save = useMutation({
		mutationFn: async (next: AutoBanDraft) => {
			const { data } = await setServiceAutoBan({
				path,
				body: autobanBody(next),
				headers: { "Idempotency-Key": crypto.randomUUID() },
			});
			return data;
		},
		onSuccess: (view) => {
			queryClient.setQueryData(getServiceAutoBanQueryKey({ path }), view);
			setEdited(null);
			setRefusal(null);
			void refreshBans();
			toast.success(
				view.policy.enabled ? "Auto-ban saved" : "Auto-ban is off",
				view.policy.enabled
					? `${service.name} bans an address after ${view.policy.threshold} blocked requests within ${spanPhrase(view.policy.windowSeconds)}.`
					: `${service.name} no longer bans addresses; bans in force were lifted.`,
			);
		},
		onError: (error) =>
			setRefusal(autobanRefusal(errorCode(error), errorMessage(error))),
	});

	const unban = useMutation({
		mutationFn: async (ban: BanView) => {
			await unbanServiceAddress({
				path: { ...path, banId: ban.id },
				headers: { "Idempotency-Key": crypto.randomUUID() },
			});
			return ban.id;
		},
		onSuccess: (id) => {
			setUnbanning((current) => new Set(current).add(id));
			window.setTimeout(
				() =>
					setUnbanning((current) => {
						const next = new Set(current);
						next.delete(id);
						return next;
					}),
				UNBAN_WATCH_MS,
			);
			void queryClient.invalidateQueries({
				queryKey: getServiceAutoBanQueryKey({ path }),
			});
			void refreshBans();
			toast.success(
				"Unban sent",
				"The address is free again once the agent confirms.",
			);
		},
		// A ban that already ended (409) is gone from the list on refetch.
		onError: () => void refreshBans(),
	});

	const change = (next: AutoBanDraft) => {
		save.reset();
		setRefusal(null);
		setEdited(next);
	};

	if (query.isPending) {
		return (
			<Section title="Auto-ban">
				<SectionBody className="flex flex-col gap-2">
					<Skeleton className="h-5 w-2/3 rounded-ctl" />
					<Skeleton className="h-14 rounded-ctl" />
				</SectionBody>
			</Section>
		);
	}
	if (query.isError) {
		return (
			<Section title="Auto-ban">
				<SectionBody>
					<Alert variant="destructive">
						<AlertTitle>Auto-ban could not be read</AlertTitle>
						<AlertDescription>{errorMessage(query.error)}</AlertDescription>
					</Alert>
				</SectionBody>
			</Section>
		);
	}
	const view = query.data;
	if (!draft || !errors) return null;
	// Off stays switchable when it is on, so a service can always be released.
	const blocked = !draft.enabled && (!view.analytics || !view.supported);
	const fieldProblem = (field: AutoBanField) =>
		refusal && refusal.field === field ? refusal.message : undefined;

	return (
		<>
			<Section
				title="Auto-ban"
				description="Ban an address that keeps tripping the firewall: for a while every request from it answers 403 before any rule is looked at."
				aside={
					view.activeBans > 0
						? `${view.activeBans} ${view.activeBans === 1 ? "address" : "addresses"} banned`
						: undefined
				}
			>
				<Warnings view={view} service={service} />
				<AutoBanFields
					draft={draft}
					errors={errors}
					blocked={blocked}
					enabledProblem={fieldProblem("enabled")}
					refusalFor={fieldProblem}
					onChange={change}
				/>
				<SectionBody className="flex flex-col gap-1.5 text-ink-3 text-xs leading-[1.5]">
					<p className="m-0">
						<span className="text-ink-2">What counts.</span> A request counts
						when the standard protection or one of your block rules with a path,
						method, header, query or user-agent condition answered it 403. Rules
						that only look at the address or the country never count, and
						neither do allow rules or the addresses never banned (private
						networks, loopback and the allow list).
					</p>
					<p className="m-0">
						<span className="text-ink-2">How it is enforced.</span> Checked by
						the edge guard next to the proxy: adds a local hop to every request
						of this service. The ban follows the log by a second or two, so the
						request that crosses the threshold can still reach the rules alone.
						The first time an agent that supports auto-ban runs, the server's
						proxy container is recreated once.
					</p>
				</SectionBody>
				{dirty || save.isError ? (
					<SaveBar
						state={save.isError ? "failed" : invalid ? "invalid" : "dirty"}
						message={
							save.isError
								? (refusal?.message ?? errorMessage(save.error))
								: invalid
									? "Fix the highlighted settings."
									: "Unsaved changes, applied without a deploy."
						}
						pending={save.isPending}
						canSave={dirty && !invalid}
						onDiscard={() => {
							save.reset();
							setRefusal(null);
							setEdited(null);
						}}
						onSave={() => save.mutate(draft)}
					/>
				) : null}
			</Section>
			<Section
				title="Active bans"
				description="Addresses banned now. An IPv6 visitor is banned as its whole /64 network."
			>
				<SectionBody>
					<ActiveBans
						query={active}
						names={names}
						unbanning={unbanning}
						onUnban={(ban) =>
							void confirm({
								title: `Unban ${ban.address ?? ban.prefix}?`,
								body: "The address is let back in at once. It can be banned again if it keeps tripping the firewall; put it on the allow list to keep it out of auto-ban for good.",
								cta: "Unban",
								hold: false,
								onConfirm: async () => {
									await unban.mutateAsync(ban);
								},
							})
						}
					/>
				</SectionBody>
			</Section>
			<Section
				title="Recently ended"
				description="Past bans are kept for 90 days. The address is forgotten when a ban ends; only its network prefix stays."
			>
				<SectionBody>
					<EndedBans query={ended} />
				</SectionBody>
			</Section>
		</>
	);
}

/** Conditions that keep auto-ban from working as set, and how to fix them. */
function Warnings({ view, service }: { view: AutoBanView; service: Service }) {
	const warnings = view.warnings ?? [];
	const hosts = view.hosts ?? [];
	if (view.supported && view.analytics && warnings.length === 0) return null;
	return (
		<SectionBody className="flex flex-col gap-2">
			{view.supported ? null : (
				<Alert variant="warning">
					<AlertTitle>The agent on {view.server} is too old</AlertTitle>
					<AlertDescription>
						Auto-ban needs a newer agent on this service's server. Update the
						agent, then switch it on.
					</AlertDescription>
				</Alert>
			)}
			{view.analytics ? null : (
				<Alert variant="warning">
					<AlertTitle>Traffic analytics is off</AlertTitle>
					<AlertDescription>
						Auto-ban decides from the proxy's access log, so it needs traffic
						analytics on
						{view.server ? (
							<>
								{" "}
								<span className="font-mono text-ink-2">{view.server}</span>
							</>
						) : null}
						.{" "}
						{service.serverId ? (
							<Link
								to="/servers/$serverId/settings/$section"
								params={{ serverId: service.serverId, section: "monitoring" }}
								className="text-accent-ink underline"
							>
								Turn it on in the server's settings
							</Link>
						) : (
							"Deploy the service to a server first."
						)}
					</AlertDescription>
				</Alert>
			)}
			{warnings.includes("cloudflare_proxied_not_locked") ? (
				<Alert variant="warning">
					<AlertTitle>Some visitors cannot be banned</AlertTitle>
					<AlertDescription>
						{hosts.length > 0 ? (
							<>
								<span className="font-mono text-ink-2">
									{listPhrase(hosts, "and")}
								</span>{" "}
								{hosts.length === 1 ? "is" : "are"} proxied by Cloudflare
							</>
						) : (
							"A domain of this service is proxied by Cloudflare"
						)}{" "}
						without being locked to it, so the proxy only sees Cloudflare's
						addresses and cannot tell visitors apart: they are never banned.
						Lock the origin to Cloudflare, or use a tunnel.
					</AlertDescription>
				</Alert>
			) : null}
		</SectionBody>
	);
}

function AutoBanFields({
	draft,
	errors,
	blocked,
	enabledProblem,
	refusalFor,
	onChange,
}: {
	draft: AutoBanDraft;
	errors: AutoBanFieldErrors;
	blocked: boolean;
	enabledProblem: string | undefined;
	refusalFor(field: AutoBanField): string | undefined;
	onChange(next: AutoBanDraft): void;
}) {
	const id = useId();
	const thresholdError = errors.threshold ?? refusalFor("threshold");
	const windowError = errors.window ?? refusalFor("window");
	const durationError = errors.duration ?? refusalFor("duration");
	const allowError = errors.allow ?? refusalFor("allow");
	return (
		<>
			<SettingRow
				label="Auto-ban"
				htmlFor={`${id}-enabled`}
				description="Off by default. A change applies at once, without a deployment."
			>
				<Switch
					id={`${id}-enabled`}
					checked={draft.enabled}
					disabled={blocked}
					onCheckedChange={(enabled) => onChange({ ...draft, enabled })}
				/>
				<FieldError>{enabledProblem}</FieldError>
			</SettingRow>
			<SettingRow
				label="Blocked requests to ban"
				htmlFor={`${id}-threshold`}
				description={`${AUTOBAN_THRESHOLD.min} to ${AUTOBAN_THRESHOLD.max} counted requests from one address.`}
			>
				<Input
					id={`${id}-threshold`}
					inputMode="numeric"
					autoComplete="off"
					className="max-w-[120px] font-mono"
					aria-invalid={thresholdError !== undefined}
					value={draft.threshold}
					onChange={(event) =>
						onChange({ ...draft, threshold: event.target.value })
					}
				/>
				<FieldError>{thresholdError}</FieldError>
			</SettingRow>
			<SettingRow
				label="Counting window"
				htmlFor={`${id}-window`}
				description={`How far back those requests count: ${spanPhrase(AUTOBAN_WINDOW_SECONDS.min)} to ${spanPhrase(AUTOBAN_WINDOW_SECONDS.max)}.`}
			>
				<SpanInput
					id={`${id}-window`}
					label="Counting window"
					span={draft.window}
					invalid={windowError !== undefined}
					onChange={(window) => onChange({ ...draft, window })}
				/>
				<FieldError>{windowError}</FieldError>
			</SettingRow>
			<SettingRow
				label="Ban length"
				htmlFor={`${id}-duration`}
				description={`How long a ban lasts: ${spanPhrase(AUTOBAN_DURATION_SECONDS.min)} to ${spanPhrase(AUTOBAN_DURATION_SECONDS.max)}.`}
			>
				<SpanInput
					id={`${id}-duration`}
					label="Ban length"
					span={draft.duration}
					invalid={durationError !== undefined}
					onChange={(duration) => onChange({ ...draft, duration })}
				/>
				<FieldError>{durationError}</FieldError>
			</SettingRow>
			<SettingRow
				label="Never banned"
				description={`Addresses or CIDR networks that are never counted or banned, up to ${MAX_AUTOBAN_ALLOW}. Adding one lifts a ban it covers.`}
				className="items-start"
			>
				<StringList
					label="Never banned"
					rowLabel="address"
					values={draft.allow}
					issues={{
						listWide: allowError,
						row: (index) => errors.allowRows[index],
					}}
					max={MAX_AUTOBAN_ALLOW}
					placeholder="203.0.113.0/24"
					addLabel="Add an address"
					onChange={(allow) => onChange({ ...draft, allow })}
				/>
			</SettingRow>
		</>
	);
}

/** An amount and its unit. */
function SpanInput({
	id,
	label,
	span,
	invalid,
	onChange,
}: {
	id: string;
	label: string;
	span: AutoBanSpan;
	invalid: boolean;
	onChange(next: AutoBanSpan): void;
}) {
	return (
		<div className="flex items-center gap-2">
			<Input
				id={id}
				inputMode="decimal"
				autoComplete="off"
				className="max-w-[100px] font-mono"
				aria-invalid={invalid}
				value={span.amount}
				onChange={(event) => onChange({ ...span, amount: event.target.value })}
			/>
			<Select
				value={span.unit}
				items={AUTOBAN_UNITS.map((unit) => ({ value: unit, label: unit }))}
				onValueChange={(unit) =>
					onChange({ ...span, unit: (unit ?? span.unit) as AutoBanUnit })
				}
			>
				<SelectTrigger aria-label={`${label} unit`} className="w-[112px]">
					<SelectValue />
				</SelectTrigger>
				<SelectContent alignItemWithTrigger={false}>
					{AUTOBAN_UNITS.map((unit) => (
						<SelectItem key={unit} value={unit}>
							{unit}
						</SelectItem>
					))}
				</SelectContent>
			</Select>
		</div>
	);
}

function ActiveBans({
	query,
	names,
	unbanning,
	onUnban,
}: {
	query: BansQuery;
	names: ReadonlyMap<string, string>;
	unbanning: ReadonlySet<string>;
	onUnban(ban: BanView): void;
}) {
	if (query.isPending) return <Skeleton className="h-14 rounded-ctl" />;
	if (query.isError) {
		return (
			<Alert variant="destructive">
				<AlertTitle>Bans could not be read</AlertTitle>
				<AlertDescription>{errorMessage(query.error)}</AlertDescription>
			</Alert>
		);
	}
	const bans = query.data.items ?? [];
	if (bans.length === 0) {
		return (
			<p className="m-0 text-ink-3 text-xs leading-[1.45]">
				No address is banned right now.
			</p>
		);
	}
	return (
		<div className="overflow-hidden border-line border-y">
			<Table stack>
				<TableHeader>
					<TableRow>
						<TableHead>Address</TableHead>
						<TableHead>Banned since</TableHead>
						<TableHead>Expires</TableHead>
						<TableHead>Strikes</TableHead>
						<TableHead>Rules</TableHead>
						<TableHead className="w-px" />
					</TableRow>
				</TableHeader>
				<TableBody>
					{bans.map((ban) => {
						const pending = unbanning.has(ban.id);
						const address = ban.address ?? ban.prefix;
						return (
							<TableRow key={ban.id}>
								<TableCell pin="start" className="font-mono text-ink-1">
									{address}
								</TableCell>
								<TableCell label="Banned since" className="text-ink-3 text-xs">
									<span title={absoluteTime(ban.startedAt)}>
										<Relative iso={ban.startedAt} />
									</span>
								</TableCell>
								<TableCell label="Expires" className="text-ink-3 text-xs">
									<Relative iso={ban.expiresAt} />
								</TableCell>
								<TableCell label="Strikes" className="text-ink-2 text-xs">
									{ban.strikes}
								</TableCell>
								<TableCell label="Rules" className="text-ink-3 text-xs">
									{banRuleNames(ban.rules, names).join(", ") || "—"}
								</TableCell>
								<TableCell pin="end">
									<span className="flex justify-end">
										<Button
											type="button"
											variant="ghost"
											size="sm"
											disabled={pending}
											aria-label={`Unban ${address}`}
											onClick={() => onUnban(ban)}
										>
											{pending ? <Spinner /> : null}
											{pending ? "Unbanning" : "Unban"}
										</Button>
									</span>
								</TableCell>
							</TableRow>
						);
					})}
				</TableBody>
			</Table>
		</div>
	);
}

function EndedBans({ query }: { query: BansQuery }) {
	if (query.isPending) return <Skeleton className="h-14 rounded-ctl" />;
	if (query.isError) {
		return (
			<Alert variant="destructive">
				<AlertTitle>Past bans could not be read</AlertTitle>
				<AlertDescription>{errorMessage(query.error)}</AlertDescription>
			</Alert>
		);
	}
	const bans = query.data.items ?? [];
	if (bans.length === 0) {
		return (
			<p className="m-0 text-ink-3 text-xs leading-[1.45]">
				No ban has ended yet.
			</p>
		);
	}
	return (
		<div className="overflow-hidden border-line border-y">
			<Table stack>
				<TableHeader>
					<TableRow>
						<TableHead>Network</TableHead>
						<TableHead>Reason</TableHead>
						<TableHead>Ended</TableHead>
					</TableRow>
				</TableHeader>
				<TableBody>
					{bans.map((ban) => (
						<TableRow key={ban.id}>
							<TableCell pin="start" className="font-mono text-ink-1">
								{ban.prefix}
							</TableCell>
							<TableCell label="Reason" className="text-ink-3 text-xs">
								{banEndReason(ban.endReason)}
							</TableCell>
							<TableCell label="Ended" className="text-ink-3 text-xs">
								<span
									title={ban.endedAt ? absoluteTime(ban.endedAt) : undefined}
								>
									<Relative iso={ban.endedAt ?? ban.expiresAt} />
								</span>
							</TableCell>
						</TableRow>
					))}
				</TableBody>
			</Table>
		</div>
	);
}

/** Discard and Save for auto-ban: it acts at once, so it has its own bar. */
function SaveBar({
	state,
	message,
	pending,
	canSave,
	onDiscard,
	onSave,
}: {
	state: "dirty" | "invalid" | "failed";
	message: string;
	pending: boolean;
	canSave: boolean;
	onDiscard(): void;
	onSave(): void;
}) {
	return (
		<div
			className="surface-float sticky bottom-0 z-10 -mx-1 flex flex-wrap items-center gap-3 rounded-ctl px-3 py-2.5"
			aria-live="polite"
		>
			<SaveBarStatus state={state}>{message}</SaveBarStatus>
			<Button
				type="button"
				variant="secondary"
				disabled={pending}
				onClick={onDiscard}
			>
				Discard
			</Button>
			<Button
				type="button"
				variant="primary"
				disabled={!canSave || pending}
				onClick={onSave}
			>
				{pending ? <Spinner /> : null}
				Save auto-ban
			</Button>
		</div>
	);
}
