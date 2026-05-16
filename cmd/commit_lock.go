package cmd

import (
	"fmt"

	"github.com/gilsstudio/mhelm/internal/commitlock"
	"github.com/spf13/cobra"
)

var (
	commitLockGitName  string
	commitLockGitEmail string
	commitLockMessage  string
	commitLockPush     bool
)

var commitLockCmd = &cobra.Command{
	Use:          "commit-lock [dir]",
	SilenceUsage: true,
	Short:        "Stage and commit chart-lock.json (+ image-values.yaml) back to the checked-out branch",
	Long: `Stage <dir>/chart-lock.json (and <dir>/image-values.yaml when it exists)
and commit them to the currently checked-out branch.

This is the lockfile-commit step the mhelm Action runs after mirror+wrap.
It is also usable from non-Actions CI or locally — the git guard is the
whole point of putting it in the tool:

  git add -- <dir>/chart-lock.json   # stage FIRST
  git diff --cached --quiet          # then ask "did anything change?"

Staging before the diff is what makes a brand-new (untracked) lockfile on
the very first run get committed instead of silently discarded — the
common hand-rolled "git diff --quiet -- <dir>/chart-lock.json" guard is
blind to untracked files and eats the first lock. When nothing changed
this is a clean no-op: no empty commit, exit 0.

The default commit message carries [skip ci] so the lockfile commit
does not re-trigger a path-filtered mirror workflow.

--push runs "git pull --rebase --autostash" then "git push" (the
pull-rebase closes the non-fast-forward window against a concurrent
human push). Without --push only a local commit is made.

This commits to the *checked-out* branch (HEAD) — it never assumes
'main', so workflow_dispatch on a feature branch commits to that branch.
Drift's branch+PR flow is intentionally NOT handled here: drift is the
caller's responsibility (a different model — open a PR, don't push to
the checked-out branch).`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := "."
		if len(args) == 1 {
			dir = args[0]
		}
		res, err := commitlock.Run(commitlock.Options{
			Dir:      dir,
			Message:  commitLockMessage,
			GitName:  commitLockGitName,
			GitEmail: commitLockGitEmail,
			Push:     commitLockPush,
		})
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if !res.Committed {
			fmt.Fprintln(out, "no lockfile changes")
			return nil
		}
		fmt.Fprintf(out, "committed: %s\n", res.Message)
		if res.Pushed {
			fmt.Fprintln(out, "pushed")
		}
		return nil
	},
}

func init() {
	commitLockCmd.Flags().StringVar(&commitLockGitName, "git-name", "",
		"if set, repo-local `git config user.name` before committing (CI bot identity)")
	commitLockCmd.Flags().StringVar(&commitLockGitEmail, "git-email", "",
		"if set, repo-local `git config user.email` before committing")
	commitLockCmd.Flags().StringVar(&commitLockMessage, "message", "",
		"override the commit message (default: chore(mhelm): update chart-lock.json for <dir> [skip ci])")
	commitLockCmd.Flags().BoolVar(&commitLockPush, "push", false,
		"git pull --rebase --autostash then git push after the commit")
}
