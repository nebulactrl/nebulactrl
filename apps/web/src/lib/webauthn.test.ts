import { afterEach, describe, expect, test } from "bun:test";
import {
	base64urlDecode,
	base64urlEncode,
	classifyCeremonyError,
	createPasskey,
	decodeCreationOptions,
	decodeRequestOptions,
	encodeAuthentication,
	encodeRegistration,
	getPasskey,
	PasskeyError,
	passkeysSupported,
} from "./webauthn";

const bytes = (...values: number[]) => new Uint8Array(values);

describe("base64url", () => {
	test("round-trips bytes the url-safe alphabet changes", () => {
		const raw = bytes(251, 255, 191, 0, 1, 2, 250);
		const text = base64urlEncode(raw);
		expect(text).toBe("-_-_AAEC-g");
		expect(Array.from(base64urlDecode(text))).toEqual(Array.from(raw));
	});

	test("accepts padded input and never pads output", () => {
		expect(base64urlEncode(bytes(1, 2))).toBe("AQI");
		expect(Array.from(base64urlDecode("AQI="))).toEqual([1, 2]);
		expect(Array.from(base64urlDecode("AQI"))).toEqual([1, 2]);
	});

	test("accepts an ArrayBuffer", () => {
		expect(base64urlEncode(bytes(1, 2, 3).buffer)).toBe("AQID");
	});
});

describe("options decoding", () => {
	test("registration options turn ids and the challenge into bytes", () => {
		const out = decodeCreationOptions({
			publicKey: {
				challenge: "AQID",
				rp: { id: "localhost", name: "NebulaCtrl" },
				user: { id: "BAUG", name: "a@b.c", displayName: "A" },
				pubKeyCredParams: [{ type: "public-key", alg: -7 }],
				excludeCredentials: [{ id: "BwgJ", type: "public-key" }],
				authenticatorSelection: { residentKey: "required" },
			},
		});
		expect(Array.from(new Uint8Array(out.challenge as ArrayBuffer))).toEqual([
			1, 2, 3,
		]);
		expect(Array.from(new Uint8Array(out.user.id as ArrayBuffer))).toEqual([
			4, 5, 6,
		]);
		expect(out.user.name).toBe("a@b.c");
		expect(out.excludeCredentials).toHaveLength(1);
		expect(
			Array.from(
				new Uint8Array(out.excludeCredentials?.[0]?.id as ArrayBuffer),
			),
		).toEqual([7, 8, 9]);
		expect(out.authenticatorSelection?.residentKey).toBe("required");
		expect(out.rp.id).toBe("localhost");
	});

	test("registration options without exclusions get an empty list", () => {
		const out = decodeCreationOptions({
			publicKey: {
				challenge: "AQID",
				rp: { name: "x" },
				user: { id: "BAUG", name: "n", displayName: "d" },
				pubKeyCredParams: [],
			},
		});
		expect(out.excludeCredentials).toEqual([]);
	});

	test("request options keep the transports of allowed credentials", () => {
		const out = decodeRequestOptions({
			publicKey: {
				challenge: "AQID",
				rpId: "localhost",
				userVerification: "required",
				allowCredentials: [
					{ id: "BAUG", type: "public-key", transports: ["usb"] },
				],
			},
		});
		expect(out.rpId).toBe("localhost");
		expect(out.allowCredentials?.[0]?.transports).toEqual(["usb"]);
		expect(
			Array.from(new Uint8Array(out.allowCredentials?.[0]?.id as ArrayBuffer)),
		).toEqual([4, 5, 6]);
	});

	test("a discoverable sign-in carries no allow list", () => {
		const out = decodeRequestOptions({
			publicKey: { challenge: "AQID", rpId: "localhost" },
		});
		expect(out.allowCredentials).toEqual([]);
	});
});

