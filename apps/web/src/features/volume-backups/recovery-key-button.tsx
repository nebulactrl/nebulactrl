import { useMutation } from "@tanstack/react-query";
import { KeyRoundIcon } from "lucide-react";
import { errorMessage } from "#/api/client-config";
import { revealVolumeBackupRecoveryKey } from "#/api/gen/sdk.gen";
import { useConfirm } from "#/components/common/confirm-dialog";
import { Button } from "#/components/ui/button";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { recoveryKeyFile } from "./model";

/** Hands `text` to the browser as a file download. */
function downloadText(filename: string, text: string): void {
	const url = URL.createObjectURL(new Blob([text], { type: "text/plain" }));
	const a = document.createElement("a");
	a.href = url;
	a.download = filename;
	document.body.append(a);
	a.click();
	a.remove();
	URL.revokeObjectURL(url);
}

/**
 * Reveals the repository key of a service's volume backups once and offers it
 * as a text file. Nothing is kept on the page afterwards.
 */
export function RecoveryKeyButton({
	serviceId,
	serviceSlug,
}: {
	serviceId: string;
	serviceSlug: string;
}) {
	const confirm = useConfirm();
	const reveal = useMutation({
		mutationFn: async () => {
			const { data } = await revealVolumeBackupRecoveryKey({
				path: { serviceId },
				body: {},
			});
			const file = recoveryKeyFile(data, serviceSlug);
			downloadText(file.filename, file.text);
		},
		onSuccess: () =>
			toast.success(
				"Recovery key downloaded",
				"Store the file apart from the backups.",
			),
		onError: (e) =>
			toast.error("Could not reveal the recovery key", errorMessage(e)),
	});
	return (
		<Button
			type="button"
			variant="secondary"
			size="sm"
			disabled={reveal.isPending}
			onClick={() =>
				void confirm({
					title: "Download the recovery key?",
					body: "The key is shown once, as a file, and the reveal is recorded in the audit log. Anyone with the file and access to the backups can read your files: keep it apart from them.",
					cta: "Download key",
					onConfirm: async () => {
						await reveal.mutateAsync();
					},
				})
			}
		>
			{reveal.isPending ? <Spinner /> : <KeyRoundIcon aria-hidden />}
			Download recovery key
		</Button>
	);
}
