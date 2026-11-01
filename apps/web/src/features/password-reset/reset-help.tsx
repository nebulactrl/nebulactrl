import { Link } from "@tanstack/react-router";
import { RESET_COMMAND } from "./messages";

/**
 * What to do when reset by email is off: an admin can generate a link, and the
 * only admin of an installation can print one on the host. Shown on the sign-in
 * page and on the request page, where there is nobody else to ask.
 */
export function ResetByHostHelp({ lead }: { lead: string }) {
	return (
		<>
			{lead} Running the only admin account? On the host, run{" "}
			<code className="rounded-[4px] bg-surface-well px-1 py-0.5 font-mono text-[11px] text-ink-2">
				{RESET_COMMAND}
			</code>{" "}
			to print a reset link.{" "}
			<Link
				to="/docs/$"
				params={{ _splat: "operations/troubleshooting" }}
				hash="locked-out"
				className="text-accent-ink underline"
			>
				How
			</Link>
		</>
	);
}
