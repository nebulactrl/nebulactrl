import { useMutation } from "@tanstack/react-query";
import { useEffect } from "react";
import { previewEnvTemplates } from "#/api/gen/sdk.gen";
import type {
	EnvTemplatePreviewInput,
	EnvTemplateScan,
	SuggestedVariable,
} from "#/api/gen/types.gen";
import { useDebounced } from "#/features/templates/use-debounced";
import { exampleItems } from "#/features/variables/env-example";

/** Typing a URL or token settles before the repository is read. */
const SETTLE_MS = 700;

export interface EnvPreview {
	/** The repository is being looked at (typing settled or the read is running). */
	loading: boolean;
	/** The answer for the repository as it is now; undefined while loading or on failure. */
	scan: EnvTemplateScan | undefined;
	/** The keys to review, with the proposed values. */
	items: SuggestedVariable[];
}

/**
 * Reads the template env files of the repository the create dialog points at
 * (through the git provider when it can, else on the chosen server), without
 * creating anything. `null` means the source is not complete yet. A failed
 * request is no answer: the dialog simply has no Variables step.
 */
export function useEnvTemplatePreview(
	environmentId: string,
	input: EnvTemplatePreviewInput | null,
): EnvPreview {
	const fingerprint = input ? JSON.stringify(input) : "";
	const settled = useDebounced(fingerprint, SETTLE_MS);
	const { mutate, reset, data, isPending, isSuccess } = useMutation({
		mutationFn: async (body: EnvTemplatePreviewInput) => {
			const { data } = await previewEnvTemplates({
				path: { environmentId },
				body,
			});
			return data;
		},
	});

	useEffect(() => {
		if (settled === "") reset();
		else mutate(JSON.parse(settled) as EnvTemplatePreviewInput);
	}, [settled, mutate, reset]);

	const current = settled === fingerprint;
	const scan = current && isSuccess ? data : undefined;
	return {
		loading: fingerprint !== "" && (!current || isPending),
		scan,
		items: exampleItems(scan?.items),
	};
}
