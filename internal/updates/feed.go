package updates

import (
	"cmp"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// maxFeedBytes is the largest appcast Sapu reads.
const maxFeedBytes = 10 << 20

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err == nil && int64(len(data)) > limit {
		return nil, errors.New("the update feed is too large")
	}
	return data, err
}

// An appcast is an RSS feed. Sparkle puts the versions on the item or on its
// enclosure.
type appcast struct {
	Items []struct {
		Build     string `xml:"http://www.andymatuschak.org/xml-namespaces/sparkle version"`
		Short     string `xml:"http://www.andymatuschak.org/xml-namespaces/sparkle shortVersionString"`
		Channel   string `xml:"http://www.andymatuschak.org/xml-namespaces/sparkle channel"`
		Enclosure struct {
			Build string `xml:"http://www.andymatuschak.org/xml-namespaces/sparkle version,attr"`
			Short string `xml:"http://www.andymatuschak.org/xml-namespaces/sparkle shortVersionString,attr"`
		} `xml:"enclosure"`
	} `xml:"channel>item"`
}

// feedItem is the newest release in an appcast.
type feedItem struct {
	Short string // sparkle:shortVersionString
	Build string // sparkle:version
}

// newestItem returns the release with the highest version. Items on a
// channel (betas) are ignored.
func newestItem(data []byte) (feedItem, error) {
	var feed appcast
	if err := xml.Unmarshal(data, &feed); err != nil {
		return feedItem{}, fmt.Errorf("the update feed is not readable: %w", err)
	}
	var best feedItem
	found := false
	for _, it := range feed.Items {
		if it.Channel != "" {
			continue
		}
		c := feedItem{
			Short: strings.TrimSpace(cmp.Or(it.Short, it.Enclosure.Short)),
			Build: strings.TrimSpace(cmp.Or(it.Build, it.Enclosure.Build)),
		}
		if c.Short == "" && c.Build == "" {
			continue
		}
		if !found || newer(c, best) {
			best, found = c, true
		}
	}
	if !found {
		return feedItem{}, errors.New("the update feed has no versions")
	}
	return best, nil
}

// newer reports whether a is a later release than b.
func newer(a, b feedItem) bool {
	if a.Build != "" && b.Build != "" {
		return compareVersions(a.Build, b.Build) > 0
	}
	return compareVersions(a.Short, b.Short) > 0
}

// parseBrew reads `brew outdated --cask --json=v2`.
func parseBrew(data []byte) ([]Update, error) {
	var doc struct {
		Casks []struct {
			Name      string   `json:"name"`
			Installed []string `json:"installed_versions"`
			Current   string   `json:"current_version"`
		} `json:"casks"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("brew printed something Sapu cannot read: %w", err)
	}
	var out []Update
	for _, c := range doc.Casks {
		installed := ""
		if len(c.Installed) > 0 {
			installed = c.Installed[len(c.Installed)-1]
		}
		out = append(out, Update{
			Name: c.Name, Source: SourceHomebrew,
			Installed: caskVersion(installed), Available: caskVersion(c.Current),
			How:     "Run: brew upgrade --cask " + c.Name,
			Command: []string{"brew", "upgrade", "--cask", c.Name},
		})
	}
	return out, nil
}

// caskVersion drops what brew adds after a comma: "3.39.13,abc123" is
// version 3.39.13.
func caskVersion(v string) string {
	v, _, _ = strings.Cut(v, ",")
	return v
}

// masLine is a line of `mas outdated`: "497799835 Xcode (14.1 -> 14.2)".
var masLine = regexp.MustCompile(`^\s*\d+\s+(.+?)\s+\((.+?)\s*->\s*(.+?)\)\s*$`)

func parseMas(data []byte) []Update {
	var out []Update
	for _, line := range strings.Split(string(data), "\n") {
		if m := masLine.FindStringSubmatch(line); m != nil {
			out = append(out, Update{
				Name: m[1], Source: SourceAppStore, Installed: m[2], Available: m[3],
				How: "Open the App Store and choose Updates.",
			})
		}
	}
	return out
}
