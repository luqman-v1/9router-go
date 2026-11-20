package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// genTeam regenerates wiki/team.md from the GitHub API. It exists because the
// page was written once, by hand, from a snapshot of the contributors list:
// every commit since then moved the real numbers further from the ones on the
// page, and people who landed after the snapshot were simply missing. The
// gate pattern fixed the providers page the same way — the source of truth and
// the page are kept together by a command that runs where the data is.
//
// The owner card is written by hand and this generator leaves it alone: the
// owner is stable, the card carries a curated title no API returns, and the
// page reads better for it. Everything below the owner card is generated.

// ownerLogin is the repository owner. The generator renders this card itself
// so the commit count moves with the repository: a hand-written count is how
// the page went stale in the first place. The title is the one field no API
// returns, and the page reads better for it, so it is curated here.
const ownerLogin = "luqman-v1"

// ownerTitle is the owner card's byline.
const ownerTitle = "Backend Engineer with Positive Mental Attitude"

// teamPath is the page this generator writes.
const teamPath = "wiki/team.md"

// contributor is the slice of the GitHub contributors API this page needs.
type contributor struct {
	Login         string `json:"login"`
	Contributions int    `json:"contributions"`
	AvatarURL     string `json:"avatar_url"`
	Type          string `json:"type"`
	HTMLURL       string `json:"html_url"`
}

// ghUser is the slice of the users API needed for a card's display name.
type ghUser struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// ghClient is the HTTP client for the GitHub API. The token is optional: an
// unauthenticated request is rate-limited to 60 per hour per IP, which a
// contributor list of this size can exhaust on a shared runner, so the Pages
// workflow always passes GITHUB_TOKEN and a local run without one still works
// when the API is reachable.
type ghClient struct {
	http  *http.Client
	token string
}

func (c *ghClient) get(ctx context.Context, url string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request for %s: %w", url, err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body) // drain so the connection is reused
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("GET %s: status %d: %s", url, resp.StatusCode, string(body))
	}
	if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
		return fmt.Errorf("decode %s: %w", url, err)
	}
	return nil
}

// fetchContributors returns the repository's contributors, paginated until the
// API reports a short page.
func (c *ghClient) fetchContributors(ctx context.Context, repo string) ([]contributor, error) {
	var out []contributor
	for page := 1; ; page++ {
		var batch []contributor
		url := fmt.Sprintf("https://api.github.com/repos/%s/contributors?per_page=100&page=%d", repo, page)
		if err := c.get(ctx, url, &batch); err != nil {
			return nil, err
		}
		out = append(out, batch...)
		if len(batch) < 100 {
			break
		}
	}
	return out, nil
}

// fetchName resolves a display name. A user with no name set falls back to
// the login, which is what the card shows anyway.
func (c *ghClient) fetchName(ctx context.Context, login string) (string, error) {
	var u ghUser
	if err := c.get(ctx, "https://api.github.com/users/"+login, &u); err != nil {
		return "", err
	}
	if u.Name == "" {
		return login, nil
	}
	return u.Name, nil
}

// ownerCard is the owner's card: larger avatar, the curated title, and the
// commit count filled in from the API. The %s/%d order matches the template.
const ownerCard = `  <div class="contrib-card owner">
    <img src="%s&s=400" alt="%s" loading="lazy" decoding="async" width="88" height="88" />
    <div>
      <p class="contrib-name"><a href="https://github.com/` + ownerLogin + `">%s</a></p>
      <p class="contrib-login">@` + ownerLogin + `</p>
      <p class="contrib-count">` + ownerTitle + `</p>
      <p class="contrib-count">%d commits · repository owner</p>
    </div>
  </div>
`

// card renders one contributor card, matching the markup the page's CSS
// already styles: avatar (lazy, sized), name, login, commit count.
func card(c contributor, name string) string {
	var b strings.Builder
	b.WriteString("  <div class=\"contrib-card\">\n")
	fmt.Fprintf(&b, "    <img src=\"%s&s=200\" alt=\"%s\" loading=\"lazy\" decoding=\"async\" width=\"64\" height=\"64\" />\n",
		c.AvatarURL, name)
	fmt.Fprintf(&b, "    <p class=\"contrib-name\"><a href=\"%s\">%s</a></p>\n", c.HTMLURL, name)
	fmt.Fprintf(&b, "    <p class=\"contrib-login\">@%s</p>\n", c.Login)
	count := "commit"
	if c.Contributions != 1 {
		count = "commits"
	}
	fmt.Fprintf(&b, "    <p class=\"contrib-count\">%d %s</p>\n", c.Contributions, count)
	b.WriteString("  </div>\n")
	return b.String()
}

