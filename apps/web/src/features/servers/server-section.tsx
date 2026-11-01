import type { ReactNode } from "react";

/** A titled block on a server tab: the section head, an optional one-line note, then its content. */
export function ServerSection({
	id,
	title,
	description,
	children,
}: {
	id: string;
	title: string;
	description?: string;
	children: ReactNode;
}) {
	return (
		<section
			id={id}
			aria-labelledby={`${id}-title`}
			className="flex flex-col gap-3"
		>
			<h2 id={`${id}-title`} className="section-head">
				{title}
				{description ? (
					<span className="font-sans text-xs normal-case tracking-normal">
						{description}
					</span>
				) : null}
			</h2>
			{children}
		</section>
	);
}
