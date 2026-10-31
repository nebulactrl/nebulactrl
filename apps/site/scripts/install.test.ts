import { describe, expect, test } from "bun:test";
import fs from "node:fs";
import path from "node:path";
import { assembleInstaller } from "./lib/installer.ts";

// The site's /install.sh must be byte for byte what the control plane serves at
// GET /install.sh. Both are `installdeploy.Script()` (deploy/install/embed.go):
// the control plane calls it in internal/agents/installer.go, the site through
// `go run ./tests/install/assemble`. The Go test TestControlPlaneServesTheInstaller
// pins the HTTP response to Script(); these tests pin the site to the same output.

const repo = path.resolve(import.meta.dir, "../../..");

describe("install.sh", () => {
	const built = assembleInstaller();

	test("it is the assembled installer: libraries and assets inlined, a shell script", () => {
		const text = built.toString("utf8");
		expect(text.startsWith("#!/")).toBe(true);
		expect(text).toContain("NC_ASSET_EOF_0");
		expect(text).not.toContain("NEBULACTRL_SECRET_KEY=base64");
	});

	test("it is byte identical to `go run ./tests/install/assemble`", () => {
		const result = Bun.spawnSync(["go", "run", "./tests/install/assemble"], {
			cwd: repo,
		});
		expect(result.exitCode).toBe(0);
		expect(Buffer.compare(built, Buffer.from(result.stdout))).toBe(0);
	});

	test("dist/install.sh, when a build exists, is the same bytes", () => {
		const file = path.join(import.meta.dir, "../dist/install.sh");
		if (!fs.existsSync(file)) return;
		expect(Buffer.compare(fs.readFileSync(file), built)).toBe(0);
	});

	// Opt in with NEBULACTRL_CONTROL_PLANE_URL=http://127.0.0.1:18090 (a control plane
	// built from this checkout): compares the bytes it actually serves.
	const live = process.env.NEBULACTRL_CONTROL_PLANE_URL;
	test.skipIf(!live)(
		"a running control plane serves the same bytes at /install.sh",
		async () => {
			const response = await fetch(`${live}/install.sh`);
			expect(response.status).toBe(200);
			expect(
				Buffer.compare(Buffer.from(await response.arrayBuffer()), built),
			).toBe(0);
		},
	);
});
