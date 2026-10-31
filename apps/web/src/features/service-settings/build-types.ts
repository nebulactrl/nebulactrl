/** The build methods a Git service can use, shared by Settings and the source switch. */
export const BUILD_TYPES: ReadonlyArray<{
	value: string;
	text: string;
	label: string;
	/** One line under the control. */
	blurb: string;
	/** The longer explanation, in an info tip. */
	info?: string;
}> = [
	{
		value: "auto",
		text: "Automatic",
		label: "Automatic (Dockerfile, else Railpack)",
		blurb: "Uses the Dockerfile when there is one, otherwise Railpack.",
		info: "Railpack detects the language and builds it.",
	},
	{
		value: "dockerfile",
		text: "Dockerfile",
		label: "Dockerfile",
		blurb: "Always builds the Dockerfile; fails if it is missing.",
	},
	{
		value: "railpack",
		text: "Railpack",
		label: "Railpack",
		blurb: "Builds without a Dockerfile and ignores one in the repository.",
		info: "A zero-config builder for Node, Python, Go, PHP, Ruby, static sites and more.",
	},
	{
		value: "starter:node",
		text: "Starter: Node.js",
		label: "Starter Dockerfile · Node.js",
		blurb: "A generated Dockerfile you can read in the build log.",
	},
	{
		value: "starter:python",
		text: "Starter: Python",
		label: "Starter Dockerfile · Python",
		blurb: "A generated Dockerfile you can read in the build log.",
	},
	{
		value: "starter:go",
		text: "Starter: Go",
		label: "Starter Dockerfile · Go",
		blurb: "A generated multi-stage Dockerfile you can read in the build log.",
	},
	{
		value: "starter:ruby",
		text: "Starter: Ruby",
		label: "Starter Dockerfile · Ruby",
		blurb: "A generated Dockerfile you can read in the build log.",
	},
	{
		value: "starter:php",
		text: "Starter: PHP",
		label: "Starter Dockerfile · PHP",
		blurb:
			"A generated Apache and PHP Dockerfile you can read in the build log.",
	},
	{
		value: "starter:static",
		text: "Starter: static site",
		label: "Starter Dockerfile · static site",
		blurb: "Serves prebuilt files with nginx.",
		info: "Put index.html at the root, or in public/, dist/ or build/.",
	},
];
