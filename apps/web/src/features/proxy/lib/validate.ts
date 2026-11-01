// Validators that mirror pkg/agentprotocol/proxy.go and internal/domains/rule.go.
// The control plane stays authoritative (it answers 422 with a code); these
// only catch the same mistakes while typing. Each returns an error sentence, or
// null when the value is fine.

const HOST_RE = /^[A-Za-z0-9]([A-Za-z0-9._-]{0,251}[A-Za-z0-9])?$/;
const PATH_PREFIX_RE = /^\/[A-Za-z0-9._~/-]*[A-Za-z0-9._~-]$/;
const HEADER_NAME_RE = /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/;
const CORS_ORIGIN_RE = /^https?:\/\/[A-Za-z0-9.-]+(:[0-9]{1,5})?$/;
const METHOD_RE = /^[A-Z]{3,10}$/;
const BASIC_USER_RE = /^[A-Za-z0-9._-]{1,64}$/;

/** Headers an operator can never set or remove on requests. */
const FORBIDDEN_REQUEST_HEADERS: Record<string, true> = {
	host: true,
	"content-length": true,
	"transfer-encoding": true,
	connection: true,
	upgrade: true,
	te: true,
	trailer: true,
	"keep-alive": true,
	"proxy-authorization": true,
	"proxy-connection": true,
	"set-cookie": true,
	"x-forwarded-for": true,
	"x-real-ip": true,
};

/** Ports the proxy keeps for itself: HTTP, HTTPS, its probe and admin entrypoints. */
const RESERVED_LISTEN_PORTS: readonly number[] = [80, 443, 8099, 9000];

export const MAX_HEADERS = 30;
export const MAX_REDIRECTS = 10;
export const MAX_CORS_ORIGINS = 20;
export const MAX_BASIC_USERS = 50;

export function normalizeHostname(value: string): string {
	return value.trim().toLowerCase().replace(/\.$/, "");
}

function validHostname(host: string): boolean {
	return HOST_RE.test(host);
}

export function validateHostname(host: string): string | null {
	if (host.trim() === "") return "Enter a hostname.";
	return validHostname(normalizeHostname(host))
		? null
		: "Use a hostname such as app.example.com (no scheme, no path).";
}

