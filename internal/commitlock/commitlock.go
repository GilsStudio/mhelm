// Package commitlock stages and commits the generated chart-lock.json
// (and image-values.yaml when present) back to the checked-out branch.
//
// This is the implementation of the contract README.md has always
// stated ("the Action automatically commits lockfile updates") but
// action.yml never carried before v0.7.0. Centralising it here — rather
// than in inline shell — means the git guard is unit-testable against
// real temp repos and every consumer inherits the *correct* guard
// instead of hand-rolling the subtly-wrong `git diff --quiet` one.
//
// The load-bearing detail: the guard stages first, then runs
// `git diff --cached --quiet`. The naive consumer pattern
// (`git diff --quiet -- <dir>/chart-lock.json` before staging) is blind
// to untracked files, so on the very first run — when the lockfile does
// not exist in HEAD yet — it reports "no changes" and the freshly
// generated lock is discarded with the runner. Staging first makes the
// diff see a brand-new file (exit 1 -> commit) and a modified tracked
// file (exit 1 -> commit) while still no-op'ing when nothing changed
// (exit 0 -> skip).
package commitlock

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Options configures a single commit-lock run.
type Options struct {
	// Dir is the chart directory (holding chart-lock.json), relative to
	// RepoDir. Used for the git pathspecs and the default message.
	Dir string
	// RepoDir is the directory git runs in (the repo working tree).
	// Empty means the process working directory.
	RepoDir string
	// Message overrides the commit message. Empty uses the default
	// `chore(mhelm): update chart-lock.json for <dir> [skip ci]`. The
	// `[skip ci]` token is deliberate: it stops the lockfile commit from
	// re-triggering a path-filtered mirror workflow.
	Message string
	// GitName / GitEmail, when set, are written via repo-local
	// `git config user.{name,email}` before committing — so a CI bot can
	// author the commit without a global identity. Left empty, the
	// repo's existing identity is used (the right default for local use).
	GitName  string
	GitEmail string
	// Push runs `git pull --rebase --autostash` then `git push` after a
	// successful commit. The pull-rebase closes the non-fast-forward
	// window against a concurrent human push. Off by default so local
	// invocations only produce a local commit; the Action sets it.
	Push bool
}

// Result reports what happened. Committed is false for the clean no-op
// case (nothing staged differed from HEAD) — distinct from an error.
type Result struct {
	Committed bool
	Pushed    bool
	Message   string
}

// Run executes the stage -> guard -> commit (-> pull --rebase -> push)
// sequence. A clean no-op (no staged changes) returns Result{} with a
// nil error — never an empty commit.
func Run(o Options) (Result, error) {
	lockRel := filepath.Join(o.Dir, "chart-lock.json")
	if _, err := os.Stat(filepath.Join(o.RepoDir, lockRel)); err != nil {
		if os.IsNotExist(err) {
			return Result{}, fmt.Errorf("%s not found — run `mhelm mirror` first", lockRel)
		}
		return Result{}, fmt.Errorf("stat %s: %w", lockRel, err)
	}

	if o.GitName != "" {
		if err := git(o.RepoDir, "config", "user.name", o.GitName); err != nil {
			return Result{}, err
		}
	}
	if o.GitEmail != "" {
		if err := git(o.RepoDir, "config", "user.email", o.GitEmail); err != nil {
			return Result{}, err
		}
	}

	if err := git(o.RepoDir, "add", "--", lockRel); err != nil {
		return Result{}, err
	}
	// image-values.yaml is optional (absent for wrap charts and for
	// charts whose values paths didn't match) — stage it only if it
	// exists, mirroring the skip semantics of `mhelm discover`.
	imgRel := filepath.Join(o.Dir, "image-values.yaml")
	if _, err := os.Stat(filepath.Join(o.RepoDir, imgRel)); err == nil {
		if err := git(o.RepoDir, "add", "--", imgRel); err != nil {
			return Result{}, err
		}
	}

	changed, err := stagedChanges(o.RepoDir)
	if err != nil {
		return Result{}, err
	}
	if !changed {
		return Result{Committed: false}, nil
	}

	msg := o.Message
	if msg == "" {
		msg = fmt.Sprintf("chore(mhelm): update chart-lock.json for %s [skip ci]", o.Dir)
	}
	if err := git(o.RepoDir, "commit", "-m", msg); err != nil {
		return Result{}, err
	}
	res := Result{Committed: true, Message: msg}

	if o.Push {
		if err := git(o.RepoDir, "pull", "--rebase", "--autostash"); err != nil {
			return res, fmt.Errorf("git pull --rebase --autostash (commit made, push aborted): %w", err)
		}
		if err := git(o.RepoDir, "push"); err != nil {
			return res, fmt.Errorf("git push (commit made): %w", err)
		}
		res.Pushed = true
	}
	return res, nil
}

// stagedChanges reports whether the index differs from HEAD. It maps
// `git diff --cached --quiet`'s exit status: 0 = no staged changes,
// 1 = staged changes, anything else = a real git error.
func stagedChanges(repoDir string) (bool, error) {
	cmd := exec.Command("git", "diff", "--cached", "--quiet")
	cmd.Dir = repoDir
	err := cmd.Run()
	if err == nil {
		return false, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return true, nil
	}
	return false, fmt.Errorf("git diff --cached --quiet: %w", err)
}

func git(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
