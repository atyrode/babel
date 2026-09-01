package e2e_test

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Direct recovery is the guarantee that makes the archive trustworthy
// independently of Babel: "the restic binary plus the repository password alone
// restore every source tree" (SPEC.md §14). It is what an operator falls back on
// when Babel is broken, absent, or a version that no longer runs, and when
// PostgreSQL is gone — the catalog is rebuildable convenience state, never
// archive truth (decision 43).
//
// So this test restores with `restic` itself. No Babel code participates in the
// recovery path, which is the whole point: a helper of Babel's could pass while
// the property failed. The only Babel involvement is the push that created the
// snapshot, and the roots it reported.
//
// Against an object store the sentence gains exactly one word, because restic
// accepts an object-store credential in one place only: the restic binary, the
// repository password, and the two AWS_ variables that are the credential's
// only form (config.RepositoryStore). Nothing else — no document, no catalog,
// no host identity — reaches the recovery below, which is why this is the
// strongest form of the bullet: an operator holding a password file and a key
// pair gets every source tree back out of the bucket.
func TestArchiveRestoresWithResticAlone(t *testing.T) {
	e := newEnv(t)
	e.writeSources(t)
	repo := acceptRepository(t, e.repository)
	e.useRepository(t, repo)

	e.bootstrapRepo(t)
	push := okJSON[pushResult](t, e, e.with("archive", "push", "--json")...)
	if push.Incomplete {
		t.Fatalf("push reported an incomplete backup: %+v", push)
	}
	if len(push.Roots) == 0 {
		t.Fatalf("push reported no backup roots: %+v", push)
	}

	// Everything restic needs: the repository locator, the password file, and
	// the store credential when the repository is one that authenticates.
	// Notably absent: any Babel configuration, any catalog, any host identity.
	target := t.TempDir()
	cmd := exec.Command(resticBinary(t),
		"-r", e.repository,
		"--password-file", e.passwordFile,
		"restore", push.SnapshotID,
		"--target", target,
	)
	// A cache directory inside the test's own tree keeps the run hermetic.
	cmd.Env = append(os.Environ(), "RESTIC_CACHE_DIR="+filepath.Join(e.cacheHome, "restic-direct"))
	cmd.Env = append(cmd.Env, repo.resticEnv()...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("restic restore: %v\n%s", err, out)
	}

	// Every regular file under every backup root must be present and identical.
	// Walking the sources rather than the restore is deliberate: a restore that
	// dropped files would pass the reverse comparison.
	var checked int
	for _, root := range push.Roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.Type().IsRegular() {
				return nil
			}
			want, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			// restic reproduces the absolute source path under the target.
			got, err := os.ReadFile(filepath.Join(target, path))
			if err != nil {
				return err
			}
			if !bytes.Equal(got, want) {
				t.Errorf("restic-only restore of %s differs: %d bytes restored, %d archived",
					path, len(got), len(want))
			}
			checked++
			return nil
		})
		if err != nil {
			t.Fatalf("walk backup root %s: %v", root, err)
		}
	}
	// The three-harness fixture set is substantial; a walk that silently found
	// nothing would otherwise report success.
	if checked < 8 {
		t.Fatalf("only %d files were compared, which is fewer than the fixture set holds", checked)
	}
}
