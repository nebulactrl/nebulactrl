import { useMutation, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useEffect, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { updateInstanceServices } from "#/api/gen/sdk.gen";
import { Button } from "#/components/ui/button";
import {
	Field,
	FieldDescription,
	FieldError,
	FieldLabel,
} from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { SettingsPlate } from "#/features/settings/section";
import { gibText, parseQuotaGib } from "./model";
import { instanceServicesQuery } from "./queries";

/**
 * The backup quota every team gets unless the table below sets its own.
 * Lowering it never deletes a backup: a team over the new quota only cannot
 * add new ones.
 */
export function DefaultQuotaPlate({
	defaultQuotaBytes,
}: {
	defaultQuotaBytes: number;
}) {
	const queryClient = useQueryClient();
	const saved = gibText(defaultQuotaBytes);
	const [text, setText] = useState(saved);
	const [error, setError] = useState<string | null>(null);

	// Follow the control plane's value whenever it changes (a save, a refetch).
	useEffect(() => {
		setText(saved);
		setError(null);
	}, [saved]);

	const save = useMutation({
		mutationFn: async (bytes: number) => {
			const { data } = await updateInstanceServices({
				body: { defaultQuotaBytes: bytes },
				throwOnError: true,
			});
			return data;
		},
		onSuccess: (data) => {
			queryClient.setQueryData(instanceServicesQuery().queryKey, data);
			toast.success(
				"Default quota saved",
				"Teams without their own quota use it from now on.",
			);
		},
		onError: (e) => setError(errorMessage(e)),
	});

	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (save.isPending) return;
		const parsed = parseQuotaGib(text);
		if (!parsed.ok) {
			setError(parsed.message);
			return;
		}
		setError(null);
		save.mutate(parsed.bytes);
	};

	const changed = text.trim() !== saved;

	return (
		<SettingsPlate
			id="default-quota"
			title="Default quota"
			tip="Each team's backups are kept in its own bucket. A team that reaches its quota gets a plain error on new backups; nothing already stored is deleted to make room. The team and you are alerted at 80% and at 100%. Images and logs have no quota: they follow their retention."
			tipLabel="About the default quota"
		>
			<form onSubmit={submit} className="flex flex-col gap-3">
				<Field>
					<FieldLabel htmlFor="default-quota-gib">
						Backup storage per team (GiB)
					</FieldLabel>
					<div className="flex flex-wrap items-center gap-3">
						<Input
							id="default-quota-gib"
							scale="lg"
							inputMode="decimal"
							autoComplete="off"
							className="w-32 font-mono"
							aria-invalid={error !== null}
							value={text}
							onChange={(e) => {
								setText(e.target.value);
								setError(null);
							}}
						/>
						<Button
							type="submit"
							variant="secondary"
							disabled={save.isPending || !changed}
						>
							{save.isPending ? <Spinner /> : null}
							Save
						</Button>
					</div>
					{error ? (
						<FieldError>{error}</FieldError>
					) : (
						<FieldDescription>
							At least 1 GiB. Teams with their own quota keep it.
						</FieldDescription>
					)}
				</Field>
			</form>
		</SettingsPlate>
	);
}
