/**
 * The label in the top bar on pages outside a project. The page's own h1
 * already says which page it is, so the bar says where it applies instead:
 * the team's name on the team's pages, "Your account" and "This
 * installation" on the other two scopes. A path no route owns says so rather
 * than borrowing "Projects". Undefined while the team's name is unknown.
 */
export function pageCrumb(
	pathname: string,
	teamName: string | undefined,
): string | undefined {
	if (pathname.startsWith("/account")) return "Your account";
	if (pathname.startsWith("/admin")) return "This installation";
	if (pathname.startsWith("/invite/")) return "Invitation";
	if (pathname.startsWith("/dev/dither")) return "Dither gallery";
	if (
		pathname === "/" ||
		pathname === "" ||
		pathname.startsWith("/servers") ||
		pathname.startsWith("/settings") ||
		pathname.startsWith("/status-pages")
	) {
		return teamName;
	}
	return "Page not found";
}
