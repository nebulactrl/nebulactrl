import { spawnSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const repoRoot = path.resolve(
	fileURLToPath(new URL(".", import.meta.url)),
	"../../../..",
);

/**
 * The installer exactly as the control plane serves it at GET /install.sh:
 * deploy/install/install.sh with its libraries and files inlined, assembled by
 * `installdeploy.Script()` (deploy/install/embed.go). `go run
 * ./tests/install/assemble` prints that same function's result, so the site
 * calls it instead of re-implementing the assembly.
 *
 * `NEBULACTRL_INSTALL_SH` may point at a pre-assembled copy for a build host
 * without Go (a CI artifact of `go run ./tests/install/assemble`).
 */
export function assembleInstaller(): Buffer {
	const prebuilt = process.env.NEBULACTRL_INSTALL_SH;
	if (prebuilt) return fs.readFileSync(prebuilt);
	const result = spawnSync("go", ["run", "./tests/install/assemble"], {
		cwd: repoRoot,
		maxBuffer: 16 * 1024 * 1024,
	});
	if (result.error) {
		throw new Error(
			[
				`could not run \`go run ./tests/install/assemble\`: ${result.error.message}`,
				"The site serves the installer the control plane serves, assembled by Go.",
				"Install Go, or set NEBULACTRL_INSTALL_SH to a file produced by that command.",
			].join("\n"),
		);
	}
	if (result.status !== 0) {
		throw new Error(
			`installer assembly failed (exit ${result.status}):\n${result.stderr.toString()}`,
		);
	}
	return result.stdout;
}
