import { cn } from "cn";
import type { PlatformServiceView } from "#/api/gen/types.gen";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { StatusIcon, type StatusKind } from "#/components/ui/status-icon";
import { type StepRow, type StepState, statusSummary } from "./model";

/** A status icon and its words: the service's state in one line. */
export function StatusLine({
	view,
	className,
}: {
	view: Pick<PlatformServiceView, "status" | "step">;
	className?: string;
}) {
	const summary = statusSummary(view);
	return (
		<span
			className={cn(
				"inline-flex items-center gap-1.5 text-ink-1 text-xs",
				className,
			)}
		>
			<StatusIcon kind={summary.kind} label="" />
			{summary.label}
		</span>
	);
}

const STEP_ICON: Record<StepState, StatusKind> = {
	done: "healthy",
	current: "deploying",
	todo: "pending",
	failed: "failed",
};

/** The stages of a set-up, each with its icon and words. */
export function StepList({ rows }: { rows: readonly StepRow[] }) {
	return (
		<ol className="m-0 flex list-none flex-col gap-1.5 p-0">
			{rows.map((row) => (
				<li
					key={row.step}
					aria-current={row.state === "current" ? "step" : undefined}
					className={cn(
						"flex items-center gap-2 text-xs",
						row.state === "todo" ? "text-ink-4" : "text-ink-1",
					)}
				>
					<StatusIcon kind={STEP_ICON[row.state]} label="" />
					<span>{row.label}</span>
				</li>
			))}
		</ol>
	);
}

/** A caution on a warning wash: the tone icon and the words. */
export function WarningNote({
	title,
	children,
}: {
	title?: string;
	children: string;
}) {
	return (
		<Alert variant="warning">
			{title ? <AlertTitle>{title}</AlertTitle> : null}
			<AlertDescription>{children}</AlertDescription>
		</Alert>
	);
}
