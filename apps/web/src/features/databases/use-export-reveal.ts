import { useMutation } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { revealDatabaseExportMutation } from "#/api/gen/@tanstack/react-query.gen";
import { writeClipboard } from "#/components/common/copy-button";
import { toast } from "#/components/ui/toast";
import { REVEAL_MAX_MS, type Reveal, startReveal } from "./reveal";

export type RevealMode = "copy" | "show";

/**
 * Reveals an export of a managed database, to copy or to show for 60 s. The
 * value comes from the reveal endpoint (audited server-side as
 * `secret.reveal`) through a mutation that is reset the moment it resolves,
 * so it never sits in the query or mutation cache; the only copy lives in
 * this hook's state, and only after "show", until it expires. `run` resolves
 * once the value is copied, shown or has failed (a toast says which).
 */
export function useExportReveal(serviceId: string) {
	const reveal = useMutation({ ...revealDatabaseExportMutation(), gcTime: 0 });
	const [shown, setShown] = useState<Reveal | null>(null);
	const [mode, setMode] = useState<RevealMode | null>(null);

	// Gone at the server's expiry (or the client cap), and on unmount.
	useEffect(() => {
		if (shown === null) return;
		const timer = window.setTimeout(
			() => setShown(null),
			Math.max(0, Math.min(shown.clearAt - Date.now(), REVEAL_MAX_MS)),
		);
		return () => window.clearTimeout(timer);
	}, [shown]);

	const run = async (key: string, next: RevealMode, failure: string) => {
		setMode(next);
		try {
			const revealed = await reveal.mutateAsync({
				path: { serviceId },
				body: { key },
			});
			reveal.reset();
			if (next === "show") {
				setShown(startReveal(revealed, Date.now()));
				return;
			}
			if (await writeClipboard(revealed.value)) {
				toast.success("Copied", "Revealed values are audited and not kept.");
			} else {
				toast.error(
					"Nothing was copied",
					"The browser blocked the clipboard. Use Show for 60 s instead.",
				);
			}
		} catch (error) {
			reveal.reset();
			toast.error(failure, errorMessage(error));
		} finally {
			setMode(null);
		}
	};

	return { shown, mode, busy: mode !== null, run, hide: () => setShown(null) };
}
