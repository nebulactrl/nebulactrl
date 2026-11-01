import { describe, expect, test } from "bun:test";
import {
	BITBUCKET_TOKEN_URL,
	backupGuide,
	backupHostKind,
	bitbucketGuide,
	cloudflareAopGuide,
	cloudflareDns01Guide,
	cloudflareIntegrationGuide,
	cloudflareTokenUrl,
	discordWebhookGuide,
	dns01Guide,
	firstEmptyId,
	gitCloneGuide,
	giteaGuide,
	giteaTokenUrl,
	gitHostKind,
	githubCloneTokenUrl,
	gitlabGuide,
	gitlabTokenUrl,
	gitProviderGuide,
	headscaleApiKeyGuide,
	MAXMIND_LICENSE_KEY_URL,
	maxmindGuide,
	normalizeBaseUrl,
	registryGuide,
	registryHostOf,
	registryKindOf,
	registryPushGuide,
	route53Guide,
	route53Policy,
	s3BackupPolicy,
	slackWebhookGuide,
	smtpGuide,
	tailscaleOAuthGuide,
	telegramGuide,
} from "./credential-links";

function templateOf(url: string) {
	const parsed = new URL(url);
	return {
		origin: parsed.origin + parsed.pathname,
		groups: JSON.parse(parsed.searchParams.get("permissionGroupKeys") ?? "[]"),
		accountId: parsed.searchParams.get("accountId"),
		zoneId: parsed.searchParams.get("zoneId"),
		name: parsed.searchParams.get("name"),
	};
}

describe("Cloudflare template URL", () => {
	test("DNS only asks for DNS edit and zone read", () => {
		const guide = cloudflareIntegrationGuide({ tunnel: false });
		const t = templateOf(guide.url ?? "");
		expect(t.origin).toBe("https://dash.cloudflare.com/profile/api-tokens");
		expect(t.groups).toEqual([
			{ key: "dns", type: "edit" },
			{ key: "zone", type: "read" },
		]);
		expect(t.accountId).toBe("*");
		expect(t.zoneId).toBe("all");
		expect(t.name).toBe("NebulaCtrl DNS");
		expect(guide.prefilled).toBe(true);
	});

	test("DNS and tunnel are one combined template", () => {
		const guide = cloudflareIntegrationGuide({ tunnel: true });
		const t = templateOf(guide.url ?? "");
		expect(t.groups).toEqual([
			{ key: "dns", type: "edit" },
			{ key: "zone", type: "read" },
			{ key: "argotunnel", type: "edit" },
		]);
		expect(t.name).toBe("NebulaCtrl DNS and Tunnel");
		expect(guide.permissions.map((p) => p.label)).toContain(
			"Account · Cloudflare Tunnel · Edit",
		);
	});

	test("the DNS-01 template has no tunnel permission", () => {
		const t = templateOf(cloudflareDns01Guide().url ?? "");
		expect(t.groups.map((g: { key: string }) => g.key)).toEqual([
			"dns",
			"zone",
		]);
		expect(t.name).toBe("NebulaCtrl DNS-01");
	});

	test("the JSON is percent-encoded, not raw", () => {
		const url = cloudflareTokenUrl([{ key: "dns", type: "edit" }], "A B&C");
		expect(url).toContain(
			"permissionGroupKeys=%5B%7B%22key%22%3A%22dns%22%2C%22type%22%3A%22edit%22%7D%5D",
		);
		expect(url).toContain("&accountId=%2A&zoneId=all&name=A%20B%26C");
	});

	test("the zone settings read stays optional and out of the template", () => {
		const guide = cloudflareIntegrationGuide({ tunnel: false });
		const optional = guide.permissions.filter((p) => p.optional);
		expect(optional.map((p) => p.label)).toEqual([
			"Zone · Zone Settings · Read",
			"Zone · Zone Settings · Edit",
		]);
		expect(guide.url).not.toContain("zone_settings");
	});
});

