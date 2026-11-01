import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { serviceQuery } from "#/api/hooks";
import { Alert, AlertDescription } from "#/components/ui/alert";
import {
	Dialog,
	DialogBody,
	DialogContent,
	DialogDescription,
	DialogHeader,
	DialogTitle,
	useOpenedByKeyboard,
} from "#/components/ui/dialog";
import { Label } from "#/components/ui/label";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { Skeleton } from "#/components/ui/skeleton";
import { initialScaleProcess, scaleRows } from "./model";
import { ScalingForm } from "./scaling-form";

export interface ScaleDialogProps {
	serviceId: string;
	open: boolean;
	onOpenChange: (open: boolean) => void;
	/** The process to open on when the service has several; the default process when absent. */
	process?: string | undefined;
}

/**
 * "Scale <name>": the replica count and autoscaling of one service, or of one
 * of its processes (a select picks it when the service has several).
 */
export function ScaleDialog({
	serviceId,
	open,
	onOpenChange,
	process,
}: ScaleDialogProps) {
	const service = useQuery({ ...serviceQuery(serviceId), enabled: open });
	const instant = useOpenedByKeyboard(open);
	// `null`: the reader has not picked, so the dialog follows `process`.
	const [picked, setPicked] = useState<string | null>(null);
	useEffect(() => {
		if (!open) setPicked(null);
	}, [open]);
	const rows = service.data ? scaleRows(service.data) : [];
	const chosen = service.data
		? initialScaleProcess(service.data, picked ?? process)
		: undefined;
	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent size="md" showCloseButton instant={instant}>
				<DialogHeader>
					<DialogTitle>
						{service.data ? `Scale ${service.data.name}` : "Scale service"}
					</DialogTitle>
					<DialogDescription>
						Applies immediately. The current release keeps running and nothing
						is rebuilt.
					</DialogDescription>
				</DialogHeader>
				<DialogBody>
					{service.data ? (
						<div className="flex flex-col gap-4">
							{rows.length > 1 ? (
								<div className="flex flex-col gap-1.5">
									<Label htmlFor="scale-process">Process</Label>
									<Select
										value={chosen ?? null}
										items={rows.map((row) => ({
											value: row.name,
											label: row.name,
										}))}
										onValueChange={(next) => {
											if (next !== null) setPicked(next);
										}}
									>
										<SelectTrigger id="scale-process" className="w-full">
											<SelectValue placeholder="Process" />
										</SelectTrigger>
										<SelectContent alignItemWithTrigger={false}>
											{rows.map((row) => (
												<SelectItem key={row.name} value={row.name}>
													<span className="font-mono">{row.name}</span>
													<span className="ml-2 text-ink-3">{row.text}</span>
												</SelectItem>
											))}
										</SelectContent>
									</Select>
								</div>
							) : null}
							<ScalingForm
								// A new process starts from its own settings, not the last one's edits.
								key={chosen ?? ""}
								service={service.data}
								process={chosen}
								onApplied={() => onOpenChange(false)}
							/>
						</div>
					) : service.isError ? (
						<Alert variant="destructive">
							<AlertDescription>Could not load this service.</AlertDescription>
						</Alert>
					) : (
						<Skeleton className="h-24 w-full" />
					)}
				</DialogBody>
			</DialogContent>
		</Dialog>
	);
}
