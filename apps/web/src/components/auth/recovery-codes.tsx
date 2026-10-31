import { DownloadIcon } from "lucide-react";
import { useState } from "react";
import { CopyButton } from "#/components/common/copy-button";
import { Button } from "#/components/ui/button";
import { CheckLabel } from "#/components/ui/check-label";
import { recoveryCodesText } from "./auth-messages";

/**
 * The ten single-use recovery codes, shown once. The person must say they
 * stored them before the way out lights up: after this panel closes the codes
 * cannot be shown again, only replaced.
 */
export function RecoveryCodesPanel({
	codes,
	account,
	doneLabel = "Done",
	onDone,
}: {
	codes: readonly string[];
	account: string;
	doneLabel?: string;
	onDone: () => void;
}) {
	const [saved, setSaved] = useState(false);

	const download = () => {
		const blob = new Blob([recoveryCodesText(codes, account)], {
			type: "text/plain",
		});
		const url = URL.createObjectURL(blob);
		const link = document.createElement("a");
		link.href = url;
		link.download = "nebulactrl-recovery-codes.txt";
		link.click();
		URL.revokeObjectURL(url);
	};

	return (
		<div className="flex flex-col gap-4">
			<p className="text-pretty text-ink-3 text-xs leading-relaxed">
				If you lose your authenticator, each of these codes signs you in once.
				NebulaCtrl keeps only hashes, so they cannot be shown again, only
				replaced.
			</p>
			<ul
				aria-label="Recovery codes"
				className="surface-well m-0 grid list-none grid-cols-1 gap-x-6 gap-y-1.5 rounded-card p-3.5 sm:grid-cols-2"
			>
				{codes.map((code) => (
					<li
						key={code}
						className="select-all font-mono text-ink-1 text-sm tabular-nums"
					>
						{code}
					</li>
				))}
			</ul>
			<div className="flex flex-wrap items-center gap-2">
				<CopyButton variant="outline" value={codes.join("\n")}>
					Copy all
				</CopyButton>
				<Button type="button" variant="outline" onClick={download}>
					<DownloadIcon />
					Download .txt
				</Button>
			</div>
			<CheckLabel
				className="items-start gap-2.5 text-sm"
				checkboxClassName="mt-0.5"
				checked={saved}
				onCheckedChange={setSaved}
			>
				I stored these codes somewhere safe
			</CheckLabel>
			<div className="flex justify-end">
				<Button
					type="button"
					variant="primary"
					disabled={!saved}
					onClick={onDone}
				>
					{doneLabel}
				</Button>
			</div>
		</div>
	);
}
