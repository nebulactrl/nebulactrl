import { useId } from "react";
import { CodeBlock } from "#/components/common/code-block";
import { useConfirm } from "#/components/common/confirm-dialog";
import { CopyButton } from "#/components/common/copy-button";
import { Relative } from "#/components/common/relative";
import { DocsLink } from "#/components/docs/docs-link";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { StatusIcon } from "#/components/ui/status-icon";
import { Switch } from "#/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "#/components/ui/tabs";
import {
	Section,
	SectionBody,
	SettingRow,
} from "#/features/service-settings/fields";
import { buildSnippets, maskedUrl } from "./snippets";
import { useDeployHook } from "./use-deploy-hook";

const OUTCOME_TEXT: Record<string, string> = {
	deploying: "started a deployment",
	queued: "queued behind a running deployment",
	ignored: "ignored (not this image or tag)",
};

/** A labelled, selectable line on the well surface, with the design's Copy chip. */
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
 * Settings → Deploy hook. Like Maintenance it is operational: turning it on,
 * off or rotating it acts at once and stages nothing. The URL is shown once,
 * when its secret is created.
 */
export function DeployHookSection({
	serviceId,
	serviceName,
}: {
	serviceId: string;
	serviceName: string;
}) {
	const switchId = useId();
	const confirm = useConfirm();
	const { hook, revealed, dismiss, setEnabled, rotate } =
		useDeployHook(serviceId);
	const view = hook.data;
	const enabled = view?.enabled === true;
	const busy = setEnabled.isPending || rotate.isPending;

	const toggle = async (next: boolean) => {
		if (next) {
			setEnabled.mutate(true);
			return;
		}
		const ok = await confirm({
			title: `Turn off the deploy hook of ${serviceName}?`,
			body: "CI pipelines and registry webhooks that call it get 404 from now on. Turning it on again creates a new URL; the old one never works again.",
			cta: "Turn off",
			danger: true,
			hold: false,
		});
		if (ok) setEnabled.mutate(false);
	};

	const askRotate = async () => {
		const ok = await confirm({
			title: "Rotate the deploy hook URL?",
			body: "The current URL stops working immediately. Update the CI secret or registry webhook that uses it with the new URL, which is shown once.",
			cta: "Rotate URL",
			danger: true,
			hold: false,
		});
		if (ok) rotate.mutate();
	};

	const aside = (
		<span className="inline-flex items-center gap-1.5">
			<StatusIcon kind={enabled ? "live" : "stopped"} label="" />
			<span>{enabled ? "On" : "Off"}</span>
		</span>
	);

	return (
		<Section
			title="Deploy hook"
			description={
				<>
					A URL that deploys this service like the Deploy button, for CI
					pipelines and container registries. Off until you turn it on. It
					applies at once and stages nothing.{" "}
					<DocsLink to="operations/deploy-hooks">Learn more</DocsLink>
				</>
			}
			aside={view ? aside : undefined}
		>
			{view?.isProduction ? (
				<SectionBody className="py-1.5">
					<Alert variant="warning">
						<AlertTitle>Production environment</AlertTitle>
						<AlertDescription>
							Anyone who has this URL can deploy to production without signing
							in. Keep it in your CI's secret store, never in a repository or a
							chat, and rotate it if it leaks. Calls are limited to{" "}
							{view.rateLimit}.
						</AlertDescription>
					</Alert>
				</SectionBody>
			) : null}

			<SettingRow
				label="Enable deploy hook"
				description="Needs the deployments:write permission."
				htmlFor={switchId}
			>
				{hook.isPending ? (
					<Skeleton className="h-[22px] w-[38px] rounded-full" />
				) : hook.isError ? (
					<span className="text-bad text-xs">
						Could not load the deploy hook. Reload to try again.
					</span>
				) : (
					<div className="flex items-center gap-3">
						<Switch
							id={switchId}
							checked={enabled}
							disabled={busy}
							onCheckedChange={(next) => void toggle(next)}
						/>
						{busy ? <Spinner /> : null}
					</div>
				)}
			</SettingRow>

			{view && enabled ? (
				<>
					<SectionBody
						anchor="deploy-hook-url"
						className="flex flex-col gap-3 py-2"
					>
						{revealed ? (
							<>
								<div className="flex items-center gap-2">
									<Badge tone="warn" shape="capsule">
										Shown once
									</Badge>
									<span className="text-ink-3 text-xs">
										It cannot be read again. Rotate it to get a new one.
									</span>
								</div>
								<CopyLine label="Deploy hook URL" value={revealed} />
								<div>
									<Button variant="secondary" onClick={dismiss}>
										I have saved it
									</Button>
								</div>
							</>
						) : (
							<>
								<div className="flex flex-col gap-1">
									<span className="type-label">Deploy hook URL</span>
									<code className="surface-well break-all rounded-ctl px-3.5 py-2.5 font-mono text-ink-3 text-xs leading-[1.7]">
										{maskedUrl(view.urlPattern)}
									</code>
									<span className="type-secondary">
										The secret was shown when the hook was turned on or last
										rotated. Lost it? Rotate to get a new URL.
									</span>
								</div>
								<div>
									<Button
										variant="secondary"
										disabled={busy}
										onClick={() => void askRotate()}
									>
										{rotate.isPending ? (
											<Spinner data-icon="inline-start" />
										) : null}
										Rotate URL…
									</Button>
								</div>
							</>
						)}
					</SectionBody>

					<SettingRow
						label="Last call"
						description="What the most recent call to the URL did."
					>
						<span className="text-ink-2 text-sm">
							{view.lastCalledAt ? (
								<>
									<Relative iso={view.lastCalledAt} />
									{" · "}
									{OUTCOME_TEXT[view.lastOutcome] ?? "received"}
								</>
							) : (
								"Never called"
							)}
							{view.queued
								? " · a call is waiting for the running deployment"
								: ""}
						</span>
					</SettingRow>

					<SectionBody
						anchor="deploy-hook-snippets"
						className="flex flex-col gap-2 py-2"
					>
						<span className="type-label">Set it up</span>
						<Snippets url={revealed ?? view.urlPattern} image={view.image} />
					</SectionBody>
				</>
			) : null}
		</Section>
	);
}

function Snippets({ url, image }: { url: string; image: string }) {
	const snippets = buildSnippets({ url, image });
	return (
		<Tabs defaultValue="curl">
			<TabsList variant="default">
				{snippets.map((s) => (
					<TabsTrigger key={s.id} value={s.id}>
						{s.label}
					</TabsTrigger>
				))}
			</TabsList>
			{snippets.map((s) => (
				<TabsContent
					key={s.id}
					value={s.id}
					className="flex flex-col gap-2 pt-3"
				>
					<p className="type-secondary m-0">{s.intro}</p>
					{s.caution ? (
						<Alert variant="warning">
							<AlertDescription>{s.caution}</AlertDescription>
						</Alert>
					) : null}
					<CodeBlock copyValue={s.body}>
						<pre className="m-0 whitespace-pre-wrap break-all font-mono text-xs leading-[1.7]">
							{s.body}
						</pre>
					</CodeBlock>
				</TabsContent>
			))}
		</Tabs>
	);
}
