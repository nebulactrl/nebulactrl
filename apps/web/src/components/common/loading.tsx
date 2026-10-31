import { cn } from "cn";
import { LoaderCircleIcon } from "lucide-react";
import * as React from "react";

interface LoadingProps extends Omit<React.ComponentProps<"div">, "children"> {
	/** A short caption, e.g. "Loading project". */
	label: string;
	/** How long to wait before showing anything. Fast loads show nothing. Default 300. */
	delayMs?: number;
}

/**
 * The route and section loader: a small spinner and a caption, shown only
 * after `delayMs`. The icon turns at 600 ms linear and holds still under
 * reduced motion; the caption is the cue.
 */
function Loading({ label, delayMs = 300, className, ...props }: LoadingProps) {
	const [visible, setVisible] = React.useState(delayMs <= 0);

	React.useEffect(() => {
		if (delayMs <= 0) return;
		const timer = window.setTimeout(() => setVisible(true), delayMs);
		return () => window.clearTimeout(timer);
	}, [delayMs]);

	if (!visible) return null;

	return (
		<div
			role="status"
			className={cn(
				"flex h-full min-h-40 w-full items-center justify-center gap-2 text-ink-3 text-sm",
				className,
			)}
			{...props}
		>
			<LoaderCircleIcon
				aria-hidden
				className="size-4 shrink-0 motion-safe:animate-[nc-spin_0.6s_linear_infinite]"
			/>
			<span>{label}</span>
		</div>
	);
}

export { Loading };
