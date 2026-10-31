import { useEffect, useState } from "react";

/**
 * The mobile menu and the docs sidebar drawer share one toggle. It flips
 * `data-open` on the panel element named by `controls` (plain HTML that exists
 * without JavaScript), closes on Escape and when a link inside the panel is used.
 * The three bars rotate into an X, they do not swap icons.
 */
export default function MenuToggle({
	controls,
	label,
	text,
}: {
	controls: string;
	label: string;
	/** A visible word beside the bars, for a toggle that sits near another one. The label should contain it. */
	text?: string;
}) {
	const [open, setOpen] = useState(false);

	useEffect(() => {
		const panel = document.getElementById(controls);
		if (!panel) return;
		panel.dataset.open = open ? "true" : "false";
		if (!open) return;
		// The full screen menu covers the page: keep the page from scrolling under it.
		const overflow = document.body.style.overflow;
		if (panel.classList.contains("menu-overlay"))
			document.body.style.overflow = "hidden";
		const onKey = (event: KeyboardEvent) => {
			if (event.key === "Escape") setOpen(false);
		};
		const onClick = (event: MouseEvent) => {
			if ((event.target as HTMLElement).closest("a[href]")) setOpen(false);
		};
		document.addEventListener("keydown", onKey);
		panel.addEventListener("click", onClick);
		return () => {
			document.body.style.overflow = overflow;
			document.removeEventListener("keydown", onKey);
			panel.removeEventListener("click", onClick);
		};
	}, [open, controls]);

	return (
		<button
			type="button"
			aria-expanded={open}
			aria-controls={controls}
			aria-label={open ? `Close ${label}` : `Open ${label}`}
			onClick={() => setOpen((v) => !v)}
			className={`pressable relative inline-flex h-10 cursor-pointer items-center justify-center rounded-full text-ink-1 hover:bg-surface-pop ${text ? "gap-2 px-3" : "w-10"}`}
		>
			<span aria-hidden className="relative block h-3 w-5">
				<span
					className="absolute left-0 h-0.5 w-5 rounded-full bg-current transition-transform duration-200 ease-[cubic-bezier(0.23,1,0.32,1)]"
					style={{
						top: open ? 5 : 0,
						transform: open ? "rotate(45deg)" : "none",
					}}
				/>
				<span
					className="absolute top-[5px] left-0 h-0.5 w-5 rounded-full bg-current transition-opacity duration-150"
					style={{ opacity: open ? 0 : 1 }}
				/>
				<span
					className="absolute left-0 h-0.5 w-5 rounded-full bg-current transition-transform duration-200 ease-[cubic-bezier(0.23,1,0.32,1)]"
					style={{
						top: open ? 5 : 10,
						transform: open ? "rotate(-45deg)" : "none",
					}}
				/>
			</span>
			{text ? <span className="font-semibold text-sm">{text}</span> : null}
		</button>
	);
}
