import { cn } from "cn";
import { RotateCcwIcon, XIcon } from "lucide-react";
import { useEffect, useId, useRef, useState } from "react";
import { Button } from "#/components/ui/button";
import { Kbd } from "#/components/ui/kbd";
import {
	Sheet,
	SheetBody,
	SheetContent,
	SheetDescription,
	SheetFooter,
	SheetHeader,
	SheetTitle,
} from "#/components/ui/sheet";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { Textarea } from "#/components/ui/textarea";
import { ServiceConfigChip, useFileNotes } from "#/features/project-config";
import { ComposeDiff } from "#/features/stacks/compose-diff";
import {
	FIRST_DEPLOYMENT,
	type ReviewGroup,
	type ReviewLine,
} from "#/features/staged/staged";
import { GuardedKey, useGuard } from "./guarded-key";
import type { EnvironmentDeploy } from "./use-environment-deploy";

/** The deploy message limit the control plane accepts. */
export const MESSAGE_MAX = 200;

const SIGN_TONE: Record<ReviewLine["sign"], string> = {
	"+": "text-ok",
	"−": "text-bad",
	"~": "text-warn",
};
const SIGN_NAME: Record<ReviewLine["sign"], string> = {
	"+": "added",
	"−": "removed",
	"~": "changed",
};

function Line({
	line,
	serviceName,
	discarding,
	disabled,
	onDiscard,
}: {
	line: ReviewLine;
	serviceName: string;
	discarding: boolean;
	disabled: boolean;
	onDiscard(): void;
}) {
	return (
		<li
			data-sign={SIGN_NAME[line.sign]}
			className={cn(
				"flex gap-2.5 px-3 py-[5px] font-mono text-xs leading-[1.4] transition-opacity duration-quick",
				line.diff ? "items-start" : "items-center",
				discarding && "opacity-50",
			)}
		>
			<span className={cn("w-3 shrink-0", SIGN_TONE[line.sign])}>
				<span aria-hidden>{line.sign}</span>
				<span className="sr-only">{SIGN_NAME[line.sign]}</span>
			</span>
			<span
				className={cn(
					"flex min-w-0 flex-1 gap-2.5 max-[759px]:flex-col max-[759px]:gap-0.5",
					line.diff && "flex-col gap-1.5",
				)}
			>
				<span className="max-w-[60%] shrink-0 text-ink-1 [overflow-wrap:anywhere] max-[759px]:max-w-full">
					{line.text}
				</span>
				{line.value === undefined ? null : (
					<span title={line.value} className="min-w-0 break-words text-ink-3">
						{line.value}
					</span>
				)}
				{line.diff ? <ComposeDiff diff={line.diff} className="w-full" /> : null}
			</span>
			{line.note ? (
				<span className="shrink-0 font-sans text-2xs text-ink-4 leading-none">
					{line.note}
				</span>
			) : null}
			{line.field === FIRST_DEPLOYMENT ? (
				// A new service has no earlier release to go back to; only
				// deleting it removes this change.
				<span aria-hidden className="size-6 shrink-0" />
			) : (
				<button
					type="button"
					disabled={disabled}
					onClick={onDiscard}
					aria-label={`Discard change to ${line.text} on ${serviceName}`}
					title="Discard this change"
					className="pressable grid size-6 shrink-0 cursor-pointer place-items-center rounded-sm text-ink-4 outline-none hover:bg-white/8 hover:text-ink-1 focus-visible:text-ink-1 focus-visible:outline-2 focus-visible:outline-accent-ink disabled:cursor-default disabled:opacity-40"
				>
					<XIcon aria-hidden className="size-3" />
				</button>
			)}
		</li>
	);
}

/**
 * One service's staged lines. Its name carries the nebula.toml chip, and a
 * line the file owns says "set by nebula.toml" in its quiet voice.
 */
