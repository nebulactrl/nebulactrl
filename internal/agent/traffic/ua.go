package traffic

import (
	"strings"
	"sync"

	useragent "github.com/medama-io/go-useragent"
	crawlers "github.com/monperrus/crawler-user-agents"
)

// Client is the coarse classification of a user agent. It is derived from the
// User-Agent header and replaces it; the header itself is never shipped.
type Client struct {
	Browser string
	OS      string
	Device  string // desktop, mobile, tablet, tv, bot, unknown
	Bot     bool
}

const unknownLabel = "Unknown"

// maxUACache bounds the classifier cache; real traffic has few thousand distinct UAs.
const maxUACache = 4096

// Classifier parses user agents with an LRU-ish cache (cleared when full; the
// working set of real traffic refills in moments).
type Classifier struct {
	parser *useragent.Parser
	mu     sync.Mutex
	cache  map[string]Client
}

// NewClassifier returns a Classifier.
func NewClassifier() *Classifier {
	return &Classifier{parser: useragent.NewParser(), cache: make(map[string]Client, 256)}
}

// Classify returns browser, OS, device and bot flag for a User-Agent value.
func (c *Classifier) Classify(ua string) Client {
	c.mu.Lock()
	if v, ok := c.cache[ua]; ok {
		c.mu.Unlock()
		return v
	}
	c.mu.Unlock()

	var out Client
	if ua == "" {
		out = Client{Browser: unknownLabel, OS: unknownLabel, Device: "unknown"}
	} else {
		r := c.parser.Parse(ua)
		out.Bot = r.IsBot() || crawlers.IsCrawler(ua)
		out.Browser = label(r.GetBrowser())
		out.OS = label(r.GetOS())
		out.Device = strings.ToLower(label(r.GetDevice()))
		if out.Bot {
			out.Device = "bot"
		}
	}
	c.mu.Lock()
	if len(c.cache) >= maxUACache {
		clear(c.cache)
	}
	c.cache[ua] = out
	c.mu.Unlock()
	return out
}

func label(s string) string {
	if s == "" {
		return unknownLabel
	}
	return s
}
