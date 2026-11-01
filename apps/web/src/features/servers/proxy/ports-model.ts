// The "Proxy ports" card's logic: where the ports come from, the form's
// validation (it mirrors the control plane's ValidatePorts and the agent's
// ValidateProxyPorts) and the request body. No React here.

/** The part of the server proxy view's `ports` the card reads. */
export interface PortsView {
	http: number;
	https: number;
	appliedHttp: number;
	appliedHttps: number;
	source: "" | "flag" | "server";
	staged: boolean;
	setHttp: number | null;
	setHttps: number | null;
	agentHttp: number;
	agentHttps: number;
	busyOther: number[] | null;
	reported: boolean;
	supported: boolean;
}

/** A TCP/UDP route port reserved on the server. */
export interface ReservedPort {
	protocol: string;
	port: number;
	serviceName: string;
}

/** The proxy's admin entrypoint (the agent's default; the control plane refuses it too). */
const ADMIN_PORT = 9000;
const MIN_PORT = 1;
const MAX_PORT = 65535;

/** What the form holds: text per port; blank = the agent's own port. */
export interface PortsForm {
	http: string;
	https: string;
}

interface PortsErrors {
	http?: string;
	https?: string;
	/** A problem with the pair (equal ports, a route that holds one). */
	form?: string;
}

export function sourceLabel(source: PortsView["source"]): string {
	switch (source) {
		case "flag":
			return "Set in the agent's service flags";
		case "server":
			return "Server setting";
		default:
			return "Not reported yet";
	}
}

/** The form's starting values: the staged setting, blank when none. */
export function formOf(
	ports: Pick<PortsView, "setHttp" | "setHttps">,
): PortsForm {
	return {
		http: ports.setHttp === null ? "" : String(ports.setHttp),
		https: ports.setHttps === null ? "" : String(ports.setHttps),
	};
}

type Parsed =
	| { blank: true }
	| { blank: false; port: number }
	| { error: string };

function parse(text: string, name: string): Parsed {
	const t = text.trim();
	if (t === "") return { blank: true };
	if (!/^\d+$/.test(t)) return { error: `${name} port must be a number.` };
	const port = Number(t);
	if (port < MIN_PORT || port > MAX_PORT) {
		return {
			error: `${name} port must be between ${MIN_PORT} and ${MAX_PORT}.`,
		};
	}
	return { blank: false, port };
}

export interface PortsCheck {
	errors: PortsErrors;
	/** The request body when the form is valid: 0 = the agent's own port. */
	body: { http: number; https: number } | null;
	/** The ports the proxy would listen on after a deploy. */
	effective: { http: number; https: number };
}

/**
 * Validates the form against what the control plane and the agent refuse:
 * numbers in range, HTTP and HTTPS different, neither the admin port, neither
 * reserved by a TCP/UDP route (tcp on either, udp on HTTPS).
 */
export function checkPorts(
	form: PortsForm,
	ports: Pick<PortsView, "agentHttp" | "agentHttps">,
	reserved: readonly ReservedPort[] = [],
): PortsCheck {
	const errors: PortsErrors = {};
	const h = parse(form.http, "HTTP");
	const s = parse(form.https, "HTTPS");
	if ("error" in h) errors.http = h.error;
	if ("error" in s) errors.https = s.error;
	const effective = {
		http: "port" in h && !h.blank ? h.port : ports.agentHttp,
		https: "port" in s && !s.blank ? s.port : ports.agentHttps,
	};
	if (!errors.http && !errors.https) {
		if (effective.http === effective.https) {
			errors.form = `HTTP and HTTPS must use different ports (both are ${effective.http}).`;
		} else if (effective.http === ADMIN_PORT) {
			errors.http = `Port ${ADMIN_PORT} is the proxy's admin port.`;
		} else if (effective.https === ADMIN_PORT) {
			errors.https = `Port ${ADMIN_PORT} is the proxy's admin port.`;
		} else {
			const clash = reserved.find(
				(r) =>
					(r.protocol === "tcp" &&
						(r.port === effective.http || r.port === effective.https)) ||
					(r.protocol === "udp" && r.port === effective.https),
			);
			if (clash) {
				errors.form = `${clash.protocol.toUpperCase()} port ${clash.port} is reserved by the route ${clash.serviceName}; move that route or choose other ports.`;
			}
		}
	}
	const valid = Object.keys(errors).length === 0;
	const body = valid
		? {
				http: "port" in h && !h.blank ? h.port : 0,
				https: "port" in s && !s.blank ? s.port : 0,
			}
		: null;
	return { errors, body, effective };
}

/** Whether the form differs from the staged setting (so there is something to preview). */
export function isDirty(
	form: PortsForm,
	ports: Pick<PortsView, "setHttp" | "setHttps">,
): boolean {
	const now = formOf(ports);
	return form.http.trim() !== now.http || form.https.trim() !== now.https;
}

/** Whether anything is set that "Reset to agent defaults" would clear. */
export function canReset(
	ports: Pick<PortsView, "setHttp" | "setHttps">,
): boolean {
	return ports.setHttp !== null || ports.setHttps !== null;
}

/** "8080 and 8443", or "80 and 443". */
export function portsText(http: number, https: number): string {
	return `${http} and ${https}`;
}
