// A managed database's public port, as the form sees it. Pure, so the request
// the control plane gets (`PUT /services/{id}/database/public-port`), what it
// would refuse and what the proxy does on apply are tested without a screen.

import type {
	PublicPortView,
	SetPublicPortInputBody,
} from "#/api/gen/types.gen";
import type { LampTone } from "#/components/ui/lamp";
import {
	checkRules,
	cleanList,
	hasOpenCidr,
	type RulesCheck,
	rulesOk,
	sameNetworks,
} from "#/features/access-rules/rules";

const PUBLIC_PORT_MIN = 1024;
const PUBLIC_PORT_MAX = 65535;

/** Said when the control plane sent no warnings of its own. */
export const PLAINTEXT_NOTE =
	"Traffic on this port is plaintext: the proxy forwards raw TCP and never terminates TLS, and the managed database does not enable TLS by itself.";

export const RECREATE_PROXY_NOTE =
	"Applying a new port recreates the proxy for about a second. Connections on this server drop briefly, and HTTP comes back on its own.";

export const NO_PUBLIC_ADDRESS =
	"This server has no public address. Set the server public IP in the server settings.";

export interface PublicPortForm {
	/** Text, so a half-typed number survives. */
	port: string;
	allow: string[];
	deny: string[];
	/** What was typed to confirm opening the port to every address. */
	confirm: string;
}

/** The form an untouched section shows: the saved port, or a free port to start from. */
export function toPublicPortForm(
	view: PublicPortView | undefined,
): PublicPortForm {
	if (view?.enabled) {
		return {
			port: String(view.listenPort ?? view.suggestedPort),
			allow: [...(view.allowCidrs ?? [])],
			deny: [...(view.denyCidrs ?? [])],
			confirm: "",
		};
	}
	return {
		port: view && view.suggestedPort > 0 ? String(view.suggestedPort) : "",
		allow: [],
		deny: [],
		confirm: "",
	};
}

/** A port number from 1024 to 65535, or null. */
export function parsePublicPort(text: string): number | null {
	const trimmed = text.trim();
	if (!/^\d{1,5}$/.test(trimmed)) return null;
	const port = Number(trimmed);
	return port >= PUBLIC_PORT_MIN && port <= PUBLIC_PORT_MAX ? port : null;
}

/** The allow list admits every address of a family: the port then needs the slug typed. */
export function needsOpenConfirmation(allow: readonly string[]): boolean {
	return hasOpenCidr(cleanList(allow));
}

export interface PublicPortProblems {
	port?: string;
	confirm?: string;
	check: RulesCheck;
	/** The first reason Save is disabled, in words. */
	reason?: string;
	valid: boolean;
}

/** What is wrong with the form; mirrors what `SetDatabasePublicPort` refuses. */
export function validatePublicPort(
	form: PublicPortForm,
	slug: string,
): PublicPortProblems {
	const check = checkRules(form.allow, form.deny, { allowRequired: true });
	const problems: PublicPortProblems = { check, valid: false };
	if (parsePublicPort(form.port) === null) {
		problems.port = `Use a port from ${PUBLIC_PORT_MIN} to ${PUBLIC_PORT_MAX}.`;
	}
	if (needsOpenConfirmation(form.allow) && form.confirm.trim() !== slug) {
		problems.confirm = `Type ${slug} to open the port to every address.`;
	}
	const reason =
		problems.port ??
		(Object.keys(check.entries).length > 0
			? "Fix the highlighted networks."
			: (check.allow ?? check.deny ?? check.rules)) ??
		problems.confirm;
	if (reason !== undefined) problems.reason = reason;
	problems.valid = reason === undefined && rulesOk(check);
	return problems;
}

/** The PUT body. Call only with a form that validates. */
export function toSetRequest(
	form: PublicPortForm,
	slug: string,
): SetPublicPortInputBody {
	const deny = cleanList(form.deny);
	return {
		listenPort: Number(form.port.trim()),
		allowCidrs: cleanList(form.allow),
		...(deny.length > 0 ? { denyCidrs: deny } : {}),
		...(needsOpenConfirmation(form.allow) ? { confirmOpen: slug } : {}),
	};
}

/** The form differs from what is saved (an off port always counts as a new setting). */
export function isPublicPortDirty(
	view: PublicPortView,
	form: PublicPortForm,
): boolean {
	if (!view.enabled) return true;
	return (
		form.port.trim() !== String(view.listenPort ?? "") ||
		!sameNetworks(form.allow, view.allowCidrs ?? []) ||
		!sameNetworks(form.deny, view.denyCidrs ?? [])
	);
}

/**
 * Applying `port` makes the proxy open a listener it does not have, which
 * recreates its container. False when the port is the one already applied.
 */
export function willRecreateProxy(
	view: PublicPortView,
	port: number | null,
): boolean {
	if (port === null) return false;
	if (view.enabled && view.listenPort === port) {
		return view.deployed ? false : view.newEntrypoint;
	}
	return true;
}

/** `203.0.113.7:5432` (an IPv6 host in brackets), or null while the server has no public address. */
export function connectionAddress(view: PublicPortView): string | null {
	if (!view.enabled || !view.host || !view.listenPort) return null;
	const host =
		view.host.includes(":") && !view.host.startsWith("[")
			? `[${view.host}]`
			: view.host;
	return `${host}:${view.listenPort}`;
}

export interface PublicPortStatus {
	tone: LampTone;
	label: string;
	detail: string;
}

/** The badge and the sentence beside it, or null when there is nothing to say. */
export function publicPortStatus(
	view: PublicPortView,
): PublicPortStatus | null {
	if (view.staged) {
		return {
			tone: "staged",
			label: "Staged",
			detail: "Deploy the route to apply.",
		};
	}
	if (!view.enabled) return null;
	if (view.deployed) {
		return {
			tone: "live",
			label: "Applied",
			detail: `Open on ${view.serverName || "the server"}, port ${view.listenPort ?? ""}.`,
		};
	}
	return {
		tone: "off",
		label: "Not applied",
		detail: "The route is not deployed yet.",
	};
}

export type PublicPortField = "port" | "allow" | "confirm" | "general";

/** Which field a refusal of the control plane belongs to, and what to tell the reader. */
export function publicPortError(
	code: string | undefined,
	message: string,
): { field: PublicPortField; message: string } {
	switch (code) {
		case "open_confirmation_required":
			return {
				field: "confirm",
				message: "Type the database's slug to open the port to every address.",
			};
		case "allowlist_required":
			return { field: "allow", message: "Add at least one allowed network." };
		case "invalid_listen_port":
			return { field: "port", message };
		case "port_in_use":
			return {
				field: "port",
				message: message || "That port is already in use on this server.",
			};
		case "no_server":
			return {
				field: "general",
				message: "Place the database on a server before opening a port.",
			};
		case "agent_too_old":
			return {
				field: "general",
				message:
					"The agent on this server is too old to publish a port. Update it, then try again.",
			};
		default:
			return { field: "general", message };
	}
}

/** What to tell the reader when revealing the public connection string fails. */
export function revealProblem(
	code: string | undefined,
	message: string,
): string {
	if (code === "no_public_address") return NO_PUBLIC_ADDRESS;
	if (code === "public_port_disabled") {
		return "The public port is closed. Expose it first.";
	}
	return message;
}
