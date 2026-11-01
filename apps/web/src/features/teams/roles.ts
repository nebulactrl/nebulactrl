// Team roles and what each may do, mirrored from the control plane
// (internal/policy ScopesForRole and internal/teams). The server enforces all
// of it; the console uses these to show only the actions that can succeed.

export const ROLES = ["owner", "admin", "developer", "viewer"] as const;

export type Role = (typeof ROLES)[number];

export const ROLE_LABEL: Record<Role, string> = {
	owner: "Owner",
	admin: "Admin",
	developer: "Developer",
	viewer: "Viewer",
};

export const ROLE_SUMMARY: Record<Role, string> = {
	owner: "Everything, including deleting the team and managing other owners.",
	admin:
		"Everything in the team except managing owners: members, servers, integrations, tokens.",
	developer:
		"Deploys, edits variables, uses the terminal and runs tasks. No members, integrations, servers or other people's tokens.",
	viewer: "Read-only. Never sees secrets, reveals, the terminal or files.",
};

export function isRole(value: unknown): value is Role {
	return (
		typeof value === "string" && (ROLES as readonly string[]).includes(value)
	);
}

/** Owners and admins manage members, invites and the team's settings. */
export function canManageMembers(role: Role | undefined): boolean {
	return role === "owner" || role === "admin";
}

/** The roles `actor` may hand out: only owners make owners. */
export function assignableRoles(actor: Role | undefined): readonly Role[] {
	if (actor === "owner") return ROLES;
	if (actor === "admin") return ["admin", "developer", "viewer"];
	return [];
}

/**
 * Whether `actor` may change or remove a member holding `target`: owners
 * manage everyone, admins everyone but owners.
 */
export function canChangeMember(
	actor: Role | undefined,
	target: Role,
): boolean {
	if (actor === "owner") return true;
	return actor === "admin" && target !== "owner";
}

/** Owners and admins always see every project; the rest can be restricted. */
export function supportsProjectRestriction(role: Role): boolean {
	return role === "developer" || role === "viewer";
}

/** "All projects", "1 project" or "3 projects" for a member's access. */
export function describeProjectAccess(
	role: Role,
	projectIds: readonly string[] | null | undefined,
): string {
	if (!supportsProjectRestriction(role) || !projectIds) return "All projects";
	if (projectIds.length === 0) return "No projects";
	return `${projectIds.length} ${projectIds.length === 1 ? "project" : "projects"}`;
}

/** Whether a team (UI) can be deleted from here: owners only. */
export function canDeleteTeam(role: Role | undefined): boolean {
	return role === "owner";
}

export interface InviteExpiry {
	id: string;
	label: string;
	days: number;
}

export const INVITE_EXPIRIES: readonly InviteExpiry[] = [
	{ id: "1d", label: "1 day", days: 1 },
	{ id: "7d", label: "7 days", days: 7 },
	{ id: "30d", label: "30 days", days: 30 },
];

export const DEFAULT_INVITE_EXPIRY = "7d";

/** An invite link as an absolute URL: a relative path gets the page's origin. */
export function absoluteInviteLink(link: string, origin: string): string {
	if (/^https?:\/\//i.test(link)) return link;
	return `${origin.replace(/\/+$/, "")}${link.startsWith("/") ? "" : "/"}${link}`;
}

export type InviteDelivery = "sent" | "link_only" | "not_configured" | "failed";

/** One sentence on what happened to an invite's email. */
export function describeDelivery(
	delivery: InviteDelivery,
	email: string | undefined,
	error?: string,
): string {
	switch (delivery) {
		case "sent":
			return `Invite emailed to ${email}. The link below works too.`;
		case "not_configured":
			return error
				? `No email was sent: ${error}. Share the link below.`
				: "No email was sent: this team has no email channel and the instance has no SMTP relay. Share the link below.";
		case "failed":
			return `The email could not be sent${error ? `: ${error}` : ""}. Share the link below instead.`;
		default:
			return "Share this link with the person you are inviting.";
	}
}
