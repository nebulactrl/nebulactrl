import { request } from "@playwright/test";
import { e2e } from "./env";

/** The URL a browser opens for a service domain through the agent's Traefik. */
export function serviceUrl(domain: string, path = "/"): string {
	return `https://${domain}:${e2e.ingressPort}${path}`;
}

/**
 * GET through the agent's Traefik on loopback with the service's domain as the
 * Host header (no DNS involved; the certificate is self-signed, so TLS errors
 * are ignored). Returns the status and body; a refused connection is a status
 * of 0 so callers can poll.
 */
export async function getService(
	domain: string,
	path = "/",
): Promise<{ status: number; body: string }> {
	const context = await request.newContext({ ignoreHTTPSErrors: true });
	try {
		const res = await context.get(
			`https://127.0.0.1:${e2e.ingressPort}${path}`,
			{ headers: { Host: domain }, timeout: 10_000, maxRedirects: 0 },
		);
		return { status: res.status(), body: await res.text() };
	} catch {
		return { status: 0, body: "" };
	} finally {
		await context.dispose();
	}
}
