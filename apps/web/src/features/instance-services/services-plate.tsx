import { useMutation, useQueryClient } from "@tanstack/react-query";
import { RefreshCwIcon } from "lucide-react";
import { errorMessage } from "#/api/client-config";
import { checkInstanceServices } from "#/api/gen/sdk.gen";
import type { InstanceServicesView } from "#/api/gen/types.gen";
import { Relative } from "#/components/common/relative";
import { Button } from "#/components/ui/button";
import { Spinner } from "#/components/ui/spinner";
import { StatusIcon } from "#/components/ui/status-icon";
import { toast } from "#/components/ui/toast";
import { SettingsPlate } from "#/features/settings/section";
import {
	SERVICE_LABEL,
	SERVICE_ORDER,
	SERVICE_PURPOSE,
	serviceStatus,
	sizeText,
} from "./model";
import { instanceServicesQuery } from "./queries";

/**
 * The three instance services and whether each answers. "Check now" runs the
 * minute-by-minute check at once, so a service that was just fixed does not
 * wait for the next one.
 */
export function ServicesPlate({ view }: { view: InstanceServicesView }) {
	const queryClient = useQueryClient();
	const check = useMutation({
		mutationFn: async () => {
			const { data } = await checkInstanceServices({ throwOnError: true });
			return data;
		},
		onSuccess: (data) => {
			queryClient.setQueryData(instanceServicesQuery().queryKey, data);
			const broken = (data.services ?? []).filter(
				(service) => service.status === "failed",
			);
			if (broken.length === 0) {
				toast.success("Checked", "Every built-in service answers.");
			} else {
				toast.error(
					"A built-in service is not working",
					broken.map((service) => SERVICE_LABEL[service.kind]).join(", "),
				);
			}
		},
		onError: (error) => toast.error("Could not check", errorMessage(error)),
	});

	const byKind = new Map(
		(view.services ?? []).map((service) => [service.kind, service]),
	);

	return (
		<SettingsPlate
			id="services"
			title="Services"
			actions={
				<Button
					variant="secondary"
					size="sm"
					disabled={check.isPending}
					onClick={() => check.mutate()}
				>
					{check.isPending ? <Spinner /> : <RefreshCwIcon aria-hidden />}
					Check now
				</Button>
			}
		>
			<p className="type-secondary m-0 text-ink-3">
				They run on the control-plane host and are reachable only through your
				servers' link to it: nothing is published to the internet.
			</p>
			<ul className="m-0 flex list-none flex-col divide-y divide-line-faint p-0">
				{SERVICE_ORDER.map((kind) => {
					const service = byKind.get(kind);
					if (!service) return null;
					const status = serviceStatus(service.status);
					return (
						<li
							key={kind}
							className="flex flex-wrap items-start justify-between gap-x-6 gap-y-1 py-3 first:pt-0 last:pb-0"
						>
							<div className="flex min-w-0 flex-col gap-0.5">
								<span className="font-medium text-ink-1 text-sm">
									{SERVICE_LABEL[kind]}
								</span>
								<span className="max-w-[60ch] text-ink-3 text-xs leading-relaxed">
									{SERVICE_PURPOSE[kind]}
								</span>
								{service.status === "failed" && service.message !== "" ? (
									<span className="max-w-[60ch] break-words text-destructive-text text-xs">
										{service.message}
									</span>
								) : null}
							</div>
							<div className="flex flex-col items-start gap-0.5 sm:items-end">
								<span className="flex items-center gap-1.5 text-ink-1 text-xs">
									<StatusIcon kind={status.kind} label="" />
									{status.label}
								</span>
								<span className="text-ink-3 text-xs">
									{service.teams} {service.teams === 1 ? "team" : "teams"}
									{kind === "s3" && service.usedBytes > 0
										? ` · ${sizeText(service.usedBytes)} used`
										: ""}
								</span>
								{service.updatedAt && Date.parse(service.updatedAt) > 0 ? (
									<span className="text-2xs text-ink-4">
										checked <Relative iso={service.updatedAt} />
									</span>
								) : (
									<span className="text-2xs text-ink-4">not checked yet</span>
								)}
							</div>
						</li>
					);
				})}
			</ul>
		</SettingsPlate>
	);
}
