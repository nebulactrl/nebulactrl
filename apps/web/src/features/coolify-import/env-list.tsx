import { cn } from "cn";
import { ArrowRightIcon } from "lucide-react";
import type { CoolifyPlanEnv, CoolifyPlanRewrite } from "#/api/gen/types.gen";
import { Badge } from "#/components/ui/badge";
import { CheckLabel } from "#/components/ui/check-label";
import { Input } from "#/components/ui/input";
import { Switch } from "#/components/ui/switch";
import { Disclosure } from "#/features/compose-import/parts";
import {
	type EnvDraft,
	envRowState,
	type ImportDraft,
	patchEnv,
	secretReasonText,
} from "./lib";

const COLUMNS =
	"grid grid-cols-[minmax(0,1fr)_auto_auto] items-center gap-x-4 sm:grid-cols-[minmax(0,1fr)_64px_48px]";

/**
 * The variables of one resource, by key. A plan never carries a value, so
 * there is none to show: only what each variable is (secret and why,
 * rewritten into which reference, skipped and why, still waiting for a value)
 * and the switches that change it. A value the reader types for a locked
 * variable goes into a password field and into the draft, nowhere else.
 */
export function EnvList({
	resourceUuid,
	env,
	drafts,
	rewrites,
	onDraft,
}: {
	resourceUuid: string;
	env: readonly CoolifyPlanEnv[];
	drafts: Readonly<Record<string, EnvDraft>> | undefined;
	rewrites: ReadonlyMap<string, CoolifyPlanRewrite>;
	onDraft(update: (current: ImportDraft) => ImportDraft): void;
}) {
	if (env.length === 0) return null;
	const patch = (key: string, change: Parameters<typeof patchEnv>[3]) =>
		onDraft((current) => patchEnv(current, resourceUuid, key, change));
	const waiting = env.filter(
		(row) => envRowState(row, drafts?.[row.key]).needsValue,
	).length;

	return (
		<Disclosure title="Variables" count={env.length} defaultOpen={waiting > 0}>
			<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
				Keys only: the values are never shown here.
				{waiting > 0
					? ` ${waiting} still ${waiting === 1 ? "needs" : "need"} a value.`
					: ""}
			</p>
			<div className="surface-well flex max-h-96 flex-col overflow-y-auto overscroll-contain rounded-ctl border border-line">
				<div
					className={cn(
						COLUMNS,
						"sticky top-0 z-10 border-line-faint border-b bg-surface-well px-3 py-1.5",
					)}
				>
					<span className="type-label">Variable</span>
					<span className="type-label">Secret</span>
					<span className="type-label">Skip</span>
				</div>
				{env.map((row) => {
					const draft = drafts?.[row.key];
					const state = envRowState(row, draft);
					const rewrite = rewrites.get(row.key);
					const reason = secretReasonText(row.secretReason);
					// Once the reader has typed in the field it stays, even when emptied.
					const showValue = state.needsValue || draft?.value !== undefined;
					const canKeep =
						row.rewritten === true || draft?.keepCoolify !== undefined;
					return (
						<div
							key={row.key}
							className={cn(
								"flex flex-col gap-1.5 border-line-faint border-b px-3 py-2 last:border-b-0",
								state.skipped && "opacity-70",
							)}
						>
							<div className={COLUMNS}>
								<span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
									<span className="break-all font-mono text-[12px] text-ink-1">
										{row.key}
									</span>
									{row.scope !== "runtime" ? (
										<Badge>
											{row.scope === "build" ? "build" : "build + runtime"}
										</Badge>
									) : null}
									{state.secret ? (
										<Badge variant="accent">
											secret{reason ? ` · ${reason}` : ""}
										</Badge>
									) : null}
									{rewrite ? <Badge variant="accent">rewritten</Badge> : null}
									{state.needsValue ? (
										<Badge variant="warning">needs value</Badge>
									) : null}
									{state.skipped ? <Badge>skipped</Badge> : null}
								</span>
								<span>
									<Switch
										size="sm"
										aria-label={`Secret: ${row.key}`}
										checked={state.secret}
										onCheckedChange={(next) => patch(row.key, { secret: next })}
									/>
								</span>
								<span>
									<Switch
										size="sm"
										aria-label={`Skip: ${row.key}`}
										checked={state.skipped}
										onCheckedChange={(next) => patch(row.key, { skip: next })}
									/>
								</span>
							</div>
							{rewrite ? (
								<span className="flex flex-wrap items-center gap-x-2 gap-y-1 font-mono text-[12px] text-ink-2">
									<span className="text-ink-3">Coolify resource</span>
									<ArrowRightIcon
										aria-label="becomes"
										className="size-3 shrink-0 text-ink-3"
									/>
									<span className="break-all">{rewrite.to}</span>
								</span>
							) : null}
							{canKeep ? (
								<CheckLabel
									checked={state.keepCoolify}
									onCheckedChange={(next) =>
										patch(row.key, { keepCoolify: next })
									}
									className="text-[12px] text-ink-2"
								>
									Keep the Coolify host and credentials (no reference)
								</CheckLabel>
							) : null}
							{showValue ? (
								<div className="flex flex-col gap-1">
									<label
										htmlFor={`value-${resourceUuid}-${row.key}`}
										className="text-[12px] text-ink-3"
									>
										Value for {row.key}
									</label>
									<Input
										id={`value-${resourceUuid}-${row.key}`}
										type="password"
										autoComplete="off"
										autoCapitalize="off"
										spellCheck={false}
										placeholder="Type a value, or leave empty to set it later"
										className="h-7 max-w-md font-mono text-[12px]"
										value={draft?.value ?? ""}
										onChange={(event) =>
											patch(row.key, { value: event.target.value })
										}
									/>
								</div>
							) : null}
							{row.note ? (
								<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
									{row.note}
								</p>
							) : null}
						</div>
					);
				})}
			</div>
		</Disclosure>
	);
}
