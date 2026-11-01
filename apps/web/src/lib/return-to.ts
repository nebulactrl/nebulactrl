/**
 * Where to go after signing in: the page that sent the visitor to /login,
 * but only when it is a path on this origin (`/…`, not `//host` or a URL),
 * so the `returnTo` parameter can never be used as an open redirect.
 */
export function safeReturnTo(value: string | undefined): string {
	if (!value?.startsWith("/") || value.startsWith("//")) return "/";
	if (value.startsWith("/\\")) return "/";
	return value;
}
