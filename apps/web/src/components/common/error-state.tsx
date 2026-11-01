import { cn } from "cn";
import type { ReactNode } from "react";
import { ToneIcon } from "#/components/common/tone-icon";
import { Button } from "#/components/ui/button";
import {
	Empty,
	EmptyContent,
	EmptyDescription,
	EmptyHeader,
	EmptyTitle,
} from "#/components/ui/empty";

export interface ErrorStateProps {
	/** What failed, as a sentence: "Servers did not load". */
	title: string;
	/** What state things are in and how to fix it: "Nothing was changed. Check that the control plane is running, then try again." */
	body: string;
	retry?: () => void;
	/**
	 * What to do instead of retrying, e.g. a "Go to projects" link button. Use
	 * it when trying again cannot help (the thing does not exist).
	 */
	action?: ReactNode;
	/** `raised` frames it as a card on the page floor; `none` (default) is for use inside an existing panel. */
	frame?: "raised" | "none";
	/**
	 * A page-sized state: the icon carries the tone, and the page floor stays
	 * neutral instead of washing the whole viewport in the danger colour.
	 */
	neutral?: boolean;
	className?: string;
}

/** A query that failed, in place of the content it would have shown. */
export function ErrorState({
	title,
	body,
	retry,
	action,
	frame = "none",
	neutral = false,
	className,
}: ErrorStateProps) {
	return (
		<Empty
			role="alert"
			data-tone={neutral ? undefined : "danger"}
			className={cn(
				"relative rounded-card py-10",
				!neutral && "tone-callout",
				frame === "raised" && "surface-raised py-14",
				className,
			)}
		>
			<EmptyHeader>
				<EmptyTitle className="flex items-center justify-center gap-2">
					<ToneIcon tone="danger" />
					{title}
				</EmptyTitle>
				<EmptyDescription>{body}</EmptyDescription>
			</EmptyHeader>
			{retry || action ? (
				<EmptyContent>
					{retry ? (
						<Button variant="secondary" onClick={retry}>
							Try again
						</Button>
					) : null}
					{action}
				</EmptyContent>
			) : null}
		</Empty>
	);
}