describe("Cloudflare Authenticated Origin Pulls guide", () => {
	test("names the zone settings permission and links to the token page", () => {
		const guide = cloudflareAopGuide();
		expect(guide.url).toBe("https://dash.cloudflare.com/profile/api-tokens");
		expect(guide.prefilled).toBe(false);
		expect(guide.permissions.map((p) => p.label)).toEqual([
			"Zone · Zone Settings · Edit",
		]);
		expect(guide.permissions[0]?.optional).toBe(false);
		expect(guide.docs).toContain("authenticated-origin-pull");
	});

	test("the main guide lists it as optional and out of the template", () => {
		const guide = cloudflareIntegrationGuide({ tunnel: true });
		expect(guide.permissions.map((p) => p.label)).toContain(
			"Zone · Zone Settings · Edit",
		);
		expect(guide.url).not.toContain("zone_settings");
	});
});

describe("Tailscale", () => {
	test("lists both scopes and the owner tag", () => {
		const guide = tailscaleOAuthGuide({ defaultTags: ["tag:nebula-svc"] });
		const labels = guide.permissions.map((p) => p.label);
		expect(labels).toContain("auth_keys (Write)");
		expect(labels).toContain("devices:core (Write)");
		expect(labels).toContain("Tag tag:nebula");
		expect(guide.permissions.at(-1)?.why).toContain("tag:nebula-svc");
		expect(guide.url).toBe(
			"https://console.tailscale.com/admin/settings/trust-credentials",
		);
		expect(guide.prefilled).toBe(false);
	});

	test("Headscale has a command and no page", () => {
		const guide = headscaleApiKeyGuide();
		expect(guide.cta).toBeNull();
		expect(guide.url).toBeNull();
		expect(guide.policy?.text).toBe("headscale apikeys create");
	});
});

describe("instance URLs", () => {
	test("normalises trailing slashes and keeps a path prefix", () => {
		expect(normalizeBaseUrl(" https://git.example.com/ ")).toBe(
			"https://git.example.com",
		);
		expect(normalizeBaseUrl("https://example.com/gitlab//")).toBe(
			"https://example.com/gitlab",
		);
		expect(normalizeBaseUrl("http://localhost:3000")).toBe(
			"http://localhost:3000",
		);
	});

	test("rejects what is not an http(s) URL", () => {
		expect(normalizeBaseUrl("")).toBeNull();
		expect(normalizeBaseUrl("git.example.com")).toBeNull();
		expect(normalizeBaseUrl("ftp://git.example.com")).toBeNull();
		expect(normalizeBaseUrl("https://user:pw@git.example.com")).toBeNull();
	});
});

describe("GitLab", () => {
	test("gitlab.com by default", () => {
		expect(gitlabTokenUrl("")).toBe(
			"https://gitlab.com/-/user_settings/personal_access_tokens?name=NebulaCtrl&scopes=api,read_repository",
		);
	});

	test("self-hosted base URL is respected", () => {
		expect(gitlabTokenUrl("https://git.corp.example/")).toBe(
			"https://git.corp.example/-/user_settings/personal_access_tokens?name=NebulaCtrl&scopes=api,read_repository",
		);
		expect(gitlabTokenUrl("https://corp.example/gitlab")).toStartWith(
			"https://corp.example/gitlab/-/user_settings/",
		);
	});

	test("a malformed base gives no link and says why", () => {
		expect(gitlabTokenUrl("nonsense")).toBeNull();
		const guide = gitlabGuide("nonsense");
		expect(guide.url).toBeNull();
		expect(guide.whyNoLink).toContain("instance URL");
	});

	test("lists the scopes", () => {
		expect(gitlabGuide("").permissions.map((p) => p.label)).toEqual([
			"api",
			"read_repository",
		]);
	});
});

