import type { DatabaseView } from "#/api/gen/types.gen";

/** Shown wherever a credential variable cannot be edited by hand. */
export const CREDENTIAL_LOCK_MESSAGE =
	"Managed by the database: use Rotate credentials";

/**
 * The variable keys a deployed managed database owns. Before the first
 * deploy nothing is locked (the credentials are not in use yet); after it,
 * the API refuses a manual edit with `credential_managed`, so the UI says so
 * up front. `database` is undefined for a service that is not a managed
 * database.
 */
export function lockedCredentialKeys(
	database: Pick<DatabaseView, "deployed" | "credentialKeys"> | undefined,
): ReadonlySet<string> {
	if (!database?.deployed) return new Set();
	return new Set(database.credentialKeys ?? []);
}
