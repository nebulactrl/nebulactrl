import { CopyButton } from "#/components/common/copy-button";
import { Badge } from "#/components/ui/badge";

function CopyLine({ label, value }: { label: string; value: string }) {
	return (
		<div className="flex flex-col gap-1">
			<span className="type-label">{label}</span>
			<div className="surface-well flex items-start gap-3 rounded-ctl py-2 pr-2.5 pl-3.5">
				<code className="min-w-0 flex-1 select-all break-all py-0.5 font-mono text-ink-1 text-xs leading-[1.7]">
					{value}
				</code>
				<CopyButton variant="chip" value={value} className="shrink-0" />
			</div>
		</div>
	);
}

/**
 * What to enter at Bitbucket to create the webhook by hand: the URL and, once,
 * the secret that signs it.
 */
export function WebhookPanel({
	url,
	secret,
}: {
	url: string;
	secret?: string | undefined;
}) {
	return (
		<div className="flex flex-col gap-3">
			{url === "" ? (
				<p className="m-0 text-bad text-xs">
					NebulaCtrl does not know its public address. Set NEBULACTRL_PUBLIC_URL
					and restart it, then come back: Bitbucket needs a URL it can reach.
				</p>
			) : (
				<CopyLine label="Webhook URL" value={url} />
			)}
			{secret ? (
				<>
					<div className="flex items-center gap-2">
						<Badge tone="warn" shape="capsule">
							Shown once
						</Badge>
						<span className="text-ink-3 text-xs">
							Rotate the secret to get a new one.
						</span>
					</div>
					<CopyLine label="Secret" value={secret} />
				</>
			) : null}
			<ol className="m-0 flex list-decimal flex-col gap-1 pl-5 text-ink-2 text-xs leading-relaxed">
				<li>
					In the repository: Repository settings → Webhooks → Add webhook.
				</li>
				<li>Paste the URL and the secret.</li>
				<li>
					Trigger:{" "}
					<span className="font-medium text-ink-1">Repository push</span>. Save.
				</li>
			</ol>
			<p className="m-0 text-ink-3 text-xs leading-relaxed">
				Bitbucket's push event lists no changed files, so every push to the
				branch deploys: watch paths cannot filter it. There are no commit
				statuses and no previews for Bitbucket.
			</p>
		</div>
	);
}
