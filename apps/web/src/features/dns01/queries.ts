// DNS-01 slice: provider credentials, a server's resolvers and wildcard
// certificates. The shapes mirror internal/dns01 (the generated client is
// the transport; these are the views the UI reads).

import {
	getServerDns01Options,
	listDnsCredentialsOptions,
	listDnsProvidersOptions,
	listLegoDnsProvidersOptions,
} from "#/api/gen/@tanstack/react-query.gen";
import type { StatusKind } from "#/components/ui/status-icon";

/** A lego DNS provider code: cloudflare, route53, ovh, gandiv5, pdns, … */
export type DnsProvider = string;
type StagedChange = "none" | "add" | "update" | "remove";
type CertStatus = "pending" | "active" | "error";

export interface DnsProviderSpec {
	code: DnsProvider;
	name: string;
	help?: string;
	docs: string;
	/** A lego provider without a curated form: free-form variables from lego's catalog. */
	generic?: boolean;
	/** Alternative authentication methods; set every variable of at least one. */
	oneOf?: string[][] | null;
	/** Older names lego also accepts for the code; a credential uses the code. */
	aliases?: string[] | null;
	fields: {
		env: string;
		label: string;
		help?: string;
		secret: boolean;
		required: boolean;
	}[];
}

export interface DnsCredential {
	id: string;
	scope: "team" | "server";
	serverId: string | null;
	name: string;
	provider: DnsProvider;
	providerName: string;
	fields: {
		env: string;
		label: string;
		secret: boolean;
		required: boolean;
		set: boolean;
		value?: string;
	}[];
	rev: number;
	usedBy: { serverId: string; serverName: string }[];
}

export interface DnsResolverView {
	provider: DnsProvider;
	providerName: string;
	credentialId: string;
	credentialName: string;
	delaySeconds: number;
	staging: boolean;
	change: StagedChange;
	/** Derived from the Cloudflare connection: it cannot be edited or removed here. */
	automatic: boolean;
	rotated: boolean;
}

export interface WildcardCertView {
	id: string;
	domain: string;
	names: string[];
	includeApex: boolean;
	provider: DnsProvider;
	coverGenerated: boolean;
	default: boolean;
	status: CertStatus;
	statusMessage: string;
	issuer: string;
	notAfter: string | null;
	daysToExpiry: number | null;
	change: StagedChange;
}

export interface ServerDns01 {
	resolvers: DnsResolverView[];
	wildcards: WildcardCertView[];
	acmeEmail: string;
	baseDomain: string;
	staged: boolean;
}

export interface DomainCert {
	source: "wildcard" | "dns01" | "acme" | "custom" | "none";
	status: CertStatus | "unknown";
	message: string;
	resolver: string;
	wildcard: string;
	issuer: string;
	notAfter: string | null;
	daysToExpiry: number | null;
	/** Why the certificate cannot be issued as configured (HTTP-01 off port 80); "" when fine. */
	warning?: string;
	/** In a connected Cloudflare zone: NebulaCtrl obtains the certificate with DNS-01 through Cloudflare. */
	automatic: boolean;
	/** Automatic DNS-01 is chosen but the server's proxy does not run the Cloudflare resolver yet. */
	staged: boolean;
	/** Why a name in a connected zone uses HTTP-01 instead; "" when it does not. */
	fallback: string;
	/** Which ACME store really holds the host's certificate; "" while there is none. */
	issuedVia: "" | "http-01" | "dns-01";
}

export const serverDns01Query = (serverId: string) =>
	getServerDns01Options({ path: { serverId } });
export const dnsCredentialsQuery = (serverId?: string) =>
	listDnsCredentialsOptions(serverId ? { query: { serverId } } : {});
export const dnsProvidersQuery = () => listDnsProvidersOptions();
/** Every other lego provider, for the generic form. Fetched when the form needs it. */
export const legoProvidersQuery = () => listLegoDnsProvidersOptions();

/** Expiry as words: amber under 30 days, red under 7. */
export function expiryTone(days: number | null): "ok" | "warn" | "bad" {
	if (days === null) return "ok";
	if (days < 7) return "bad";
	if (days < 30) return "warn";
	return "ok";
}

