import { cn } from "cn";
import type { ReactNode } from "react";
import { DocsLink } from "#/components/docs/docs-link";
import { InfoTip } from "#/components/settings/info-tip";
import { Card, CardHeader } from "#/components/ui/card";

/** One settings page: its h2, a line on what it is for, and its plates. */
export function SettingsSection({
	id,
	title,
	description,
	docs,
	children,
}: {
	id: string;
	title: string;
	description: string;
	docs?: string | undefined;
	children: ReactNode;
}) {
	return (
		<section
			id={id}
			aria-labelledby={`${id}-title`}
			className="flex min-w-0 flex-col gap-4"
		>
			<div className="flex flex-col gap-2">
				<h2 id={`${id}-title`} className="section-head">
					{title}
				</h2>
				<p className="type-secondary text-ink-3">
					{description}
					{docs ? (
						<>
							{" "}
							<DocsLink to={docs} />
						</>
					) : null}
				</p>
			</div>
			<div className="flex min-w-0 flex-col gap-5">{children}</div>
		</section>
	);
}

/**
 * A raised plate on a settings page: an h3 title, an optional info tip for the
 * longer explanation and the content. A page's danger zone is the last plate
 * and passes `tone="danger"`.
 */
export function SettingsPlate({
	id,
	title,
	tip,
	tipLabel,
	tone = "default",
	actions,
	children,
	flush = false,
	className,
}: {
	id?: string;
	title: string;
	/** The longer explanation, in a tooltip. Never a warning. */
	tip?: ReactNode;
	/** What the tip explains, for screen readers ("About two-step verification"). */
	tipLabel?: string;
	tone?: "default" | "danger";
	actions?: ReactNode;
	children: ReactNode;
	/** The content runs edge to edge (a table): no padding, no gap. */
	flush?: boolean;
	className?: string;
}) {
	const headingId = id ? `${id}-heading` : undefined;
	return (
		<Card
			variant={tone === "danger" ? "danger" : "raised"}
			{...(id ? { id } : {})}
			role="group"
			aria-labelledby={headingId}
			className={cn("scroll-mt-4", className)}
		>
			<CardHeader className="items-center">
				<div className="flex min-w-0 items-center gap-2">
					<h3
						id={headingId}
						className={cn(
							"font-semibold text-sm leading-[1.4]",
							tone === "danger" ? "text-destructive-text" : "text-ink-1",
						)}
					>
						{title}
					</h3>
					{tip ? (
						<InfoTip label={tipLabel ?? `About ${title.toLowerCase()}`}>
							{tip}
						</InfoTip>
					) : null}
				</div>
				{actions}
			</CardHeader>
			<div className={cn("flex flex-col", !flush && "gap-4 px-5 py-5")}>
				{children}
			</div>
		</Card>
	);
}

/** A page that only says why the person cannot use it: one sentence, no form. */
export function SettingsNotice({ children }: { children: ReactNode }) {
	return (
		<p role="status" className="type-secondary max-w-prose text-ink-2">
			{children}
		</p>
	);
}
