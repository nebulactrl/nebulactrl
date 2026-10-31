import { useMutation, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useEffect, useState } from "react";
import { errorMessage } from "#/api/client-config";
import {
	createBackupDestination,
	updateBackupDestination,
} from "#/api/gen/sdk.gen";
import type { DestinationView } from "#/api/gen/types.gen";
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
import { invalidateBackups } from "./queries";

type Kind = "s3" | "local";

interface Draft {
	name: string;
	kind: Kind;
	endpoint: string;
	/** Amazon S3 itself: no endpoint to enter, and the IAM guidance applies. */
	awsS3: boolean;
	region: string;
	bucket: string;
	prefix: string;
	pathStyle: boolean;
	compatMode: boolean;
	accessKey: string;
	secretKey: string;
	localDir: string;
}

function draftFor(d: DestinationView | null): Draft {
	return {
		name: d?.name ?? "",
		kind: (d?.kind as Kind | undefined) ?? "s3",
		endpoint: d?.endpoint ?? "",
		awsS3: d ? (d.endpoint ?? "") === "" : false,
		region: d?.region ?? "",
		bucket: d?.bucket ?? "",
		prefix: d?.prefix ?? "",
		pathStyle: d?.pathStyle ?? false,
		compatMode: d?.compatMode ?? true,
		accessKey: "",
		secretKey: "",
		localDir: d?.localDir ?? "",
	};
}

