import { cn } from "cn";
import { type ReactNode, type RefObject, useRef, useState } from "react";
import { LogoMark } from "#/components/brand/logo-mark";
import { AsciiBackdrop } from "#/components/common/ascii/ascii-backdrop";
import { PanelCard } from "#/components/ui/panel-card";

/** The wordmark heading every screen outside the console, at 190x59. */
export function AuthLogo() {
	return <LogoMark wordmark size={59} />;
}

/** Build-time release, when the deployment sets VITE_APP_VERSION. Absent: no caption. */
const VERSION = import.meta.env.VITE_APP_VERSION;

/**
 * A full-screen page: setup and sign-in. The animated ASCII Eclipse sits behind
 * `cardRef`'s panel; the halftone floor is what shows until it paints, and
 * stays when WebGL2 is unavailable.
 */
export function AuthScreen({
	children,
	className,
	cardRef,
	quietRef,
}: {
	children: ReactNode;
	className?: string;
	cardRef: RefObject<HTMLElement | null>;
	quietRef?: RefObject<HTMLElement | null> | undefined;
}) {
	const [ascii, setAscii] = useState(false);
	return (
		<div
			data-ascii={ascii ? "on" : undefined}
			className="ht-arc min-h-svh overflow-hidden text-ink-1"
		>
			<AsciiBackdrop
				cardRef={cardRef}
				quietRef={quietRef}
				active={ascii}
				onActiveChange={setAscii}
			/>
			<main className={cn("relative min-h-svh", className)}>{children}</main>
			{VERSION ? (
				// A solid chip: its contrast never depends on the dots behind it.
				<span
					aria-hidden
					className="absolute right-7 bottom-6 rounded-[5px] bg-surface-well px-2 py-1.5 font-mono text-[10.5px] text-ink-4 uppercase leading-none tracking-[0.14em]"
				>
					v{VERSION}
				</span>
			) : null}
		</div>
	);
}

/**
 * The centred panel with the wordmark above it. `width` is the panel's max
 * width in px.
 */
export function AuthCard({
	children,
	className,
	width = 400,
}: {
	children: ReactNode;
	className?: string;
	width?: 400 | 440;
}) {
	const cardRef = useRef<HTMLDivElement>(null);
	const logoRef = useRef<HTMLDivElement>(null);
	return (
		<AuthScreen
			cardRef={cardRef}
			quietRef={logoRef}
			// Anchored near the middle, not centred on the card: a notice that
			// appears above the fields grows the card downwards and nothing jumps.
			className="flex flex-col items-center justify-start gap-5 px-5 pt-[max(2.5rem,calc(50svh-17rem))] pb-10"
		>
			<div ref={logoRef} className="flex">
				<AuthLogo />
			</div>
			<PanelCard
				ref={cardRef}
				className={cn(
					"gap-4",
					width === 440 ? "w-[min(440px,100%)]" : "w-[min(400px,100%)]",
					className,
				)}
			>
				{children}
			</PanelCard>
		</AuthScreen>
	);
}
