import { useMutation, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useEffect, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { updateTeamSliceQuota } from "#/api/gen/sdk.gen";
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
import { Field, FieldError, FieldLabel } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { gibText, parseQuotaGib, sizeText, type TeamRow } from "./model";
import { invalidateInstanceServices } from "./queries";

/**
 * One team's own backup quota. Leave it at the default with "Use the default";
 * a quota under what the team stores already only blocks its new backups.
 */
export function QuotaDialog({
	row,
	defaultQuotaBytes,
	onClose,
}: {
	row: TeamRow | null;
	defaultQuotaBytes: number;
	onClose: () => void;
}) {
	const queryClient = useQueryClient();
	const slice = row?.backup;
	const [text, setText] = useState("");
	const [error, setError] = useState<string | null>(null);

	useEffect(() => {
		if (!slice) return;
		setText(gibText(slice.effectiveQuotaBytes));
		setError(null);
	}, [slice]);

	const save = useMutation({
		mutationFn: async (quotaBytes: number | null) => {
			if (!slice) return;
			await updateTeamSliceQuota({
				path: { sliceId: slice.id },
				body: { quotaBytes },
				throwOnError: true,
			});
		},
		onSuccess: async (_, quotaBytes) => {
			await invalidateInstanceServices(queryClient);
			toast.success(
				quotaBytes === null ? "Back on the default quota" : "Quota saved",
				row?.teamName,
			);
			onClose();
		},
		onError: (e) => setError(errorMessage(e)),
	});

	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (save.isPending) return;
		const parsed = parseQuotaGib(text);
		if (!parsed.ok) {
			setError(parsed.message);
			return;
		}
		setError(null);
		save.mutate(parsed.bytes);
	};

	const typed = parseQuotaGib(text);
	const overUse =
		slice !== undefined && typed.ok && typed.bytes < slice.usedBytes;

	return (
		<Dialog open={row !== null} onOpenChange={(o) => (o ? null : onClose())}>
			<DialogContent size="md">
				<form className="contents" onSubmit={submit}>
					<DialogHeader>
						<DialogTitle>Backup quota for {row?.teamName}</DialogTitle>
						<DialogDescription>
							The most this team's backups may take. The default is{" "}
							{sizeText(defaultQuotaBytes)}.
						</DialogDescription>
					</DialogHeader>
					<DialogBody>
						<Field>
							<FieldLabel htmlFor="team-quota-gib">Quota (GiB)</FieldLabel>
							<Input
								id="team-quota-gib"
								scale="lg"
								inputMode="decimal"
								autoComplete="off"
								className="w-40 font-mono"
								aria-invalid={error !== null}
								value={text}
								onChange={(e) => {
									setText(e.target.value);
									setError(null);
								}}
							/>
							{error ? <FieldError>{error}</FieldError> : null}
						</Field>
						{overUse && slice ? (
							<p className="m-0 text-warning-text text-xs leading-relaxed">
								The team already stores {sizeText(slice.usedBytes)}. Its new
								backups fail until it is under the quota; nothing stored is
								deleted.
							</p>
						) : null}
					</DialogBody>
					<DialogFooter>
						{slice &&
						slice.quotaBytes !== null &&
						slice.quotaBytes !== undefined ? (
							<Button
								type="button"
								variant="ghost"
								className="mr-auto"
								disabled={save.isPending}
								onClick={() => save.mutate(null)}
							>
								Use the default
							</Button>
						) : null}
						<Button type="button" variant="ghost" onClick={onClose}>
							Cancel
						</Button>
						<Button type="submit" variant="primary" disabled={save.isPending}>
							{save.isPending ? <Spinner /> : null}
							Save quota
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
