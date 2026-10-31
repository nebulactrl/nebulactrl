import { describe, expect, test } from "bun:test";
import {
	base64urlDecode,
	base64urlEncode,
	classifyCeremonyError,
	decodeCreationOptions,
	decodeRequestOptions,
	encodeAuthentication,
	encodeRegistration,
	PasskeyError,
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
