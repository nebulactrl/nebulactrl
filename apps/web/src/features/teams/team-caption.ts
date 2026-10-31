function plural(count: number, noun: string): string {
	return `${count} ${noun}${count === 1 ? "" : "s"}`;
}

/**
 * The caption under the team's name in the sidebar header: "Owner · 4
 * projects · 2 servers". A count that has not loaded is left out, and so is
 * "0 servers": a team that has none yet is not helped by being told so.
 */
export function teamCaption({
	role,
	projects,
	servers,
}: {
	role: string | undefined;
	projects: number | undefined;
	servers: number | undefined;
}): string {
	const parts: string[] = [];
	if (role) parts.push(role);
	if (projects !== undefined) parts.push(plural(projects, "project"));
	if (servers !== undefined && servers > 0) {
		parts.push(plural(servers, "server"));
	}
	return parts.join(" · ");
}
