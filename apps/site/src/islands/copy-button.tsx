import { CheckIcon } from "@phosphor-icons/react/dist/csr/Check";
import { CopyIcon } from "@phosphor-icons/react/dist/csr/Copy";
import { useEffect, useRef, useState } from "react";
import { MotionKey, Swap } from "./motion/key.tsx";

const COPIED_MS = 1600;

async function writeClipboard(text: string): Promise<boolean> {
	try {
		await navigator.clipboard.writeText(text);
		return true;
	} catch {
		// Insecure context or denied permission: fall back to a selection copy.
		const area = document.createElement("textarea");
		area.value = text;
		area.setAttribute("readonly", "");
		area.style.position = "fixed";
		area.style.opacity = "0";
		document.body.append(area);
		area.select();
		let ok = false;
		try {
			ok = document.execCommand("copy");
		} catch {
			ok = false;
		}
		area.remove();
		return ok;
	}
}

/** The Copy key: a raised accent key with a spring press, its icon and label swapping in place (islands/motion/key.tsx). Without JavaScript it is hidden and the command stays selectable text. */
export default function CopyButton({
	text,
	label = "Copy install command",
	compact = false,
}: {
	text: string;
	label?: string;
	/** A smaller key for a narrow well (from lg up). */
	compact?: boolean;
}) {
	const [state, setState] = useState<"idle" | "copied" | "failed">("idle");
	const timer = useRef<number | undefined>(undefined);
	useEffect(() => () => window.clearTimeout(timer.current), []);

	const onClick = async () => {
		const ok = await writeClipboard(text);
		setState(ok ? "copied" : "failed");
		window.clearTimeout(timer.current);
		timer.current = window.setTimeout(() => setState("idle"), COPIED_MS);
	};

	const status =
		state === "copied"
			? "Copied"
			: state === "failed"
				? "Copy failed. Press Ctrl+C"
				: "";
	return (
		<MotionKey
			type="button"
			onClick={onClick}
			aria-label={label}
			className={`btn-primary inline-flex h-10 shrink-0 cursor-pointer items-center gap-2 rounded py-0 pr-3.5 pl-3 font-semibold text-sm ${compact ? "lg:h-8 lg:gap-1.5 lg:pr-3 lg:pl-2.5" : ""}`}
		>
			<span aria-hidden className="inline-flex">
				<Swap swapKey={state === "copied" ? "check" : "copy"}>
					{state === "copied" ? (
						<CheckIcon weight="bold" className="size-4" />
					) : (
						<CopyIcon className="size-4" />
					)}
				</Swap>
			</span>
			<span aria-hidden className="inline-flex">
				<Swap swapKey={state}>
					{state === "copied"
						? "Copied"
						: state === "failed"
							? "Ctrl+C"
							: "Copy"}
				</Swap>
			</span>
			<span className="sr-only" aria-live="polite">
				{status}
			</span>
		</MotionKey>
	);
}
