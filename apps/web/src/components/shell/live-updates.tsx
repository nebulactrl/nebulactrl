import { useQueryClient } from "@tanstack/react-query";
import { AnimatePresence, motion } from "motion/react";
import { useEffect } from "react";
import { startEventsFeed, useEventsStatus } from "#/api/sse";
import { ToneIcon } from "#/components/common/tone-icon";
import { Spinner } from "#/components/ui/spinner";

/**
 * Keeps the `/events` invalidation feed open for as long as the shell is
 * mounted, and says so when it is not: queries keep polling every 15 s, so
 * the pill is a promise about immediacy, not about correctness.
 */
export function LiveUpdates() {
	const queryClient = useQueryClient();
	useEffect(() => startEventsFeed(queryClient), [queryClient]);
	const status = useEventsStatus();
	const paused = status === "paused";
	return (
		<AnimatePresence>
			{paused ? (
				<motion.div
					key="live-updates-paused"
					role="status"
					initial={{ opacity: 0, y: -6 }}
					animate={{ opacity: 1, y: 0 }}
					exit={{ opacity: 0, y: -6 }}
					transition={{ duration: 0.15, ease: "easeOut" }}
					data-tone="warning"
					className="surface-pop tone-callout pointer-events-none fixed top-[60px] left-1/2 z-50 flex -translate-x-1/2 items-center gap-2 rounded-full px-3 py-1.5 text-[12px] text-ink-2 leading-none"
				>
					<ToneIcon tone="warning" />
					Live updates paused — retrying
					<Spinner className="size-3" />
				</motion.div>
			) : null}
		</AnimatePresence>
	);
}
