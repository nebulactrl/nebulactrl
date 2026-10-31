// Linking a single-sign-on account from Account → Security: which providers
// can still be linked, and what the button says.

/** The providers the person has no linked account at, in the order given. */
export function unlinkedProviders<P extends { id: string }>(
	providers: readonly P[],
	identities: readonly { providerId: string }[],
): P[] {
	const linked = new Set(identities.map((identity) => identity.providerId));
	return providers.filter((provider) => !linked.has(provider.id));
}

/** "Link your SSO account" when there is one provider to link, else the provider by name. */
export function linkLabel(
	provider: { name: string },
	linkable: number,
): string {
	return linkable === 1 ? "Link your SSO account" : `Link ${provider.name}`;
}
