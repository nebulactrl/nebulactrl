import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { PackageIcon, PlusIcon } from "lucide-react";
import { useState } from "react";
import { errorMessage } from "#/api/client-config";
import { useConfirm } from "#/components/common/confirm-dialog";
import { EmptyState } from "#/components/common/empty-state";
import { ErrorState } from "#/components/common/error-state";
import { DocsLink } from "#/components/docs/docs-link";
import { Button } from "#/components/ui/button";
import { Skeleton } from "#/components/ui/skeleton";
import { toast } from "#/components/ui/toast";
import { deleteRegistry } from "./api";
import type { Registry } from "./model";
import { invalidateRegistries, registriesQuery } from "./queries";
import { RegistryCard } from "./registry-card";
import { RegistryDialog } from "./registry-dialog";

/**
 * Team settings → Image registries: where built images are stored so a build server and
 * the server that runs the app can share them. Every team has the built-in
 * registry, which the instance admin runs; add your own (any OCI registry with
 * a user name and a token) to use another. The page lists each registry with
 * a per-server test and the images pushed to it.
 */
export function RegistriesSection() {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const registries = useQuery(registriesQuery());
	const [creating, setCreating] = useState(false);
	const [editing, setEditing] = useState<Registry | null>(null);

	const remove = useMutation({
		mutationFn: (registry: Registry) => deleteRegistry(registry.id),
		onSuccess: async (_, registry) => {
			toast.success(
				`Deleted ${registry.name}`,
				"Images already in it stay there; NebulaCtrl no longer uses it.",
			);
			await invalidateRegistries(queryClient);
		},
		onError: (e) =>
			toast.error("Could not delete the registry", errorMessage(e)),
	});

	if (registries.isPending) return <Skeleton className="h-40 rounded-card" />;
	if (registries.isError && !registries.data) {
		return (
			<ErrorState
				frame="raised"
				title="Registries did not load"
				body="Registries are for team admins. Check your role and session and try again; nothing was changed."
				retry={() => void registries.refetch()}
			/>
		);
	}

	const items = registries.data ?? [];

	const actions = (
		<Button variant="primary" onClick={() => setCreating(true)}>
			<PlusIcon />
			Add registry
		</Button>
	);

	return (
		<div className="flex flex-col gap-4">
			{items.length === 0 ? (
				<EmptyState
					illustration="box"
					title="Add a registry to build on a build server"
					body={
						<>
							A build server pushes the image it built to a registry, and the
							server that runs the app pulls it by digest. Add your own (GitHub,
							Docker Hub, GitLab, Harbor or any OCI registry).{" "}
							<DocsLink to="operations/build-servers" />
						</>
					}
					action={actions}
				/>
			) : (
				<>
					<div className="flex flex-wrap items-center justify-between gap-3">
						<p className="m-0 flex items-center gap-2 text-ink-3 text-xs">
							<PackageIcon aria-hidden="true" className="size-4" />
							{items.length} {items.length === 1 ? "registry" : "registries"}.
							Builds on a build server push here; apps pull by digest.
						</p>
						{actions}
					</div>
					<div className="grid gap-3">
						{items.map((registry) => (
							<RegistryCard
								key={registry.id}
								registry={registry}
								onEdit={() => setEditing(registry)}
								onDelete={() =>
									void confirm({
										title: `Delete ${registry.name}?`,
										body: "NebulaCtrl stops using it. Images already pushed stay in the registry, but a release built from it can then be rolled back only by rebuilding its commit.",
										cta: "Delete registry",
										danger: true,
										hold: false,
										onConfirm: async () => {
											await remove.mutateAsync(registry);
										},
									})
								}
							/>
						))}
					</div>
				</>
			)}
			<RegistryDialog
				open={creating}
				onOpenChange={setCreating}
				registry={null}
			/>
			<RegistryDialog
				open={editing !== null}
				onOpenChange={(open) => (open ? null : setEditing(null))}
				registry={editing}
			/>
		</div>
	);
}
