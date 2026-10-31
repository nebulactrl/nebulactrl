import { describe, expect, test } from "bun:test";
import { securityPrimary } from "./primary-action";

describe("securityPrimary", () => {
	test("no second factor yet: exactly one card is primary, the passkey when it can be added", () => {
		expect(
			securityPrimary({
				totpEnabled: false,
				passkeyCount: 0,
				canAddPasskey: true,
			}),
		).toBe("passkey");
	});
	test("passkeys unavailable here: the authenticator app takes the key", () => {
		expect(
			securityPrimary({
				totpEnabled: false,
				passkeyCount: 0,
				canAddPasskey: false,
			}),
		).toBe("totp");
	});
	test("with any second factor nothing is primary", () => {
		expect(
			securityPrimary({
				totpEnabled: true,
				passkeyCount: 0,
				canAddPasskey: true,
			}),
		).toBe("none");
		expect(
			securityPrimary({
				totpEnabled: false,
				passkeyCount: 2,
				canAddPasskey: true,
			}),
		).toBe("none");
		expect(
			securityPrimary({
				totpEnabled: true,
				passkeyCount: 1,
				canAddPasskey: false,
			}),
		).toBe("none");
	});
});
