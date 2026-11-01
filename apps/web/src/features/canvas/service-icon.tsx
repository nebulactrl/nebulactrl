import { cn } from "cn";
import {
	BoxesIcon,
	BoxIcon,
	ContainerIcon,
	DatabaseIcon,
	GitBranchIcon,
	GlobeIcon,
	KeyRoundIcon,
	type LucideIcon,
	NetworkIcon,
	SettingsIcon,
} from "lucide-react";
import type { CSSProperties } from "react";
import { BRAND_ICONS, type BrandSlug, isBrandSlug } from "#/assets/brand-icons";
import spriteUrl from "#/assets/brand-sprite.svg?url";

/** The parts of a service the icon is chosen from; `api/models` `Service` satisfies it. */
export interface ServiceIconSource {
	kind?: string | undefined;
	sourceType?: string | undefined;
	config?:
		| {
				image?: string | undefined;
				gitUrl?: string | undefined;
				/** The managed-database spec; its engine picks the mark. */
				database?: { engine?: string | undefined } | undefined;
				/** A route service's protocol (http, tcp, udp) picks its glyph. */
				route?: { protocol?: string | undefined } | undefined;
		  }
		| undefined;
}

/** Image repository names (last path segment, or a `bitnami/<name>`-style segment) to marks. */
const IMAGE_ALIASES: Record<string, BrandSlug> = {
	postgres: "postgresql",
	postgresql: "postgresql",
	postgis: "postgresql",
	pgvector: "postgresql",
	timescaledb: "postgresql",
	redis: "redis",
	"redis-stack": "redis",
	"redis-stack-server": "redis",
	mysql: "mysql",
	"mysql-server": "mysql",
	mariadb: "mariadb",
	mongo: "mongodb",
	mongodb: "mongodb",
	"mongodb-community-server": "mongodb",
	nginx: "nginx",
	node: "nodedotjs",
	nodejs: "nodedotjs",
	bun: "bun",
	deno: "deno",
	python: "python",
	golang: "go",
	go: "go",
	rust: "rust",
	php: "php",
	"php-fpm": "php",
	ruby: "ruby",
	openjdk: "openjdk",
	"eclipse-temurin": "openjdk",
	java: "openjdk",
	dotnet: "dotnet",
	traefik: "traefikproxy",
	caddy: "caddy",
	rabbitmq: "rabbitmq",
	kafka: "apachekafka",
	"cp-kafka": "apachekafka",
	minio: "minio",
	grafana: "grafana",
	prometheus: "prometheus",
	elasticsearch: "elasticsearch",
	clickhouse: "clickhouse",
	"clickhouse-server": "clickhouse",
	meilisearch: "meilisearch",
	"gitlab-ce": "gitlab",
	"gitlab-ee": "gitlab",
	gitlab: "gitlab",
};

/** Git hosts (domain suffix) to marks. */
const GIT_HOSTS: ReadonlyArray<readonly [string, BrandSlug]> = [
	["github.com", "github"],
	["gitlab.com", "gitlab"],
	["bitbucket.org", "bitbucket"],
];

/** Image reference without registry host, tag or digest: `ghcr.io/o/app:1` -> `o/app`. */
function imagePath(image: string): string[] {
	const withoutDigest = image.split("@")[0] ?? "";
	const segments = withoutDigest.toLowerCase().split("/").filter(Boolean);
	const first = segments[0] ?? "";
	if (
		segments.length > 1 &&
		(first.includes(".") || first.includes(":") || first === "localhost")
	) {
		segments.shift();
	}
	const last = segments.pop();
	if (last !== undefined) segments.push(last.split(":")[0] ?? last);
	return segments;
}

function brandForImage(image: string | undefined): BrandSlug | null {
	if (!image) return null;
	const segments = imagePath(image);
	const name = segments[segments.length - 1];
	if (name) {
		const alias = IMAGE_ALIASES[name];
		if (alias) return alias;
	}
	// `mcr.microsoft.com/dotnet/aspnet`, `library/redis`: a leading namespace can be the brand.
	for (const segment of segments) {
		const alias = IMAGE_ALIASES[segment];
		if (alias) return alias;
		if (isBrandSlug(segment)) return segment;
	}
	return null;
}

function gitHost(url: string): string | null {
	const scp = /^[\w.-]+@([\w.-]+):/.exec(url);
	if (scp?.[1]) return scp[1].toLowerCase();
	try {
		return new URL(url).hostname.toLowerCase();
	} catch {
		return null;
	}
}

function brandForGit(gitUrl: string | undefined): BrandSlug | null {
	if (!gitUrl) return null;
	const host = gitHost(gitUrl);
	if (!host) return null;
	for (const [suffix, slug] of GIT_HOSTS) {
		if (host === suffix || host.endsWith(`.${suffix}`)) return slug;
	}
	return null;
}

export type ResolvedServiceIcon =
	| {
			type: "brand";
			slug: BrandSlug;
			title: string;
			tile: string;
			glyph: string;
	  }
	| {
			type: "generic";
			Icon: LucideIcon;
			title: string;
			tile: string;
			glyph: string;
	  };

const GENERIC_GLYPH = "#ffffff";

