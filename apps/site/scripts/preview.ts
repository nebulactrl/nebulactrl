/**
 * Serves apps/site/dist the way a static host would: directory indexes, the
 * branded 404 with a 404 status, correct types for .md and install.sh, and gzip
 * so transfer sizes in a browser or Lighthouse run are realistic.
 * `bun run preview` (PORT, default 4174).
 */
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(fileURLToPath(new URL(".", import.meta.url)), "..");
const dist = path.join(root, "dist");
if (!fs.existsSync(path.join(dist, "index.html"))) {
	console.error(
		"dist is empty: run `SITE_URL=https://example.test bun run build` first",
	);
	process.exit(1);
}

const TYPES: Record<string, string> = {
	".html": "text/html; charset=utf-8",
	".css": "text/css; charset=utf-8",
	".js": "text/javascript; charset=utf-8",
	".json": "application/json; charset=utf-8",
	".txt": "text/plain; charset=utf-8",
	".md": "text/markdown; charset=utf-8",
	".xml": "application/xml; charset=utf-8",
	".svg": "image/svg+xml",
	".ico": "image/x-icon",
	".png": "image/png",
	".avif": "image/avif",
	".webp": "image/webp",
	".woff2": "font/woff2",
	".sh": "text/x-shellscript; charset=utf-8",
};
const COMPRESSIBLE = new Set([
	".html",
	".css",
	".js",
	".json",
	".txt",
	".md",
	".xml",
	".svg",
	".sh",
]);

function resolve(pathname: string): string | null {
	const clean = path
		.normalize(decodeURIComponent(pathname))
		.replace(/^(\.\.[/\\])+/, "");
	for (const candidate of [
		clean,
		path.join(clean, "index.html"),
		`${clean}.html`,
	]) {
		const file = path.join(dist, candidate);
		if (
			file.startsWith(dist + path.sep) &&
			fs.existsSync(file) &&
			fs.statSync(file).isFile()
		)
			return file;
	}
	return null;
}

const port = Number(process.env.PORT ?? 4174);
Bun.serve({
	port,
	fetch(request) {
		const url = new URL(request.url);
		const file = resolve(url.pathname);
		const status = file ? 200 : 404;
		const target = file ?? path.join(dist, "404.html");
		const ext = path.extname(target);
		const headers: Record<string, string> = {
			"Content-Type": TYPES[ext] ?? "application/octet-stream",
			"Cache-Control": url.pathname.startsWith("/assets/")
				? "public, max-age=31536000, immutable"
				: "no-cache",
			"X-Content-Type-Options": "nosniff",
		};
		let body: Uint8Array<ArrayBuffer> = new Uint8Array(fs.readFileSync(target));
		if (
			COMPRESSIBLE.has(ext) &&
			(request.headers.get("accept-encoding") ?? "").includes("gzip")
		) {
			body = Bun.gzipSync(body);
			headers["Content-Encoding"] = "gzip";
			headers.Vary = "Accept-Encoding";
		}
		return new Response(request.method === "HEAD" ? null : body, {
			status,
			headers,
		});
	},
});
console.log(
	`site preview: http://localhost:${port}  (serving ${path.relative(process.cwd(), dist) || "dist"})`,
);
