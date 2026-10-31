package main

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/templates"
)

// categoryKeywords maps upstream category/tag words to the gallery categories,
// checked in this order (first category with a hit wins; the most specific
// categories come first).
var categoryKeywords = []struct {
	category string
	words    []string
}{
	{templates.CategoryGames, []string{"game", "games", "gaming", "minecraft", "steam", "terraria", "valheim", "factorio"}},
	{templates.CategoryAI, []string{"ai", "llm", "ml", "machine-learning", "chatbot", "rag", "embeddings", "vector", "gpt", "agent", "agents", "mcp", "ocr"}},
	{templates.CategorySecurity, []string{"security", "auth", "authentication", "authorization", "sso", "identity", "iam", "oidc", "oauth", "password", "passwords", "secrets", "vault", "captcha", "2fa", "mfa", "vpn"}},
	{templates.CategoryAnalytics, []string{"analytics", "statistics", "monitoring", "observability", "uptime", "metrics", "logging", "logs", "tracing", "status", "status-page", "apm", "web-analytics"}},
	{templates.CategoryAutomation, []string{"automation", "workflow", "workflows", "no-code", "nocode", "low-code", "lowcode", "scheduling", "integration", "integrations", "rpa", "scraping", "crawler"}},
	{templates.CategoryMedia, []string{"media", "music", "photos", "photo", "video", "audio", "podcast", "streaming", "movies", "tv", "books", "ebook", "comics", "anime", "gallery", "rss", "reader", "feed", "radio"}},
	{templates.CategoryStorage, []string{"storage", "files", "file", "backup", "sync", "archive", "documents-storage", "s3", "object-storage", "cloud-storage", "file-sharing", "filesharing", "upload", "uploads"}},
	{templates.CategoryData, []string{"database", "databases", "db", "backend", "baas", "search", "search-engine", "queue", "broker", "messaging-broker", "message-broker", "cache", "orm", "api", "graphql", "data", "etl", "bi", "analytics-engine", "olap"}},
	{templates.CategoryDevtools, []string{"devtools", "developer-tools", "development", "developer", "git", "ci", "cd", "code", "registry", "testing", "devops", "infrastructure", "ide", "docker", "kubernetes", "deployment", "paas", "build", "package", "pastebin", "snippets"}},
	{templates.CategoryWeb, []string{"cms", "blog", "blogging", "website", "websites", "seo", "landing", "forms", "form", "ecommerce", "e-commerce", "commerce", "shop", "wiki", "documentation", "docs", "static", "newsletter", "publishing", "page-builder", "headless"}},
	{templates.CategoryProductivity, []string{"productivity", "helpdesk", "crm", "project", "management", "notes", "note-taking", "calendar", "task", "tasks", "kanban", "finance", "budget", "email", "mail", "messaging", "communication", "chat", "collaboration", "erp", "invoice", "invoicing", "accounting", "office", "knowledge", "knowledge-base", "bookmarks", "todo", "support", "survey", "polls", "hr"}},
	{templates.CategoryHomelab, []string{"home", "homelab", "dashboard", "dashboards", "networking", "network", "proxy", "dns", "adblock", "self-hosted", "selfhosted", "iot", "smart-home", "home-automation", "health", "family", "tools", "utility", "utilities"}},
}

var wordSplitRe = regexp.MustCompile(`[^a-z0-9-]+`)

// mapCategory picks one of the gallery categories. It is always a guess
// and carries a REVIEW comment.
func mapCategory(src *source) string {
	var words []string
	for _, w := range wordSplitRe.Split(strings.ToLower(src.Category), -1) {
		words = append(words, w)
	}
	for _, tg := range src.Tags {
		for _, w := range wordSplitRe.Split(strings.ToLower(tg), -1) {
			words = append(words, w)
		}
	}
	set := map[string]bool{}
	for _, w := range words {
		if w != "" {
			set[w] = true
		}
	}
	// the source category outranks tags
	for _, c := range categoryKeywords {
		for _, w := range c.words {
			if strings.EqualFold(strings.TrimSpace(src.Category), w) {
				return c.category
			}
		}
	}
	for _, c := range categoryKeywords {
		for _, w := range c.words {
			if set[w] {
				return c.category
			}
		}
	}
	return templates.CategoryHomelab
}

var tagFixRe = regexp.MustCompile(`[^a-z0-9-]+`)

func cleanTags(tags []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range tags {
		t = strings.Trim(tagFixRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(t)), "-"), "-")
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
		if len(out) == 8 {
			break
		}
	}
	return out
}

// linksFor derives website/docs/source from the upstream metadata. Coolify only
// carries a documentation URL, so the website is a guess.
func linksFor(src *source) templates.TemplateLinks {
	l := templates.TemplateLinks{Docs: src.Docs, Website: src.Website, Source: src.Github}
	if repo := githubRepo(src.Docs); repo != "" && l.Source == "" {
		l.Source = repo
	}
	if l.Website == "" {
		switch {
		case githubRepo(src.Docs) != "":
			l.Website = githubRepo(src.Docs)
		case src.Docs != "":
			if u, err := url.Parse(src.Docs); err == nil && u.Host != "" {
				l.Website = u.Scheme + "://" + u.Host
			}
		case l.Source != "":
			l.Website = l.Source
		}
	}
	return l
}

// githubRepo returns https://github.com/<owner>/<repo> for a github URL.
func githubRepo(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Host != "github.com" && u.Host != "www.github.com") {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	return "https://github.com/" + parts[0] + "/" + parts[1]
}

var idFixRe = regexp.MustCompile(`[^a-z0-9-]+`)

// templateID makes an upstream id a valid template id.
func templateID(id string) string {
	id = strings.Trim(idFixRe.ReplaceAllString(strings.ToLower(id), "-"), "-")
	if len(id) > 63 {
		id = strings.TrimRight(id[:63], "-")
	}
	return id
}