describe("Gitea and Forgejo", () => {
	test("the applications page of the instance", () => {
		expect(giteaTokenUrl("https://code.example.com/")).toBe(
			"https://code.example.com/user/settings/applications",
		);
	});

	test("no link until the instance URL is entered", () => {
		const empty = giteaGuide("gitea", "");
		expect(empty.url).toBeNull();
		expect(empty.whyNoLink).toContain("Enter the instance URL");
		expect(giteaGuide("forgejo", "nope").whyNoLink).toContain("full instance");
	});

	test("names the provider and lists scope names", () => {
		const guide = giteaGuide("forgejo", "https://codeberg.org");
		expect(guide.provider).toBe("Forgejo");
		expect(guide.cta).toBe("Create a Forgejo token with repository access");
		expect(guide.permissions.map((p) => p.label)[0]).toContain("repository");
		expect(guide.prefilled).toBe(false);
	});
});

describe("git provider dispatcher", () => {
	test("routes each token provider to its guide", () => {
		expect(gitProviderGuide("gitlab", "")?.provider).toBe("GitLab");
		expect(gitProviderGuide("gitea", "https://g.example")?.provider).toBe(
			"Gitea",
		);
		expect(gitProviderGuide("forgejo", "https://f.example")?.provider).toBe(
			"Forgejo",
		);
		expect(gitProviderGuide("bitbucket", "")?.provider).toBe("Bitbucket");
	});

	test("GitHub keeps its App manifest flow and gets no token guide", () => {
		expect(gitProviderGuide("github", "")).toBeNull();
	});
});

describe("Bitbucket, DNS hosts and MaxMind", () => {
	test("Bitbucket needs the clone scope", () => {
		const guide = bitbucketGuide();
		expect(guide.url).toBe(
			"https://id.atlassian.com/manage-profile/security/api-tokens",
		);
		expect(guide.permissions.map((p) => p.label)).toContain(
			"read:repository:bitbucket",
		);
	});

	test("each DNS-01 provider has a guide with a link", () => {
		for (const provider of [
			"cloudflare",
			"route53",
			"hetzner",
			"digitalocean",
		] as const) {
			const guide = dns01Guide(provider);
			expect(guide.url).toStartWith("https://");
			expect(guide.docs).toStartWith("https://");
			expect(guide.permissions.length).toBeGreaterThan(0);
		}
	});

	test("DigitalOcean names the write scope and the domain scopes", () => {
		const labels = dns01Guide("digitalocean").permissions.map((p) => p.label);
		expect(labels.join(" ")).toContain("api:write");
		expect(labels.join(" ")).toContain("domain:create");
	});

	test("MaxMind links the license key page", () => {
		expect(maxmindGuide().url).toBe(MAXMIND_LICENSE_KEY_URL);
		expect(MAXMIND_LICENSE_KEY_URL).toBe(
			"https://www.maxmind.com/en/accounts/current/license-key",
		);
	});
});

describe("Route 53 policy", () => {
	interface Statement {
		Action: string | string[];
		Resource: string | string[];
	}
	const statements = (policy: string): Statement[] => {
		const parsed: { Statement: Statement[] } = JSON.parse(policy);
		return parsed.Statement;
	};
	const actions = (policy: string) =>
		statements(policy).flatMap((s) => s.Action);

	test("is valid JSON with the four actions lego needs", () => {
		expect(actions(route53Policy("")).sort()).toEqual([
			"route53:ChangeResourceRecordSets",
			"route53:GetChange",
			"route53:ListHostedZonesByName",
			"route53:ListResourceRecordSets",
		]);
	});

	test("is limited to the hosted zone that was entered", () => {
		const write = statements(route53Policy(" Z0123456789ABC ")).find((s) =>
			String(s.Action).includes("ChangeResourceRecordSets"),
		);
		expect(write?.Resource).toEqual([
			"arn:aws:route53:::hostedzone/Z0123456789ABC",
		]);
		expect(route53Guide("Z0123456789ABC").note).toContain("limited to");
	});

	test("falls back to every zone for a blank or malformed ID, never a broken ARN", () => {
		for (const id of ["", "   ", "my zone", "z123"]) {
			expect(route53Policy(id)).toContain("arn:aws:route53:::hostedzone/*");
		}
		expect(route53Guide("").note).toContain("every hosted zone");
	});

	test("the guide carries the policy as copy-ready text", () => {
		const guide = route53Guide("");
		expect(guide.policy?.text).toBe(route53Policy(""));
	});
});