function StagedGroup({
	group,
	open,
	deploy,
	disabled,
}: {
	group: ReviewGroup;
	open: boolean;
	deploy: EnvironmentDeploy;
	disabled: boolean;
}) {
	const lines = useFileNotes(group.serviceId, group.lines, open);
	return (
		<section aria-label={group.name} className="flex flex-col gap-1.5">
			<h3 className="flex min-w-0 items-center gap-2 font-medium text-[13px] text-ink-1">
				<span className="truncate">{group.name}</span>
				<ServiceConfigChip serviceId={group.serviceId} className="shrink-0" />
			</h3>
			<ul className="surface-well rounded-ctl py-1">
				{lines.map((line) => (
					<Line
						key={line.id}
						line={line}
						serviceName={group.name}
						discarding={deploy.discarding === line.id}
						disabled={disabled}
						onDiscard={() => deploy.discardChange(group.serviceId, line.field)}
					/>
				))}
			</ul>
		</section>
	);
}

export interface StagedReviewProps {
	open: boolean;
	onOpenChange(open: boolean): void;
	environmentName: string;
	isProduction: boolean;
	deploy: EnvironmentDeploy;
	/**
	 * Scope the sheet to one service (opened from its dock, menu or ⌘K entry):
	 * its lines only, and a key that deploys it alone. With nothing staged the
	 * sheet offers a redeploy of its current release.
	 */
	scopeServiceId?: string | undefined;
}

/**
 * The staged changes of one environment, per service, each with a ✕ that
 * reverts that one field. A note, then Deploy (⇧↵). In a production
 * environment the Deploy button is the guarded key and ⇧↵ first lifts its
 * guard, then deploys.
 */
