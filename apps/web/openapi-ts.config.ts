import { defineConfig } from "@hey-api/openapi-ts";

// The control plane's OpenAPI document (openapi/openapi.json at the repo
// root, written by `nebulactrl-server openapi`) is the contract; everything
// under src/api/gen is regenerated wholesale from it and committed, so an API
// change shows up as a reviewable diff on both sides.
//
// openapi-ts imports the TypeScript JS API at runtime, which TypeScript 7 does
// not have. In this package `typescript` is therefore the TS 6 compatibility
// package; `tsc` (type checking) is TS 7 via the `@typescript/native` alias.
export default defineConfig({
	input: "../../openapi/openapi.json",
	output: { path: "./src/api/gen", clean: true },
	plugins: [
		"@hey-api/typescript",
		{ name: "@hey-api/sdk", operations: { strategy: "flat" } },
		{
			name: "@hey-api/client-fetch",
			throwOnError: true,
		},
		{
			name: "@tanstack/react-query",
			queryOptions: true,
			queryKeys: true,
			infiniteQueryOptions: true,
			infiniteQueryKeys: true,
			mutationOptions: true,
		},
	],
});
