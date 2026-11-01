import { ApiTagView } from "@nebulactrl/docs/api-reference";
import type { ApiTagPage } from "@nebulactrl/docs/types";

/** An API reference page: the schema rows expand on click, so this one hydrates. */
export default function ApiTag({
	page,
	title,
}: {
	page: ApiTagPage;
	title: string;
}) {
	return <ApiTagView page={page} title={title} />;
}
