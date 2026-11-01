/**
 * The second line of the user card at the sidebar's foot: who the person is,
 * never what build runs. Their email when the name is a name; their role in
 * the team when the name is already the email, so the line never repeats it.
 */
export function userCaption(user: {
	name: string;
	email: string;
	roleLabel: string;
}): string {
	return user.name && user.name !== user.email ? user.email : user.roleLabel;
}

/** The control plane's version as the menu words it: "v0.9.2", or "development build". */
export function versionLabel(version: string): string {
	const bare = version.replace(/^v/, "");
	return bare === "dev" ? "development build" : `v${bare}`;
}
