import { useState } from "react";
import { errorMessage } from "#/api/client-config";
import type { Service } from "#/api/models";
import { Alert } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { Checkbox } from "#/components/ui/checkbox";
import {
	Dialog,
	DialogBody,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from "#/components/ui/dialog";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { type SharedOwner, useSharedMutations } from "./use-shared-mutations";

/**
 * "Share to services": stages a `KEY=${{shared.KEY}}` variable on each chosen
 * service (Railway's reference variable). Nothing is deployed.
 */
export function ShareDialog({
	owner,
	variableKey,
	services,
	onClose,
}: {
	owner: SharedOwner;
	/** The shared variable to share; null when closed. */
	variableKey: string | null;
	services: readonly Service[];
	onClose(): void;
}) {
	return (
		<Dialog open={variableKey !== null} onOpenChange={(o) => !o && onClose()}>
			<DialogContent>
				{variableKey !== null ? (
					<ShareBody
						owner={owner}
						variableKey={variableKey}
						services={services}
						onClose={onClose}
					/>
				) : null}
			</DialogContent>
		</Dialog>
	);
}

function ShareBody({
	owner,
	variableKey,
	services,
	onClose,
}: {
	owner: SharedOwner;
	variableKey: string;
	services: readonly Service[];
	onClose(): void;
}) {
	const { share } = useSharedMutations(owner);
	const [chosen, setChosen] = useState<ReadonlySet<string>>(new Set());
	const toggle = (id: string, on: boolean) =>
		setChosen((current) => {
			const next = new Set(current);
			if (on) next.add(id);
			else next.delete(id);
			return next;
		});
	return (
		<>
			<DialogHeader>
				<DialogTitle>Share {variableKey}</DialogTitle>
				<DialogDescription>
					Stages {variableKey} = {owner.layer === "team" ? "team" : "shared"}.
					{variableKey} (as a reference) on each service you choose. A service
					that already defines {variableKey} is left as it is.
				</DialogDescription>
			</DialogHeader>
			<DialogBody>
				{services.length === 0 ? (
					<p className="type-secondary">
						This environment has no services yet.
					</p>
				) : (
					<ul aria-label="Services" className="flex flex-col gap-1">
						{services.map((service) => (
							<li key={service.id} className="flex items-center gap-2.5 py-1">
								<Checkbox
									id={`share-${service.id}`}
									checked={chosen.has(service.id)}
									onCheckedChange={(checked) =>
										toggle(service.id, checked === true)
									}
								/>
								<label
									htmlFor={`share-${service.id}`}
									className="text-[13px] text-ink-1"
								>
									{service.name}
									<span className="ml-2 font-mono text-[11.5px] text-ink-3">
										{service.slug}
									</span>
								</label>
							</li>
						))}
					</ul>
				)}
				{share.isError ? (
					<Alert variant="destructive">{errorMessage(share.error)}</Alert>
				) : null}
			</DialogBody>
			<DialogFooter>
				<Button type="button" variant="ghost" onClick={onClose}>
					Cancel
				</Button>
				<Button
					type="button"
					disabled={chosen.size === 0 || share.isPending}
					onClick={() =>
						share.mutate(
							{ key: variableKey, serviceIds: [...chosen] },
							{
								onSuccess: (data) => {
									toast.success(
										`Shared ${variableKey}`,
										"Staged on the chosen services until the next deploy.",
									);
									for (const warning of data?.warnings ?? [])
										toast.warning(warning.message);
									onClose();
								},
							},
						)
					}
				>
					{share.isPending ? <Spinner /> : null}
					Stage on {chosen.size || ""} service{chosen.size === 1 ? "" : "s"}
				</Button>
			</DialogFooter>
		</>
	);
}
