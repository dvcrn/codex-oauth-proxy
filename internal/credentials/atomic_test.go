package credentials

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestFilesystemCredentialsWriteAtomically(t *testing.T) {
	creds := &OAuthCredentials{
		AccessToken: "new-access", RefreshToken: "new-refresh", ExpiresAt: 1234, UserID: "account",
	}
	for _, operation := range []string{"initialize", "refresh"} {
		t.Run(operation, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "auth.json")
			const original = `{"tokens":{"access_token":"old-access","refresh_token":"old-refresh","account_id":"account"}}`
			if err := os.WriteFile(path, []byte(original), 0644); err != nil {
				t.Fatal(err)
			}
			old, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer old.Close()

			fetcher := NewFSCredentialsFetcher(path)
			if operation == "initialize" {
				err = InitFromOAuth(path, creds)
			} else {
				err = fetcher.UpdateTokens(creds.AccessToken, creds.RefreshToken, creds.ExpiresAt)
			}
			if err != nil {
				t.Fatal(err)
			}

			// An open descriptor distinguishes atomic replacement from truncation.
			previous, err := io.ReadAll(old)
			if err != nil || string(previous) != original {
				t.Fatalf("original credential file changed: %q, err=%v", previous, err)
			}
			got, err := fetcher.GetFullCredentials()
			if err != nil || *got != *creds {
				t.Fatalf("credentials = %+v, err=%v, want %+v", got, err, creds)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0600 {
				t.Fatalf("credential permissions = %v, want 0600", info.Mode().Perm())
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 {
				t.Fatalf("temporary files left behind: %v, err=%v", entries, err)
			}
		})
	}
}

func TestFilesystemCredentialsPreserveSymlinks(t *testing.T) {
	for _, linkType := range []string{"absolute", "relative", "relative-parent-link"} {
		for _, operation := range []string{"initialize", "refresh", "initialize-missing"} {
			t.Run(linkType+"/"+operation, func(t *testing.T) {
				dir := t.TempDir()
				targetDir := filepath.Join(dir, "nested", "target")
				linkDir := filepath.Join(dir, "nested", "links")
				for _, parent := range []string{targetDir, linkDir} {
					if err := os.MkdirAll(parent, 0700); err != nil {
						t.Fatal(err)
					}
				}
				target := filepath.Join(targetDir, "auth.json")
				if operation != "initialize-missing" {
					if err := InitFromOAuth(target, &OAuthCredentials{AccessToken: "old-access", UserID: "account"}); err != nil {
						t.Fatal(err)
					}
				}
				if linkType == "relative-parent-link" {
					alias := filepath.Join(dir, "alias")
					if err := os.Symlink(linkDir, alias); err != nil {
						t.Fatal(err)
					}
					linkDir = alias
				}
				path := filepath.Join(linkDir, "auth.json")
				linkTarget := target
				if linkType != "absolute" {
					linkTarget = filepath.Join("..", "target", "auth.json")
				}
				if err := os.Symlink(linkTarget, path); err != nil {
					t.Fatal(err)
				}

				creds := &OAuthCredentials{AccessToken: "new-access", RefreshToken: "new-refresh", ExpiresAt: 1234, UserID: "account"}
				var err error
				if operation == "refresh" {
					err = NewFSCredentialsFetcher(path).UpdateTokens(creds.AccessToken, creds.RefreshToken, creds.ExpiresAt)
				} else {
					err = InitFromOAuth(path, creds)
				}
				if err != nil {
					t.Fatal(err)
				}
				if got, err := os.Readlink(path); err != nil || got != linkTarget {
					t.Errorf("symlink changed: target=%q, err=%v", got, err)
				}
				got, err := NewFSCredentialsFetcher(target).GetFullCredentials()
				if err != nil {
					t.Fatal(err)
				}
				if *got != *creds {
					t.Errorf("target credentials = %+v, want %+v", got, creds)
				}
				info, err := os.Stat(target)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != 0600 {
					t.Errorf("target permissions = %v, want 0600", info.Mode().Perm())
				}
				for _, parent := range []string{targetDir, linkDir} {
					matches, err := filepath.Glob(filepath.Join(parent, ".auth-*"))
					if err != nil || len(matches) != 0 {
						t.Errorf("temporary files left behind: %v, err=%v", matches, err)
					}
				}
			})
		}
	}
}

func TestInitFromOAuthCleansUpAfterRenameFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(path, "keep")
	if err := os.WriteFile(marker, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := InitFromOAuth(path, &OAuthCredentials{AccessToken: "synthetic-token"}); err == nil {
		t.Fatal("expected an error replacing a directory with credentials")
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "original" {
		t.Fatalf("existing destination changed: %q, err=%v", data, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "auth.json" {
		t.Fatalf("temporary files left behind: %v, err=%v", entries, err)
	}
}