describe("focus after returning", () => {
	const fields: Record<string, string | null> = {
		a: "filled",
		b: "  ",
		c: "",
		gone: null,
	};
	const read = (id: string) => fields[id] ?? null;

	test("picks the first empty field", () => {
		expect(firstEmptyId(["a", "b", "c"], read)).toBe("b");
	});

	test("skips fields that are not on the page", () => {
		expect(firstEmptyId(["gone", "c"], read)).toBe("c");
	});

	test("does nothing when every field is filled", () => {
		expect(firstEmptyId(["a", "gone"], read)).toBeNull();
	});
});

describe("service git token", () => {
	test("GitHub gets a fine-grained link with read-only contents for the owner", () => {
		const guide = gitCloneGuide("https://github.com/acme/shop.git");
		const url = new URL(guide?.url ?? "");
		expect(url.origin + url.pathname).toBe(
			"https://github.com/settings/personal-access-tokens/new",
		);
		expect(url.searchParams.get("target_name")).toBe("acme");
		expect(url.searchParams.get("contents")).toBe("read");
		expect(url.searchParams.get("metadata")).toBe("read");
		expect(url.searchParams.get("name")?.length).toBeLessThanOrEqual(40);
		expect(guide?.prefilled).toBe(true);
	});

	test("an owner that is not a valid slug is left out", () => {
		expect(githubCloneTokenUrl("not valid!")).not.toContain("target_name");
	});

	test("GitLab asks for read_repository only, on the instance", () => {
		const url = gitCloneGuide("https://gitlab.corp.example/team/app.git")?.url;
		expect(url).toBe(
			"https://gitlab.corp.example/-/user_settings/personal_access_tokens?name=NebulaCtrl%20clone&scopes=read_repository",
		);
		expect(gitCloneGuide("https://gitlab.com/a/b")?.url).toStartWith(
			"https://gitlab.com/-/user_settings/",
		);
	});

	test("Forgejo, Gitea and Bitbucket reuse their builders with read scopes", () => {
		const forgejo = gitCloneGuide("https://codeberg.org/a/b.git");
		expect(forgejo?.url).toBe(
			"https://codeberg.org/user/settings/applications",
		);
		expect(forgejo?.permissions.map((p) => p.label)).toEqual([
			"repository (Read)",
		]);
		expect(gitCloneGuide("https://gitea.corp.example/a/b")?.provider).toBe(
			"Gitea",
		);
		expect(gitCloneGuide("https://bitbucket.org/a/b.git")?.url).toBe(
			BITBUCKET_TOKEN_URL,
		);
	});

	test("an unknown host gets the checklist and no link", () => {
		const guide = gitCloneGuide("https://git.corp.example/a/b.git");
		expect(guide?.url).toBeNull();
		expect(guide?.cta).toBeNull();
		expect(guide?.note).toContain("git.corp.example");
	});

	test("no guide until the URL parses", () => {
		expect(gitCloneGuide("")).toBeNull();
		expect(gitCloneGuide("git@github.com:a/b.git")).toBeNull();
		expect(gitCloneGuide("acme/shop")).toBeNull();
	});

	test("host kinds", () => {
		expect(gitHostKind("gitlab.example.org")).toBe("gitlab");
		expect(gitHostKind("forgejo.example.org")).toBe("forgejo");
		expect(gitHostKind("git.example.org")).toBeNull();
	});
});