/** Create or edit a backup destination. Keys are write-only: an edit leaves them empty to keep the stored ones. */
export function DestinationDialog({
	open,
	onOpenChange,
	destination,
}: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	destination: DestinationView | null;
}) {
	const queryClient = useQueryClient();
	const editing = destination !== null;
	const [draft, setDraft] = useState<Draft>(() => draftFor(destination));
	const [error, setError] = useState<string | null>(null);

	useEffect(() => {
		if (open) {
			setDraft(draftFor(destination));
			setError(null);
		}
	}, [open, destination]);

	const set = <K extends keyof Draft>(key: K, value: Draft[K]) =>
		setDraft((prev) => ({ ...prev, [key]: value }));

	const save = useMutation({
		mutationFn: async () => {
			const d = draft;
			if (destination) {
				await updateBackupDestination({
					path: { destinationId: destination.id },
					body: {
						name: d.name.trim(),
						...(d.kind === "s3"
							? {
									endpoint: d.awsS3 ? "" : d.endpoint.trim(),
									region: d.region.trim(),
									bucket: d.bucket.trim(),
									prefix: d.prefix.trim(),
									pathStyle: d.pathStyle,
									compatMode: d.compatMode,
									accessKey: d.accessKey,
									secretKey: d.secretKey,
								}
							: { localDir: d.localDir.trim(), prefix: d.prefix.trim() }),
					},
				});
				return;
			}
			await createBackupDestination({
				body:
					d.kind === "s3"
						? {
								name: d.name.trim(),
								kind: "s3",
								endpoint: d.awsS3 ? "" : d.endpoint.trim(),
								region: d.region.trim(),
								bucket: d.bucket.trim(),
								prefix: d.prefix.trim(),
								pathStyle: d.pathStyle,
								compatMode: d.compatMode,
								accessKey: d.accessKey,
								secretKey: d.secretKey,
							}
						: {
								name: d.name.trim(),
								kind: "local",
								localDir: d.localDir.trim(),
								prefix: d.prefix.trim(),
							},
			});
		},
		onSuccess: async () => {
			toast.success(
				editing ? "Destination saved" : "Destination added",
				"Test it to confirm the agent can write there.",
			);
			await invalidateBackups(queryClient);
			onOpenChange(false);
		},
		onError: (e) => setError(errorMessage(e)),
	});

	const s3 = draft.kind === "s3";
	const aws = draft.awsS3;
	const valid =
		draft.name.trim() !== "" &&
		(s3
			? draft.bucket.trim() !== "" &&
				(aws || draft.endpoint.trim() !== "") &&
				(editing || (draft.accessKey !== "" && draft.secretKey !== ""))
			: true);

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
						<DialogTitle>
							{editing ? `Edit ${destination.name}` : "Add backup destination"}
						</DialogTitle>
						<DialogDescription>
							Where database backups are stored. An S3-compatible bucket
							survives the loss of a server; a directory on the server does not.
						</DialogDescription>
					</DialogHeader>
					<DialogBody>
						<Field>
							<FieldLabel htmlFor="dest-name">Name</FieldLabel>
							<Input
								id="dest-name"
								autoFocus
								autoComplete="off"
								value={draft.name}
								onChange={(e) => set("name", e.target.value)}
							/>
						</Field>
						{editing ? null : (
							<Field>
								<FieldLabel id="dest-kind-label">Type</FieldLabel>
								<Segmented<Kind>
									aria-label="Destination type"
									font="sans"
									density="regular"
									value={draft.kind}
									onValueChange={(v) => set("kind", v)}
									options={[
										{ value: "s3", label: "S3-compatible bucket" },
										{ value: "local", label: "Directory on the server" },
									]}
								/>
							</Field>
						)}
						{s3 ? (
							<>
								{editing ? null : (
									<Field>
										<FieldLabel id="dest-store-label">Store</FieldLabel>
										<Segmented<"aws" | "other">
											aria-label="Store"
											font="sans"
											density="regular"
											value={aws ? "aws" : "other"}
											onValueChange={(v) => set("awsS3", v === "aws")}
											options={[
												{ value: "other", label: "Other S3-compatible" },
												{ value: "aws", label: "Amazon S3" },
											]}
										/>
										<FieldDescription>
											R2, Backblaze B2, Hetzner, Wasabi, MinIO and the like need
											an endpoint.
										</FieldDescription>
									</Field>
								)}
								{aws ? null : (
									<Field>
										<FieldLabel htmlFor="dest-endpoint">Endpoint</FieldLabel>
										<Input
											id="dest-endpoint"
											autoComplete="off"
											spellCheck={false}
											placeholder="https://s3.example.com"
											value={draft.endpoint}
											onChange={(e) => set("endpoint", e.target.value)}
											disabled={editing && destination.storedBackups > 0}
										/>
									</Field>
								)}
								<div className="grid gap-3 sm:grid-cols-2">
									<Field>
										<FieldLabel htmlFor="dest-bucket">Bucket</FieldLabel>
										<Input
											id="dest-bucket"
											autoComplete="off"
											spellCheck={false}
											value={draft.bucket}
											onChange={(e) => set("bucket", e.target.value)}
											disabled={editing && destination.storedBackups > 0}
										/>
									</Field>
									<Field>
										<FieldLabel htmlFor="dest-region">
											Region{aws ? "" : " (optional)"}
										</FieldLabel>
										<Input
											id="dest-region"
											autoComplete="off"
											spellCheck={false}
											placeholder={aws ? "us-east-1" : ""}
											value={draft.region}
											onChange={(e) => set("region", e.target.value)}
										/>
									</Field>
								</div>
								<Field>
									<FieldLabel htmlFor="dest-prefix">Key prefix</FieldLabel>
									<Input
										id="dest-prefix"
										autoComplete="off"
										spellCheck={false}
										placeholder="nebulactrl/production (optional)"
										value={draft.prefix}
										onChange={(e) => set("prefix", e.target.value)}
										disabled={editing && destination.storedBackups > 0}
									/>
								</Field>
								{aws || draft.endpoint.trim() !== "" ? (
									<CredentialHelp
										guide={backupGuide({
											endpoint: aws ? "" : draft.endpoint,
											bucket: draft.bucket,
											prefix: draft.prefix,
										})}
										fieldIds={["dest-access", "dest-secret"]}
									/>
								) : null}
								<div className="grid gap-3 sm:grid-cols-2">
									<Field>
										<FieldLabel htmlFor="dest-access">
											Access key{editing ? " (leave empty to keep)" : ""}
										</FieldLabel>
										<Input
											id="dest-access"
											autoComplete="off"
											spellCheck={false}
											className="font-mono"
											placeholder={
												editing && destination.accessKeySet ? "••••••••" : ""
											}
											value={draft.accessKey}
											onChange={(e) => set("accessKey", e.target.value)}
										/>
									</Field>
									<Field>
										<FieldLabel htmlFor="dest-secret">
											Secret key{editing ? " (leave empty to keep)" : ""}
										</FieldLabel>
										<Input
											id="dest-secret"
											type="password"
											autoComplete="new-password"
											className="font-mono"
											placeholder={
												editing && destination.secretKeySet ? "••••••••" : ""
											}
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
									<FieldLabel htmlFor="dest-dir">Directory name</FieldLabel>
									<Input
										id="dest-dir"
										autoComplete="off"
										spellCheck={false}
										placeholder="nightly (optional)"
										value={draft.localDir}
										onChange={(e) => set("localDir", e.target.value)}
										disabled={editing && destination.storedBackups > 0}
									/>
									<FieldDescription>
										A sub-directory of the agent's backup directory (
										<code className="type-mono">--backup-dir</code>, default{" "}
										<code className="type-mono">
											/var/lib/nebulactrl-agent/backups
										</code>
										).
									</FieldDescription>
								</Field>
								<Alert variant="warning">
									<AlertTitle>Not off-site</AlertTitle>
									<AlertDescription>
										Backups stay on the database's own server. If that server is
										lost, so are they. Use this as a first copy, next to a
										bucket.
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
							{editing ? "Save" : "Add destination"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
