package config

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// DefaultUpdateFeedURL is the public release feed the update check reads.
const DefaultUpdateFeedURL = "https://nebulactrl.com/releases/latest.json"

// Update configures the control plane's check for a newer release
// (internal/sysupdate).
type Update struct {
	// Check is NEBULACTRL_UPDATE_CHECK (default true). False: the control plane
	// never contacts the feed and shows no update notice.
	Check bool
	// FeedURL is NEBULACTRL_UPDATE_FEED_URL: the feed to read (a mirror or an
	// air-gapped copy). https only: the update the feed announces runs as root, so
	// a plain http feed is refused at start. Default DefaultUpdateFeedURL.
	FeedURL string
}

func loadUpdate(getenv func(string) string) (Update, error) {
	u := Update{Check: true, FeedURL: DefaultUpdateFeedURL}
	var errs []string
	if v := strings.TrimSpace(getenv("NEBULACTRL_UPDATE_CHECK")); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			errs = append(errs, fmt.Sprintf("NEBULACTRL_UPDATE_CHECK: %v", err))
		} else {
			u.Check = b
		}
	}
	if v := strings.TrimSpace(getenv("NEBULACTRL_UPDATE_FEED_URL")); v != "" {
		p, err := url.Parse(v)
		if err != nil || p.Scheme != "https" || p.Host == "" {
			errs = append(errs, "NEBULACTRL_UPDATE_FEED_URL must be an https:// URL (a plain http feed is refused: the update it announces runs as root)")
		} else {
			u.FeedURL = v
		}
	}
	if len(errs) > 0 {
		return u, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return u, nil
}
