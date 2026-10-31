import { useMutation, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useEffect, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { restoreVolumeBackup } from "#/api/gen/sdk.gen";
import type { VolumeBackupView } from "#/api/gen/types.gen";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import {
	Dialog,
	DialogBody,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from "#/components/ui/dialog";
import {
	Field,
	FieldDescription,
	FieldError,
	FieldLabel,
} from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Segmented } from "#/components/ui/segmented";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { absoluteTime } from "#/lib/format";
import {
	formatBackupTime,
	initialRestoreState,
	inPlaceSentence,
	newVolumeSentence,
	type RestoreMode,
	type RestoreState,
	restorableVolumes,
	restoreReady,
	restoreRequest,
} from "./model";
import { invalidateVolumeBackups } from "./queries";

/**
 * Restore a volume from a backup: into a new volume (default; the current data
 * is kept and the swap is staged until Deploy) or over the volume in place.
 */
export function VolumeRestoreDialog({
	backup,
	serviceSlug,
	currentVolumes,
	onClose,
}: {
	backup: VolumeBackupView | null;
	serviceSlug: string;
	/** The service's volumes now: a volume dropped since the backup can not be restored. */
	currentVolumes: readonly string[];
	onClose: () => void;
}) {
	const queryClient = useQueryClient();
	const { available, missing } = restorableVolumes(
		backup?.volumes ?? [],
		currentVolumes,
	);
	const [state, setState] = useState<RestoreState>(() =>
		initialRestoreState(available),
	);
	const [error, setError] = useState<string | null>(null);
	const backupId = backup?.id;
	// biome-ignore lint/correctness/useExhaustiveDependencies: a fresh dialog per backup
	useEffect(() => {
		if (backupId === undefined) return;
		setState(initialRestoreState(available));
		setError(null);
	}, [backupId]);

	const start = useMutation({
		mutationFn: () => {
			if (!backup) throw new Error("no backup");
			return restoreVolumeBackup({
				path: { backupId: backup.id },
				body: restoreRequest(state),
			});
		},
		onSuccess: async () => {
			toast.success(
				state.mode === "new_volume"
					? "Restoring into a new volume"
					: "Restore started",
				state.mode === "new_volume"
					? "When it finishes the change is staged: review it in the staged bar and press Deploy."
					: "A safety backup is taken first; the service is stopped while the files are replaced.",
			);
			await invalidateVolumeBackups(queryClient);
			onClose();
		},
		onError: (e) => setError(errorMessage(e)),
	});

	const set = (patch: Partial<RestoreState>) =>
		setState((prev) => ({ ...prev, ...patch }));
	const valid = restoreReady(state, serviceSlug);
	const inPlace = state.mode === "in_place";
	const submit = (e: FormEvent) => {
		e.preventDefault();
		if (!valid || start.isPending) return;
		setError(null);
		start.mutate();
	};

	return (
		<Dialog open={backup !== null} onOpenChange={(o) => (o ? null : onClose())}>
			<DialogContent size="lg">
				<form onSubmit={submit} className="contents">
					<DialogHeader>
						<DialogTitle>Restore volume</DialogTitle>
						<DialogDescription>
							{backup
								? `Backup of ${formatBackupTime(backup.createdAt)} (${absoluteTime(backup.createdAt)}).`
								: null}
						</DialogDescription>
					</DialogHeader>
					<DialogBody>
						<Field>
							<FieldLabel htmlFor="vr-volume">Volume</FieldLabel>
							<Select
								value={state.volume}
								items={available.map((v) => ({ value: v, label: v }))}
								onValueChange={(v) => set({ volume: String(v ?? "") })}
							>
								<SelectTrigger id="vr-volume" className="w-full">
									<SelectValue placeholder="Choose a volume" />
								</SelectTrigger>
								<SelectContent alignItemWithTrigger={false}>
									{available.map((v) => (
										<SelectItem key={v} value={v}>
											{v}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
							{missing.length > 0 ? (
								<FieldDescription>
									{missing.join(", ")} {missing.length === 1 ? "is" : "are"} no
									longer {missing.length === 1 ? "a volume" : "volumes"} of this
									service and can not be restored. The files are still in the
									backup: browse them to download.
								</FieldDescription>
							) : null}
						</Field>
						<Field>
							<FieldLabel id="vr-mode">How</FieldLabel>
							<Segmented<RestoreMode>
								aria-label="Restore mode"
								font="sans"
								density="regular"
								value={state.mode}
								onValueChange={(mode) => set({ mode })}
								options={[
									{ value: "new_volume", label: "Restore into a new volume" },
									{ value: "in_place", label: "Restore in place" },
								]}
							/>
						</Field>
						{inPlace ? (
							<>
								<Alert variant="warning">
									<AlertTitle>This overwrites the volume</AlertTitle>
									<AlertDescription>
										{inPlaceSentence(state.volume)} Writes your app makes during
										the restore are lost.
									</AlertDescription>
								</Alert>
								<Field>
									<FieldLabel htmlFor="vr-confirm">
										Type <code className="type-mono">{serviceSlug}</code> to
										continue
									</FieldLabel>
									<Input
										id="vr-confirm"
										autoComplete="off"
										spellCheck={false}
										className="font-mono"
										value={state.confirm}
										onChange={(e) => set({ confirm: e.target.value })}
									/>
								</Field>
							</>
						) : (
							<Alert variant="info">
								<AlertTitle>Safe by default</AlertTitle>
								<AlertDescription>
									{backup ? newVolumeSentence(backup.createdAt) : null} The old
									volume stays on the server, unmounted, until you delete it
									under Settings, Volumes.
								</AlertDescription>
							</Alert>
						)}
						{error ? <FieldError>{error}</FieldError> : null}
					</DialogBody>
					<DialogFooter>
						<Button type="button" variant="ghost" onClick={onClose}>
							Cancel
						</Button>
						<Button
							type="submit"
							variant={inPlace ? "danger-solid" : "primary"}
							disabled={!valid || start.isPending}
						>
							{start.isPending ? <Spinner /> : null}
							{inPlace ? "Overwrite volume" : "Restore into new volume"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
