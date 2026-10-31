import { describe, expect, test } from "bun:test";
import {
	formatSecret,
	isPlausibleCode,
	isTotpShape,
	loginErrorMessage,
	normalizeCode,
	providerStartPath,
	recoveryCodesLevel,
	recoveryCodesText,
	ssoLinkOutcome,
} from "./auth-messages";

describe("loginErrorMessage", () => {
	test("explains every code the callback can send", () => {
		for (const code of [
			"oidc_denied",
			"invalid_oidc_state",
			"oidc_failed",
			"email_not_verified",
			"domain_not_allowed",
			"signup_disabled",
			"invite_invalid",
			"rate_limited",
		]) {
			expect(loginErrorMessage(code)).toBeTruthy();
		}
	});
	test("falls back for a code it does not know and stays silent without one", () => {
		expect(loginErrorMessage("something_new")).toContain("did not complete");
		expect(loginErrorMessage(undefined)).toBeUndefined();
		expect(loginErrorMessage("")).toBeUndefined();
	});
	test("a code that names an Object.prototype member is an unknown code, always a string", () => {
		for (const code of [
			"__proto__",
			"constructor",
			"toString",
			"hasOwnProperty",
			"valueOf",
		]) {
			const message = loginErrorMessage(code);
			expect(typeof message).toBe("string");
			expect(message).toContain("did not complete");
		}
	});
});

describe("codes", () => {
	test("normalizeCode drops whitespace", () => {
		expect(normalizeCode(" 123 456 ")).toBe("123456");
	});
	test("isTotpShape wants exactly six digits", () => {
		expect(isTotpShape("123 456")).toBe(true);
		expect(isTotpShape("12345")).toBe(false);
		expect(isTotpShape("12345a")).toBe(false);
		expect(isTotpShape("abcd-efgh-ijkl-mnop")).toBe(false);
	});
	test("isPlausibleCode checks the shape of the active field", () => {
		expect(isPlausibleCode("123456", false)).toBe(true);
		expect(isPlausibleCode("abcd-efgh-ijkl-mnop", false)).toBe(false);
		expect(isPlausibleCode("abcd-efgh-ijkl-mnop", true)).toBe(true);
		expect(isPlausibleCode("abcdefghijklmnop", true)).toBe(true);
		expect(isPlausibleCode("abcd-efgh", true)).toBe(false);
	});
});

describe("formatSecret", () => {
	test("groups in fours without a trailing space", () => {
		expect(formatSecret("JBSWY3DPEHPK3PXP")).toBe("JBSW Y3DP EHPK 3PXP");
		expect(formatSecret("ABCDEFG")).toBe("ABCD EFG");
		expect(formatSecret("")).toBe("");
	});
});

describe("recoveryCodesText", () => {
	test("lists every code once, with the account", () => {
		const text = recoveryCodesText(
			["aaaa-bbbb-cccc-dddd", "eeee-ffff-gggg-hhhh"],
			"ada@example.com",
		);
		expect(text).toContain("Account: ada@example.com");
		expect(text).toContain("aaaa-bbbb-cccc-dddd\neeee-ffff-gggg-hhhh");
		expect(text).not.toContain("\n\n\n");
	});
	test("omits the account line when unknown", () => {
		expect(recoveryCodesText(["x"], "")).not.toContain("Account:");
	});
});

describe("recoveryCodesLevel", () => {
	test("warns when few are left", () => {
		expect(recoveryCodesLevel(10)).toBe("ok");
		expect(recoveryCodesLevel(4)).toBe("ok");
		expect(recoveryCodesLevel(3)).toBe("low");
		expect(recoveryCodesLevel(1)).toBe("low");
		expect(recoveryCodesLevel(0)).toBe("none");
	});
});

describe("providerStartPath", () => {
	test("points at the server redirect, with the invite when there is one", () => {
		expect(providerStartPath("abc")).toBe("/api/v1/auth/oidc/abc/start");
		expect(providerStartPath("abc", "tok en")).toBe(
			"/api/v1/auth/oidc/abc/start?invite=tok%20en",
		);
	});
});

describe("ssoLinkOutcome", () => {
	test("reads a finished link and a refused one from the fragment", () => {
		expect(ssoLinkOutcome("#sso=linked")).toEqual({ kind: "linked" });
		expect(ssoLinkOutcome("sso=linked")).toEqual({ kind: "linked" });
		const refused = ssoLinkOutcome("#sso-error=identity_conflict");
		expect(refused?.kind).toBe("error");
		expect(refused?.kind === "error" && refused.message).toContain(
			"already linked",
		);
	});
	test("an unknown code still gets a sentence, other fragments say nothing", () => {
		const unknown = ssoLinkOutcome("#sso-error=__proto__");
		expect(unknown?.kind === "error" && unknown.message).toContain(
			"did not complete",
		);
		expect(ssoLinkOutcome("")).toBeUndefined();
		expect(ssoLinkOutcome("#section")).toBeUndefined();
	});
	test("signing in with an existing email explains how to link", () => {
		expect(loginErrorMessage("account_exists")).toContain("Account → Security");
	});
});
