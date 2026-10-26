package bot

import (
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
	"io"
	"net/url"
	"os"
	"time"
)

type Seed struct {
	Channel   int64  `yaml:"channel"`
	URL       string `yaml:"url"`
	MaxImages int    `yaml:"max_images"`
}
type Config struct {
	Server               string                  `yaml:"server"`
	Origin               string                  `yaml:"origin"`
	Username             string                  `yaml:"username"`
	PasswordFile         string                  `yaml:"password_file"`
	TokenFile            string                  `yaml:"token_file"`
	Database             string                  `yaml:"database"`
	Poll                 time.Duration           `yaml:"poll_interval"`
	ChannelPollIntervals map[int64]time.Duration `yaml:"channel_poll_intervals"`
	Admins               []uint32                `yaml:"admin_ids"`
	Channels             []int64                 `yaml:"channels"`
	Feeds                []Seed                  `yaml:"feeds"`
	AllowHTTP            bool                    `yaml:"allow_http_feeds"`
}

func LoadConfig(path string) (Config, error) {
	return loadConfig(path, false)
}
func LoadDiscoveryConfig(path string) (Config, error) { return loadConfig(path, true) }
func loadConfig(path string, discovery bool) (Config, error) {
	var c Config
	// #nosec G304 -- explicit operator configuration path.
	f, e := os.Open(path)
	if e != nil {
		return c, e
	}
	defer f.Close()
	if info, e := f.Stat(); e != nil || info.Size() > 65536 {
		return c, errors.New("configuration exceeds 64 KiB")
	}
	d := yaml.NewDecoder(io.LimitReader(f, 65537))
	d.KnownFields(true)
	if e = d.Decode(&c); e != nil {
		return c, e
	}
	var extra any
	if e = d.Decode(&extra); e != io.EOF {
		return c, errors.New("configuration must contain one YAML document")
	}
	if c.Poll == 0 {
		c.Poll = 5 * time.Minute
	}
	if c.Database == "" {
		c.Database = "feedbot.db"
	}
	if c.Poll < 30*time.Second || c.Poll > 24*time.Hour {
		return c, errors.New("poll_interval must be 30s..24h")
	}
	for _, s := range []string{c.Server, c.Origin} {
		u, e := url.Parse(s)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
			return c, errors.New("server and origin must be HTTPS origins")
		}
	}
	if c.Username == "" || len(c.Username) > 128 || (c.TokenFile == "") == (c.PasswordFile == "") {
		return c, errors.New("username and exactly one of token_file/password_file required")
	}
	if (!discovery && len(c.Channels) == 0) || len(c.Channels) > 100 || len(c.Feeds) > 100 {
		return c, errors.New("configure 1..100 channels and <=100 feeds")
	}
	seen := map[int64]bool{}
	for _, id := range c.Channels {
		if id <= 0 || seen[id] {
			return c, errors.New("channel IDs must be unique positive store IDs")
		}
		seen[id] = true
	}
	for id, interval := range c.ChannelPollIntervals {
		if !seen[id] || interval < 30*time.Second || interval > 24*time.Hour {
			return c, errors.New("channel_poll_intervals requires configured channel IDs and durations 30s..24h")
		}
	}
	for _, s := range c.Feeds {
		if s.MaxImages < 0 || s.MaxImages > 10 {
			return c, errors.New("max_images must be 0 (default 3) or 1..10")
		}
		if !seen[s.Channel] {
			return c, fmt.Errorf("feed channel %d not configured", s.Channel)
		}
		if _, e := ValidateURL(s.URL, c.AllowHTTP); e != nil {
			return c, e
		}
	}
	return c, nil
}
func (c Config) Authorized(id *uint32, verified bool, channel int64) bool {
	if id == nil || !verified {
		return false
	}
	ch := false
	for _, v := range c.Channels {
		if v == channel {
			ch = true
		}
	}
	if !ch {
		return false
	}
	for _, v := range c.Admins {
		if v == *id {
			return true
		}
	}
	return false
}

func (c Config) ChannelPollInterval(channel int64) time.Duration {
	if d, ok := c.ChannelPollIntervals[channel]; ok {
		return d
	}
	return pollInterval(c)
}
