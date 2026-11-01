import { ClipboardCopyIcon, EyeIcon, LockIcon } from "lucide-react";
import type { ExportView } from "#/api/gen/types.gen";
import { CopyButton } from "#/components/common/copy-button";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { SectionLabel } from "#/components/ui/section-label";
import { Spinner } from "#/components/ui/spinner";
import {
	Tooltip,
	TooltipContent,
	TooltipTrigger,
} from "#/components/ui/tooltip";
import { RevealedValue } from "#/features/databases/connection-string";
import {
	EXPORT_ROLE_TEXT,
	exportRole,
	orderExports,
} from "#/features/databases/replicas/model";
import { useExportReveal } from "#/features/databases/use-export-reveal";
import { useRevealHash } from "#/lib/hooks/use-reveal-hash";

/** The anchor `?tab=variables#connection` scrolls to. */
const CONNECTION_ANCHOR = "connection";

// Module level, so the hook's lookup stays stable between renders.
const findConnection = (hash: string) =>
	hash === CONNECTION_ANCHOR
		? () => document.getElementById(CONNECTION_ANCHOR)
		: undefined;

/**
 * What a managed database offers other services to reference: one dense,
 * read-only line per export, above the service's own variables. Each line has
 * the name (its description is the tooltip), a secret badge, the reference
 * with a copy button and, for a secret, the audited reveal: Show for 60 s, or
 * Copy the value. Nothing here is stored on the service, so nothing is staged.
 */
export function ConnectionVariables({
	serviceId,
	exports,
}: {
	serviceId: string;
	exports: readonly ExportView[];
}) {
	// A cold load of `#connection` scrolls here once the tab has drawn.
	useRevealHash(findConnection);
	if (exports.length === 0) return null;
	return (
		<section
			id={CONNECTION_ANCHOR}
			aria-label="Connection variables"
			className="flex flex-col gap-2 scroll-mt-4"
		>
			<SectionLabel>Connection variables</SectionLabel>
			<p className="type-secondary">
				Read-only. Reference one from another service's variables; secrets are
				shown only on request, and each reveal is audited.
			</p>
			{exports.some((entry) => exportRole(entry.key) !== null) ? (
				<p className="type-secondary">
					<span className="font-mono">DATABASE_WRITE_*</span> is always the
					current primary. <span className="font-mono">DATABASE_READ_*</span> is
					a read replica the service can reach, the primary when there is none.
				</p>
			) : null}
			<ul
				aria-label="Connection variables"
				className="surface-raised divide-y divide-line-faint rounded-card"
			>
				{orderExports(exports).map((entry) => (
					<ConnectionRow key={entry.key} serviceId={serviceId} entry={entry} />
				))}
			</ul>
		</section>
	);
}

function ConnectionRow({
	serviceId,
	entry,
}: {
	serviceId: string;
	entry: ExportView;
}) {
	const { shown, mode, busy, run, hide } = useExportReveal(serviceId);
	const failure = `Could not reveal ${entry.key}`;
	const role = exportRole(entry.key);
	return (
		<li className="flex flex-col px-3">
			<div className="grid min-h-9 grid-cols-[minmax(0,auto)_minmax(0,1fr)_auto] items-center gap-x-2.5">
				<span className="flex min-w-0 items-center gap-1.5">
					<NameWithTip entry={entry} />
					{entry.secret ? (
						<Badge title="Secret: its value is shown only on request">
							<LockIcon aria-hidden />
							secret
						</Badge>
					) : null}
					{entry.primary ? <Badge variant="accent">primary</Badge> : null}
					{role ? <Badge title={EXPORT_ROLE_TEXT[role]}>{role}</Badge> : null}
				</span>
				<code
					title="Reference this export from another service's variables"
					className="min-w-0 truncate font-mono text-2xs text-ink-2"
				>
					{entry.reference}
				</code>
				<span className="flex items-center gap-0.5">
					<CopyButton
						value={entry.reference}
						label={`Copy reference ${entry.key}`}
						variant="ghost"
						size="icon-sm"
					/>
					{entry.secret ? (
						<>
							<Button
								variant="ghost"
								size="icon-sm"
								aria-label={`Show ${entry.key} for 60 seconds`}
								title="Show for 60 s"
								disabled={busy || shown !== null}
								onClick={() => void run(entry.key, "show", failure)}
							>
								{mode === "show" ? <Spinner /> : <EyeIcon aria-hidden />}
							</Button>
							<Button
								variant="ghost"
								size="icon-sm"
								aria-label={`Copy the value of ${entry.key}`}
								title="Copy the value"
								disabled={busy}
								onClick={() => void run(entry.key, "copy", failure)}
							>
								{mode === "copy" ? (
									<Spinner />
								) : (
									<ClipboardCopyIcon aria-hidden />
								)}
							</Button>
						</>
					) : null}
				</span>
			</div>
			{shown ? <RevealedValue reveal={shown} onHide={hide} /> : null}
		</li>
	);
}

/** The name; its description, when it has one, is the tooltip. */
function NameWithTip({ entry }: { entry: ExportView }) {
	const name = (
		<span className="truncate font-medium font-mono text-[11.5px] text-ink-1">
			{entry.key}
		</span>
	);
	if (!entry.description) return name;
	return (
		<Tooltip>
			<TooltipTrigger
				render={
					<button
						type="button"
						className="min-w-0 cursor-default rounded-sm text-left outline-none focus-visible:focus-ring"
					/>
				}
			>
				{name}
			</TooltipTrigger>
			<TooltipContent>{entry.description}</TooltipContent>
		</Tooltip>
	);
}
