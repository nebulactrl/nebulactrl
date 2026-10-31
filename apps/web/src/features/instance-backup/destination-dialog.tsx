import { useMutation, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useEffect, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { updateInstanceBackup } from "#/api/gen/sdk.gen";
import type { InstanceBackupView } from "#/api/gen/types.gen";
import { CredentialHelp } from "#/components/common/credential-help";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { CheckLabel } from "#/components/ui/check-label";
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
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { backupGuide } from "#/lib/credential-links";
import {
	type DestinationDraft,
	type DestKind,
	draftFor,
	draftToPatch,
	draftValid,
	NOT_OFF_SITE_WARNING,
} from "./model";
import { invalidateInstanceBackup } from "./queries";

/**
 * Where control-plane backups go: an S3-compatible bucket, or a directory on the
 * control-plane host (labelled "not off-site"). Keys are write-only: leave them
 * empty to keep the stored ones.
 */
export function InstanceDestinationDialog({
	open,
	onOpenChange,
	settings,
}: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	settings: InstanceBackupView;
}) {
	const queryClient = useQueryClient();
	const [draft, setDraft] = useState<DestinationDraft>(() =>
		draftFor(settings),
	);
	const [error, setError] = useState<string | null>(null);

	useEffect(() => {
		if (open) {
			setDraft(draftFor(settings));
			setError(null);
		}
	}, [open, settings]);

	const set = <K extends keyof DestinationDraft>(
		key: K,
		value: DestinationDraft[K],
	) => setDraft((prev) => ({ ...prev, [key]: value }));

	const save = useMutation({
		mutationFn: () => updateInstanceBackup({ body: draftToPatch(draft) }),
		onSuccess: async () => {
			toast.success(
				"Destination saved",
				"Test it to confirm backups can be written there.",
			);
			await invalidateInstanceBackup(queryClient);
			onOpenChange(false);
		},
		onError: (e) => setError(errorMessage(e)),
	});

	const s3 = draft.kind === "s3";
	const builtin = draft.kind === "builtin";
	// The key kinds start afresh when the kind changes: stored keys do not carry over.
	const keysStored =
		settings.destination.kind === "s3" &&
		settings.destination.accessKeySet &&
		settings.destination.secretKeySet;
	const valid = draftValid(draft, settings);

	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (!valid || save.isPending) return;
		setError(null);
		save.mutate();
	};

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent size="lg">
				<form onSubmit={submit} className="contents">
					<DialogHeader>
						<DialogTitle>Backup destination</DialogTitle>
						<DialogDescription>
							Where the control plane writes its database backups. An
							S3-compatible bucket elsewhere survives the loss of this host; a
							directory on it or the built-in storage does not.
						</DialogDescription>
					</DialogHeader>
					<DialogBody>
						<Field>
							<FieldLabel id="ib-kind-label">Type</FieldLabel>
							<Segmented<DestKind>
								aria-label="Destination type"
								font="sans"
								density="regular"
								value={draft.kind}
								onValueChange={(v) => set("kind", v)}
								options={[
									{ value: "s3", label: "S3-compatible bucket" },
									{ value: "builtin", label: "Built-in storage" },
									{
										value: "local",
										label: "Directory on this host",
										disabled: !settings.localDirEnabled,
									},
								]}
							/>
							{settings.localDirEnabled ? null : (
								<FieldDescription>
									A directory needs{" "}
									<code className="type-mono">NEBULACTRL_BACKUP_DIR</code> on
									the control plane, with a volume mounted there.
								</FieldDescription>
							)}
						</Field>
						{builtin ? (
							<Alert variant="warning">
								<AlertTitle>Not off-site</AlertTitle>
								<AlertDescription>{NOT_OFF_SITE_WARNING}</AlertDescription>
							</Alert>
						) : s3 ? (
							<>
								<Field>
									<FieldLabel htmlFor="ib-endpoint">Endpoint</FieldLabel>
									<Input
										id="ib-endpoint"
										autoComplete="off"
										spellCheck={false}
										placeholder="https://s3.example.com (empty for AWS S3)"
										value={draft.endpoint}
										onChange={(e) => set("endpoint", e.target.value)}
									/>
								</Field>
								<div className="grid gap-3 sm:grid-cols-2">
									<Field>
										<FieldLabel htmlFor="ib-bucket">Bucket</FieldLabel>
										<Input
											id="ib-bucket"
											autoComplete="off"
											spellCheck={false}
											value={draft.bucket}
											onChange={(e) => set("bucket", e.target.value)}
										/>
									</Field>
									<Field>
										<FieldLabel htmlFor="ib-region">Region</FieldLabel>
										<Input
											id="ib-region"
											autoComplete="off"
											spellCheck={false}
											placeholder="us-east-1"
											value={draft.region}
											onChange={(e) => set("region", e.target.value)}
										/>
									</Field>
								</div>
								<Field>
									<FieldLabel htmlFor="ib-prefix">Key prefix</FieldLabel>
									<Input
										id="ib-prefix"
										autoComplete="off"
										spellCheck={false}
										placeholder="nebulactrl (optional)"
										value={draft.prefix}
										onChange={(e) => set("prefix", e.target.value)}
									/>
								</Field>
								<CredentialHelp
									guide={backupGuide({
										endpoint: draft.endpoint,
										bucket: draft.bucket,
										prefix: draft.prefix,
									})}
									fieldIds={["ib-access", "ib-secret"]}
								/>
								<div className="grid gap-3 sm:grid-cols-2">
									<Field>
										<FieldLabel htmlFor="ib-access">
											Access key{keysStored ? " (leave empty to keep)" : ""}
										</FieldLabel>
										<Input
											id="ib-access"
											autoComplete="off"
											spellCheck={false}
											className="font-mono"
											placeholder={keysStored ? "••••••••" : ""}
											value={draft.accessKey}
											onChange={(e) => set("accessKey", e.target.value)}
										/>
									</Field>
									<Field>
										<FieldLabel htmlFor="ib-secret">
											Secret key{keysStored ? " (leave empty to keep)" : ""}
										</FieldLabel>
										<Input
											id="ib-secret"
											type="password"
											autoComplete="new-password"
											className="font-mono"
											placeholder={keysStored ? "••••••••" : ""}
											value={draft.secretKey}
											onChange={(e) => set("secretKey", e.target.value)}
										/>
									</Field>
								</div>
								<FieldDescription>
									Keys are stored encrypted and never shown again.
								</FieldDescription>
								<CheckLabel
									checked={draft.pathStyle}
									onCheckedChange={(v) => set("pathStyle", v)}
								>
									Path-style addressing (MinIO, SeaweedFS, Garage and most
									self-hosted stores)
								</CheckLabel>
								<CheckLabel
									checked={draft.compatMode}
									onCheckedChange={(v) => set("compatMode", v)}
								>
									Compatibility mode (needed by most non-AWS stores: limits the
									SDK's default checksums)
								</CheckLabel>
							</>
						) : (
							<>
								<Field>
									<FieldLabel htmlFor="ib-dir">Directory name</FieldLabel>
									<Input
										id="ib-dir"
										autoComplete="off"
										spellCheck={false}
										placeholder="nightly (optional)"
										value={draft.localDir}
										onChange={(e) => set("localDir", e.target.value)}
									/>
									<FieldDescription>
										A sub-directory of{" "}
										<code className="type-mono">NEBULACTRL_BACKUP_DIR</code> on
										the control-plane host.
									</FieldDescription>
								</Field>
								<Alert variant="warning">
									<AlertTitle>Not off-site</AlertTitle>
									<AlertDescription>
										These backups stay on the host that runs the control plane.
										If that host is lost, so are they. Use a directory as a
										first copy next to a bucket, or sync it elsewhere.
									</AlertDescription>
								</Alert>
							</>
						)}
						{error ? <FieldError>{error}</FieldError> : null}
					</DialogBody>
					<DialogFooter>
						<Button
							type="button"
							variant="ghost"
							onClick={() => onOpenChange(false)}
						>
							Cancel
						</Button>
						<Button
							type="submit"
							variant="primary"
							disabled={!valid || save.isPending}
						>
							{save.isPending ? <Spinner /> : null}
							Save destination
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
