import { useMutation, useQueryClient } from "@tanstack/react-query";
import { cn } from "cn";
import {
	type FormEvent,
	type ReactNode,
	useEffect,
	useRef,
	useState,
} from "react";
import { errorMessage } from "#/api/client-config";
import { updateServer } from "#/api/gen/sdk.gen";
import type { UpdateServerInputBody } from "#/api/gen/types.gen";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { Card, CardContent, CardFooter } from "#/components/ui/card";
import { FieldError } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { KvRow } from "#/components/ui/kv-row";
import { SectionLabel } from "#/components/ui/section-label";
import { Spinner } from "#/components/ui/spinner";
import { Switch } from "#/components/ui/switch";
import { toast } from "#/components/ui/toast";
import {
	applyRole,
	BUILD_CONCURRENCY,
	concurrencyError,
	parseConcurrency,
	type ServerRole,
	serverRole,
} from "#/features/build-placement/model";
import { RoleControl } from "#/features/build-placement/role-control";
import { baseDomainHint } from "./base-domain-hint";
import {
	type GeoDraft,
	GeoFields,
	geoDraftBody,
	geoDraftOf,
	useGeoProblem,
} from "./geo-settings-section";
import { SERVER_SETTING_LIMITS, type ServerView } from "./model";
import { serverViewQuery, serverViewsQuery } from "./queries";

interface Draft {
	name: string;
	publicIp: string;
	baseDomain: string;
	acmeEmail: string;
	metricsRetentionDays: string;
	diskAlertThresholdPercent: string;
	trafficAnalyticsEnabled: boolean;
	trafficHourlyRetentionDays: string;
	trafficDailyRetentionDays: string;
	geo: GeoDraft;
	acceptWorkloads: boolean;
	buildEnabled: boolean;
	buildMaxConcurrent: string;
	buildAllowEmulation: boolean;
	buildAllowPreviews: boolean;
}

function draftOf(server: ServerView): Draft {
	const s = server.settings;
	return {
		name: server.name,
		publicIp: server.publicIp ?? "",
		baseDomain: server.baseDomain ?? "",
		acmeEmail: server.acmeEmail ?? "",
		acceptWorkloads: server.acceptWorkloads,
		buildEnabled: server.buildEnabled,
		buildMaxConcurrent: String(server.buildMaxConcurrent),
		buildAllowEmulation: server.buildAllowEmulation,
		buildAllowPreviews: server.buildAllowPreviews,
		metricsRetentionDays: String(s.metricsRetentionDays),
		diskAlertThresholdPercent: String(s.diskAlertThresholdPercent),
		trafficAnalyticsEnabled: s.trafficAnalyticsEnabled,
		trafficHourlyRetentionDays: String(s.trafficHourlyRetentionDays),
		trafficDailyRetentionDays: String(s.trafficDailyRetentionDays),
		geo: geoDraftOf(server),
	};
}

type NumericKey =
	| "metricsRetentionDays"
	| "diskAlertThresholdPercent"
	| "trafficHourlyRetentionDays"
	| "trafficDailyRetentionDays";

function parseBounded(text: string, key: NumericKey): number | null {
	const value = Number(text);
	const { min, max } = SERVER_SETTING_LIMITS[key];
	return Number.isInteger(value) && value >= min && value <= max ? value : null;
}

export function validateDraft(
	draft: Draft,
): Partial<Record<keyof Draft, string>> {
	const errors: Partial<Record<keyof Draft, string>> = {};
	if (draft.name.trim() === "") errors.name = "A server needs a name.";
	if (draft.buildEnabled) {
		const concurrency = concurrencyError(draft.buildMaxConcurrent);
		if (concurrency) errors.buildMaxConcurrent = concurrency;
	}
	const numeric: [NumericKey, string][] = [
		["metricsRetentionDays", "Keep 1–90 days."],
		["diskAlertThresholdPercent", "Use 50–99 percent."],
		["trafficHourlyRetentionDays", "Keep 2–90 days."],
		["trafficDailyRetentionDays", "Keep 7–365 days."],
	];
	for (const [key, message] of numeric) {
		if (parseBounded(draft[key], key) === null) errors[key] = message;
	}
	return errors;
}

