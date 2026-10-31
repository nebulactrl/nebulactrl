import { useMutation } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { errorCode, errorMessage } from "#/api/client-config";
import { revealDatabasePublicConnectionMutation } from "#/api/gen/@tanstack/react-query.gen";
import { writeClipboard } from "#/components/common/copy-button";
import { toast } from "#/components/ui/toast";
import { revealProblem } from "./public-port";
import { REVEAL_MAX_MS, type Reveal, startReveal } from "./reveal";
import type { RevealMode } from "./use-export-reveal";

/**
 * Reveals the external connection string of a database's public port (with
 * the password in it), to copy or to show for 60 s. Like `useExportReveal`
 * the value comes through a mutation that is reset the moment it resolves, so
 * it never sits in the query or mutation cache; the only copy lives in this
 * hook's state, and only after "show", until it expires. A refusal stays in
 * `problem` (the guidance for a server without a public address belongs next
 * to the control, not in a toast that goes away).
 */
export function usePublicReveal(serviceId: string) {
	const reveal = useMutation({
		...revealDatabasePublicConnectionMutation(),
		gcTime: 0,
	});
	const [shown, setShown] = useState<Reveal | null>(null);
	const [mode, setMode] = useState<RevealMode | null>(null);
	const [problem, setProblem] = useState<string | null>(null);

	// Gone at the server's expiry (or the client cap), and on unmount.
	useEffect(() => {
		if (shown === null) return;
		const timer = window.setTimeout(
			() => setShown(null),
			Math.max(0, Math.min(shown.clearAt - Date.now(), REVEAL_MAX_MS)),
		);
		return () => window.clearTimeout(timer);
	}, [shown]);

	const run = async (next: RevealMode) => {
		setMode(next);
		setProblem(null);
		try {
			const revealed = await reveal.mutateAsync({
				path: { serviceId },
				body: {},
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
			setProblem(revealProblem(errorCode(error), errorMessage(error)));
		} finally {
			setMode(null);
		}
	};

	return {
		shown,
		mode,
		busy: mode !== null,
		problem,
		run,
		hide: () => setShown(null),
	};
}