export function expiryLabel(days: number | null): string {
	if (days === null) return "";
	if (days < 0) return `expired ${-days} day${days === -1 ? "" : "s"} ago`;
	if (days < 1) return "expires within a day";
	return `${days} day${days === 1 ? "" : "s"} left`;
}

const SOURCE_TEXT: Record<DomainCert["source"], string> = {
	wildcard: "Covered by a wildcard certificate",
	dns01: "Issued through DNS-01",
	acme: "Issued by Let's Encrypt (HTTP-01) on the first request",
	custom: "Uploaded certificate",
	none: "No TLS: plain HTTP only",
};

/** The certificate of a name in a connected Cloudflare zone, obtained through Cloudflare. */
const CLOUDFLARE_DNS01_TEXT = "Certificate via Cloudflare DNS (DNS-01)";

/** An automatic name whose existing certificate was issued over HTTP-01 and renews that way. */
const HTTP01_RENEWAL_TEXT = "Certificate via HTTP-01, renews over HTTP-01";

/** Said with it: only names without a certificate yet use DNS-01. */
const NEW_NAMES_DNS01_TEXT =
	"New names in this zone use DNS-01 through Cloudflare.";

/** Said while the server's proxy does not run the Cloudflare resolver yet. */
export const CLOUDFLARE_DNS01_STAGED_TEXT = `${CLOUDFLARE_DNS01_TEXT} after the next proxy deploy. The current certificate keeps serving until then.`;

export interface CertificateLine {
	badge: { tone: "live" | "failed" | "off"; kind: StatusKind; label: string };
	/** Where the certificate comes from, in words. */
	source: string;
	/** Why the name does not use DNS-01 through Cloudflare; "" when it does or is not in a connected zone. */
	reason: string;
	/** The domain's certificate is derived from the Cloudflare connection: there is no resolver to choose. */
	automatic: boolean;
}

/**
 * The words of a domain's certificate line: the status badge, where the
 * certificate comes from and, for a name in a connected zone that uses HTTP-01,
 * why. Automatic DNS-01 says so when DNS-01 holds or will issue the
 * certificate, says when it waits for a proxy deploy, and says when an existing
 * certificate stays on HTTP-01.
 */
export function certificateLine(cert: DomainCert): CertificateLine {
	const automatic = cert.automatic && cert.source === "dns01";
	if (automatic && cert.staged) {
		return {
			badge: { tone: "off", kind: "pending", label: "Staged" },
			source: CLOUDFLARE_DNS01_STAGED_TEXT,
			reason: "",
			automatic,
		};
	}
	const badge: CertificateLine["badge"] =
		cert.status === "active"
			? { tone: "live", kind: "healthy", label: "Issued" }
			: cert.status === "error"
				? { tone: "failed", kind: "failed", label: "Problem" }
				: cert.status === "pending"
					? { tone: "off", kind: "pending", label: "Waiting for certificate" }
					: { tone: "off", kind: "unknown", label: "Not observed" };
	if (automatic && cert.issuedVia === "http-01") {
		return {
			badge,
			source: HTTP01_RENEWAL_TEXT,
			reason: NEW_NAMES_DNS01_TEXT,
			automatic,
		};
	}
	const source = automatic
		? CLOUDFLARE_DNS01_TEXT
		: `${SOURCE_TEXT[cert.source]}${cert.wildcard ? ` (${cert.wildcard})` : ""}`;
	return {
		badge,
		source,
		reason: cert.source === "acme" ? cert.fallback : "",
		automatic,
	};
}

/** A resolver's name: the automatic one is derived from the Cloudflare connection. */
export function resolverLabel(
	resolver: Pick<DnsResolverView, "providerName" | "automatic">,
): string {
	return resolver.automatic
		? "Cloudflare (automatic, from the connected account)"
		: resolver.providerName;
}

/** A leading `*.` followed by a valid hostname. */
export function isWildcardHost(host: string): boolean {
	return /^\*\.([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]*[a-z0-9]$/.test(
		host.trim().toLowerCase(),
	);
}