/** Valkey has no mark in simple-icons yet: a key glyph on its brand-adjacent blue-violet. */
const VALKEY_TILE = "oklch(0.52 0.15 280)";

/** Catalog engine ids (`GET /database-engines`) to brand marks. Valkey has none. */
const ENGINE_BRANDS: Record<string, BrandSlug> = {
	postgres: "postgresql",
	mysql: "mysql",
	mariadb: "mariadb",
	redis: "redis",
	mongodb: "mongodb",
	clickhouse: "clickhouse",
};

/** A managed Valkey, or a custom database running the Valkey image. */
function isValkey(service: ServiceIconSource): boolean {
	if (service.config?.database?.engine === "valkey") return true;
	return imagePath(service.config?.image ?? "").includes("valkey");
}

/**
 * Which mark a service shows: the brand of its database engine, else of its
 * image repository, else the brand of its git host, else a generic glyph for
 * its kind (database, worker, git source, docker image, empty).
 */
function resolveServiceIcon(service: ServiceIconSource): ResolvedServiceIcon {
	const slug =
		ENGINE_BRANDS[service.config?.database?.engine ?? ""] ??
		brandForImage(service.config?.image) ??
		brandForGit(service.config?.gitUrl);
	if (slug) {
		const brand = BRAND_ICONS[slug];
		return {
			type: "brand",
			slug,
			title: brand.title,
			tile: brand.tile,
			glyph: brand.glyph,
		};
	}
	if (service.kind === "stack") {
		return {
			type: "generic",
			Icon: BoxesIcon,
			title: "Compose stack",
			tile: "oklch(0.5 0.12 262)",
			glyph: GENERIC_GLYPH,
		};
	}
	if (service.kind === "database") {
		if (isValkey(service)) {
			return {
				type: "generic",
				Icon: KeyRoundIcon,
				title: "Valkey",
				tile: VALKEY_TILE,
				glyph: GENERIC_GLYPH,
			};
		}
		return {
			type: "generic",
			Icon: DatabaseIcon,
			title: "Database",
			tile: "oklch(0.55 0.13 250)",
			glyph: GENERIC_GLYPH,
		};
	}
	if (service.kind === "route") {
		const l4 =
			service.config?.route?.protocol === "tcp" ||
			service.config?.route?.protocol === "udp";
		return {
			type: "generic",
			Icon: l4 ? NetworkIcon : GlobeIcon,
			title: l4 ? "TCP/UDP port" : "External service",
			tile: "oklch(0.52 0.11 195)",
			glyph: GENERIC_GLYPH,
		};
	}
	if (service.kind === "worker") {
		return {
			type: "generic",
			Icon: SettingsIcon,
			title: "Worker",
			tile: "oklch(0.45 0.03 262)",
			glyph: GENERIC_GLYPH,
		};
	}
	if (service.sourceType === "git" || service.config?.gitUrl) {
		return {
			type: "generic",
			Icon: GitBranchIcon,
			title: "Git repository",
			tile: "var(--color-accent-fill-bot)",
			glyph: GENERIC_GLYPH,
		};
	}
	if (service.sourceType === "image" || service.config?.image) {
		return {
			type: "generic",
			Icon: ContainerIcon,
			title: "Docker image",
			tile: "oklch(0.56 0.12 235)",
			glyph: GENERIC_GLYPH,
		};
	}
	return {
		type: "generic",
		Icon: BoxIcon,
		title: "Empty service",
		tile: "oklch(0.45 0.03 262)",
		glyph: GENERIC_GLYPH,
	};
}

export interface ServiceIconProps {
	service: ServiceIconSource;
	/** Tile edge in px. */
	size?: number;
	className?: string;
	/** Accessible name; the icon is decorative (aria-hidden) without it. */
	label?: string;
}

/**
 * A service's mark on a glossy raised tile. Brand marks come from
 * the build-time sprite (`bun run sprite`); nothing is fetched at runtime.
 */
export function ServiceIcon({
	service,
	size = 28,
	className,
	label,
}: ServiceIconProps) {
	const icon = resolveServiceIcon(service);
	const glyphSize = Math.round(size * 0.57);
	return (
		<span
			data-slot="service-icon"
			{...(label
				? { role: "img", "aria-label": label }
				: { "aria-hidden": true })}
			className={cn("service-tile select-none", className)}
			style={
				{
					width: size,
					height: size,
					borderRadius: Math.round(size * 0.29),
					color: icon.glyph,
					// `--t` is the tile colour the `service-tile` utility reads.
					"--t": icon.tile,
				} as CSSProperties
			}
		>
			{icon.type === "brand" ? (
				<svg
					width={glyphSize}
					height={glyphSize}
					viewBox="0 0 24 24"
					fill="currentColor"
					aria-hidden="true"
					style={{ filter: "drop-shadow(0 1px 0 rgb(2 4 10 / 0.35))" }}
				>
					<use href={`${spriteUrl}#brand-${icon.slug}`} />
				</svg>
			) : (
				<icon.Icon
					width={glyphSize}
					height={glyphSize}
					strokeWidth={1.8}
					aria-hidden="true"
					style={{ filter: "drop-shadow(0 1px 0 rgb(2 4 10 / 0.35))" }}
				/>
			)}
		</span>
	);
}