// generateTeam builds the page's contributor section and writes wiki/team.md,
// preserving everything up to the end of the owner card (the static head) and
// everything from the "## Not listed" heading (the static tail).
func generateTeam(repo string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	token := os.Getenv("GITHUB_TOKEN")
	client := &ghClient{http: &http.Client{Timeout: 30 * time.Second}, token: token}

	contributors, err := client.fetchContributors(ctx, repo)
	if err != nil {
		return fmt.Errorf("fetch contributors: %w", err)
	}
	if len(contributors) == 0 {
		return fmt.Errorf("the contributors API returned no one; refusing to write an empty roster")
	}

	// Bots are not people. They stay out of the grid and are named in the
	// page's "Not listed" section, so their absence reads as a decision.
	var bots []string
	var people []contributor
	var owner *contributor
	for i, c := range contributors {
		if c.Type == "Bot" {
			bots = append(bots, c.Login)
			continue
		}
		if c.Login == ownerLogin {
			owner = &contributors[i]
			continue
		}
		people = append(people, c)
	}
	if owner == nil {
		return fmt.Errorf("the contributors API did not report the owner (%s); refusing to write a page without one", ownerLogin)
	}

	// Deterministic order: contributions descending, then login ascending so
	// two people with the same count always render the same way.
	sort.Slice(people, func(i, j int) bool {
		if people[i].Contributions != people[j].Contributions {
			return people[i].Contributions > people[j].Contributions
		}
		return people[i].Login < people[j].Login
	})
	sort.Strings(bots)

	// Resolve display names. Sequential: the list is short, the API is the
	// rate limit, and parallelism buys nothing but 429s.
	names := make(map[string]string, len(people)+1)
	for _, p := range append(people, *owner) {
		name, err := client.fetchName(ctx, p.Login)
		if err != nil {
			// A missing name must not lose the contributor: the login is the
			// card's identity either way.
			name = p.Login
		}
		names[p.Login] = name
	}

	var b strings.Builder
	b.WriteString("\n")
	fmt.Fprintf(&b, ownerCard, owner.AvatarURL, names[ownerLogin], names[ownerLogin], owner.Contributions)
	b.WriteString("\n")
	for _, p := range people {
		b.WriteString(card(p, names[p.Login]))
		b.WriteString("\n")
	}
	generated := b.String()

	// Splice: head through the owner card's closing div, then the generated
	// grid, then the static tail from "## Not listed".
	raw, err := os.ReadFile(teamPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", teamPath, err)
	}
	src := string(raw)

	// The grid's real closing tag is the one directly before "## Not listed".
	// The owner card contains nested </div>s, so searching for the first one
	// that precedes the heading would splice inside the owner card and append
	// the generated cards to the old ones — a grid of 54 where 31 belong.
	// The head is everything up to (and including) the grid's opening div.
	// Everything between it and "## Not listed" is generated.
	marker := `<div class="contrib-grid">`
	headEnd := strings.Index(src, marker)
	if headEnd < 0 {
		return fmt.Errorf("%s: could not find the grid's opening div; the page's shape changed and this generator needs updating", teamPath)
	}
	head := src[:headEnd+len(marker)] + "\n"

	tail := "\n## Not listed\n\n"
	if len(bots) > 0 {
		list := make([]string, len(bots))
		for i, bot := range bots {
			list[i] = "`" + bot + "`"
		}
		if len(bots) > 1 {
			tail += strings.Join(list, ", ") +
				" appear in the repository's commit history. The GitHub API reports" +
				" those accounts' type as `Bot`: they compress images and keep the" +
				" dependency graph current, and are not people, so they are not" +
				" listed among the contributors.\n"
		} else {
			tail += strings.Join(list, ", ") +
				" appears in the repository's commit history. The GitHub API reports" +
				" that account's type as `Bot`: it compresses images and keeps the" +
				" dependency graph current, and is not a person, so it is not" +
				" listed among the contributors.\n"
		}
	} else {
		tail += "No bot accounts appear in the commit history.\n"
	}

	out := head + generated + "  </div>\n\n" + tail
	if out == src {
		return nil // already current
	}
	if err := os.WriteFile(teamPath, []byte(out), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", teamPath, err)
	}
	fmt.Printf("team.md regenerated: %d contributors, %d bot(s) excluded\n", len(people), len(bots))
	return nil
}
