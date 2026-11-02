// Test helper: a browser that can run WebAuthn, for the passkey buttons.
// happy-dom has neither PublicKeyCredential nor navigator.credentials.
import { spyOn } from "bun:test";

const buffer = (...bytes: number[]) => new Uint8Array(bytes).buffer;

/** What `navigator.credentials.get` hands back for a signed-in passkey. */
export class FakePublicKeyCredential {
	id = "cred-1";
	rawId = buffer(1, 2, 3);
	type = "public-key";
	authenticatorAttachment = "platform";
	response = {
		clientDataJSON: buffer(4, 5),
		authenticatorData: buffer(6, 7),
		signature: buffer(8, 9),
		userHandle: null,
	};
	getClientExtensionResults() {
		return {};
	}
}

/** The JSON `FakePublicKeyCredential` becomes on the wire. */
export const FAKE_ASSERTION = {
	id: "cred-1",
	rawId: "AQID",
	type: "public-key",
	authenticatorAttachment: "platform",
	clientExtensionResults: {},
	response: {
		clientDataJSON: "BAU",
		authenticatorData: "Bgc",
		signature: "CAk",
	},
};

/** The options the server sends for a sign-in ceremony. */
export const REQUEST_OPTIONS = {
	publicKey: { challenge: "AQID", rpId: "localhost", timeout: 60000 },
};

const original = {
	credential: Object.getOwnPropertyDescriptor(
		globalThis,
		"PublicKeyCredential",
	),
	windowCredential: Object.getOwnPropertyDescriptor(
		window,
		"PublicKeyCredential",
	),
	credentials: Object.getOwnPropertyDescriptor(navigator, "credentials"),
	secure: Object.getOwnPropertyDescriptor(window, "isSecureContext"),
};

function restoreProperty(
	target: object,
	name: string,
	descriptor: PropertyDescriptor | undefined,
) {
	if (descriptor) Object.defineProperty(target, name, descriptor);
	else Reflect.deleteProperty(target, name);
}

/**
 * Makes the page WebAuthn-capable. `get` is what the authenticator does when
 * asked to sign in. Call the returned function (afterEach) to undo it.
 */
export function installPasskeys(
	get: () => Promise<unknown> = async () => new FakePublicKeyCredential(),
) {
	for (const target of [globalThis, window]) {
		Object.defineProperty(target, "PublicKeyCredential", {
			value: FakePublicKeyCredential,
			configurable: true,
			writable: true,
		});
	}
	Object.defineProperty(window, "isSecureContext", {
		value: true,
		configurable: true,
	});
	const credentials = { get, create: async () => null };
	Object.defineProperty(navigator, "credentials", {
		value: credentials,
		configurable: true,
	});
	const getSpy = spyOn(credentials, "get");
	return {
		get: getSpy,
		restore() {
			restoreProperty(globalThis, "PublicKeyCredential", original.credential);
			restoreProperty(window, "PublicKeyCredential", original.windowCredential);
			restoreProperty(navigator, "credentials", original.credentials);
			restoreProperty(window, "isSecureContext", original.secure);
		},
	};
}

/** An authenticator prompt the person dismissed. */
export function dismissedPrompt(): Promise<never> {
	return Promise.reject(new DOMException("closed", "NotAllowedError"));
}
