package eval

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// RepoCache manages a local bare-repo cache to avoid repeated GitHub clones.
// First access fetches from GitHub; subsequent clones are local (instant, no network).
// Thread-safe — multiple goroutines can request the same repo concurrently.
type RepoCache struct {
	dir   string
	mu    sync.Mutex
	locks map[string]*sync.Mutex // per-repo locks for concurrent fetch safety
}

// NewRepoCache creates a cache in the given directory. Creates the dir if needed.
func NewRepoCache(dir string) *RepoCache {
	os.MkdirAll(dir, 0755)
	return &RepoCache{
		dir:   dir,
		locks: make(map[string]*sync.Mutex),
	}
}

// DefaultRepoCache returns a cache at /tmp/intermix/repo-cache.
func DefaultRepoCache() *RepoCache {
	return NewRepoCache(filepath.Join(os.TempDir(), "intermix", "repo-cache"))
}

// repoKey converts a URL to a safe directory name.
// "https://github.com/django/django" → "github.com_django_django.git"
func repoKey(url string) string {
	key := url
	key = strings.TrimPrefix(key, "https://")
	key = strings.TrimPrefix(key, "http://")
	key = strings.TrimSuffix(key, ".git")
	key = strings.ReplaceAll(key, "/", "_")
	return key + ".git"
}

// repoLock returns or creates a per-repo mutex.
func (c *RepoCache) repoLock(url string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := repoKey(url)
	if l, ok := c.locks[key]; ok {
		return l
	}
	l := &sync.Mutex{}
	c.locks[key] = l
	return l
}

// Ensure fetches the bare repo into the cache if not already present.
// Returns the local bare repo path. Safe for concurrent calls.
func (c *RepoCache) Ensure(url string) (string, error) {
	key := repoKey(url)
	barePath := filepath.Join(c.dir, key)

	// Fast path: already cached
	if _, err := os.Stat(filepath.Join(barePath, "HEAD")); err == nil {
		return barePath, nil
	}

	// Slow path: need to fetch. Lock per-repo to avoid duplicate fetches.
	lock := c.repoLock(url)
	lock.Lock()
	defer lock.Unlock()

	// Double-check after acquiring lock
	if _, err := os.Stat(filepath.Join(barePath, "HEAD")); err == nil {
		return barePath, nil
	}

	fmt.Printf("  repo-cache: fetching %s...\n", url)
	start := time.Now()

	cmd := exec.Command("git", "clone", "--bare", url, barePath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// Clean up partial clone
		os.RemoveAll(barePath)
		return "", fmt.Errorf("git clone --bare %s: %w: %s", url, err, stderr.String())
	}

	fmt.Printf("  repo-cache: cached %s in %v\n", key, time.Since(start).Round(time.Second))
	return barePath, nil
}

// CloneFrom clones from the local cache into destDir and checks out a commit.
// Much faster than cloning from GitHub (local I/O only, ~1-2s vs 10-20s).
func (c *RepoCache) CloneFrom(url, destDir, commit string) error {
	barePath, err := c.Ensure(url)
	if err != nil {
		return err
	}

	cmd := exec.Command("git", "clone", barePath, destDir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone (from cache) %s: %w: %s", barePath, err, stderr.String())
	}

	// Set the real remote URL so Skaffen can identify the repo
	setURL := exec.Command("git", "remote", "set-url", "origin", url)
	setURL.Dir = destDir
	setURL.Run() // best-effort

	if commit != "" {
		checkoutCmd := exec.Command("git", "checkout", commit)
		checkoutCmd.Dir = destDir
		var coStderr bytes.Buffer
		checkoutCmd.Stderr = &coStderr
		if err := checkoutCmd.Run(); err != nil {
			return fmt.Errorf("git checkout %s: %w: %s", commit, err, coStderr.String())
		}
	}

	return nil
}

// WarmRepos pre-fetches a list of repo URLs into the cache concurrently.
// Returns after all fetches complete. Errors are logged but not fatal.
func (c *RepoCache) WarmRepos(urls []string) {
	// Deduplicate
	seen := make(map[string]bool)
	var unique []string
	for _, url := range urls {
		if !seen[url] {
			seen[url] = true
			unique = append(unique, url)
		}
	}

	fmt.Printf("repo-cache: warming %d repos...\n", len(unique))
	start := time.Now()

	var wg sync.WaitGroup
	// Limit concurrent GitHub fetches to avoid rate limits
	sem := make(chan struct{}, 3)
	for _, url := range unique {
		wg.Add(1)
		go func(u string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if _, err := c.Ensure(u); err != nil {
				fmt.Printf("  repo-cache: WARN: %s: %v\n", u, err)
			}
		}(url)
	}
	wg.Wait()

	fmt.Printf("repo-cache: warmed %d repos in %v\n", len(unique), time.Since(start).Round(time.Second))
}
