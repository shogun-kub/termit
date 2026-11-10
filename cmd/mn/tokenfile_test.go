package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// tokenFile writes a token file with the given mode and returns its path.
func tokenFile(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "admin.token")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	// WriteFile applies the umask to mode, so set it again to test exactly what we asked for.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
	return path
}

func TestReadTokenFileAcceptsAProtectedFile(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no POSIX file modes; the permission check is a Linux guarantee")
	}
	path := tokenFile(t, "s3cr3t-token-value\n", 0o600)
	token, err := readTokenFile(path)
	if err != nil {
		t.Fatalf("readTokenFile: %v", err)
	}
	if token != "s3cr3t-token-value" {
		t.Errorf("token = %q, want it trimmed of the trailing newline", token)
	}
}

func TestReadTokenFileRefusesWhatItCannotVouchFor(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no POSIX file modes; the permission check is a Linux guarantee")
	}
	cases := map[string]struct {
		prepare func(t *testing.T) string
		want    string
	}{
		"group-readable": {
			prepare: func(t *testing.T) string { return tokenFile(t, "x\n", 0o640) },
			want:    "make it 0600",
		},
		"world-readable": {
			prepare: func(t *testing.T) string { return tokenFile(t, "x\n", 0o644) },
			want:    "make it 0600",
		},
		"empty": {
			prepare: func(t *testing.T) string { return tokenFile(t, "\n  \n", 0o600) },
			want:    "is empty",
		},
		"missing": {
			prepare: func(t *testing.T) string { return filepath.Join(t.TempDir(), "absent.token") },
			want:    "does not exist",
		},
		"directory": {
			prepare: func(t *testing.T) string { return t.TempDir() },
			want:    "not a regular file",
		},
		"too large": {
			prepare: func(t *testing.T) string {
				return tokenFile(t, strings.Repeat("a", maxTokenFileSize+1), 0o600)
			},
			want: "too large",
		},
	}
	for name, tc := range cases {
		path := tc.prepare(t)
		_, err := readTokenFile(path)
		if err == nil {
			t.Errorf("%s: the file was accepted", name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %q, want it to mention %q", name, err, tc.want)
		}
	}
}

// A file owned by somebody else is a file that somebody else can rewrite, which would let them choose
// the credential this command presents. The check is platform-specific, so the test is too.
func TestReadTokenFileAcceptsAFileOwnedByTheCaller(t *testing.T) {
	// Testing this needs a file that really belongs to somebody else, which an unprivileged test cannot
	// create. What is testable everywhere is the reverse: our own file passes the check.
	if runtime.GOOS == "windows" {
		t.Skip("ownership is not modelled on Windows")
	}
	path := tokenFile(t, "x\n", 0o600)
	if err := checkTokenFilePermissions(path, mustStat(t, path)); err != nil {
		t.Fatalf("a file this user owns was refused: %v", err)
	}
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info
}

func TestWriteTokenFileIsPrivate(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "admin.token")
	if err := writeTokenFile(path, "the-token"); err != nil {
		t.Fatalf("writeTokenFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %04o, want 0600", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "the-token\n" {
		t.Errorf("content = %q, want the token and a newline", data)
	}
	// What we wrote must be what the reader accepts.
	back, err := readTokenFile(path)
	if err != nil {
		t.Fatalf("readTokenFile: %v", err)
	}
	if back != "the-token" {
		t.Errorf("round trip = %q", back)
	}
}

func TestWriteTokenFileRefusesAnEmptyPath(t *testing.T) {
	t.Parallel()
	if err := writeTokenFile("", "x"); err == nil {
		t.Fatal("an empty path was accepted")
	}
}

func TestWriteAdminTokenPrintsOnlyWhenThereIsNoFile(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	if err := writeAdminToken(&out, "", "printed-token"); err != nil {
		t.Fatalf("writeAdminToken: %v", err)
	}
	if !strings.Contains(out.String(), "printed-token") {
		t.Errorf("the token was not printed:\n%s", out.String())
	}

	path := filepath.Join(t.TempDir(), "admin.token")
	out.Reset()
	if err := writeAdminToken(&out, path, "file-token"); err != nil {
		t.Fatalf("writeAdminToken: %v", err)
	}
	if strings.Contains(out.String(), "file-token") {
		t.Errorf("the token was printed as well as written:\n%s", out.String())
	}
	if !strings.Contains(out.String(), path) {
		t.Errorf("the output does not name the file:\n%s", out.String())
	}
}

// A write that fails must still leave the operator with the token: it is already in the database, so a
// swallowed token would be an unadministrable server.
func TestWriteAdminTokenPrintsWhenTheFileCannotBeWritten(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	// A parent that is a regular file cannot be created as a directory, so the write must fail.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	err := writeAdminToken(&out, filepath.Join(blocker, "admin.token"), "rescued-token")
	if err == nil {
		t.Fatal("writing under a regular file reported success")
	}
	if !strings.Contains(out.String(), "rescued-token") {
		t.Errorf("the token was lost:\n%s", out.String())
	}
}

func TestResolveAdminTokenPrecedence(t *testing.T) {
	// No t.Parallel: t.Setenv forbids it.
	cases := map[string]struct {
		token, file string
		envToken    string
		envFile     string
		want        string
		wantErr     string
	}{
		"the flag wins":                     {token: "from-flag", envToken: "from-env", want: "from-flag"},
		"the file flag wins over env token": {file: "file", envToken: "from-env", want: "from-file"},
		"the environment token":             {envToken: "from-env", envFile: "file", want: "from-env"},
		"the environment file":              {envFile: "file", want: "from-file"},
		"nothing at all":                    {wantErr: "an admin token is required"},
		"both flags":                        {token: "t", file: "file", wantErr: "not both"},
	}
	for name, tc := range cases {
		dir := t.TempDir()
		path := filepath.Join(dir, "admin.token")
		if err := os.WriteFile(path, []byte("from-file\n"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		file := tc.file
		if file == "file" {
			file = path
		}
		envFile := tc.envFile
		if envFile == "file" {
			envFile = path
		}
		t.Setenv(adminTokenEnv, tc.envToken)
		t.Setenv(adminTokenFileEnv, envFile)

		got, err := resolveAdminToken(tc.token, file)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%s: error = %v, want it to mention %q", name, err, tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: token = %q, want %q", name, got, tc.want)
		}
	}
}
