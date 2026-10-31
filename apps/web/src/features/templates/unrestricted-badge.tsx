import { cn } from "cn";
import { LockOpenIcon } from "lucide-react";
import { Badge } from "#/components/ui/badge";
import {
	Tooltip,
	TooltipContent,
	TooltipTrigger,
} from "#/components/ui/tooltip";

/**
 * Marks a template that sets a Docker option only an unrestricted server
 * accepts (devices, GPUs, a runtime, privileged mode and the like). A calm
 * neutral tag, not an alert: the template is fine, it just has a placement
 * requirement. The tooltip says what that means and who can change it.
 */
export function UnrestrictedBadge({ className }: { className?: string }) {
	return (
		<Tooltip>
			<TooltipTrigger
				render={
					<Badge
						tabIndex={0}
						className={cn(
							"relative cursor-default outline-none focus-visible:focus-ring",
							className,
						)}
					/>
				}
			>
				<LockOpenIcon aria-hidden />
				Needs an unrestricted server
			</TooltipTrigger>
			<TooltipContent className="max-w-64 flex-col items-start gap-1">
				<span>
					This template gives a container host access (devices, GPUs, a runtime
					or similar). It installs only on a server an instance admin made
					unrestricted.
				</span>
			</TooltipContent>
		</Tooltip>
	);
}
