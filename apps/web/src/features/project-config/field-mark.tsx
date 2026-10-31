import { useQuery } from "@tanstack/react-query";
import { cn } from "cn";
import { FileTextIcon, FileWarningIcon } from "lucide-react";
import { createContext, type ReactNode, useContext } from "react";
import { errorMessage } from "#/api/client-config";
import { serviceQuery } from "#/api/hooks";
import { useProjectContext } from "#/components/shell/use-project-context";
import { Button } from "#/components/ui/button";
import {
	Popover,
	PopoverContent,
	PopoverDescription,
	PopoverHeader,
	PopoverTitle,
	PopoverTrigger,
} from "#/components/ui/popover";
import { toast } from "#/components/ui/toast";
import {
	Tooltip,
	TooltipContent,
	TooltipTrigger,
} from "#/components/ui/tooltip";
import { changesForField, fieldMark, markTooltip } from "./model";
import {
	useApplyFromGit,
	usePlanFromGit,
	useReleaseFields,
	useServiceConfigSource,
} from "./queries";

const ServiceContext = createContext<string | undefined>(undefined);

/**
 * Names the service whose controls sit inside, so a mark deep in a settings
 * section does not need the service threaded through every row.
 */
export function ConfigServiceProvider({
	serviceId,
	children,
}: {
	serviceId: string;
	children: ReactNode;
}) {
	return (
		<ServiceContext.Provider value={serviceId}>
			{children}
		</ServiceContext.Provider>
	);
}

const ICON = "size-3.5 shrink-0";

/** The drifted mark: amber, with Revert to file and Stop managing. */
function DriftMark({
	serviceId,
	field,
	fieldIds,
	exact,
	state,
	tooltip,
}: {
	serviceId: string;
	field: string;
	/** The owned field ids the mark stands for: Stop managing releases these. */
	fieldIds: readonly string[];
	exact: boolean;
	state: string;
	tooltip: string;
}) {
	const context = useProjectContext();
	const service = useQuery(serviceQuery(serviceId));
	const target = {
		projectId: context?.projectId ?? "",
		environmentSlug: context?.environment?.slug ?? "",
	};
	const plan = usePlanFromGit(target);
	const apply = useApplyFromGit(target);
	const release = useReleaseFields(serviceId);
	const busy = plan.isPending || apply.isPending || release.isPending;
	const slug = service.data?.slug;
	const canRevert = target.environmentSlug !== "" && slug !== undefined;

	const revert = async () => {
		if (slug === undefined) return;
		try {
			const planned = await plan.mutateAsync(undefined);
			const changes = changesForField(planned, slug, field, exact);
			if (changes.length === 0) {
				toast.info(
					"Nothing to revert",
					"The file no longer differs from this field.",
				);
				return;
			}
			await apply.mutateAsync({
				only: changes.map((change) => change.path),
				idempotencyKey: crypto.randomUUID(),
			});
			toast.success("Reverted to nebula.toml", "Staged: deploy to ship it.");
		} catch (error) {
			toast.error("Could not revert", errorMessage(error));
		}
	};

	const stop = () =>
		release.mutate([...fieldIds], {
			onSuccess: () =>
				toast.success(
					"No longer managed",
					"The value stays as it is; nebula.toml does not touch it any more.",
				),
			onError: (error) =>
				toast.error("Could not stop managing", errorMessage(error)),
		});

	return (
		<Popover>
			<PopoverTrigger
				aria-label="Edited here, differs from nebula.toml"
				className="inline-flex h-5 cursor-pointer items-center gap-1 rounded-sm px-1 font-sans font-medium text-[11px] text-status-degraded-on outline-none hover:bg-white/8 focus-visible:focus-ring"
			>
				<FileWarningIcon aria-hidden className={ICON} />
				{state === "conflict" ? "both changed" : "edited here"}
			</PopoverTrigger>
			<PopoverContent align="start" className="w-[300px]">
				<PopoverHeader>
					<PopoverTitle>
						{state === "conflict"
							? "Changed here and in the file"
							: "Edited here"}
					</PopoverTitle>
					<PopoverDescription>{tooltip}</PopoverDescription>
				</PopoverHeader>
				<div className="mt-3 flex flex-wrap gap-2">
					{canRevert ? (
						<Button
							size="sm"
							variant="secondary"
							disabled={busy}
							onClick={() => void revert()}
						>
							Revert to file
						</Button>
					) : null}
					<Button size="sm" variant="ghost" disabled={busy} onClick={stop}>
						Stop managing
					</Button>
				</div>
			</PopoverContent>
		</Popover>
	);
}

/**
 * A small file icon beside a control the file sets. "Set by nebula.toml
 * (acme/shop@3f9c2a1). Editing here is allowed and shows as drift until the
 * file changes or you re-apply." When the dashboard value differs from the
 * file, the mark says so in words and offers Revert to file and Stop
 * managing. `field` is a field id (`config.port`) or a group (`variables`).
 */
export function FieldMark({
	field,
	serviceId,
	exact = false,
	className,
}: {
	field: string;
	/** Defaults to the service of the nearest `ConfigServiceProvider`. */
	serviceId?: string;
	/** Match this one field only, not the fields under it (a hostname is not a group). */
	exact?: boolean;
	className?: string;
}) {
	const fromContext = useContext(ServiceContext);
	const id = serviceId ?? fromContext;
	const source = useServiceConfigSource(id);
	const mark = fieldMark(source.data, field, { exact });
	if (!mark || !source.data || id === undefined) return null;
	const tooltip = markTooltip(source.data);
	if (mark.drifted) {
		return (
			<span className={cn("inline-flex", className)}>
				<DriftMark
					serviceId={id}
					field={field}
					fieldIds={mark.fields}
					exact={exact}
					state={mark.state}
					tooltip={tooltip}
				/>
			</span>
		);
	}
	return (
		<Tooltip>
			<TooltipTrigger
				render={
					<span
						className={cn(
							"inline-flex size-5 items-center justify-center rounded-sm text-ink-4",
							className,
						)}
					/>
				}
			>
				<FileTextIcon aria-hidden className={ICON} />
				<span className="sr-only">Set by nebula.toml</span>
			</TooltipTrigger>
			<TooltipContent>{tooltip}</TooltipContent>
		</Tooltip>
	);
}