describe("registry login", () => {
	test("kinds come from the image reference", () => {
		expect(registryKindOf("traefik/whoami:latest")).toBe("dockerhub");
		expect(registryKindOf("nginx")).toBe("dockerhub");
		expect(registryKindOf("docker.io/library/nginx")).toBe("dockerhub");
		expect(registryKindOf("ghcr.io/acme/app:1")).toBe("ghcr");
		expect(registryKindOf("registry.gitlab.com/acme/app")).toBe("gitlab");
		expect(registryKindOf("registry.corp.example:5000/app")).toBe("other");
		expect(registryKindOf("localhost:5000/app")).toBe("other");
		expect(registryKindOf("")).toBe("other");
		expect(registryHostOf("acme/app")).toBe("");
	});

	test("GHCR is a classic token with read:packages", () => {
		const guide = registryGuide("ghcr.io/acme/app");
		expect(guide.url).toBe(
			"https://github.com/settings/tokens/new?scopes=read:packages&description=NebulaCtrl%20registry",
		);
		expect(guide.permissions[0]?.label).toBe("read:packages");
	});

	test("Docker Hub asks for a read-only token", () => {
		const guide = registryGuide("traefik/whoami");
		expect(guide.url).toBe(
			"https://app.docker.com/settings/personal-access-tokens",
		);
		expect(guide.permissions[0]?.label).toContain("Read-only");
	});

	test("GitLab's registry prefills read_registry", () => {
		expect(registryGuide("registry.gitlab.com/a/b").url).toBe(
			"https://gitlab.com/-/user_settings/personal_access_tokens?name=NebulaCtrl%20registry&scopes=read_registry",
		);
	});

	test("another registry has a checklist and no link", () => {
		const guide = registryGuide("registry.corp.example/app");
		expect(guide.url).toBeNull();
		expect(guide.cta).toBeNull();
	});
});

describe("registry push login", () => {
	test("GHCR is a classic token with write:packages", () => {
		const guide = registryPushGuide("ghcr.io");
		expect(guide.url).toBe(
			"https://github.com/settings/tokens/new?scopes=write:packages&description=NebulaCtrl%20builds",
		);
		expect(guide.permissions[0]?.label).toBe("write:packages");
	});

	test("Docker Hub asks for a Read & Write token", () => {
		for (const host of ["docker.io", "index.docker.io"]) {
			const guide = registryPushGuide(host);
			expect(guide.url).toBe(
				"https://app.docker.com/settings/personal-access-tokens",
			);
			expect(guide.permissions[0]?.label).toContain("Read & Write");
		}
	});

	test("GitLab's registry prefills write_registry", () => {
		expect(registryPushGuide("registry.gitlab.com").url).toBe(
			"https://gitlab.com/-/user_settings/personal_access_tokens?name=NebulaCtrl%20builds&scopes=write_registry",
		);
	});

	test("any other host is a checklist with no link", () => {
		for (const host of ["registry.corp.example:5000", "localhost:5000", ""]) {
			const guide = registryPushGuide(host);
			expect(guide.url).toBeNull();
			expect(guide.cta).toBeNull();
		}
	});
});

