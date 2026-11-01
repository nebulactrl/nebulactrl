// Certificate lifetime as a bar: how much of the validity window is left.

import type { ServerCertificate } from "#/api/gen/types.gen";

const DAY_MS = 86_400_000;

export type ExpiryTone = "ok" | "warn" | "bad";

export interface CertExpiry {
	/** Share of the validity window still ahead, 0..1 (0 when expired or not yet valid). */
	fraction: number;
	/** Whole days until `notAfter`; negative once expired. */
	daysLeft: number;
	tone: ExpiryTone;
	label: string;
}

/** Amber under 30 days left, red under 7 or when expired. */
export const EXPIRY_WARN_DAYS = 30;
export const EXPIRY_BAD_DAYS = 7;

export function certExpiry(
	cert: Pick<ServerCertificate, "notBefore" | "notAfter">,
	now: number = Date.now(),
): CertExpiry {
	const from = Date.parse(cert.notBefore);
	const to = Date.parse(cert.notAfter);
	if (Number.isNaN(from) || Number.isNaN(to) || to <= from) {
		return { fraction: 0, daysLeft: 0, tone: "bad", label: "Unknown validity" };
	}
	const daysLeft = Math.floor((to - now) / DAY_MS);
	if (now < from) {
		return {
			fraction: 1,
			daysLeft,
			tone: "warn",
			label: `Not valid until ${new Date(from).toISOString().slice(0, 10)}`,
		};
	}
	const fraction = Math.min(1, Math.max(0, (to - now) / (to - from)));
	if (now >= to) {
		const ago = Math.max(1, Math.ceil((now - to) / DAY_MS));
		return {
			fraction: 0,
			daysLeft,
			tone: "bad",
			label: `Expired ${ago} day${ago === 1 ? "" : "s"} ago`,
		};
	}
	const tone: ExpiryTone =
		daysLeft < EXPIRY_BAD_DAYS
			? "bad"
			: daysLeft < EXPIRY_WARN_DAYS
				? "warn"
				: "ok";
	const label =
		daysLeft < 1
			? "Expires within a day"
			: `${daysLeft} day${daysLeft === 1 ? "" : "s"} left`;
	return { fraction, daysLeft, tone, label };
}

/** The first `max` names, then "+N more". */
export function namesSummary(names: readonly string[], max = 3): string {
	if (names.length <= max) return names.join(", ");
	return `${names.slice(0, max).join(", ")} +${names.length - max} more`;
}

/** Short hex fingerprint: `AB:CD:EF:12…`. */
export function shortFingerprint(hex: string): string {
	const pairs = hex.toUpperCase().match(/.{1,2}/g) ?? [];
	return `${pairs.slice(0, 6).join(":")}${pairs.length > 6 ? "…" : ""}`;
}

/** A pasted or uploaded PEM looks like what the upload expects (the server parses it for real). */
export function pemProblem(
	text: string,
	kind: "certificate" | "key",
): string | null {
	const trimmed = text.trim();
	if (trimmed === "") return `Add the ${kind} PEM.`;
	if (trimmed.length > 65_536) return "At most 64 KB.";
	if (kind === "certificate") {
		return /-----BEGIN CERTIFICATE-----/.test(trimmed) &&
			/-----END CERTIFICATE-----/.test(trimmed)
			? null
			: "This is not a PEM certificate (BEGIN CERTIFICATE).";
	}
	if (
		/-----BEGIN ENCRYPTED PRIVATE KEY-----|Proc-Type:.*ENCRYPTED/.test(trimmed)
	) {
		return "The key is passphrase-protected. Remove the passphrase first (openssl rsa -in key.pem -out key-plain.pem).";
	}
	return /-----BEGIN (?:[A-Z]+ )?PRIVATE KEY-----/.test(trimmed)
		? null
		: "This is not a PEM private key (BEGIN PRIVATE KEY).";
}
