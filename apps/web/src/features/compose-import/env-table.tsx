import { cn } from "cn";
import type { ComposeChoices, ComposePlanService } from "#/api/gen/types.gen";
import { Badge } from "#/components/ui/badge";
import { CheckLabel } from "#/components/ui/check-label";
import { Switch } from "#/components/ui/switch";
import { envRowState, setEnvChoice } from "./lib";
import { Disclosure } from "./parts";

const COLUMNS =
	"grid grid-cols-[minmax(0,1fr)_auto_auto] items-center gap-x-3 sm:grid-cols-[minmax(0,1.1fr)_minmax(0,1.4fr)_64px_48px]";

/**
 * The variables of one service. A secret shows as dots and has no reveal: the
 * server never sends the value back. The toggles write flags into the choices;
 * no value ever leaves this table.
 */
export function EnvTable({
	service,
	choices,
	onChoices,
}: {
	service: ComposePlanService;
	choices: ComposeChoices;
	onChoices(update: (current: ComposeChoices) => ComposeChoices): void;
}) {
	const env = service.env ?? [];
	if (env.length === 0) return null;
	const patch = (key: string, change: Parameters<typeof setEnvChoice>[3]) =>
		onChoices((current) => setEnvChoice(current, service.key, key, change));

	return (
		<Disclosure title="Environment variables" count={env.length}>
			<div className="surface-well flex max-h-80 flex-col overflow-y-auto overscroll-contain rounded-ctl border border-line">
				<div
					className={cn(
						COLUMNS,
						"sticky top-0 z-10 border-line-faint border-b bg-surface-well px-3 py-1.5",
					)}
				>
					<span className="type-label">Variable</span>
					<span className="type-label hidden sm:block">Value</span>
					<span className="type-label">Secret</span>
					<span className="type-label">Skip</span>
				</div>
				{env.map((row) => {
					const choice = choices.services?.[service.key]?.env?.[row.key];
					const state = envRowState(row, choice);
					return (
						<div
							key={row.key}
							className={cn(
								"flex flex-col gap-1 border-line-faint border-b px-3 py-2 last:border-b-0",
								state.skipped && "opacity-70",
							)}
						>
							<div className={COLUMNS}>
								<span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
									<span className="truncate font-mono text-[12px] text-ink-1">
										{row.key}
									</span>
									{state.rewritten ? (
										<Badge variant="accent">rewritten</Badge>
									) : null}
								</span>
								<span className="hidden min-w-0 truncate font-mono text-[12px] text-ink-2 sm:block">
									{state.secret ? (
										<>
											••••••<span className="sr-only"> (secret, hidden)</span>
										</>
									) : row.value === "" ? (
										<span className="text-ink-4">empty</span>
									) : (
										row.value
									)}
								</span>
								<span className="flex flex-col items-start gap-0.5">
									<Switch
										size="sm"
										aria-label={`Secret: ${row.key}`}
										checked={state.secret}
										onCheckedChange={(next) => patch(row.key, { secret: next })}
									/>
									{row.secretGuess && choice?.secret === undefined ? (
										<span className="text-3xs text-ink-4">guessed</span>
									) : null}
								</span>
								<span>
									<Switch
										size="sm"
										aria-label={`Skip: ${row.key}`}
										checked={state.skipped}
										disabled={state.skippedByPlan}
										onCheckedChange={(next) => patch(row.key, { skip: next })}
									/>
								</span>
							</div>
							<span className="min-w-0 truncate font-mono text-2xs text-ink-3 sm:hidden">
								{state.secret
									? "••••••"
									: row.value === ""
										? "empty"
										: row.value}
							</span>
							{state.canKeepHosts ? (
								<CheckLabel
									checked={state.keepHosts}
									onCheckedChange={(next) =>
										patch(row.key, { rewrite: next ? false : null })
									}
									className="text-[12px] text-ink-2"
								>
									Keep original host names
								</CheckLabel>
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
