package covers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const userAgent = "easy-music/1.0 ( https://github.com/nine9123/easy-music )"

// musicBrainz looks up a release and fetches its front cover from the Cover
// Art Archive. MusicBrainz asks for at most one request per second and a
// meaningful User-Agent: https://musicbrainz.org/doc/MusicBrainz_API/Rate_Limiting
type musicBrainz struct {
	searchURL string // overridable in tests
	caaURL    string
	hc        *http.Client

	mu   sync.Mutex
	last time.Time
}

func newMusicBrainz() *musicBrainz {
	return &musicBrainz{searchURL: "https://musicbrainz.org/ws/2/release/", caaURL: "https://coverartarchive.org", hc: httpClient}
}

type mbSearch struct {
	Releases []struct {
		ID           string `json:"id"`
		Score        int    `json:"score"`
		ReleaseGroup struct {
			ID string `json:"id"`
		} `json:"release-group"`
	} `json:"releases"`
}

// front returns the front cover image, or nil if none was found.
func (m *musicBrainz) front(ctx context.Context, artist, album string) ([]byte, error) {
	m.wait(ctx)
	q := fmt.Sprintf(`release:"%s" AND artist:"%s"`, luceneEscape(album), luceneEscape(artist))
	u := m.searchURL + "?fmt=json&limit=5&query=" + url.QueryEscape(q)
	var res mbSearch
	if err := m.getJSON(ctx, u, &res); err != nil {
		return nil, err
	}
	tried := map[string]bool{}
	for _, r := range res.Releases {
		if r.Score < 90 {
			break // results are sorted by score; the rest are guesses
		}
		for _, p := range []string{"/release/" + r.ID, "/release-group/" + r.ReleaseGroup.ID} {
			if strings.HasSuffix(p, "/") || tried[p] {
				continue
			}
			tried[p] = true
			if data, err := m.get(ctx, m.caaURL+p+"/front-500"); err == nil {
				return data, nil
			}
		}
	}
	return nil, nil
}

// wait enforces the 1 request/second limit across all lookups.
func (m *musicBrainz) wait(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d := time.Until(m.last.Add(1100 * time.Millisecond)); d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
		}
	}
	m.last = time.Now()
}

func (m *musicBrainz) getJSON(ctx context.Context, u string, v any) error {
	b, err := m.get(ctx, u)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func (m *musicBrainz) get(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json, image/*")
	resp, err := m.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 16<<20))
}

func luceneEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s)
}
