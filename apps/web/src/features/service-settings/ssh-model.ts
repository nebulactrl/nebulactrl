/**
 * Pure helpers of the SSH deploy key rows: which git URLs clone over SSH and
 * how fingerprints read in a narrow column. The control plane decides what is
 * valid (internal/gitssh); this only decides what to show while typing.
 */

/**
 * True for `ssh://[user@]host[:port]/path` and the scp form
 * `[user@]host:path` (`git@github.com:org/repo.git`). Other URLs, including
 * anything with `://` that is not ssh, are not SSH.
 */
export function looksLikeSshUrl(raw: string): boolean {
	const url = raw.trim();
	if (url.startsWith("ssh://")) return true;
	if (url === "" || url.includes("://") || /\s/.test(url)) return false;
	return /^(?:[A-Za-z0-9._~-]+@)?(?:\[[0-9A-Fa-f:.]+\]|[^\s:/@[\]]+):.+$/.test(
		url,
	);
}

/** `SHA256:abcdefghij…uvwxyz`: enough of a fingerprint to compare by eye. */
export function shortFingerprint(fingerprint: string): string {
	const [algo, body] = fingerprint.split(":", 2);
	if (!body || body.length <= 20) return fingerprint;
	return `${algo}:${body.slice(0, 10)}…${body.slice(-8)}`;
}

/** "Ed25519", "RSA", "ECDSA" for a key type like `ssh-ed25519`. */
export function keyTypeLabel(type: string): string {
	if (type === "ssh-ed25519") return "Ed25519";
	if (type === "ssh-rsa") return "RSA";
	if (type.startsWith("ecdsa-")) return "ECDSA";
	return type;
}
