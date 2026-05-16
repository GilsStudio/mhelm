package commitlock

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// run is a tiny git helper for arranging test fixtures (not the code
// under test — that is commitlock.git).
func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newRepo makes a repo with a configured identity and one initial
// commit (so HEAD exists and `git diff --cached` has a base).
func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "git", "init", "-q", "-b", "main")
	run(t, dir, "git", "config", "user.name", "Test")
	run(t, dir, "git", "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "git", "add", "-A")
	run(t, dir, "git", "commit", "-q", "-m", "seed")
	return dir
}

func writeChart(t *testing.T, repo, chartDir, body string) {
	t.Helper()
	full := filepath.Join(repo, chartDir)
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(full, "chart-lock.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func headCount(t *testing.T, repo string) int {
	t.Helper()
	out := run(t, repo, "git", "rev-list", "--count", "HEAD")
	n := 0
	for _, c := range out {
		n = n*10 + int(c-'0')
	}
	return n
}

func TestRun_Guard(t *testing.T) {
	t.Run("untracked first run commits", func(t *testing.T) {
		repo := newRepo(t)
		writeChart(t, repo, "platform/cilium", `{"v":1}`)
		before := headCount(t, repo)
		res, err := Run(Options{Dir: "platform/cilium", RepoDir: repo})
		if err != nil {
			t.Fatal(err)
		}
		if !res.Committed {
			t.Fatal("untracked first-run lock must be committed, not discarded")
		}
		if got := headCount(t, repo); got != before+1 {
			t.Fatalf("commit count = %d, want %d", got, before+1)
		}
		if !strings.Contains(res.Message, "[skip ci]") {
			t.Errorf("default message missing [skip ci]: %q", res.Message)
		}
	})

	t.Run("tracked modified commits", func(t *testing.T) {
		repo := newRepo(t)
		writeChart(t, repo, "platform/cilium", `{"v":1}`)
		if _, err := Run(Options{Dir: "platform/cilium", RepoDir: repo}); err != nil {
			t.Fatal(err)
		}
		writeChart(t, repo, "platform/cilium", `{"v":2}`)
		before := headCount(t, repo)
		res, err := Run(Options{Dir: "platform/cilium", RepoDir: repo})
		if err != nil {
			t.Fatal(err)
		}
		if !res.Committed {
			t.Fatal("modified tracked lock must be committed")
		}
		if got := headCount(t, repo); got != before+1 {
			t.Fatalf("commit count = %d, want %d", got, before+1)
		}
	})

	t.Run("no change is a clean no-op", func(t *testing.T) {
		repo := newRepo(t)
		writeChart(t, repo, "platform/cilium", `{"v":1}`)
		if _, err := Run(Options{Dir: "platform/cilium", RepoDir: repo}); err != nil {
			t.Fatal(err)
		}
		before := headCount(t, repo)
		res, err := Run(Options{Dir: "platform/cilium", RepoDir: repo})
		if err != nil {
			t.Fatal(err)
		}
		if res.Committed {
			t.Fatal("unchanged lock must NOT produce an (empty) commit")
		}
		if got := headCount(t, repo); got != before {
			t.Fatalf("commit count = %d, want unchanged %d", got, before)
		}
	})

	t.Run("image-values.yaml staged when present", func(t *testing.T) {
		repo := newRepo(t)
		writeChart(t, repo, "platform/cilium", `{"v":1}`)
		if err := os.WriteFile(filepath.Join(repo, "platform/cilium", "image-values.yaml"), []byte("a: b\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Run(Options{Dir: "platform/cilium", RepoDir: repo}); err != nil {
			t.Fatal(err)
		}
		tracked := run(t, repo, "git", "ls-files", "platform/cilium")
		if !strings.Contains(tracked, "image-values.yaml") {
			t.Fatalf("image-values.yaml not committed; tracked:\n%s", tracked)
		}
	})

	t.Run("missing lock errors with mirror hint", func(t *testing.T) {
		repo := newRepo(t)
		_, err := Run(Options{Dir: "platform/cilium", RepoDir: repo})
		if err == nil {
			t.Fatal("expected error for missing chart-lock.json")
		}
		if !strings.Contains(err.Error(), "mhelm mirror") {
			t.Errorf("error should hint to run mhelm mirror, got: %v", err)
		}
	})

	t.Run("git identity flags applied", func(t *testing.T) {
		repo := t.TempDir()
		run(t, repo, "git", "init", "-q", "-b", "main")
		// A HEAD exists but NO persistent user.name/email is configured:
		// the seed commit supplies identity inline, the lockfile commit
		// must still succeed purely via the flags.
		if err := os.WriteFile(filepath.Join(repo, "README"), []byte("seed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		run(t, repo, "git", "add", "-A")
		run(t, repo, "git", "-c", "user.name=Seed", "-c", "user.email=seed@example.com",
			"commit", "-q", "-m", "seed")
		writeChart(t, repo, "platform/cilium", `{"v":1}`)
		res, err := Run(Options{
			Dir: "platform/cilium", RepoDir: repo,
			GitName: "github-actions[bot]", GitEmail: "bot@users.noreply.github.com",
		})
		if err != nil {
			t.Fatal(err)
		}
		if !res.Committed {
			t.Fatal("expected commit")
		}
		if author := run(t, repo, "git", "log", "-1", "--pretty=%an"); author != "github-actions[bot]" {
			t.Errorf("author = %q, want github-actions[bot]", author)
		}
	})
}

// TestRun_ConcurrentPush proves the pull --rebase --autostash before
// push closes the non-fast-forward window: origin advances under us
// between our commit and our push, and the push still lands.
func TestRun_ConcurrentPush(t *testing.T) {
	origin := t.TempDir()
	run(t, origin, "git", "init", "-q", "--bare", "-b", "main")

	seed := newRepo(t)
	run(t, seed, "git", "remote", "add", "origin", origin)
	run(t, seed, "git", "push", "-q", "origin", "main")

	clone := func() string {
		d := t.TempDir()
		run(t, d, "git", "clone", "-q", origin, ".")
		run(t, d, "git", "config", "user.name", "Test")
		run(t, d, "git", "config", "user.email", "test@example.com")
		return d
	}

	a := clone()
	b := clone()

	// B pushes an unrelated change first → origin/main moves ahead of A.
	if err := os.WriteFile(filepath.Join(b, "other.txt"), []byte("from B\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, b, "git", "add", "-A")
	run(t, b, "git", "commit", "-q", "-m", "B change")
	run(t, b, "git", "push", "-q", "origin", "main")

	// A commits the lock and pushes — would be a non-fast-forward
	// without the pull --rebase --autostash that Run does.
	writeChart(t, a, "platform/cilium", `{"v":1}`)
	res, err := Run(Options{Dir: "platform/cilium", RepoDir: a, Push: true})
	if err != nil {
		t.Fatalf("concurrent push should rebase-then-succeed: %v", err)
	}
	if !res.Committed || !res.Pushed {
		t.Fatalf("expected committed+pushed, got %+v", res)
	}

	// Origin must now carry BOTH B's change and A's lock.
	verify := t.TempDir()
	run(t, verify, "git", "clone", "-q", origin, ".")
	if _, err := os.Stat(filepath.Join(verify, "other.txt")); err != nil {
		t.Errorf("B's change lost from origin: %v", err)
	}
	if _, err := os.Stat(filepath.Join(verify, "platform/cilium/chart-lock.json")); err != nil {
		t.Errorf("A's lock not on origin: %v", err)
	}
}

func TestRun_NoPushLeavesLocalCommitOnly(t *testing.T) {
	origin := t.TempDir()
	run(t, origin, "git", "init", "-q", "--bare", "-b", "main")
	repo := newRepo(t)
	run(t, repo, "git", "remote", "add", "origin", origin)
	run(t, repo, "git", "push", "-q", "origin", "main")

	writeChart(t, repo, "platform/cilium", `{"v":1}`)
	res, err := Run(Options{Dir: "platform/cilium", RepoDir: repo, Push: false})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Committed || res.Pushed {
		t.Fatalf("want committed && !pushed, got %+v", res)
	}
	// origin still at the seed commit (1) — local repo at 2.
	originCount := run(t, origin, "git", "rev-list", "--count", "main")
	if originCount != "1" {
		t.Errorf("origin advanced without --push: rev count = %s, want 1", originCount)
	}
}
