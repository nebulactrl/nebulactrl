import { useEffect, useRef, useState } from "react";
import type { EnrollmentToken } from "#/api/gen/types.gen";
import { COPIED_MS, writeClipboard } from "#/components/common/copy-button";
import { Relative } from "#/components/common/relative";
import { CopyCheckIcon } from "#/components/icons/animated/copy-check-icon";
import { TextMorph } from "#/components/motion/text-morph";
import { Field, FieldDescription, FieldLabel } from "#/components/ui/field";
import { lastInputWasKeyboard } from "#/lib/input-modality";
import { commandLines, enrollmentExpired } from "./enrollment";

/**
 * A key that copies: the secondary key with its travel (a
 * keycap means a shortcut), with a copy icon that swaps to a check and
 * "Copied" for 1.5s. Over plain http the clipboard API is missing, so it
 * says "Select and copy" instead of pretending.
 */
export function CopyKey({ value }: { value: string }) {
	const [state, setState] = useState<"idle" | "copied" | "failed">("idle");
	const [down, setDown] = useState(false);
	const [instant, setInstant] = useState(false);
	const timeoutRef = useRef<number | undefined>(undefined);
	useEffect(() => () => window.clearTimeout(timeoutRef.current), []);

	const copy = async () => {
		setInstant(lastInputWasKeyboard());
		const ok = await writeClipboard(value);
		setState(ok ? "copied" : "failed");
		window.clearTimeout(timeoutRef.current);
		timeoutRef.current = window.setTimeout(() => setState("idle"), COPIED_MS);
	};
	const release = () => setDown(false);

	return (
		<>
			<button
				type="button"
				data-pressed={down || undefined}
				onClick={() => void copy()}
				onPointerDown={() => setDown(true)}
				onPointerUp={release}
				onPointerLeave={release}
				onPointerCancel={release}
				className="btn-secondary pressable inline-flex h-7 min-w-[92px] cursor-pointer items-center justify-center gap-1.5 rounded-md px-2.5 text-xs outline-none focus-visible:focus-ring pointer-coarse:h-9"
			>
				<CopyCheckIcon
					copied={state === "copied"}
					instant={instant}
					className="size-3.5"
					checkClassName="text-ok"
				/>
				<TextMorph
					instant={instant}
					className={state === "copied" ? "text-ok" : undefined}
					text={
						state === "copied"
							? "Copied"
							: state === "failed"
								? "Select and copy"
								: "Copy"
					}
				/>
			</button>
			<span className="sr-only" role="status" aria-live="polite">
				{state === "copied" ? "Copied" : ""}
			</span>
		</>
	);
}

/** Re-renders once the token's expiry passes, so "expires in" turns into "expired". No token counts as expired. */
export function useEnrollmentExpired(
	enrollment: EnrollmentToken | undefined,
): boolean {
	const [, setTick] = useState(0);
	const expiresAt = enrollment?.expiresAt;
	useEffect(() => {
		if (expiresAt === undefined) return;
		const left = Date.parse(expiresAt) - Date.now();
		// setTimeout caps at about 24 days; a token lives an hour.
		if (left <= 0) return;
		const id = window.setTimeout(() => setTick((tick) => tick + 1), left + 50);
		return () => window.clearTimeout(id);
	}, [expiresAt]);
	return enrollment === undefined || enrollmentExpired(enrollment);
}

/**
 * The install command in an engraved well with a key to copy it. One flag
 * per line, so a narrow screen reads it as a list of arguments and scrolls
 * the long token sideways instead of breaking it mid-word.
 */
export function InstallCommandField({
	enrollment,
}: {
	enrollment: EnrollmentToken;
}) {
	const expired = useEnrollmentExpired(enrollment);
	const lines = commandLines(enrollment.installCommand);
	return (
		<Field>
			<FieldLabel>Run this on the host</FieldLabel>
			<div className="surface-well flex items-start gap-3 rounded-card py-3 pr-3 pl-3.5">
				{/* A read-only text box: focusable, scrolls sideways by keyboard, and
				    is announced as what it is. One flag per line, never wrapped. */}
				<textarea
					readOnly
					wrap="off"
					spellCheck={false}
					rows={lines.length}
					aria-label="Install command"
					value={lines.join("\n")}
					className="m-0 block min-w-0 flex-1 resize-none select-all overflow-x-auto whitespace-pre rounded-sm border-0 bg-transparent p-0 font-mono text-ink-1 text-xs leading-[1.7] outline-none focus-visible:focus-ring"
				/>
				<CopyKey value={enrollment.installCommand} />
			</div>
			<FieldDescription>
				{expired ? (
					"This command has expired. Ask for a new one."
				) : (
					<>
						Needs Docker and a root shell. The token works once and expires{" "}
						<Relative iso={enrollment.expiresAt} />.
					</>
				)}
			</FieldDescription>
		</Field>
	);
}
