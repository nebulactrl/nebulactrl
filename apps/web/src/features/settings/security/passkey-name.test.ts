import { describe, expect, test } from "bun:test";
import { suggestPasskeyName } from "./passkey-name";

describe("suggestPasskeyName", () => {
	test("names the browser and the system", () => {
		expect(
			suggestPasskeyName(
				"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36",
			),
		).toBe("Chrome on macOS");
		expect(
			suggestPasskeyName(
				"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15",
			),
		).toBe("Safari on macOS");
		expect(
			suggestPasskeyName(
				"Mozilla/5.0 (X11; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0",
			),
		).toBe("Firefox on Linux");
		expect(
			suggestPasskeyName(
				"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 Edg/130.0.0.0",
			),
		).toBe("Edge on Windows");
	});

	test("names mobile and Chromium-derived browsers", () => {
		expect(
			suggestPasskeyName(
				"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1",
			),
		).toBe("Safari on iOS");
		expect(
			suggestPasskeyName(
				"Mozilla/5.0 (iPad; CPU OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/130.0.0.0 Mobile/15E148 Safari/604.1",
			),
		).toBe("Chrome on iOS");
		expect(
			suggestPasskeyName(
				"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Mobile Safari/537.36",
			),
		).toBe("Chrome on Android");
		expect(
			suggestPasskeyName(
				"Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36",
			),
		).toBe("Chrome on ChromeOS");
		expect(
			suggestPasskeyName(
				"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 OPR/115.0.0.0",
			),
		).toBe("Opera on Windows");
	});

	test("names the system alone when the browser is unknown, and the browser alone when the system is", () => {
		expect(suggestPasskeyName("SomeApp/1.0 (Windows NT 10.0)")).toBe("Windows");
		expect(suggestPasskeyName("Firefox/131.0")).toBe("Firefox");
	});

	test("falls back to a plain name", () => {
		expect(suggestPasskeyName("")).toBe("Passkey");
		expect(suggestPasskeyName("curl/8.0")).toBe("Passkey");
	});
});