/** The draft's fields that travel in a PATCH as themselves; `geo` is spread out by `geoDraftBody`. */
type BodyKey = Exclude<keyof Draft, "geo">;

/** The pages the one draft is cut into: each saves only its own fields. */
export type SettingsPart = "general" | "scheduling" | "monitoring";

const PART_KEYS: Record<SettingsPart, readonly (keyof Draft)[]> = {
	general: ["name", "publicIp", "baseDomain", "acmeEmail"],
	scheduling: [
		"acceptWorkloads",
		"buildEnabled",
		"buildMaxConcurrent",
		"buildAllowEmulation",
		"buildAllowPreviews",
	],
	monitoring: [
		"metricsRetentionDays",
		"diskAlertThresholdPercent",
		"trafficAnalyticsEnabled",
		"trafficHourlyRetentionDays",
		"trafficDailyRetentionDays",
		"geo",
	],
};

const SAVED: Record<SettingsPart, string> = {
	general: "Name, address and certificate contact apply from now on.",
	scheduling: "Placement and builds follow the new role from now on.",
	monitoring:
		"Retention, alerts and geolocation apply from now on. A changed database source or level downloads a new database.",
};

/** The part's own fields of a draft, in a fixed order. */
function pickPart(draft: Draft, part: SettingsPart): Partial<Draft> {
	return Object.fromEntries(PART_KEYS[part].map((key) => [key, draft[key]]));
}

/**
 * One page of a server's settings (general, scheduling or monitoring): the
 * fields of that part, saved with PATCH on their own.
 */