/** A service domain: a hostname, or a wildcard hostname (`*.example.com`). */
export function validateDomainHostname(host: string): string | null {
	const h = normalizeHostname(host);
	if (h.startsWith("*.")) {
		return /^\*\.([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]*[a-z0-9]$/.test(
			h,
		)
			? null
			: "Use a wildcard such as *.example.com.";
	}
	return validateHostname(host);
}

/** "" or "/x[/y]" with no trailing slash, at most 200 characters. */
export function validatePathPrefix(prefix: string): string | null {
	if (prefix === "") return null;
	if (
		prefix.length > 200 ||
		!PATH_PREFIX_RE.test(prefix) ||
		prefix.includes("//") ||
		prefix.includes("/../") ||
		prefix.endsWith("/..")
	) {
		return "Use /segment or /segment/segment, without a trailing slash.";
	}
	return null;
}

function validIPv4(text: string): boolean {
	const parts = text.split(".");
	if (parts.length !== 4) return false;
	return parts.every(
		(part) =>
			/^\d{1,3}$/.test(part) &&
			Number(part) <= 255 &&
			(part === "0" || !part.startsWith("0")),
	);
}

function validIPv6(text: string): boolean {
	if (!text.includes(":") || /[^0-9A-Fa-f:.]/.test(text)) return false;
	try {
		new URL(`http://[${text}]/`);
		return true;
	} catch {
		return false;
	}
}

export function validateUsername(user: string): string | null {
	return BASIC_USER_RE.test(user)
		? null
		: "Use 1-64 letters, digits, dots, dashes or underscores.";
}

export function validateHeaderName(
	name: string,
	request: boolean,
): string | null {
	if (name === "" || name.length > 100 || !HEADER_NAME_RE.test(name)) {
		return "Not a valid header name.";
	}
	if (request && FORBIDDEN_REQUEST_HEADERS[name.toLowerCase()] === true) {
		return `${name} cannot be set on requests.`;
	}
	if (!request && name.toLowerCase() === "set-cookie") {
		return "Set-Cookie cannot be set by the proxy.";
	}
	return null;
}

export function validateHeaderValue(value: string): string | null {
	if (value.length > 2048 || /[\r\n\0]/.test(value)) {
		return "Too long, or contains a line break.";
	}
	return null;
}

export function validateCorsOrigin(origin: string): string | null {
	return origin === "*" || CORS_ORIGIN_RE.test(origin)
		? null
		: "Use * or an origin like https://app.example.com.";
}

export function validateCorsMethod(method: string): string | null {
	return METHOD_RE.test(method) ? null : "Use upper-case methods such as GET.";
}

export function validateCorsHeaderName(name: string): string | null {
	return HEADER_NAME_RE.test(name) ? null : "Not a valid header name.";
}

/**
 * Constructs the control plane's regexp engine (RE2) rejects but JavaScript
 * accepts: lookaround, backreferences and atomic groups.
 */
const NON_RE2 = /\(\?(?:=|!|<=|<!|>)|\\[1-9]/;

/** A redirect rule's regex, compiled the way the backend does (Go RE2 syntax). */
export function validateRedirectRegex(regex: string): string | null {
	if (regex === "") return "Enter a regex.";
	if (regex.length > 512) return "At most 512 characters.";
	if (NON_RE2.test(regex)) {
		return "Lookaround and backreferences are not supported (RE2 syntax).";
	}
	// Go writes named groups (?P<n>…) and leading flags (?i): neither exists in
	// every JavaScript engine, so peel them off before the compile check.
	const probe = regex.replace(/^\(\?[imsU]+\)/, "").replace(/\(\?P</g, "(?<");
	try {
		new RegExp(probe);
	} catch (error) {
		const detail = error instanceof Error ? error.message : "invalid regex";
		return `Invalid regex: ${detail.replace(/^Invalid regular expression: /, "")}`;
	}
	return null;
}

export function validateRedirectReplacement(
	replacement: string,
): string | null {
	if (replacement === "") return "Enter a replacement.";
	if (replacement.length > 2048 || /[\r\n\0]/.test(replacement)) {
		return "At most 2048 characters, no line breaks.";
	}
	return null;
}

/** A tcp/udp host port: 1024-65535, not one of the proxy's own. */
export function validateListenPort(port: number): string | null {
	if (!Number.isInteger(port) || port < 1024 || port > 65535) {
		return "Choose a port between 1024 and 65535.";
	}
	if (RESERVED_LISTEN_PORTS.includes(port)) {
		return `Port ${port} is reserved for the proxy.`;
	}
	return null;
}

/** An external upstream: host:port (IPv6 in brackets). */
export function validateUpstreamAddress(address: string): string | null {
	const text = address.trim();
	if (text === "") return "Enter host:port.";
	let host: string;
	let port: string;
	const bracket = /^\[([^\]]+)\]:(\d+)$/.exec(text);
	if (bracket) {
		host = bracket[1] ?? "";
		port = bracket[2] ?? "";
		if (!validIPv6(host)) return "Not a valid IPv6 address.";
	} else {
		const colon = text.lastIndexOf(":");
		if (colon <= 0) return "Use host:port, e.g. 192.168.1.20:8080.";
		host = text.slice(0, colon);
		port = text.slice(colon + 1);
		if (host.includes(":"))
			return "Put IPv6 addresses in brackets: [fd7a::1]:80.";
		if (!validIPv4(host) && !validHostname(host)) {
			return "The host is not a valid name or IP address.";
		}
	}
	const n = Number(port);
	if (!/^\d+$/.test(port) || n < 1 || n > 65535) {
		return "The port must be between 1 and 65535.";
	}
	return null;
}

/** Wildcard (`*.example.com`, one label) or exact certificate name cover a host. */
export function certCovers(names: readonly string[], host: string): boolean {
	const wanted = host.toLowerCase();
	return names.some((raw) => {
		const name = raw.toLowerCase();
		if (name === wanted) return true;
		if (!name.startsWith("*.")) return false;
		const dot = wanted.indexOf(".");
		return dot > 0 && wanted.slice(dot + 1) === name.slice(2);
	});
}
