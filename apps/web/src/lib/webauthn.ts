// The browser half of passkeys. The server speaks the WebAuthn JSON form
// (base64url strings); `navigator.credentials` wants ArrayBuffers. The
// conversions are pure functions so they can be tested without a browser; the
// two ceremony functions at the bottom are the only DOM touch points.

/** Base64url (RFC 4648 §5) without padding, as WebAuthn JSON uses. */
export function base64urlEncode(bytes: ArrayBuffer | Uint8Array): string {
	const view = bytes instanceof Uint8Array ? bytes : new Uint8Array(bytes);
	let binary = "";
	for (const byte of view) binary += String.fromCharCode(byte);
	return btoa(binary)
		.replace(/\+/g, "-")
		.replace(/\//g, "_")
		.replace(/=+$/, "");
}

export function base64urlDecode(text: string): Uint8Array<ArrayBuffer> {
	const padded = text.replace(/-/g, "+").replace(/_/g, "/");
	const binary = atob(padded + "=".repeat((4 - (padded.length % 4)) % 4));
	const out = new Uint8Array(new ArrayBuffer(binary.length));
	for (let i = 0; i < binary.length; i++) out[i] = binary.charCodeAt(i);
	return out;
}

interface DescriptorJSON {
	id: string;
	type: PublicKeyCredentialType;
	transports?: AuthenticatorTransport[];
}

function descriptor(item: DescriptorJSON): PublicKeyCredentialDescriptor {
	return {
		id: base64urlDecode(item.id),
		type: item.type,
		...(item.transports ? { transports: item.transports } : {}),
	};
}

/** What the server sends for a registration: `{ publicKey: {...} }`. */
export interface CreationOptionsJSON {
	publicKey: Omit<
		PublicKeyCredentialCreationOptions,
		"challenge" | "user" | "excludeCredentials"
	> & {
		challenge: string;
		user: { id: string; name: string; displayName: string };
		excludeCredentials?: DescriptorJSON[];
	};
}

/** What the server sends for a sign-in: `{ publicKey: {...} }`. */
export interface RequestOptionsJSON {
	publicKey: Omit<
		PublicKeyCredentialRequestOptions,
		"challenge" | "allowCredentials"
	> & {
		challenge: string;
		allowCredentials?: DescriptorJSON[];
	};
}

export function decodeCreationOptions(
	options: CreationOptionsJSON,
): PublicKeyCredentialCreationOptions {
	const { challenge, user, excludeCredentials, ...rest } = options.publicKey;
	return {
		...rest,
		challenge: base64urlDecode(challenge),
		user: { ...user, id: base64urlDecode(user.id) },
		excludeCredentials: (excludeCredentials ?? []).map(descriptor),
	};
}

export function decodeRequestOptions(
	options: RequestOptionsJSON,
): PublicKeyCredentialRequestOptions {
	const { challenge, allowCredentials, ...rest } = options.publicKey;
	return {
		...rest,
		challenge: base64urlDecode(challenge),
		allowCredentials: (allowCredentials ?? []).map(descriptor),
	};
}

/** `PublicKeyCredential.toJSON()` for a registration, spelled out for browsers without it. */
export interface RegistrationResponseJSON {
	id: string;
	rawId: string;
	type: "public-key";
	authenticatorAttachment?: string;
	clientExtensionResults: Record<string, unknown>;
	response: {
		clientDataJSON: string;
		attestationObject: string;
		transports: string[];
		authenticatorData?: string;
		publicKeyAlgorithm?: number;
		publicKey?: string;
	};
}

/** `PublicKeyCredential.toJSON()` for a sign-in. */
export interface AuthenticationResponseJSON {
	id: string;
	rawId: string;
	type: "public-key";
	authenticatorAttachment?: string;
	clientExtensionResults: Record<string, unknown>;
	response: {
		clientDataJSON: string;
		authenticatorData: string;
		signature: string;
		userHandle?: string;
	};
}

export function encodeRegistration(credential: {
	id: string;
	rawId: ArrayBuffer;
	type: string;
	authenticatorAttachment?: string | null;
	response: {
		clientDataJSON: ArrayBuffer;
		attestationObject: ArrayBuffer;
		getTransports?: () => string[];
	};
	getClientExtensionResults?: () => Record<string, unknown>;
}): RegistrationResponseJSON {
	return {
		id: credential.id,
		rawId: base64urlEncode(credential.rawId),
		type: "public-key",
		...(credential.authenticatorAttachment
			? { authenticatorAttachment: credential.authenticatorAttachment }
			: {}),
		clientExtensionResults: credential.getClientExtensionResults?.() ?? {},
		response: {
			clientDataJSON: base64urlEncode(credential.response.clientDataJSON),
			attestationObject: base64urlEncode(credential.response.attestationObject),
			transports: credential.response.getTransports?.() ?? [],
		},
	};
}

export function encodeAuthentication(credential: {
	id: string;
	rawId: ArrayBuffer;
	type: string;
	authenticatorAttachment?: string | null;
	response: {
		clientDataJSON: ArrayBuffer;
		authenticatorData: ArrayBuffer;
		signature: ArrayBuffer;
		userHandle: ArrayBuffer | null;
	};
	getClientExtensionResults?: () => Record<string, unknown>;
}): AuthenticationResponseJSON {
	const { userHandle } = credential.response;
	return {
		id: credential.id,
		rawId: base64urlEncode(credential.rawId),
		type: "public-key",
		...(credential.authenticatorAttachment
			? { authenticatorAttachment: credential.authenticatorAttachment }
			: {}),
		clientExtensionResults: credential.getClientExtensionResults?.() ?? {},
		response: {
			clientDataJSON: base64urlEncode(credential.response.clientDataJSON),
			authenticatorData: base64urlEncode(credential.response.authenticatorData),
			signature: base64urlEncode(credential.response.signature),
			...(userHandle && userHandle.byteLength > 0
				? { userHandle: base64urlEncode(userHandle) }
				: {}),
		},
	};
}

/** Why a ceremony ended without a credential, in words for the person. */
export type PasskeyFailure =
	| "cancelled"
	| "already_registered"
	| "unsupported"
	| "failed";

export class PasskeyError extends Error {
	readonly kind: PasskeyFailure;
	constructor(kind: PasskeyFailure, message: string) {
		super(message);
		this.name = "PasskeyError";
		this.kind = kind;
	}
}

/** Map what `navigator.credentials` threw to a failure kind. */
export function classifyCeremonyError(error: unknown): PasskeyError {
	if (error instanceof PasskeyError) return error;
	const name =
		typeof error === "object" && error !== null && "name" in error
			? String((error as { name: unknown }).name)
			: "";
	switch (name) {
		case "NotAllowedError":
		case "AbortError":
			return new PasskeyError(
				"cancelled",
				"The passkey prompt was closed or timed out.",
			);
		case "InvalidStateError":
			return new PasskeyError(
				"already_registered",
				"This authenticator already holds a passkey for your account.",
			);
		case "NotSupportedError":
		case "SecurityError":
			return new PasskeyError(
				"unsupported",
				"This browser or address cannot use passkeys. Passkeys need HTTPS (or localhost).",
			);
		default:
			return new PasskeyError(
				"failed",
				"The passkey could not be used. Try again.",
			);
	}
}

/** Whether this page can run WebAuthn at all (HTTPS or localhost, and a browser that has it). */
export function passkeysSupported(): boolean {
	return (
		typeof window !== "undefined" &&
		window.isSecureContext &&
		typeof window.PublicKeyCredential !== "undefined" &&
		typeof navigator.credentials?.create === "function"
	);
}

/** Ask the authenticator to create a passkey from the server's creation options. */
export async function createPasskey(
	options: CreationOptionsJSON,
): Promise<RegistrationResponseJSON> {
	if (!passkeysSupported())
		throw classifyCeremonyError({ name: "NotSupportedError" });
	try {
		const credential = await navigator.credentials.create({
			publicKey: decodeCreationOptions(options),
		});
		if (!(credential instanceof PublicKeyCredential)) {
			throw new PasskeyError("failed", "The browser returned no passkey.");
		}
		return encodeRegistration(
			credential as unknown as Parameters<typeof encodeRegistration>[0],
		);
	} catch (error) {
		throw classifyCeremonyError(error);
	}
}

/** Ask the authenticator to sign in with a passkey from the server's request options. */
export async function getPasskey(
	options: RequestOptionsJSON,
): Promise<AuthenticationResponseJSON> {
	if (!passkeysSupported())
		throw classifyCeremonyError({ name: "NotSupportedError" });
	try {
		const credential = await navigator.credentials.get({
			publicKey: decodeRequestOptions(options),
		});
		if (!(credential instanceof PublicKeyCredential)) {
			throw new PasskeyError("failed", "The browser returned no passkey.");
		}
		return encodeAuthentication(
			credential as unknown as Parameters<typeof encodeAuthentication>[0],
		);
	} catch (error) {
		throw classifyCeremonyError(error);
	}
}
