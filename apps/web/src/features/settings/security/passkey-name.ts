/**
 * A starting name for a new passkey, from the browser's user agent: "Chrome on
 * macOS". The person can change it; it only helps them tell passkeys apart.
 */
export function suggestPasskeyName(userAgent: string): string {
	const browser = /Edg\//.test(userAgent)
		? "Edge"
		: /OPR\/|Opera/.test(userAgent)
			? "Opera"
			: /Firefox\//.test(userAgent)
				? "Firefox"
				: /Chrome\/|CriOS\//.test(userAgent)
					? "Chrome"
					: /Safari\//.test(userAgent)
						? "Safari"
						: "";
	const system = /iPhone|iPad|iPod/.test(userAgent)
		? "iOS"
		: /Android/.test(userAgent)
			? "Android"
			: /Mac OS X|Macintosh/.test(userAgent)
				? "macOS"
				: /Windows/.test(userAgent)
					? "Windows"
					: /CrOS/.test(userAgent)
						? "ChromeOS"
						: /Linux|X11/.test(userAgent)
							? "Linux"
							: "";
	if (browser && system) return `${browser} on ${system}`;
	return browser || system || "Passkey";
}
