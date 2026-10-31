import { useState } from "react";
import type { Service } from "#/api/models";
import { Button } from "#/components/ui/button";
import { scaleRows } from "#/features/scaling/model";
import { ScaleDialog } from "#/features/scaling/scale-dialog";

/**
 * What each process scales to, one row each, with "Scale…" opening the dialog
 * on that process. Scaling is operational (it applies at once and is never
 * staged), so the rows say so wherever they are shown.
 */
export function ScaleRows({ service }: { service: Service }) {
	const rows = scaleRows(service);
	const [open, setOpen] = useState(false);
	// Kept apart from `open`, so the dialog does not change process while it closes.
	const [process, setProcess] = useState<string | undefined>(undefined);
	return (
		<>
			<ul aria-label="Scaling of each process" className="flex flex-col">
				{rows.map((row) => (
					<li
						key={row.name}
						className="flex flex-wrap items-center gap-x-3 gap-y-1 border-line-faint border-b py-2 last:border-b-0"
					>
						<span className="font-medium font-mono text-[13px] text-ink-1">
							{row.name}
						</span>
						<span className="font-mono text-ink-2 text-xs">{row.text}</span>
						{row.reason ? (
							<span className="type-secondary min-w-0 flex-1">
								{row.reason}
							</span>
						) : null}
						<Button
							variant="secondary"
							size="xs"
							className="ml-auto"
							disabled={!row.eligible}
							onClick={() => {
								setProcess(row.name);
								setOpen(true);
							}}
						>
							Scale…
						</Button>
					</li>
				))}
			</ul>
			<ScaleDialog
				serviceId={service.id}
				process={process}
				open={open}
				onOpenChange={setOpen}
			/>
		</>
	);
}

/** One process' scale line and its "Scale…" button, for a process card. */
export function ScaleLine({
	service,
	process,
}: {
	service: Service;
	process: string;
}) {
	const row = scaleRows(service).find(
		(candidate) => candidate.name === process,
	);
	const [open, setOpen] = useState(false);
	if (!row) {
		return (
			<span className="type-secondary">
				Scale appears here once the process is saved.
			</span>
		);
	}
	return (
		<>
			<div className="flex flex-wrap items-center gap-x-3 gap-y-1">
				<span className="font-mono text-ink-2 text-xs">{row.text}</span>
				{row.reason ? (
					<span className="type-secondary min-w-0 flex-1">{row.reason}</span>
				) : null}
				<Button
					variant="secondary"
					size="xs"
					className="ml-auto"
					disabled={!row.eligible}
					onClick={() => setOpen(true)}
				>
					Scale…
				</Button>
			</div>
			<ScaleDialog
				serviceId={service.id}
				process={process}
				open={open}
				onOpenChange={setOpen}
			/>
		</>
	);
}
