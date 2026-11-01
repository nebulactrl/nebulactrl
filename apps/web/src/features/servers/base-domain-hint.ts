/**
 * What an empty base domain means for a server: apps get `<ip>.sslip.io`, so
 * with no public IP yet there is nothing to fall back to and the field says so.
 */
export function baseDomainHint(publicIp: string): string {
	const ip = publicIp.trim();
	if (ip === "") {
		return "No public IP yet, so there is no fallback. Set the public IP above, or enter a domain here.";
	}
	return `Empty falls back to ${ip}.sslip.io.`;
}
