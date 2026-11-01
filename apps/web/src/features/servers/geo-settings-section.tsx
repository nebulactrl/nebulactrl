import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { cn } from "cn";
import { type KeyboardEvent, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { getMaxMindCredentialsOptions } from "#/api/gen/@tanstack/react-query.gen";
import {
	deleteMaxMindCredentials,
	setMaxMindCredentials,
} from "#/api/gen/sdk.gen";
import { CredentialHelp } from "#/components/common/credential-help";
import { Button } from "#/components/ui/button";
import { FieldError } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { KvRow } from "#/components/ui/kv-row";
import { SectionLabel } from "#/components/ui/section-label";
import { Spinner } from "#/components/ui/spinner";
import { Switch } from "#/components/ui/switch";
import { toast } from "#/components/ui/toast";
import { maxmindGuide } from "#/lib/credential-links";
import { OptionSelect } from "../metrics/ui/option-select";
import { type GeoStatusTone, geoStatusView } from "./geo-status";
import {
	isOnline,
	parseRefreshDays,
	SERVER_SETTING_LIMITS,
	type ServerSettings,
	type ServerView,
} from "./model";
import { serverViewQuery, serverViewsQuery } from "./queries";

type GeoSource = ServerSettings["geoSource"];
type GeoLevel = ServerSettings["geoLevel"];

/** The geolocation fields of the server settings form, as typed. */
export interface GeoDraft {
	enabled: boolean;
	source: GeoSource;
	level: GeoLevel;
	refreshDays: string;
}

export function geoDraftOf(server: ServerView): GeoDraft {
	const s = server.settings;
	return {
		enabled: s.geoEnabled,
		source: s.geoSource,
		level: s.geoLevel,
		refreshDays: String(s.geoRefreshDays),
	};
}

/** The PATCH fields of a draft; an invalid day count falls back to the minimum (the form blocks saving it first). */
export function geoDraftBody(draft: GeoDraft) {
	return {
		geoEnabled: draft.enabled,
		geoSource: draft.source,
		geoLevel: draft.level,
		geoRefreshDays:
			parseRefreshDays(draft.refreshDays) ??
			SERVER_SETTING_LIMITS.geoRefreshDays.min,
	};
}

/**
 * Why the draft cannot be saved yet, or null. Reads the team's MaxMind
 * account only while `active`, so a page that does not show these fields
 * costs no request.
 */
export function useGeoProblem(draft: GeoDraft, active: boolean): string | null {
	const credentials = useQuery({
		...getMaxMindCredentialsOptions(),
		enabled: active,
	});
	if (!active) return null;
	if (parseRefreshDays(draft.refreshDays) === null) return "Use 1–90 days.";
	if (
		draft.enabled &&
		draft.source === "maxmind" &&
		credentials.data?.configured !== true
	) {
		return "Add your MaxMind account ID and licence key first.";
	}
	return null;
}

const SOURCES = [
	{ value: "dbip", label: "DB-IP Lite (no key)" },
	{ value: "maxmind", label: "MaxMind GeoLite2 (key)" },
] as const;

const LEVELS = [
	{ value: "country", label: "Country (~8 MB)" },
	{ value: "city", label: "City (~130 MB)" },
] as const;

const TONE_TEXT: Record<GeoStatusTone, string> = {
	ok: "text-ok",
	wait: "text-ink-2",
	bad: "text-bad",
	muted: "text-ink-3",
};

/**
 * The GeoIP database on the host as its agent reports it: whether it has
 * downloaded, which release, its size and when it was last refreshed. It
 * describes the saved setting, not the draft.
 */
function DatabaseStatus({ server }: { server: ServerView }) {
	const view = geoStatusView({
		enabled: server.settings.geoEnabled,
		online: isOnline(server),
		status: server.geoStatus,
	});
	return (
		<div id="geo-status" className="flex w-full flex-col gap-1.5">
			<span className={cn("font-medium text-sm", TONE_TEXT[view.tone])}>
				{view.headline}
			</span>
			{view.detail ? (
				<span className="text-ink-3 text-xs leading-[1.4]">{view.detail}</span>
			) : null}
			{view.rows.length > 0 ? (
				<dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1 text-xs">
					{view.rows.map((row) => (
						<div key={row.label} className="contents">
							<dt className="text-ink-3">{row.label}</dt>
							<dd
								className="min-w-0 font-mono text-ink-1 tabular-nums"
								{...(row.title ? { title: row.title } : {})}
							>
								{row.value}
							</dd>
						</div>
					))}
				</dl>
			) : null}
		</div>
	);
}

/**
 * Request-origin geolocation rows of one server: on/off, the database source,
 * country or city level, how often the agent refreshes it, and the state of
 * the database on the host. The MaxMind account is the team's, shared by
 * every server that uses it. The rows are controlled; the settings form
 * owns the draft and the save.
 */
export function GeoFields({
	server,
	draft,
	onChange,
	trafficOff,
}: {
	server: ServerView;
	draft: GeoDraft;
	onChange: (next: GeoDraft) => void;
	/** Traffic analytics is off in the draft, so geolocation has nothing to count. */
	trafficOff: boolean;
}) {
	const queryClient = useQueryClient();
	const credentials = useQuery(getMaxMindCredentialsOptions());
	const keyConfigured = credentials.data?.configured === true;
	const refreshDays = parseRefreshDays(draft.refreshDays);

	const refresh = () =>
		Promise.all([
			queryClient.invalidateQueries({
				queryKey: serverViewQuery(server.id).queryKey,
			}),
			queryClient.invalidateQueries({ queryKey: serverViewsQuery().queryKey }),
			queryClient.invalidateQueries({
				queryKey: getMaxMindCredentialsOptions().queryKey,
			}),
		]);

	return (
		<>
			<SectionLabel className="pt-4 pb-1">
				Where requests come from
			</SectionLabel>
			<KvRow
				label="Geolocate requests"
				description={
					trafficOff
						? "Needs traffic analytics, which is off for this server."
						: "The agent looks each client IP up in a local database and ships only per-minute counts per country (and city). IPs are never stored or sent."
				}
				htmlFor="srv-geo"
			>
				<Switch
					id="srv-geo"
					checked={draft.enabled}
					disabled={trafficOff}
					onCheckedChange={(enabled) => onChange({ ...draft, enabled })}
				/>
			</KvRow>
			<KvRow
				label="Database"
				description="DB-IP Lite needs no key (CC BY 4.0, credited next to the data). MaxMind GeoLite2 uses your team's account."
			>
				<OptionSelect<GeoSource>
					aria-label="Geolocation database"
					value={draft.source}
					options={SOURCES}
					onChange={(source) => onChange({ ...draft, source })}
				/>
			</KvRow>
			{draft.source === "maxmind" ? (
				<MaxMindAccount
					serverCount={credentials.data?.serversUsing ?? 0}
					configured={keyConfigured}
					accountId={credentials.data?.accountId ?? ""}
					onChanged={refresh}
				/>
			) : null}
			<KvRow
				label="Detail"
				description="Country needs a ~8 MB database on the server; city adds a map pin per city but needs ~130 MB of disk. Changing it downloads a new database and deletes the old one."
			>
				<OptionSelect<GeoLevel>
					aria-label="Geolocation detail"
					value={draft.level}
					options={LEVELS}
					onChange={(level) => onChange({ ...draft, level })}
				/>
			</KvRow>
			<KvRow
				label="Refresh the database every (days)"
				description="1–90. The agent re-downloads only when a newer release exists."
				htmlFor="srv-geo-days"
			>
				<div className="flex w-full max-w-[160px] flex-col gap-1.5">
					<Input
						id="srv-geo-days"
						type="number"
						scale="lg"
						min={SERVER_SETTING_LIMITS.geoRefreshDays.min}
						max={SERVER_SETTING_LIMITS.geoRefreshDays.max}
						value={draft.refreshDays}
						onChange={(e) =>
							onChange({ ...draft, refreshDays: e.target.value })
						}
						aria-invalid={refreshDays === null}
					/>
					{refreshDays === null ? (
						<FieldError>Use 1–90 days.</FieldError>
					) : null}
				</div>
			</KvRow>
			<KvRow
				label="Database status"
				description="What the agent reports for the saved setting."
				className="border-b-0"
			>
				<DatabaseStatus server={server} />
			</KvRow>
		</>
	);
}

/** The team's MaxMind account: ID and write-only licence key, shared by its servers. */
function MaxMindAccount({
	configured,
	accountId,
	serverCount,
	onChanged,
}: {
	configured: boolean;
	accountId: string;
	serverCount: number;
	onChanged: () => Promise<unknown>;
}) {
	const [editing, setEditing] = useState(!configured);
	const [account, setAccount] = useState(accountId);
	const [key, setKey] = useState("");
	const accountOk = /^[0-9]{1,20}$/.test(account.trim());
	const keyOk = /^[A-Za-z0-9_-]{6,100}$/.test(key.trim());

	const store = useMutation({
		mutationFn: () =>
			setMaxMindCredentials({
				body: { accountId: account.trim(), licenseKey: key.trim() },
			}),
		onSuccess: async () => {
			setKey("");
			setEditing(false);
			toast.success(
				"MaxMind account saved",
				"The key is stored encrypted and sent to agents that use it.",
			);
			await onChanged();
		},
	});
	/** Enter in either field saves the key, not the settings form around it. */
	const saveOnEnter = (e: KeyboardEvent<HTMLInputElement>) => {
		if (e.key !== "Enter") return;
		e.preventDefault();
		if (accountOk && keyOk && !store.isPending) store.mutate();
	};
	const remove = useMutation({
		mutationFn: () => deleteMaxMindCredentials(),
		onSuccess: async () => {
			setEditing(true);
			setAccount("");
			toast.success("MaxMind account removed");
			await onChanged();
		},
	});

	return (
		<KvRow
			label="MaxMind account"
			description="Shared by every server of this team that uses GeoLite2. The licence key is write-only."
		>
			{editing ? (
				<div className="flex w-full max-w-[360px] flex-col gap-2">
					<CredentialHelp
						guide={maxmindGuide()}
						fieldIds={["maxmind-account", "maxmind-key"]}
					/>
					<Input
						id="maxmind-account"
						aria-label="MaxMind account ID"
						scale="lg"
						inputMode="numeric"
						className="font-mono"
						placeholder="Account ID (numbers)"
						value={account}
						onChange={(e) => setAccount(e.target.value)}
						onKeyDown={saveOnEnter}
						autoComplete="off"
					/>
					<Input
						id="maxmind-key"
						aria-label="MaxMind licence key"
						type="password"
						scale="lg"
						className="font-mono"
						placeholder="Licence key"
						value={key}
						onChange={(e) => setKey(e.target.value)}
						onKeyDown={saveOnEnter}
						autoComplete="off"
					/>
					{store.isError ? (
						<FieldError>{errorMessage(store.error)}</FieldError>
					) : null}
					<span className="flex items-center gap-2">
						<Button
							type="button"
							size="sm"
							variant="primary"
							disabled={!accountOk || !keyOk || store.isPending}
							onClick={() => store.mutate()}
						>
							{store.isPending ? <Spinner /> : null}
							Save key
						</Button>
						{configured ? (
							<Button
								type="button"
								size="sm"
								variant="ghost"
								onClick={() => {
									setEditing(false);
									setKey("");
								}}
							>
								Cancel
							</Button>
						) : null}
					</span>
				</div>
			) : (
				<div className="flex w-full max-w-[360px] flex-col gap-2">
					<span className="font-mono text-ink-1 text-xs">
						Account {accountId} · licence key stored
					</span>
					{remove.isError ? (
						<FieldError>{errorMessage(remove.error)}</FieldError>
					) : null}
					<span className="flex items-center gap-2">
						<Button
							type="button"
							size="sm"
							variant="secondary"
							onClick={() => {
								setAccount(accountId);
								setEditing(true);
							}}
						>
							Replace key
						</Button>
						<Button
							type="button"
							size="sm"
							variant="ghost"
							disabled={remove.isPending || serverCount > 0}
							title={
								serverCount > 0
									? `${serverCount} server(s) use MaxMind; switch them to DB-IP first`
									: undefined
							}
							onClick={() => remove.mutate()}
						>
							Remove
						</Button>
					</span>
				</div>
			)}
		</KvRow>
	);
}
