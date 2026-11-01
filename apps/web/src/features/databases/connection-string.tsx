import { ClipboardCopyIcon, EyeIcon, EyeOffIcon } from "lucide-react";
import type { ReactNode } from "react";
import { Button } from "#/components/ui/button";
import { Input } from "#/components/ui/input";
import { Spinner } from "#/components/ui/spinner";
import { useNow } from "#/features/canvas/inspector/overview/use-now";
import { type Reveal, secondsLeft } from "./reveal";
import { useExportReveal } from "./use-export-reveal";

/** The value on screen, with its countdown. The caller owns (and clears) the state. */
export function RevealedValue({
	reveal,
	onHide,
}: {
	reveal: Reveal;
	onHide(): void;
}) {
	const now = useNow(1_000);
	const left = secondsLeft(reveal, now);
	return (
		<div className="flex flex-col gap-1.5 py-2">
			<div className="flex items-center gap-2">
				<Input
					readOnly
					aria-label={`${reveal.key} (revealed)`}
					value={reveal.value}
					autoComplete="off"
					spellCheck={false}
					className="min-w-0 flex-1 font-mono"
					onFocus={(event) => event.currentTarget.select()}
				/>
				<Button variant="secondary" size="md" onClick={onHide}>
					<EyeOffIcon aria-hidden />
					Hide
				</Button>
			</div>
			<p className="text-2xs text-ink-3" role="timer" aria-live="off">
				Hides itself in {left} s. Revealed values are audited and are not kept.
			</p>
		</div>
	);
}

/**
 * Copy connection string / Show for 60 s. The value comes from the reveal
 * endpoint (audited server-side as `secret.reveal`) through a mutation that is
 * reset the moment it resolves, so it never sits in the query or mutation
 * cache; the only copy lives in this component's state, and only after "Show",
 * until it expires.
 */
export function ConnectionString({
	serviceId,
	exportKey,
	children,
}: {
	serviceId: string;
	/** The primary export, e.g. DATABASE_URL. */
	exportKey: string;
	/** More actions, in the same row as Copy and Show. */
	children?: ReactNode;
}) {
	const { shown, mode, busy, run, hide } = useExportReveal(serviceId);
	const copy = () =>
		void run(exportKey, "copy", "Could not reveal the connection string");
	const show = () =>
		void run(exportKey, "show", "Could not reveal the connection string");
	return (
		<div className="flex flex-col">
			<div className="flex flex-wrap items-center gap-2">
				<Button variant="secondary" size="md" disabled={busy} onClick={copy}>
					{mode === "copy" ? <Spinner /> : <ClipboardCopyIcon aria-hidden />}
					Copy connection string
				</Button>
				{shown === null ? (
					<Button variant="ghost" size="md" disabled={busy} onClick={show}>
						{mode === "show" ? <Spinner /> : <EyeIcon aria-hidden />}
						Show for 60 s
					</Button>
				) : null}
				{children}
			</div>
			{shown ? <RevealedValue reveal={shown} onHide={hide} /> : null}
		</div>
	);
}
