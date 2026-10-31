/**
 * The words of the domain editor that depend on the domain itself: whether it
 * is a local name, and what its path prefix does to a request. Pure, so the
 * copy can be tested without drawing the editor.
 */

/** Said beside a control that acts on the running system the moment it changes. */
export const APPLIES_NOW = "Applies now";

/** Said beside a control that saves as soon as it changes but only takes effect on a deploy. */
export const SAVES_STAGED = "Saves at once · applies on next deploy";

const LOCAL_SUFFIXES = [
	".localhost",
	".local",
	".internal",
	".home.arpa",
	".test",
	".example",
	".invalid",
];

/**
 * Whether the proxy treats a hostname as local, the way the agent does
 * (`isLocalName`): an IP address, a name without a dot, or one under a
 * reserved suffix. Such a name needs no public DNS record and gets no public
 * certificate. A leading `*.` is looked past, so is a trailing dot.
 */
export function isLocalHost(hostname: string): boolean {
	const host = hostname
		.trim()
		.toLowerCase()
		.replace(/^\*\./, "")
		.replace(/\.$/, "");
	if (host === "") return false;
	// IPv4 dotted quad, or anything with a colon (IPv6).
	if (/^\d{1,3}(\.\d{1,3}){3}$/.test(host) || host.includes(":")) return true;
	if (!host.includes(".")) return true;
	return LOCAL_SUFFIXES.some((suffix) => host.endsWith(suffix));
}

/** The certificate sentence of a local name, which the proxy serves its default certificate for. */
export const LOCAL_CERTIFICATE_NOTE =
	"A local hostname gets the proxy's default certificate. No public certificate is requested.";

/**
 * Whether the certificate block should show its status and resolver choice. A
 * local name that has no issued certificate has nothing to report: "Not
 * observed, issued by Let's Encrypt" would promise what never happens.
 */
export function showsCertificateStatus(
	hostname: string,
	cert: { status: string } | null,
	tlsAuto: boolean,
): boolean {
	if (!tlsAuto) return true;
	if (!isLocalHost(hostname)) return true;
	return cert?.status === "active";
}

/**
 * What the www redirect option explains. A public name needs a record per
 * hostname and a certificate for each; a local one needs neither.
 */
export function wwwRedirectHelp(input: {
	on: boolean;
	hostname: string;
	counterpart: string;
}): string {
	const { on, hostname, counterpart } = input;
	const what = on
		? `${counterpart} answers with a permanent redirect (301) to ${hostname}, keeping the path and query. `
		: `Also route ${counterpart} and send its visitors here with a permanent redirect (301). `;
	return isLocalHost(hostname)
		? `${what}Local hostnames need no DNS record and no public certificate.`
		: `${what}Both hostnames get a certificate and each needs its own DNS record: check the DNS status of both.`;
}

/** A path as the proxy writes it: a leading slash, no trailing one, "" for none. */
export function normalisePrefix(prefix: string): string {
	const trimmed = prefix.trim().replace(/\/+$/, "");
	if (trimmed === "") return "";
	return trimmed.startsWith("/") ? trimmed : `/${trimmed}`;
}

/**
 * The example under "Strip the prefix", built from the prefix being edited:
 * `/api` turns `/api/users` into `/users`, and an added prefix goes on after
 * the strip. Null while there is no path prefix, since there is nothing to
 * strip.
 */
export function stripPrefixExample(
	pathPrefix: string,
	addPrefix: string,
): { from: string; to: string } | null {
	const prefix = normalisePrefix(pathPrefix);
	if (prefix === "") return null;
	return {
		from: `${prefix}/users`,
		to: `${normalisePrefix(addPrefix)}/users`,
	};
}
