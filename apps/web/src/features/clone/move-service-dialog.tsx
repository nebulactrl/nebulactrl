import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useEffect, useId, useState } from "react";
import { errorCode, errorMessage } from "#/api/client-config";
import { moveService } from "#/api/gen/sdk.gen";
import { environmentsQuery } from "#/api/hooks";
import { invalidateServices } from "#/api/invalidate";
import type { Service } from "#/api/models";
import { Button } from "#/components/ui/button";
import {
	Dialog,
	DialogBody,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from "#/components/ui/dialog";
import { Field, FieldError, FieldLabel } from "#/components/ui/field";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { type MovePlan, toMovePlan } from "./model";
import { EnvironmentSelect } from "./pickers";
import { MovePlanView } from "./plan-view";

/**
 * "Move <service>": re-homes the service in another environment, staged until
 * its next deployment. Picking an environment asks the server for the plan
 * (broken references, removed links, a new slug); a move that breaks
 * references needs a second, explicit click.
 */
export function MoveServiceDialog({
	service,
	open,
	onOpenChange,
}: {
	service: Service;
	open: boolean;
	onOpenChange: (open: boolean) => void;
}) {
	const queryClient = useQueryClient();
	const navigate = useNavigate();
	const id = useId();
	const environments = useQuery({
		...environmentsQuery(service.projectId),
		enabled: open,
	});
	const [environmentId, setEnvironmentId] = useState("");
	const [plan, setPlan] = useState<MovePlan | null>(null);

	useEffect(() => {
		if (open) {
			setEnvironmentId("");
			setPlan(null);
		}
	}, [open]);

	const target = environments.data?.find((e) => e.id === environmentId);

	const check = useMutation({
		mutationFn: async (targetId: string) => {
			const { data } = await moveService({
				path: { serviceId: service.id },
				body: { environmentId: targetId, dryRun: true },
			});
			return toMovePlan(data);
		},
		onSuccess: setPlan,
	});

	const move = useMutation({
		mutationFn: async () => {
			const { data } = await moveService({
				path: { serviceId: service.id },
				body: { environmentId, force: plan?.blocked === true },
			});
			return data;
		},
		onSuccess: async () => {
			await invalidateServices(queryClient);
			onOpenChange(false);
			toast.success(
				`Moved ${service.name} to ${target?.name ?? "the environment"}`,
				"Staged: deploy it to apply the move.",
			);
			if (target) {
				void navigate({
					to: "/p/$projectId/$envSlug",
					params: { projectId: target.projectId, envSlug: target.slug },
					search: { service: service.id },
				});
			}
		},
	});

	const pick = (next: string) => {
		setEnvironmentId(next);
		setPlan(null);
		move.reset();
		if (next !== "") check.mutate(next);
	};

	const error = move.error ?? check.error;
	const force = plan?.blocked === true;

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent size="md" showCloseButton>
				<DialogHeader>
					<DialogTitle>Move {service.name}</DialogTitle>
					<DialogDescription>
						The service keeps running where it is until its next deployment,
						which joins the new environment's network and variables. Domains,
						volumes and data stay with it.
					</DialogDescription>
				</DialogHeader>
				<DialogBody>
					<Field>
						<FieldLabel htmlFor={`${id}-env`}>Move to</FieldLabel>
						<EnvironmentSelect
							id={`${id}-env`}
							environments={environments.data ?? []}
							value={environmentId}
							onChange={pick}
							exclude={service.environmentId}
						/>
					</Field>
					{check.isPending ? <Skeleton className="h-20 w-full" /> : null}
					{plan ? <MovePlanView plan={plan} /> : null}
					{error ? (
						<FieldError>
							{errorCode(error) === "move_blocked"
								? "The move breaks references or needs cross-server links removed first. Check again, or move anyway."
								: errorMessage(error)}
						</FieldError>
					) : null}
				</DialogBody>
				<DialogFooter>
					<Button
						type="button"
						variant="secondary"
						onClick={() => onOpenChange(false)}
					>
						Cancel
					</Button>
					<Button
						type="button"
						variant={force ? "destructive-solid" : "primary"}
						disabled={plan === null || move.isPending || check.isPending}
						onClick={() => move.mutate()}
					>
						{move.isPending ? <Spinner /> : null}
						{force ? "Move anyway" : "Move service"}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