describe("backup destination keys", () => {
	test("the store comes from the endpoint", () => {
		expect(backupHostKind("")).toBe("aws");
		expect(backupHostKind("https://s3.eu-west-1.amazonaws.com")).toBe("aws");
		expect(backupHostKind("https://abc123.r2.cloudflarestorage.com")).toBe(
			"r2",
		);
		expect(backupHostKind("https://abc123.eu.r2.cloudflarestorage.com")).toBe(
			"r2",
		);
		expect(backupHostKind("s3.us-west-004.backblazeb2.com")).toBe("b2");
		expect(backupHostKind("https://fsn1.your-objectstorage.com")).toBe(
			"hetzner",
		);
		expect(backupHostKind("https://s3.eu-central-1.wasabisys.com")).toBe(
			"wasabi",
		);
		expect(backupHostKind("https://minio.corp.example:9000")).toBe(
			"selfhosted",
		);
		expect(backupHostKind("http://[bad")).toBe("selfhosted");
		// A look-alike host that merely ends with the provider's name is not the provider.
		expect(backupHostKind("https://evilr2.cloudflarestorage.com")).toBe(
			"selfhosted",
		);
		expect(backupHostKind("https://notbackblazeb2.com")).toBe("selfhosted");
		expect(backupHostKind("https://xyour-objectstorage.com")).toBe(
			"selfhosted",
		);
		expect(backupHostKind("https://evilwasabisys.com")).toBe("selfhosted");
		expect(backupHostKind("https://wasabisys.com")).toBe("wasabi");
	});

	const guide = (endpoint: string) =>
		backupGuide({ endpoint, bucket: "nightly", prefix: "" });

	test("AWS and Wasabi carry the bucket-scoped policy", () => {
		for (const endpoint of ["", "https://s3.wasabisys.com"]) {
			expect(guide(endpoint).policy?.text).toBe(s3BackupPolicy("nightly", ""));
		}
	});

	test("the policy names the bucket and the five actions", () => {
		const policy = JSON.parse(s3BackupPolicy("nightly", ""));
		expect(policy.Statement[0]).toEqual({
			Effect: "Allow",
			Action: "s3:ListBucket",
			Resource: "arn:aws:s3:::nightly",
		});
		expect(policy.Statement[1].Action).toEqual([
			"s3:PutObject",
			"s3:GetObject",
			"s3:DeleteObject",
			"s3:AbortMultipartUpload",
		]);
		expect(policy.Statement[1].Resource).toBe("arn:aws:s3:::nightly/*");
	});

	test("a key prefix narrows both statements", () => {
		const policy = JSON.parse(s3BackupPolicy("nightly", "/nebulactrl/prod/"));
		expect(policy.Statement[1].Resource).toBe(
			"arn:aws:s3:::nightly/nebulactrl/prod/*",
		);
		expect(policy.Statement[0].Condition.StringLike["s3:prefix"]).toEqual([
			"nebulactrl/prod",
			"nebulactrl/prod/*",
		]);
	});

	test("a blank bucket keeps a visible placeholder", () => {
		expect(s3BackupPolicy("", "")).toContain("arn:aws:s3:::YOUR-BUCKET");
		expect(
			backupGuide({ endpoint: "", bucket: "", prefix: "" }).note,
		).toContain("Enter the bucket");
	});

	test("R2 names the permission and the bucket", () => {
		const r2 = guide("https://abc.r2.cloudflarestorage.com");
		expect(r2.url).toBe(
			"https://dash.cloudflare.com/?to=/:account/r2/overview",
		);
		expect(r2.permissions.map((p) => p.label)).toEqual([
			"Permissions: Object Read & Write",
			"Specify bucket: nightly",
		]);
	});

	test("B2, Hetzner and Wasabi link their consoles", () => {
		expect(guide("s3.us-west-004.backblazeb2.com").url).toBe(
			"https://secure.backblaze.com/app_keys.htm",
		);
		expect(guide("https://fsn1.your-objectstorage.com").url).toBe(
			"https://console.hetzner.com/",
		);
		expect(guide("https://s3.wasabisys.com").url).toBe(
			"https://console.wasabisys.com/",
		);
	});

	test("a self-hosted store is a checklist only", () => {
		const own = guide("https://minio.corp.example");
		expect(own.url).toBeNull();
		expect(own.policy).toBeUndefined();
		expect(own.permissions.map((p) => p.label)).toContain("s3:PutObject");
	});
});

describe("notification channels", () => {
	test("Slack opens the new app page", () => {
		expect(slackWebhookGuide().url).toBe(
			"https://api.slack.com/apps?new_app=1",
		);
	});

	test("Discord is a checklist", () => {
		expect(discordWebhookGuide().url).toBeNull();
	});

	test("Telegram opens BotFather and shows the chat id lookup", () => {
		const guide = telegramGuide();
		expect(guide.url).toBe("https://t.me/BotFather");
		expect(guide.policy?.text).toContain("/getUpdates");
	});

	test("SMTP is provider-agnostic", () => {
		expect(smtpGuide().url).toBeNull();
	});
});
