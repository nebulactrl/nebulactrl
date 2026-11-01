/**
 * The copy-and-paste setups of a deploy hook: curl and two CI systems, and the
 * steps that make Docker Hub or GHCR call the hook on every push. Pure, so the
 * text is tested.
 */

export type SnippetId = "curl" | "github" | "gitlab" | "dockerhub" | "ghcr";

export interface Snippet {
	id: SnippetId;
	label: string;
	/** One line above the snippet. */
	intro: string;
	/** What the user pastes; copied as shown. */
	body: string;
	/** Present when the snippet does not fit the service (a git service has no registry). */
	caution?: string;
}

/** Where an image is hosted, as far as the registry snippets care. */
export interface ImageInfo {
	host: "dockerhub" | "ghcr" | "other";
	/** `owner/name` of the repository, as the registry lists it (lowercase). */
	repo: string;
	/** The tag a pull resolves; `latest` when none is written, empty when pinned by digest. */
	tag: string;
}

/** Splits `repo[:tag][@digest]` far enough to tell which registry hosts it. */
export function imageInfo(image: string): ImageInfo | null {
	const ref = image.trim();
	if (ref === "") return null;
	const [withoutDigest = "", digest] = ref.split("@");
	const slash = withoutDigest.lastIndexOf("/");
	const colon = withoutDigest.lastIndexOf(":");
	const hasTag = colon > slash;
	const name = hasTag ? withoutDigest.slice(0, colon) : withoutDigest;
	const tag = digest ? "" : hasTag ? withoutDigest.slice(colon + 1) : "latest";
	const parts = name.split("/");
	const first = parts[0] ?? "";
	const hasHost =
		parts.length > 1 &&
		(first.includes(".") || first.includes(":") || first === "localhost");
	const host = hasHost ? first.toLowerCase() : "docker.io";
	const path = (hasHost ? parts.slice(1) : parts).join("/").toLowerCase();
	if (
		host === "docker.io" ||
		host === "index.docker.io" ||
		host === "registry-1.docker.io"
	) {
		return {
			host: "dockerhub",
			repo: path.includes("/") ? path : `library/${path}`,
			tag,
		};
	}
	if (host === "ghcr.io") return { host: "ghcr", repo: path, tag };
	return { host: "other", repo: `${host}/${path}`, tag };
}

export interface SnippetInput {
	/** The complete URL when it was just created, else the pattern with `<secret>`. */
	url: string;
	/** The service's image; empty for a git service. */
	image: string;
}

// GitHub Actions expressions, not JavaScript templates.
// biome-ignore lint/suspicious/noTemplateCurlyInString: a workflow expression
const GH_HOOK = "${{ secrets.NEBULACTRL_DEPLOY_HOOK }}";
// biome-ignore lint/suspicious/noTemplateCurlyInString: a workflow expression
const GH_SHA = "${{ github.sha }}";

const WITH_TAG_NOTE =
	"Add ?tag=<tag> (or ?digest=sha256:…) to deploy that image of the service's repository instead of the configured tag.";

export function buildSnippets({ url, image }: SnippetInput): Snippet[] {
	const info = imageInfo(image);
	const isImage = info !== null;
	const registry = (
		host: ImageInfo["host"],
		name: string,
		steps: string[],
	): { body: string; caution?: string } => {
		if (!isImage) {
			return {
				body: steps.join("\n"),
				caution:
					"This service builds from git, so a registry push never matches it. Use curl or a CI step to deploy the branch head.",
			};
		}
		const wrong =
			info.host === host
				? undefined
				: `This service's image is on ${hostLabel(info.host)}, not ${name}: pushes to ${name} are ignored.`;
		return { body: steps.join("\n"), ...(wrong ? { caution: wrong } : {}) };
	};
	const target = isImage
		? `${info.repo}:${info.tag === "" ? "<tag>" : info.tag}`
		: "the repository";

	const dockerHub = registry("dockerhub", "Docker Hub", [
		"1. Docker Hub → your repository → Webhooks → New webhook.",
		"2. Name: NebulaCtrl. Webhook URL:",
		`   ${url}`,
		`3. Save. Each push of ${target} deploys the service; other tags answer 202 "ignored".`,
	]);
	const ghcr = registry("ghcr", "GHCR", [
		"1. GitHub → the repository or organization → Settings → Webhooks → Add webhook.",
		"2. Payload URL:",
		`   ${url}`,
		"3. Content type: application/json. Leave Secret empty (the URL is the secret).",
		'4. Events: "Let me select individual events" → Packages (the package event). Add webhook.',
		`5. Each published ${target} deploys the service; other tags answer 202 "ignored".`,
	]);

	return [
		{
			id: "curl",
			label: "curl",
			intro:
				"Deploys the service's current configuration, staged changes included.",
			body: [
				`curl -fsS -X POST "${url}"`,
				...(isImage
					? ["", `# ${WITH_TAG_NOTE}`, `curl -fsS -X POST "${url}?tag=v1.4.2"`]
					: []),
			].join("\n"),
		},
		{
			id: "github",
			label: "GitHub Actions",
			intro:
				"Store the URL as the repository secret NEBULACTRL_DEPLOY_HOOK, then add a step after your build and push.",
			body: [
				"- name: Deploy to NebulaCtrl",
				"  run: |",
				`    curl -fsS -X POST "${GH_HOOK}"`,
				...(isImage
					? [
							"    # or deploy the image this run pushed:",
							`    # curl -fsS -X POST "${GH_HOOK}?tag=${GH_SHA}"`,
						]
					: []),
			].join("\n"),
		},
		{
			id: "gitlab",
			label: "GitLab CI",
			intro:
				"Store the URL as the masked CI/CD variable NEBULACTRL_DEPLOY_HOOK, then add a job.",
			body: [
				"deploy:",
				"  stage: deploy",
				"  image: curlimages/curl:8",
				"  script:",
				'    - curl -fsS -X POST "$NEBULACTRL_DEPLOY_HOOK"',
				...(isImage
					? [
							'    # or: curl -fsS -X POST "$NEBULACTRL_DEPLOY_HOOK?tag=$CI_COMMIT_SHA"',
						]
					: []),
				"  rules:",
				"    - if: $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH",
			].join("\n"),
		},
		{
			id: "dockerhub",
			label: "Docker Hub",
			intro: "Docker Hub calls the hook after every push to the repository.",
			...dockerHub,
		},
		{
			id: "ghcr",
			label: "GHCR",
			intro:
				"GitHub calls the hook when a container package is published to ghcr.io.",
			...ghcr,
		},
	];
}

function hostLabel(host: ImageInfo["host"]): string {
	return host === "dockerhub"
		? "Docker Hub"
		: host === "ghcr"
			? "GHCR"
			: "another registry";
}

/** The URL with the secret hidden, for a hook whose URL cannot be shown again. */
export function maskedUrl(pattern: string): string {
	return pattern.replace("<secret>", "••••••••••••••••");
}
