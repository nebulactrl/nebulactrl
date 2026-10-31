import { fileURLToPath } from "node:url";
import { docsPlugin } from "@nebulactrl/docs/plugin";
import tailwindcss from "@tailwindcss/vite";
import { tanstackRouter } from "@tanstack/router-plugin/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// Override with NEBULACTRL_API_URL when 8080 is taken on the dev machine.
const controlPlane = process.env.NEBULACTRL_API_URL ?? "http://127.0.0.1:8080";

// Order matters: the router plugin must generate routeTree.gen.ts before the
// React plugin transforms JSX, and Tailwind runs last as a plain CSS/JS plugin.
export default defineConfig({
	plugins: [
		tanstackRouter({ target: "react", autoCodeSplitting: true }),
		// docs/**/*.md and openapi.json compile into virtual modules for /docs.
		// Release images set VITE_GITHUB_URL to the repository. Without it, links
		// from the docs into the repository render as plain text, as on the site.
		docsPlugin({
			repoRoot: fileURLToPath(new URL("../..", import.meta.url)),
			repoUrl: process.env.VITE_GITHUB_URL?.trim() || null,
		}),
		react(),
		tailwindcss(),
	],
	resolve: {
		alias: {
			"#": fileURLToPath(new URL("./src", import.meta.url)),
		},
	},
	build: {
		outDir: "dist",
		emptyOutDir: true,
		sourcemap: false,
	},
	server: {
		port: 5173,
		proxy: {
			// None of these entries sets selfHandleResponse, so Vite's default
			// proxy streams the response through unbuffered — required for the
			// SSE endpoints (`/api/v1/events`, log followers) and the agent socket.
			// ws: the terminal and host shell sockets are under /api/v1.
			"/api": { target: controlPlane, changeOrigin: true, ws: true },
			"/agent": { target: controlPlane, changeOrigin: true, ws: true },
			"/mcp": { target: controlPlane, changeOrigin: true },
			// The CLI installer and binaries (Account settings -> API tokens); a dev server answers 404 with a reason.
			"/cli.sh": { target: controlPlane, changeOrigin: true },
			"/download": { target: controlPlane, changeOrigin: true },
		},
	},
});