describe("response encoding", () => {
	test("a registration becomes the JSON the server parses", () => {
		const json = encodeRegistration({
			id: "AQID",
			rawId: bytes(1, 2, 3).buffer,
			type: "public-key",
			authenticatorAttachment: "platform",
			response: {
				clientDataJSON: bytes(4).buffer,
				attestationObject: bytes(5).buffer,
				getTransports: () => ["internal"],
			},
			getClientExtensionResults: () => ({}),
		});
		expect(json).toEqual({
			id: "AQID",
			rawId: "AQID",
			type: "public-key",
			authenticatorAttachment: "platform",
			clientExtensionResults: {},
			response: {
				clientDataJSON: "BA",
				attestationObject: "BQ",
				transports: ["internal"],
			},
		});
	});

	test("a sign-in keeps the user handle and drops an empty one", () => {
		const base = {
			id: "AQID",
			rawId: bytes(1, 2, 3).buffer,
			type: "public-key",
			response: {
				clientDataJSON: bytes(4).buffer,
				authenticatorData: bytes(5).buffer,
				signature: bytes(6).buffer,
				userHandle: bytes(7).buffer as ArrayBuffer | null,
			},
		};
		expect(encodeAuthentication(base).response.userHandle).toBe("Bw");
		expect(
			"userHandle" in
				encodeAuthentication({
					...base,
					response: { ...base.response, userHandle: null },
				}).response,
		).toBe(false);
		expect(
			"userHandle" in
				encodeAuthentication({
					...base,
					response: { ...base.response, userHandle: new ArrayBuffer(0) },
				}).response,
		).toBe(false);
	});
});

describe("classifyCeremonyError", () => {
	test("tells a dismissed prompt from a duplicate and from a missing capability", () => {
		expect(classifyCeremonyError({ name: "NotAllowedError" }).kind).toBe(
			"cancelled",
		);
		expect(classifyCeremonyError({ name: "InvalidStateError" }).kind).toBe(
			"already_registered",
		);
		expect(classifyCeremonyError({ name: "SecurityError" }).kind).toBe(
			"unsupported",
		);
		expect(classifyCeremonyError(new Error("boom")).kind).toBe("failed");
		expect(classifyCeremonyError("nope").kind).toBe("failed");
	});

	test("keeps an error that is already classified", () => {
		const original = new PasskeyError("failed", "x");
		expect(classifyCeremonyError(original)).toBe(original);
	});
});

