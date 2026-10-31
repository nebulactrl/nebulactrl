import { cn } from "cn";
import { ArrowUpRightIcon, ChevronDownIcon, LockIcon } from "lucide-react";
import { Fragment } from "react";
import { Button } from "#/components/ui/button";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "#/components/ui/dropdown-menu";
import {
	Tooltip,
	TooltipContent,
	TooltipTrigger,
} from "#/components/ui/tooltip";
import type { OpenTarget } from "#/lib/service-urls";

const TAILNET_ONLY = "Reachable on your tailnet only";

/** Props every link out of the console shares: a new tab with no handle back. */
const NEW_TAB = {
	target: "_blank",
	rel: "noopener noreferrer",
} as const;

/**
 * The host in the header's meta line as a link to the site, with a small
 * arrow after it. The padding widens the hit area without moving the line.
 */
export function OpenDomainLink({ target }: { target: OpenTarget }) {
	return (
		<a
			href={target.url}
			{...NEW_TAB}
			aria-label={`Open ${target.host} in a new tab`}
			className="-my-1 inline-flex min-w-0 items-center gap-0.5 rounded-sm py-1 text-accent-ink outline-none hover:underline focus-visible:focus-ring"
		>
			<span className="truncate">{target.host}</span>
			<ArrowUpRightIcon aria-hidden className="size-3 shrink-0" />
		</a>
	);
}

function OpenLink({
	target,
	className,
}: {
	target: OpenTarget;
	className?: string;
}) {
	return (
		<Button
			variant="secondary"
			size="sm"
			nativeButton={false}
			className={cn("relative pr-2 focus-visible:z-10", className)}
			render={
				<a
					href={target.url}
					{...NEW_TAB}
					aria-label={`Open ${target.host} in a new tab`}
				/>
			}
		>
			Open
			<ArrowUpRightIcon aria-hidden />
		</Button>
	);
}

/**
 * "Open" in the action row: the first address in a new tab. With more than
 * one, a chevron beside it lists every address, public ones first and
 * tailnet-only ones marked. Nothing renders when there is nothing to open.
 */
export function OpenSiteButton({
	targets,
}: {
	targets: readonly OpenTarget[];
}) {
	const [primary] = targets;
	if (!primary) return null;

	if (targets.length === 1) {
		return primary.tailnetOnly ? (
			<Tooltip>
				<TooltipTrigger render={<span className="inline-flex" />}>
					<OpenLink target={primary} />
				</TooltipTrigger>
				<TooltipContent side="bottom">{TAILNET_ONLY}</TooltipContent>
			</Tooltip>
		) : (
			<OpenLink target={primary} />
		);
	}

	const firstTailnet = targets.findIndex((target) => target.tailnetOnly);
	return (
		<fieldset
			aria-label="Open the service"
			className="m-0 inline-flex min-w-0 border-0 p-0"
		>
			<OpenLink target={primary} className="rounded-r-none" />
			<DropdownMenu>
				<DropdownMenuTrigger
					render={
						<Button
							variant="secondary"
							size="sm"
							aria-label="Choose an address to open"
							className="relative -ml-px rounded-l-none px-1.5 focus-visible:z-10"
						/>
					}
				>
					<ChevronDownIcon aria-hidden />
				</DropdownMenuTrigger>
				<DropdownMenuContent align="end" className="min-w-[220px]">
					{targets.map((target, index) => (
						<Fragment key={target.url}>
							{index > 0 && index === firstTailnet ? (
								<DropdownMenuSeparator />
							) : null}
							<DropdownMenuItem
								render={
									<a
										href={target.url}
										{...NEW_TAB}
										aria-label={`Open ${target.host} in a new tab${target.tailnetOnly ? `. ${TAILNET_ONLY}` : ""}`}
									/>
								}
							>
								<ArrowUpRightIcon aria-hidden />
								<span className="min-w-0 flex-1 truncate font-mono text-xs">
									{target.host}
								</span>
								{target.tailnetOnly ? (
									<span className="flex shrink-0 items-center gap-1 text-2xs text-ink-3">
										<LockIcon aria-hidden className="size-3" />
										Tailnet only
									</span>
								) : null}
							</DropdownMenuItem>
						</Fragment>
					))}
				</DropdownMenuContent>
			</DropdownMenu>
		</fieldset>
	);
}
