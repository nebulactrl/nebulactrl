import { useQuery } from "@tanstack/react-query";
import { errorCode, errorMessage } from "#/api/client-config";
import { previewMeshAcl } from "#/api/gen/sdk.gen";
import { CodeBlock } from "#/components/common/code-block";
import { Alert, AlertDescription } from "#/components/ui/alert";
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
import { Skeleton } from "#/components/ui/skeleton";
import { RulesTable } from "./mesh-tables";
import { aclDiffText, type MeshView, meshErrorText, plural } from "./model";

/**
 * What the mesh's access policy would be right now, with nothing written. The
 * rules come first, one per link, then the whole document to copy and, when
 * the tailnet's current policy could be read and differs, what would change.
 */
export function AclPreviewDialog({
	open,
	onOpenChange,
	mesh,
}: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	mesh: MeshView;
}) {
	const preview = useQuery({
		queryKey: [{ _id: "previewMeshAcl" }],
		queryFn: async ({ signal }) => {
			const { data } = await previewMeshAcl({ signal });
			return data;
		},
		enabled: open,
		staleTime: 0,
		gcTime: 0,
		retry: false,
		refetchInterval: false,
	});
	const rules = preview.data?.rules ?? [];
	const links = mesh.links ?? [];
	const text = preview.data?.policy ?? "";
	const diff = preview.data?.diff ? aclDiffText(preview.data.diff) : "";
	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent size="xl">
				<DialogHeader>
					<DialogTitle>Rules the mesh needs</DialogTitle>
					<DialogDescription>
						{preview.data
							? `${plural(rules.length, "rule")} generated from your servers, links and grants. This is a preview: nothing is written.`
							: "Generated from your servers, links and grants. This is a preview: nothing is written."}
					</DialogDescription>
				</DialogHeader>
				<DialogBody className="flex max-h-[60vh] flex-col gap-4 overflow-y-auto overscroll-contain">
					{preview.isPending ? (
						<div aria-busy="true" className="flex flex-col gap-2">
							<Skeleton className="h-8 w-full" />
							<Skeleton className="h-8 w-full" />
							<Skeleton className="h-32 w-full" />
						</div>
					) : preview.isError ? (
						<Alert variant="destructive">
							<AlertDescription>
								{meshErrorText(
									errorCode(preview.error),
									errorMessage(preview.error),
								)}
							</AlertDescription>
						</Alert>
					) : (
						<>
							{rules.length === 0 ? (
								<p className="type-secondary">
									No rules yet: no link needs one. The policy denies everything
									between mesh nodes until a link and a grant exist.
								</p>
							) : (
								<RulesTable rules={rules} links={links} />
							)}
							<section className="flex flex-col gap-2">
								<h3 className="type-label">Policy</h3>
								<CodeBlock copyValue={text}>
									<pre className="max-h-72 overflow-auto overscroll-contain whitespace-pre font-mono text-2xs leading-[1.6]">
										{text}
									</pre>
								</CodeBlock>
							</section>
							{diff ? (
								<section className="flex flex-col gap-2">
									<h3 className="type-label">
										What would change in the tailnet
									</h3>
									<pre className="surface-well max-h-56 overflow-auto overscroll-contain rounded-ctl p-3 font-mono text-2xs text-ink-2 leading-[1.6]">
										{diff}
									</pre>
								</section>
							) : null}
						</>
					)}
				</DialogBody>
				<DialogFooter>
					<Button variant="secondary" onClick={() => onOpenChange(false)}>
						Close
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
