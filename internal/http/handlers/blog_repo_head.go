package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

var (
	errGitHubTokenMissing = errors.New("ADD_GITHUB_TOKEN or GITHUB_TOKEN not set")
	errGitHubBadRepo      = errors.New("invalid BEBII_BLOG_REPO (expect owner/repo)")
	errGitHubEmptySHA     = errors.New("github response missing sha")
)

func errGitHubHTTP(code int, body string) error {
	b := strings.TrimSpace(body)
	if len(b) > 200 {
		b = b[:200] + "…"
	}
	return fmt.Errorf("github api status %d: %s", code, b)
}

const (
	blogMainCacheTTL     = 5 * time.Minute
	blogRepoNotifyBranch = "main"
)

var (
	blogMainMu     sync.Mutex
	blogMainCached struct {
		fetchedAt time.Time
		fullSHA   string
		msgLine   string
		err       error
	}
)

func githubPATForAPI() string {
	if t := strings.TrimSpace(os.Getenv("ADD_GITHUB_TOKEN")); t != "" {
		return t
	}
	return strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
}

func bebiiBlogRepoSlug() string {
	if s := strings.TrimSpace(os.Getenv("BEBII_BLOG_REPO")); s != "" {
		return s
	}
	return "Bebii-Digital/bebii-blog"
}

type ghCommitAPIResp struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
	} `json:"commit"`
}

// fetchBebiiBlogMainHEAD returns full commit SHA and first line of message. Uses ADD_GITHUB_TOKEN or GITHUB_TOKEN.
func fetchBebiiBlogMainHEAD(ctx context.Context) (fullSHA, msgLine string, err error) {
	token := githubPATForAPI()
	if token == "" {
		return "", "", errGitHubTokenMissing
	}
	repo := bebiiBlogRepoSlug()
	if strings.Count(repo, "/") != 1 {
		return "", "", errGitHubBadRepo
	}
	url := "https://api.github.com/repos/" + repo + "/commits/" + blogRepoNotifyBranch
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "bebii-seo-dashboard")

	client := &http.Client{Timeout: 12 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return "", "", errGitHubHTTP(res.StatusCode, string(body))
	}
	var out ghCommitAPIResp
	if err := json.Unmarshal(body, &out); err != nil {
		return "", "", err
	}
	fullSHA = strings.TrimSpace(out.SHA)
	if fullSHA == "" {
		return "", "", errGitHubEmptySHA
	}
	msg := strings.TrimSpace(out.Commit.Message)
	if i := strings.IndexAny(msg, "\r\n"); i >= 0 {
		msg = strings.TrimSpace(msg[:i])
	}
	if len(msg) > 160 {
		msg = msg[:157] + "…"
	}
	return fullSHA, msg, nil
}

func getCachedBebiiBlogMainHEAD(ctx context.Context, skipCache bool) (fullSHA, msgLine string, err error) {
	blogMainMu.Lock()
	defer blogMainMu.Unlock()
	if !skipCache && time.Since(blogMainCached.fetchedAt) < blogMainCacheTTL && blogMainCached.fullSHA != "" {
		return blogMainCached.fullSHA, blogMainCached.msgLine, blogMainCached.err
	}
	if !skipCache && time.Since(blogMainCached.fetchedAt) < blogMainCacheTTL && blogMainCached.err != nil {
		return "", "", blogMainCached.err
	}
	fullSHA, msgLine, err = fetchBebiiBlogMainHEAD(ctx)
	blogMainCached.fetchedAt = time.Now()
	blogMainCached.fullSHA = fullSHA
	blogMainCached.msgLine = msgLine
	blogMainCached.err = err
	return fullSHA, msgLine, err
}
