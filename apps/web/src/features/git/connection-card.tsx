import { ExternalLinkIcon, PlayIcon } from "lucide-react";
import type { ConnectionView } from "#/api/gen/types.gen";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { Spinner } from "#/components/ui/spinner";
import { CONNECTION_STATUS, providerInfo } from "./model";
import { ProviderIcon } from "./provider-icon";

function hostOf(url: string): string {
	try {
		return new URL(url).host;
	} catch {
		return url;
	}
}

function detailLine(c: ConnectionView): string {
	const parts = [providerInfo(c.provider).label, hostOf(c.baseUrl)];
	if (c.accountLogin) parts.push(`as ${c.accountLogin}`);
	return parts.join(" · ");
}

/** One git connection: what it is, whether it works, and what you can do with it. */
export function ConnectionCard({
	connection: c,
	testing,
	onTest,
	onDeliveries,
	onAuthorize,
	onRefresh,
	onWebhook,
	onToken,
	onRemove,
}: {
	connection: ConnectionView;
	testing: boolean;
	onTest(): void;
	onDeliveries(): void;
	onAuthorize(): void;
	onRefresh(): void;
	onWebhook(): void;
	onToken(): void;
	onRemove(): void;
}) {
	const status = CONNECTION_STATUS[c.status] ?? {
		label: c.status,
		tone: "warn" as const,
	};
	const githubInstall = c.github?.installUrl ?? "";
	const pendingGithub = c.authType === "github_app" && c.status === "pending";
	return (
		<article
			data-testid="git-connection"
			aria-label={c.name}
			className="surface-raised flex min-w-0 flex-col gap-3 rounded-card p-4"
		>
			<header className="flex items-start gap-3">
				<ProviderIcon provider={c.provider} size={36} />
				<div className="flex min-w-0 flex-1 flex-col gap-0.5">
					<h4 className="m-0 truncate font-medium text-ink-1 text-sm">
						{c.name}
					</h4>
					<span className="truncate font-mono text-2xs text-ink-3">
						{detailLine(c)}
					</span>
				</div>
				<Badge tone={status.tone} lamp shape="capsule">
					{status.label}
				</Badge>
			</header>
			{c.statusMessage ? (
				<p
					className={`m-0 break-words text-xs leading-relaxed ${c.status === "error" ? "text-destructive-text" : "text-ink-2"}`}
				>
					{c.statusMessage}
				</p>
			) : null}
			<p className="m-0 text-ink-3 text-xs">
				{c.serviceCount === 0
					? "No service uses it yet."
					: `${c.serviceCount} service${c.serviceCount === 1 ? "" : "s"} deploy from it.`}
				{c.supportsPreviews ? "" : " No previews on this provider."}
			</p>
			<footer className="-mb-1 flex flex-wrap items-center gap-1 border-line-faint border-t pt-2.5">
				{pendingGithub && githubInstall ? (
					<>
						<Button
							variant="primary"
							size="sm"
							onClick={() => window.location.assign(githubInstall)}
						>
							<ExternalLinkIcon />
							Install the App
						</Button>
						<Button variant="secondary" size="sm" onClick={onRefresh}>
							I installed it
						</Button>
					</>
				) : null}
				{c.authType === "oauth" && c.status !== "active" ? (
					<Button variant="primary" size="sm" onClick={onAuthorize}>
						Authorize
					</Button>
				) : null}
				{c.status !== "pending" ? (
					<Button
						variant="secondary"
						size="sm"
						onClick={onTest}
						disabled={testing}
					>
						{testing ? <Spinner /> : <PlayIcon />}
						{testing ? "Testing…" : "Test"}
					</Button>
				) : null}
				<Button variant="ghost" size="sm" onClick={onDeliveries}>
					Deliveries
				</Button>
				{c.manualWebhook ? (
					<Button variant="ghost" size="sm" onClick={onWebhook}>
						Webhook setup
					</Button>
				) : null}
				{c.authType === "token" || c.manualWebhook ? (
					<Button variant="ghost" size="sm" onClick={onToken}>
						Replace token
					</Button>
				) : null}
				{c.authType === "oauth" && c.status === "active" ? (
					<Button variant="ghost" size="sm" onClick={onAuthorize}>
						Reauthorize
					</Button>
				) : null}
				{c.authType === "github_app" &&
				c.status === "active" &&
				githubInstall ? (
					<Button
						variant="ghost"
						size="sm"
						onClick={() => window.location.assign(githubInstall)}
					>
						Add repositories
					</Button>
				) : null}
				<Button
					variant="ghost"
					size="sm"
					className="ml-auto text-destructive-text"
					onClick={onRemove}
				>
					Remove
				</Button>
			</footer>
		</article>
	);
}
