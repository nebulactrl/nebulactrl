import { cn } from "cn";
import {
	CircleCheckIcon,
	CircleXIcon,
	LockKeyholeIcon,
	TriangleAlertIcon,
} from "lucide-react";
import type { TemplatePlanCheck } from "#/api/gen/types.gen";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Spinner } from "#/components/ui/spinner";
import { type ChecksSummary, secretsNotice } from "./lib";

const STATUS = {
	ok: { Icon: CircleCheckIcon, tone: "text-ok", word: "Passed" },
	warn: { Icon: TriangleAlertIcon, tone: "text-warn", word: "Warning" },
	fail: { Icon: CircleXIcon, tone: "text-bad", word: "Failed" },
} as const;

function CheckRow({ check }: { check: TemplatePlanCheck }) {
	const status = STATUS[check.status as keyof typeof STATUS] ?? STATUS.warn;
	return (
		<li className="flex items-start gap-2 text-[12px] text-ink-2 leading-[1.45]">
			<status.Icon
				aria-hidden
				className={cn("mt-px size-3.5 shrink-0", status.tone)}
			/>
			<span className="min-w-0 break-words">
				<span className="sr-only">{status.word}: </span>
				{check.message}
			</span>
		</li>
	);
}

/**
 * The live preflight against the chosen server: failures first, then
 * warnings, then what passed. A failed check is what keeps both install
 * buttons disabled.
 */
export function PlanChecks({
	summary,
	checking,
	waiting,
}: {
	summary: ChecksSummary | undefined;
	/** A preflight is running or about to. */
	checking: boolean;
	/** Why there is none yet: required inputs, or no server. */
	waiting: string | null;
}) {
	return (
		<section aria-label="Preflight" className="flex flex-col gap-2">
			<div className="flex min-h-5 items-center gap-2">
				<h3 className="type-label">Preflight</h3>
				{checking ? (
					<span
						role="status"
						className="flex items-center gap-1.5 text-2xs text-ink-3"
					>
						<Spinner className="size-3" />
						Checking
					</span>
				) : summary ? (
					<span className="font-mono text-2xs text-ink-3 tabular-nums">
						{summary.fail > 0 ? `${summary.fail} failed` : "no failures"}
						{summary.warn > 0 ? ` · ${summary.warn} to review` : ""}
					</span>
				) : null}
			</div>
			{summary && summary.rows.length > 0 ? (
				<ul className="m-0 flex list-none flex-col gap-1.5 p-0">
					{summary.rows.map((check) => (
						<CheckRow key={check.id} check={check} />
					))}
				</ul>
			) : waiting ? (
				<p className="text-[12px] text-ink-3">{waiting}</p>
			) : null}
		</section>
	);
}

/** "N secrets will be generated or stored encrypted", from the plan's secret list. */
export function SecretsNotice({ secrets }: { secrets: readonly string[] }) {
	const text = secretsNotice(secrets.length);
	if (!text) return null;
	return (
		<div className="flex items-start gap-2 text-[12px] text-ink-3 leading-[1.45]">
			<LockKeyholeIcon aria-hidden className="mt-px size-3.5 shrink-0" />
			<span>{text}</span>
		</div>
	);
}

/** A callout for what the reader should know before pressing install. */
export function Notice({
	title,
	children,
}: {
	title: string;
	children: React.ReactNode;
}) {
	return (
		<Alert variant="warning" role="note">
			<AlertTitle>{title}</AlertTitle>
			<AlertDescription>{children}</AlertDescription>
		</Alert>
	);
}
