package term

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"

	"github.com/R7rainz/gomux/internal/testutil"
)

// typeText types s as key presses.
func typeText(p *Pane, s string) {
	for _, r := range s {
		p.SendKey(vt.KeyPressEvent{Code: r, Text: string(r)})
	}
}

// TestShells runs every installed shell as a login shell in a pane, types a
// command and exits (CLAUDE.md §8). Shells named in GOMUX_REQUIRE_SHELLS must
// be installed; CI sets it, so a broken install can't shrink the matrix.
func TestShells(t *testing.T) {
	required := strings.Fields(os.Getenv("GOMUX_REQUIRE_SHELLS"))
	// Each command prints ABCD, which its own text doesn't contain, so only
	// real output can match.
	shells := []struct{ name, echo string }{
		{"sh", `echo AB""CD`},
		{"dash", `echo AB""CD`},
		{"bash", `echo AB""CD`},
		{"zsh", `echo AB""CD`},
		{"fish", `echo AB""CD`},
		{"nu", `echo ('AB' + 'CD')`},
	}
	for _, sh := range shells {
		t.Run(sh.name, func(t *testing.T) {
			path, err := exec.LookPath(sh.name)
			if err != nil {
				if slices.Contains(required, sh.name) {
					t.Fatalf("%s is in GOMUX_REQUIRE_SHELLS but not installed", sh.name)
				}
				t.Skipf("%s is not installed", sh.name)
			}
			t.Parallel()
			p := startShell(t, sh.name, path)

			// fish 4 shows no prompt until its startup query (DA1) is
			// answered, so this also proves the emulator's replies arrive.
			testutil.Eventually(t, 10*time.Second, "a prompt", func() bool {
				return strings.TrimSpace(screenText(p.Snapshot())) != ""
			})
			typeText(p, sh.echo)
			p.SendKey(key(vt.KeyEnter))
			testutil.Eventually(t, 10*time.Second, "a line reading ABCD", func() bool {
				return slices.Contains(strings.Split(screenText(p.Snapshot()), "\n"), "ABCD")
			})
			typeText(p, "exit 7")
			p.SendKey(key(vt.KeyEnter))
			select {
			case <-p.Done():
			case <-time.After(10 * time.Second):
				t.Fatalf("%s didn't exit:\n%s", sh.name, screenText(p.Snapshot()))
			}
			if code := p.ExitState().ExitCode(); code != 7 {
				t.Fatalf("exit code %d, want 7", code)
			}
		})
	}
}

// startShell starts the shell at path as a login shell ("-zsh") with a fresh
// HOME, so the test is about gomux and not about the machine's dotfiles.
func startShell(t *testing.T, name, path string) *Pane {
	t.Helper()
	home := t.TempDir()
	// Without a .zshrc, zsh starts its first-run wizard, which takes the keyboard.
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "TERM=xterm-256color"}
	for _, k := range []string{"USER", "LOGNAME", "LANG", "LC_ALL", "LC_CTYPE"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	p, err := StartPane(Spec{Path: path, Args: []string{"-" + name}, Dir: home, Env: env, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Kill)
	return p
}