describe("passkey ceremonies", () => {
	const realCredentials = Object.getOwnPropertyDescriptor(
		navigator,
		"credentials",
	);
	const realPKC = (window as { PublicKeyCredential?: unknown })
		.PublicKeyCredential;
	const realSecure = Object.getOwnPropertyDescriptor(window, "isSecureContext");

	class FakePublicKeyCredential {
		id = "cred-1";
		type = "public-key";
		rawId = bytes(1, 2, 3).buffer;
		authenticatorAttachment = "platform";
		response: Record<string, unknown>;
		constructor(response: Record<string, unknown>) {
			this.response = response;
		}
		getClientExtensionResults() {
			return { credProps: { rk: true } };
		}
	}

	function stubBrowser(credentials: {
		create?: (options: CredentialCreationOptions) => Promise<unknown>;
		get?: (options: CredentialRequestOptions) => Promise<unknown>;
	}) {
		Object.defineProperty(window, "isSecureContext", {
			configurable: true,
			value: true,
		});
		(window as { PublicKeyCredential?: unknown }).PublicKeyCredential =
			FakePublicKeyCredential;
		(globalThis as { PublicKeyCredential?: unknown }).PublicKeyCredential =
			FakePublicKeyCredential;
		Object.defineProperty(navigator, "credentials", {
			configurable: true,
			value: credentials,
		});
	}

	afterEach(() => {
		if (realCredentials)
			Object.defineProperty(navigator, "credentials", realCredentials);
		else Reflect.deleteProperty(navigator, "credentials");
		(window as { PublicKeyCredential?: unknown }).PublicKeyCredential = realPKC;
		(globalThis as { PublicKeyCredential?: unknown }).PublicKeyCredential =
			realPKC;
		if (realSecure)
			Object.defineProperty(window, "isSecureContext", realSecure);
		else Reflect.deleteProperty(window, "isSecureContext");
	});

	const creation = {
		publicKey: {
			challenge: "AQID",
			rp: { id: "localhost", name: "NebulaCtrl" },
			user: { id: "BAUG", name: "a@b.c", displayName: "A" },
			pubKeyCredParams: [{ type: "public-key" as const, alg: -7 }],
		},
	};
	const request = { publicKey: { challenge: "AQID" } };

	test("passkeysSupported needs a secure context, PublicKeyCredential and navigator.credentials.create", () => {
		stubBrowser({ create: async () => null });
		expect(passkeysSupported()).toBe(true);

		Object.defineProperty(window, "isSecureContext", {
			configurable: true,
			value: false,
		});
		expect(passkeysSupported()).toBe(false);
		Object.defineProperty(window, "isSecureContext", {
			configurable: true,
			value: true,
		});

		(window as { PublicKeyCredential?: unknown }).PublicKeyCredential =
			undefined;
		expect(passkeysSupported()).toBe(false);
		(window as { PublicKeyCredential?: unknown }).PublicKeyCredential =
			FakePublicKeyCredential;

		Object.defineProperty(navigator, "credentials", {
			configurable: true,
			value: {},
		});
		expect(passkeysSupported()).toBe(false);
	});

	test("createPasskey hands the decoded options to the authenticator and encodes what comes back", async () => {
		let received: CredentialCreationOptions | undefined;
		stubBrowser({
			create: async (options) => {
				received = options;
				return new FakePublicKeyCredential({
					clientDataJSON: bytes(9).buffer,
					attestationObject: bytes(8).buffer,
					getTransports: () => ["internal"],
				});
			},
		});
		const result = await createPasskey(creation);
		expect(
			Array.from(new Uint8Array(received?.publicKey?.challenge as ArrayBuffer)),
		).toEqual([1, 2, 3]);
		expect(result).toEqual({
			id: "cred-1",
			rawId: "AQID",
			type: "public-key",
			authenticatorAttachment: "platform",
			clientExtensionResults: { credProps: { rk: true } },
			response: {
				clientDataJSON: "CQ",
				attestationObject: "CA",
				transports: ["internal"],
			},
		});
	});

	test("getPasskey asks for an assertion and encodes it, leaving out an empty user handle", async () => {
		let received: CredentialRequestOptions | undefined;
		stubBrowser({
			create: async () => null,
			get: async (options) => {
				received = options;
				return new FakePublicKeyCredential({
					clientDataJSON: bytes(1).buffer,
					authenticatorData: bytes(2).buffer,
					signature: bytes(3).buffer,
					userHandle: new ArrayBuffer(0),
				});
			},
		});
		const result = await getPasskey(request);
		expect(
			Array.from(new Uint8Array(received?.publicKey?.challenge as ArrayBuffer)),
		).toEqual([1, 2, 3]);
		expect(result.response).toEqual({
			clientDataJSON: "AQ",
			authenticatorData: "Ag",
			signature: "Aw",
		});
	});

	test("without passkey support both ceremonies fail as unsupported, before touching the browser", async () => {
		Object.defineProperty(window, "isSecureContext", {
			configurable: true,
			value: false,
		});
		await expect(createPasskey(creation)).rejects.toMatchObject({
			kind: "unsupported",
		});
		await expect(getPasskey(request)).rejects.toMatchObject({
			kind: "unsupported",
		});
	});

	test("a closed prompt is cancelled and a duplicate authenticator is already_registered", async () => {
		stubBrowser({
			create: async () => {
				throw new DOMException("closed", "NotAllowedError");
			},
			get: async () => {
				throw new DOMException("dup", "InvalidStateError");
			},
		});
		await expect(createPasskey(creation)).rejects.toMatchObject({
			name: "PasskeyError",
			kind: "cancelled",
		});
		await expect(getPasskey(request)).rejects.toMatchObject({
			kind: "already_registered",
		});
	});

	test("a browser that returns something other than a public-key credential fails", async () => {
		stubBrowser({ create: async () => null, get: async () => ({ id: "x" }) });
		const created = createPasskey(creation);
		await expect(created).rejects.toBeInstanceOf(PasskeyError);
		await expect(created).rejects.toMatchObject({
			kind: "failed",
			message: "The browser returned no passkey.",
		});
		await expect(getPasskey(request)).rejects.toMatchObject({
			kind: "failed",
			message: "The browser returned no passkey.",
		});
	});
});
