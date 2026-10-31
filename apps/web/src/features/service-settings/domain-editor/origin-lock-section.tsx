import { ShieldCheckIcon } from "lucide-react";
import type { OriginLockView } from "#/api/gen/types.gen";
import { useConfirm } from "#/components/common/confirm-dialog";
import { CredentialHelp } from "#/components/common/credential-help";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { Badge } from "#/components/ui/badge";
import { StatusIcon } from "#/components/ui/status-icon";
import { Switch } from "#/components/ui/switch";
import {
	lockHeadline,
	lockLayers,
	lockReasonText,
	lockToggleEnabled,
	needsAopPermission,
	ORIGIN_LOCK_OPT_OUT_WARNING,
	showOptOutWarning,
} from "#/features/cloudflare/model";
import { cloudflareAopGuide } from "#/lib/credential-links";

/**
 * "Origin locked to Cloudflare": a proxied domain only accepts traffic that
 * comes through Cloudflare. The three layers (IP lock, Authenticated Origin
 * Pulls, real client IP) with their state, the reason when the domain is not
 * locked, and the opt-out switch. Rendered inside the Cloudflare DNS panel.
 */
export function OriginLockSection({
	hostname,
	domainId,
	lock,
	proxied,
	busy,
	onOptIn,
}: {
	hostname: string;
	domainId: string;
	lock: OriginLockView;
	proxied: boolean;
	busy: boolean;
	onOptIn: (optIn: boolean) => void;
}) {
	const confirm = useConfirm();
	const headline = lockHeadline(lock);
	const reason = lockReasonText(lock.reason);
	const switchId = `cf-lock-${domainId}`;

	const toggle = (next: boolean) => {
		if (next || !proxied) {
			onOptIn(next);
			return;
		}
		void confirm({
			title: `Open ${hostname} to the internet?`,
			body: (
				<>
					<p>{ORIGIN_LOCK_OPT_OUT_WARNING}</p>
					<p>
						The server will answer every address again, not only Cloudflare.
						Cloudflare's firewall, rate limits and bot protection can then be
						skipped by connecting to the server directly. You can lock it again
						at any time.
					</p>
				</>
			),
			cta: "Turn the lock off",
			dismiss: "Keep it locked",
			danger: true,
			onConfirm: async () => {
				onOptIn(false);
			},
		});
	};

	return (
		<div
			className="flex flex-col gap-2.5 rounded-card border border-line p-3"
			data-section="origin-lock"
		>
			<div className="flex flex-wrap items-center gap-2">
				<ShieldCheckIcon aria-hidden className="size-4 text-ink-3" />
				<span className="font-medium text-[13px] text-ink-1">
					Origin locked to Cloudflare
				</span>
				<Badge>
					<StatusIcon kind={headline.kind} label="" />
					{headline.label}
				</Badge>
			</div>
			<ul className="m-0 flex list-none flex-col gap-2 p-0">
				{lockLayers(lock).map((layer) => (
					<li key={layer.id} className="flex items-start gap-2.5">
						<span className="mt-0.5 flex shrink-0 items-center gap-1.5">
							<StatusIcon kind={layer.kind} label="" />
							<span className="w-[84px] text-[12px] text-ink-1">
								{layer.label}
							</span>
						</span>
						<span className="flex flex-col gap-0.5">
							<span className="text-[13px] text-ink-1">{layer.title}</span>
							<span className="text-ink-3 text-xs leading-[1.5]">
								{layer.detail}
							</span>
						</span>
					</li>
				))}
			</ul>
			{reason ? (
				<p className="m-0 text-ink-3 text-xs leading-[1.5]">{reason}</p>
			) : null}
			{needsAopPermission(lock) ? (
				<CredentialHelp guide={cloudflareAopGuide()} fieldIds={[]} />
			) : null}
			<div className="flex flex-wrap items-center justify-between gap-3">
				<div className="flex flex-col gap-0.5">
					<label htmlFor={switchId} className="text-[13px] text-ink-1">
						Lock the origin to Cloudflare
					</label>
					<span className="text-ink-3 text-xs">
						On by default for every proxied domain. DNS-only and tunnel domains
						are never locked.
					</span>
				</div>
				<Switch
					id={switchId}
					checked={lock.optIn}
					disabled={busy || !lockToggleEnabled(lock)}
					onCheckedChange={toggle}
				/>
			</div>
			{showOptOutWarning(lock, proxied) ? (
				<Alert variant="warning">
					<AlertDescription>{ORIGIN_LOCK_OPT_OUT_WARNING}</AlertDescription>
				</Alert>
			) : null}
		</div>
	);
}