export function StagedReview({
	open,
	onOpenChange,
	environmentName,
	isProduction,
	deploy,
	scopeServiceId,
}: StagedReviewProps) {
	const messageId = useId();
	const [message, setMessage] = useState("");
	const guard = useGuard();
	const { busy, sent } = deploy;
	const scopeService =
		scopeServiceId === undefined
			? undefined
			: deploy.services.find((service) => service.id === scopeServiceId);
	const scoped = scopeServiceId !== undefined;
	const count =
		scopeServiceId === undefined
			? deploy.count
			: deploy.stagedCountOf(scopeServiceId);
	const groups =
		scopeServiceId === undefined
			? deploy.groups
			: deploy.groups.filter((group) => group.serviceId === scopeServiceId);
	// A scoped service with nothing staged: the key redeploys its current release.
	const redeploy = scoped && count === 0 && scopeService !== undefined;
	const actionable = redeploy || count > 0;

	// The last discard empties the sheet: nothing is left to review.
	const hadChanges = useRef(false);
	useEffect(() => {
		if (!open) {
			hadChanges.current = false;
			return;
		}
		if (count > 0) hadChanges.current = true;
		else if (hadChanges.current) onOpenChange(false);
	}, [open, count, onOpenChange]);

	const send = async () => {
		if (!actionable || busy) return;
		try {
			const accepted =
				scopeServiceId === undefined
					? await deploy.deployStaged(message)
					: redeploy
						? await deploy.redeployService(scopeServiceId, message)
						: await deploy.deployStaged(message, [scopeServiceId]);
			if (accepted) {
				setMessage("");
				onOpenChange(false);
			}
		} finally {
			guard.resolved();
		}
	};

	const serviceCount = groups.length;
	const label = redeploy ? "Redeploy" : `Deploy · ${count}`;
	const title = scopeService ? `Deploy ${scopeService.name}` : "Review changes";

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent
				className="max-[759px]:w-full! max-[759px]:max-w-none! sm:max-w-[560px]"
				onKeyDown={(event) => {
					if (event.key !== "Enter" || !event.shiftKey) return;
					event.preventDefault();
					if (!isProduction) void send();
					else if (guard.armed) {
						guard.press();
						void send();
					} else guard.lift();
				}}
			>
				<SheetHeader
					className={cn("relative pb-4", isProduction && "env-rule")}
				>
					{isProduction ? (
						<span className="env-caption">Production</span>
					) : null}
					<SheetTitle>{title}</SheetTitle>
					<SheetDescription>
						{redeploy
							? `Nothing staged for ${scopeService?.name} · ${environmentName}.`
							: count === 0
								? `Nothing staged in ${environmentName}.`
								: `${count} change${count === 1 ? "" : "s"} across ${serviceCount} service${serviceCount === 1 ? "" : "s"} · ${environmentName}. Saved, not deployed.`}
					</SheetDescription>
				</SheetHeader>
				<SheetBody className="flex flex-col gap-4">
					{redeploy ? (
						<section
							aria-label="Redeploy current release"
							className="surface-well flex items-center gap-2.5 rounded-ctl px-3 py-2.5"
						>
							<RotateCcwIcon
								aria-hidden
								className="size-4 shrink-0 text-ink-3"
							/>
							<div className="flex min-w-0 flex-col gap-[3px]">
								<span className="font-medium text-[13px] text-ink-1">
									Redeploy current release
								</span>
								<span className="text-[12px] text-ink-3">
									Deploys {scopeService?.name} again with its current release
									and configuration. Nothing else changes.
								</span>
							</div>
						</section>
					) : null}
					{groups.length === 0 && count > 0 ? (
						<div role="status" aria-label="Loading changes">
							<Skeleton className="h-24 rounded-ctl" />
						</div>
					) : null}
					{groups.map((group) => (
						<StagedGroup
							key={group.serviceId}
							group={group}
							open={open}
							deploy={deploy}
							disabled={busy || sent}
						/>
					))}
					{deploy.emptyStaged.length > 0 && !scoped ? (
						<section
							aria-label="Nothing to review"
							className="surface-well flex flex-col gap-1 rounded-ctl px-3 py-2.5"
						>
							<span className="font-medium text-[13px] text-ink-1">
								Nothing to review
							</span>
							<span className="text-[12px] text-ink-3">
								{deploy.emptyStaged.map((service) => service.name).join(", ")}{" "}
								{deploy.emptyStaged.length === 1 ? "is" : "are"} marked staged
								but no change is left. Nothing deploys for{" "}
								{deploy.emptyStaged.length === 1 ? "it" : "them"}.
							</span>
						</section>
					) : null}
					<div className="flex flex-col gap-1.5">
						<label
							htmlFor={messageId}
							className="font-medium text-[13px] text-ink-1"
						>
							Deploy message{" "}
							<span className="font-normal text-ink-4">(optional)</span>
						</label>
						<Textarea
							id={messageId}
							value={message}
							rows={2}
							maxLength={MESSAGE_MAX}
							placeholder="What does this release change?"
							onChange={(event) =>
								setMessage(event.target.value.replace(/[\r\n]+/g, " "))
							}
							className="min-h-14"
						/>
						<span
							className="self-end font-mono text-[11px] text-ink-4 tabular-nums"
							aria-live="off"
						>
							{message.length}/{MESSAGE_MAX}
						</span>
					</div>
				</SheetBody>
				<SheetFooter className="flex-wrap items-center">
					{scoped ? null : (
						<Button
							variant="ghost"
							disabled={busy || sent || count === 0}
							onClick={() => void deploy.discardAll()}
						>
							Discard all
						</Button>
					)}
					<div className="ml-auto flex items-center gap-3 max-[759px]:ml-0 max-[759px]:w-full">
						{isProduction ? (
							<GuardedKey
								guard={guard}
								label={label}
								sent={sent}
								disabled={!actionable || busy}
								disabledReason="Nothing staged to deploy"
								onPress={() => void send()}
								className="max-[759px]:w-full"
							/>
						) : (
							<>
								<span className="hidden items-center gap-1 text-ink-4 text-xs sm:flex">
									<Kbd variant="key">⇧↵</Kbd>
								</span>
								<Button
									variant="primary"
									disabled={!actionable || busy || sent}
									onClick={() => void send()}
								>
									{busy || sent ? <Spinner data-icon="inline-start" /> : null}
									{sent ? "Deploying…" : label}
								</Button>
							</>
						)}
					</div>
				</SheetFooter>
			</SheetContent>
		</Sheet>
	);
}