export function ServerSettingsForm({
	server,
	part,
}: {
	server: ServerView;
	part: SettingsPart;
}) {
	const queryClient = useQueryClient();
	const [draft, setDraft] = useState<Draft>(() => draftOf(server));
	const [touched, setTouched] = useState(false);
	const baseline = JSON.stringify(pickPart(draftOf(server), part));

	// A refetch that changed the server (someone else saved) resets the form
	// only while it is clean, so typing is never overwritten.
	const lastBaseline = useRef(baseline);
	useEffect(() => {
		if (lastBaseline.current === baseline) return;
		const previous = lastBaseline.current;
		lastBaseline.current = baseline;
		setDraft((current) =>
			JSON.stringify(pickPart(current, part)) === previous
				? draftOf(server)
				: current,
		);
	}, [baseline, server, part]);

	const dirty = JSON.stringify(pickPart(draft, part)) !== baseline;
	const errors = Object.fromEntries(
		Object.entries(validateDraft(draft)).filter(([key]) =>
			PART_KEYS[part].includes(key as keyof Draft),
		),
	) as Partial<Record<keyof Draft, string>>;
	const geoProblem = useGeoProblem(draft.geo, part === "monitoring");
	const valid = Object.keys(errors).length === 0 && geoProblem === null;

	const save = useMutation({
		mutationFn: async () => {
			// `valid` gates the submit, so every number parses and is in range.
			const full: UpdateServerInputBody = {
				name: draft.name.trim(),
				publicIp: draft.publicIp.trim(),
				baseDomain: draft.baseDomain.trim(),
				acmeEmail: draft.acmeEmail.trim(),
				acceptWorkloads: draft.acceptWorkloads,
				buildEnabled: draft.buildEnabled,
				buildMaxConcurrent:
					parseConcurrency(draft.buildMaxConcurrent) ??
					BUILD_CONCURRENCY.default,
				buildAllowEmulation: draft.buildAllowEmulation,
				buildAllowPreviews: draft.buildAllowPreviews,
				metricsRetentionDays: Number(draft.metricsRetentionDays),
				diskAlertThresholdPercent: Number(draft.diskAlertThresholdPercent),
				trafficAnalyticsEnabled: draft.trafficAnalyticsEnabled,
				trafficHourlyRetentionDays: Number(draft.trafficHourlyRetentionDays),
				trafficDailyRetentionDays: Number(draft.trafficDailyRetentionDays),
			};
			// Only this page's fields travel: the others stay as they are.
			const body: UpdateServerInputBody = {
				...Object.fromEntries(
					PART_KEYS[part]
						.filter((key): key is BodyKey => key !== "geo")
						.map((key) => [key, full[key]]),
				),
				...(part === "monitoring" ? geoDraftBody(draft.geo) : {}),
			};
			const { data } = await updateServer({
				path: { serverId: server.id },
				body,
			});
			return data;
		},
		onSuccess: async () => {
			setTouched(false);
			toast.success("Server saved", SAVED[part]);
			await Promise.all([
				queryClient.invalidateQueries({
					queryKey: serverViewQuery(server.id).queryKey,
				}),
				queryClient.invalidateQueries({
					queryKey: serverViewsQuery().queryKey,
				}),
			]);
		},
	});

	const set = <K extends keyof Draft>(key: K, value: Draft[K]) =>
		setDraft((current) => ({ ...current, [key]: value }));

	const role = serverRole(draft);
	const setRole = (next: ServerRole) =>
		setDraft((current) => ({ ...current, ...applyRole(current, next) }));

	const submit = (event: FormEvent) => {
		event.preventDefault();
		setTouched(true);
		if (!valid || !dirty || save.isPending) return;
		save.mutate();
	};

	const shown = (key: keyof Draft) => (touched ? errors[key] : undefined);

	return (
		<form onSubmit={submit}>
			<Card>
				<CardContent className="py-1 [&>[data-slot=kv-row]:last-child]:border-b-0">
					{part === "general" ? (
						<>
							<SettingRow
								id="srv-name"
								label="Name"
								description="How this server appears across the console."
								error={shown("name")}
							>
								<Input
									id="srv-name"
									scale="lg"
									value={draft.name}
									onChange={(e) => set("name", e.target.value)}
									aria-invalid={shown("name") !== undefined}
								/>
							</SettingRow>
							<SettingRow
								id="srv-ip"
								label="Public IP"
								description="Domain DNS checks compare A records to this."
							>
								<Input
									id="srv-ip"
									scale="lg"
									className="font-mono"
									placeholder="203.0.113.10"
									value={draft.publicIp}
									onChange={(e) => set("publicIp", e.target.value)}
								/>
							</SettingRow>
							<SettingRow
								id="srv-base"
								label="Base domain"
								description={baseDomainHint(draft.publicIp)}
							>
								<Input
									id="srv-base"
									scale="lg"
									className="font-mono"
									placeholder="apps.example.com"
									value={draft.baseDomain}
									onChange={(e) => set("baseDomain", e.target.value)}
								/>
							</SettingRow>
							<SettingRow
								id="srv-acme"
								label="ACME email"
								description="Contact for Let's Encrypt certificates."
							>
								<Input
									id="srv-acme"
									type="email"
									scale="lg"
									placeholder="ops@example.com"
									value={draft.acmeEmail}
									onChange={(e) => set("acmeEmail", e.target.value)}
								/>
							</SettingRow>
						</>
					) : null}

					{part === "scheduling" ? (
						<>
							<KvRow
								label="Role"
								description="What this server is for: running apps, running builds, or both."
							>
								<div className="flex w-full flex-col gap-3">
									<RoleControl value={role} onChange={setRole} />
									{role !== "deployments" && server.controlPlane ? (
										<Alert variant="warning">
											<AlertTitle>This is the control-plane host</AlertTitle>
											<AlertDescription>
												Builds here compete with NebulaCtrl itself for CPU,
												memory and disk. Prefer a separate build server.
											</AlertDescription>
										</Alert>
									) : null}
									{role === "builds" && server.serviceCount > 0 ? (
										<Alert variant="info">
											<AlertDescription>
												{server.serviceCount === 1
													? "1 service already runs here and keeps running."
													: `${server.serviceCount} services already run here and keep running.`}{" "}
												No new apps are placed here.
											</AlertDescription>
										</Alert>
									) : null}
								</div>
							</KvRow>
							{role === "deployments" ? (
								<KvRow
									label="Accept workloads"
									description={
										server.controlPlane
											? "This is the control-plane host. On, you can deploy apps here next to NebulaCtrl; off keeps it for NebulaCtrl alone. Services that already run here keep running either way."
											: "On, services can be placed on this server. Off keeps what already runs here and refuses new services and moves."
									}
									htmlFor="srv-accept"
								>
									<Switch
										id="srv-accept"
										checked={draft.acceptWorkloads}
										onCheckedChange={(checked) =>
											set("acceptWorkloads", checked)
										}
									/>
								</KvRow>
							) : (
								<>
									<SettingRow
										id="srv-build-max"
										label="Builds at once"
										description="More builds wait in a queue, and the deployment says so."
										error={shown("buildMaxConcurrent")}
										narrow
									>
										<Input
											id="srv-build-max"
											type="number"
											scale="lg"
											min={BUILD_CONCURRENCY.min}
											max={BUILD_CONCURRENCY.max}
											value={draft.buildMaxConcurrent}
											onChange={(e) =>
												set("buildMaxConcurrent", e.target.value)
											}
											aria-invalid={shown("buildMaxConcurrent") !== undefined}
										/>
									</SettingRow>
									<KvRow
										label="Cross-architecture builds"
										description="Build for a different CPU architecture than this server's, under emulation. Slow; off by default, so such a build falls back to the app's server."
										htmlFor="srv-build-emulation"
									>
										<Switch
											id="srv-build-emulation"
											checked={draft.buildAllowEmulation}
											onCheckedChange={(checked) =>
												set("buildAllowEmulation", checked)
											}
										/>
									</KvRow>
									<KvRow
										label="Pull-request previews"
										description="Previews run the Dockerfile of whoever opened the pull request, including forks. Allow them only on a server with nothing else worth protecting. Without this, previews build on their own server."
										htmlFor="srv-build-previews"
									>
										<Switch
											id="srv-build-previews"
											checked={draft.buildAllowPreviews}
											onCheckedChange={(checked) =>
												set("buildAllowPreviews", checked)
											}
										/>
									</KvRow>
								</>
							)}
						</>
					) : null}

					{part === "monitoring" ? (
						<>
							<SectionLabel className="pt-4 pb-1">
								Resource metrics
							</SectionLabel>
							<SettingRow
								id="srv-metrics-days"
								label="Resource history kept (days)"
								description="The CPU, memory and disk samples behind the charts. 1–90 days. Longer chart ranges show only what was kept."
								error={shown("metricsRetentionDays")}
								narrow
							>
								<Input
									id="srv-metrics-days"
									type="number"
									scale="lg"
									min={SERVER_SETTING_LIMITS.metricsRetentionDays.min}
									max={SERVER_SETTING_LIMITS.metricsRetentionDays.max}
									value={draft.metricsRetentionDays}
									onChange={(e) => set("metricsRetentionDays", e.target.value)}
									aria-invalid={shown("metricsRetentionDays") !== undefined}
								/>
							</SettingRow>
							<SettingRow
								id="srv-disk"
								label="Disk alert threshold (%)"
								description="The server card and page warn once the root disk reaches this."
								error={shown("diskAlertThresholdPercent")}
								narrow
							>
								<Input
									id="srv-disk"
									type="number"
									scale="lg"
									min={SERVER_SETTING_LIMITS.diskAlertThresholdPercent.min}
									max={SERVER_SETTING_LIMITS.diskAlertThresholdPercent.max}
									value={draft.diskAlertThresholdPercent}
									onChange={(e) =>
										set("diskAlertThresholdPercent", e.target.value)
									}
									aria-invalid={
										shown("diskAlertThresholdPercent") !== undefined
									}
								/>
							</SettingRow>

							<SectionLabel className="pt-5 pb-1">
								Traffic analytics
							</SectionLabel>
							<KvRow
								label="Collect traffic analytics"
								description="Aggregates only (counts, status classes, latency buckets, top paths). Raw access logs and visitor IPs are never stored. Turning it on or off needs no proxy restart."
								htmlFor="srv-traffic"
							>
								<Switch
									id="srv-traffic"
									checked={draft.trafficAnalyticsEnabled}
									onCheckedChange={(checked) =>
										set("trafficAnalyticsEnabled", checked)
									}
								/>
							</KvRow>
							<SettingRow
								id="srv-hourly"
								label="Hourly traffic kept (days)"
								description={`Hour-by-hour request counts, for the detailed traffic charts. 2–90 days.${draft.trafficAnalyticsEnabled ? "" : " Nothing is collected while analytics is off."}`}
								error={shown("trafficHourlyRetentionDays")}
								narrow
							>
								<Input
									id="srv-hourly"
									type="number"
									scale="lg"
									disabled={!draft.trafficAnalyticsEnabled}
									min={SERVER_SETTING_LIMITS.trafficHourlyRetentionDays.min}
									max={SERVER_SETTING_LIMITS.trafficHourlyRetentionDays.max}
									value={draft.trafficHourlyRetentionDays}
									onChange={(e) =>
										set("trafficHourlyRetentionDays", e.target.value)
									}
									aria-invalid={
										shown("trafficHourlyRetentionDays") !== undefined
									}
								/>
							</SettingRow>
							<SettingRow
								id="srv-daily"
								label="Daily traffic kept (days)"
								description={`Day-by-day summaries, for the long traffic ranges. 7–365 days.${draft.trafficAnalyticsEnabled ? "" : " Nothing is collected while analytics is off."}`}
								error={shown("trafficDailyRetentionDays")}
								narrow
							>
								<Input
									id="srv-daily"
									type="number"
									scale="lg"
									disabled={!draft.trafficAnalyticsEnabled}
									min={SERVER_SETTING_LIMITS.trafficDailyRetentionDays.min}
									max={SERVER_SETTING_LIMITS.trafficDailyRetentionDays.max}
									value={draft.trafficDailyRetentionDays}
									onChange={(e) =>
										set("trafficDailyRetentionDays", e.target.value)
									}
									aria-invalid={
										shown("trafficDailyRetentionDays") !== undefined
									}
								/>
							</SettingRow>

							<GeoFields
								server={server}
								draft={draft.geo}
								onChange={(geo) => set("geo", geo)}
								trafficOff={!draft.trafficAnalyticsEnabled}
							/>
						</>
					) : null}
				</CardContent>
				<CardFooter className="justify-between">
					<span className="min-w-0 text-xs">
						{save.isError ? (
							<FieldError>{errorMessage(save.error)}</FieldError>
						) : geoProblem ? (
							<FieldError>{geoProblem}</FieldError>
						) : dirty ? (
							<span className="text-ink-3">You have unsaved changes.</span>
						) : (
							<span className="text-ink-3">No unsaved changes.</span>
						)}
					</span>
					<span className="flex items-center gap-2">
						{dirty ? (
							<Button
								type="button"
								variant="ghost"
								onClick={() => setDraft(draftOf(server))}
							>
								Discard
							</Button>
						) : null}
						<Button
							type="submit"
							variant="primary"
							disabled={!dirty || save.isPending}
						>
							{save.isPending ? <Spinner /> : null}
							Save changes
						</Button>
					</span>
				</CardFooter>
			</Card>
		</form>
	);
}

/** One labelled setting: the label and its why on the left, the field (and its error) on the right. */
function SettingRow({
	id,
	label,
	description,
	error,
	narrow = false,
	last = false,
	children,
}: {
	id: string;
	label: string;
	description: string;
	error?: string | undefined;
	/** A number field: a short input rather than a full-width one. */
	narrow?: boolean;
	/** The last row of the card: no rule under it. */
	last?: boolean;
	children: ReactNode;
}) {
	return (
		<KvRow
			label={label}
			description={description}
			htmlFor={id}
			className={cn(last && "border-b-0")}
		>
			<div
				className={cn(
					"flex w-full flex-col gap-1.5",
					narrow ? "max-w-[160px]" : "max-w-[360px]",
				)}
			>
				{children}
				{error ? <FieldError>{error}</FieldError> : null}
			</div>
		</KvRow>
	);
}
