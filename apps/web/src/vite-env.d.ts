/// <reference types="vite/client" />

interface ImportMetaEnv {
	/** Release shown on the sign-in and setup screens; unset omits the caption. */
	readonly VITE_APP_VERSION?: string;
	/** The public site that holds the latest docs; unset leaves the "Latest at" note out. */
	readonly VITE_SITE_URL?: string;
	/** The source repository; release images set it. Unset hides "Edit this page". */
	readonly VITE_GITHUB_URL?: string;
}

interface ImportMeta {
	readonly env: ImportMetaEnv;
}
