import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";
import { siteDev } from "./scripts/lib/dev-plugin.ts";

// `bun run dev` serves this config. `bun run build` calls Vite for the client
// bundle (scripts/build.ts) and renders the HTML itself, so the same source
// produces both.
export default defineConfig({
	plugins: [react(), tailwindcss(), siteDev()],
	appType: "custom",
	server: { port: 5174 },
	build: { sourcemap: false },
});
