// beui.dev/components/motion/expanding-arrow-button
// MIT © 2026 Saurabh Chauhan
//
// Re-skinned for the site: the `btn-secondary` key with an accent chip, 44px
// high. Hover and focus drive the expansion from CSS (`group-hover` is already
// gated to hover-capable devices by Tailwind), and the press is the shared
// `pressable` 0.96, so it needs no script and renders as plain server markup.
import { cn } from "cn";
import type { ComponentProps, CSSProperties, ReactNode } from "react";

const ARROW_OPACITY = [1, 0.78, 0.54, 0.32, 0.16] as const;

interface OwnProps {
	children: ReactNode;
	/** Extra classes for the accent chip. */
	accentClassName?: string | undefined;
	/** Extra classes for the label. */
	labelClassName?: string | undefined;
}

/** A `<button>` by default; pass `href` for an `<a>` with the same look. */
export type ExpandingArrowButtonProps = OwnProps &
	(
		| (Omit<ComponentProps<"button">, "children"> & { href?: undefined })
		| (Omit<ComponentProps<"a">, "children"> & { href: string })
	);

function DottedChevron({ className }: { className?: string }) {
	return (
		<svg
			viewBox="0 0 20 28"
			fill="none"
			aria-hidden="true"
			className={className}
		>
			<circle cx="4" cy="4" r="2" fill="currentColor" />
			<circle cx="10" cy="9" r="2" fill="currentColor" />
			<circle cx="16" cy="14" r="2" fill="currentColor" />
			<circle cx="10" cy="19" r="2" fill="currentColor" />
			<circle cx="4" cy="24" r="2" fill="currentColor" />
		</svg>
	);
}

const ROOT =
	"btn-secondary pressable group relative inline-flex h-11 cursor-pointer select-none items-center overflow-hidden rounded-card text-ink-1 outline-none focus-visible:focus-ring disabled:pointer-events-none disabled:opacity-50";

const CHIP =
	"absolute inset-y-[3px] left-[3px] z-10 w-9 overflow-hidden rounded-ctl border border-accent-fill-edge bg-linear-to-b from-accent-fill-top to-accent-fill-bot text-white shadow-accent-key transition-[width] duration-[320ms] ease-response group-hover:w-[calc(100%-6px)] group-focus-visible:w-[calc(100%-6px)]";

const LABEL =
	"relative z-0 mr-4 ml-[53px] whitespace-nowrap font-medium text-base tracking-[-0.01em] transition-[opacity,translate] duration-[160ms] ease-response group-hover:translate-x-1.5 group-hover:opacity-0 group-focus-visible:translate-x-1.5 group-focus-visible:opacity-0";

function Face({
	children,
	accentClassName,
	labelClassName,
}: OwnProps): ReactNode {
	return (
		<>
			<span aria-hidden="true" className={cn(CHIP, accentClassName)}>
				<span className="absolute inset-0 grid place-items-center transition-opacity duration-100 ease-response group-hover:opacity-0 group-focus-visible:opacity-0">
					<DottedChevron className="h-5 w-3.5" />
				</span>
				<span className="absolute inset-0 flex items-center justify-around px-2">
					{ARROW_OPACITY.map((opacity, index) => (
						<span
							key={opacity}
							style={
								{
									color: `rgb(255 255 255 / ${opacity})`,
									"--d": `${40 + index * 25}ms`,
								} as CSSProperties
							}
							className="inline-grid -translate-x-1.5 place-items-center opacity-0 transition-[opacity,translate] duration-[180ms] ease-response group-hover:translate-x-0 group-hover:opacity-100 group-hover:[transition-delay:var(--d)] group-focus-visible:translate-x-0 group-focus-visible:opacity-100 group-focus-visible:[transition-delay:var(--d)]"
						>
							<DottedChevron className="h-5 w-3.5" />
						</span>
					))}
				</span>
			</span>
			<span className={cn(LABEL, labelClassName)}>{children}</span>
		</>
	);
}

export function ExpandingArrowButton(props: ExpandingArrowButtonProps) {
	if (props.href !== undefined) {
		const { children, accentClassName, labelClassName, className, ...anchor } =
			props;
		return (
			<a {...anchor} className={cn(ROOT, className)}>
				<Face accentClassName={accentClassName} labelClassName={labelClassName}>
					{children}
				</Face>
			</a>
		);
	}
	const { children, accentClassName, labelClassName, className, ...button } =
		props;
	return (
		<button type="button" {...button} className={cn(ROOT, className)}>
			<Face accentClassName={accentClassName} labelClassName={labelClassName}>
				{children}
			</Face>
		</button>
	);
}
