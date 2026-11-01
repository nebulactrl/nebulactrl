import type { ReactNode } from "react";

/** A page-level section: the `■ TITLE ····· aside` head over its content. */
export function Block({
	title,
	aside,
	children,
}: {
	title: string;
	aside?: ReactNode;
	children: ReactNode;
}) {
	return (
		<section aria-label={title} className="flex min-w-0 flex-col gap-3">
			<h2 className="section-head">
				{title}
				{aside ? (
					<span className="font-sans text-xs normal-case tracking-normal">
						{aside}
					</span>
				) : null}
			</h2>
			{children}
		</section>
	);
}

/** A quiet engraved note: guidance, not an alert. */
export function Note({ children }: { children: ReactNode }) {
	return (
		<p className="surface-well m-0 rounded-card p-3 text-ink-3 text-xs leading-relaxed">
			{children}
		</p>
	);
}
