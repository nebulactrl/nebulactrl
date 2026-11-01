import { describe, expect, test } from "bun:test";
import { keyTypeLabel, looksLikeSshUrl, shortFingerprint } from "./ssh-model";

describe("looksLikeSshUrl", () => {
	test("accepts ssh:// and the scp form", () => {
		for (const url of [
			"ssh://git@github.com/acme/shop.git",
			"ssh://git@git.example.com:2222/acme/shop.git",
			"git@github.com:acme/shop.git",
			"github.com:acme/shop",
			"deploy@[2001:db8::1]:acme/shop.git",
			"  git@gitlab.com:g/p.git  ",
		]) {
			expect(looksLikeSshUrl(url)).toBe(true);
		}
	});
	test("rejects http(s), git:// and non-URLs", () => {
		for (const url of [
			"",
			"https://github.com/acme/shop.git",
			"http://localhost:3000/acme/shop.git",
			"git://127.0.0.1:9418/app.git",
			"just some text",
			"github.com",
			"git@github.com",
		]) {
			expect(looksLikeSshUrl(url)).toBe(false);
		}
	});
});

describe("shortFingerprint", () => {
	test("keeps both ends of a long fingerprint", () => {
		const fp = "SHA256:nThbg6kXUpJWGl7E1IGOCspRomTxdCARLviKw6E5SY8";
		expect(shortFingerprint(fp)).toBe("SHA256:nThbg6kXUp…Kw6E5SY8");
	});
	test("leaves a short value alone", () => {
		expect(shortFingerprint("SHA256:abc")).toBe("SHA256:abc");
	});
});

test("keyTypeLabel names the algorithm", () => {
	expect(keyTypeLabel("ssh-ed25519")).toBe("Ed25519");
	expect(keyTypeLabel("ssh-rsa")).toBe("RSA");
	expect(keyTypeLabel("ecdsa-sha2-nistp256")).toBe("ECDSA");
	expect(keyTypeLabel("sk-ssh-ed25519@openssh.com")).toBe(
		"sk-ssh-ed25519@openssh.com",
	);
});
