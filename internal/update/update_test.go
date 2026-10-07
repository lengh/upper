package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func archive(t *testing.T, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "upper", Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte(content))
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func serve(t *testing.T, version string, tgz []byte, sum string) {
	t.Helper()
	file := "upper_linux_" + runtime.GOARCH + ".tar.gz"
	mux := http.NewServeMux()
	mux.HandleFunc("/dl/VERSION", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(version + "\n")) })
	mux.HandleFunc("/dl/SHA256SUMS", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(sum + "  " + file + "\n")) })
	mux.HandleFunc("/dl/"+file, func(w http.ResponseWriter, _ *http.Request) { w.Write(tgz) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	BaseURL = srv.URL
}

func TestApplyReplacesBinary(t *testing.T) {
	tgz := archive(t, "#!/bin/sh\necho new\n")
	s := sha256.Sum256(tgz)
	serve(t, "v2", tgz, hex.EncodeToString(s[:]))

	if v, err := Latest(context.Background()); err != nil || v != "v2" {
		t.Fatalf("Latest = %q, %v", v, err)
	}
	exe := filepath.Join(t.TempDir(), "upper")
	os.WriteFile(exe, []byte("old"), 0o755)
	if err := Apply(context.Background(), exe); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(exe)
	if string(got) != "#!/bin/sh\necho new\n" {
		t.Fatalf("binary = %q", got)
	}
	if info, _ := os.Stat(exe); info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v", info.Mode())
	}
	if entries, _ := os.ReadDir(filepath.Dir(exe)); len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}
}

func TestApplyRejectsBadChecksum(t *testing.T) {
	serve(t, "v2", archive(t, "evil"), "0000000000000000000000000000000000000000000000000000000000000000")
	exe := filepath.Join(t.TempDir(), "upper")
	os.WriteFile(exe, []byte("old"), 0o755)
	if err := Apply(context.Background(), exe); err == nil {
		t.Fatal("tampered download was installed")
	}
	if got, _ := os.ReadFile(exe); string(got) != "old" {
		t.Fatal("old binary was modified")
	}
}

func TestApplyFollowsSymlink(t *testing.T) {
	tgz := archive(t, "new")
	s := sha256.Sum256(tgz)
	serve(t, "v2", tgz, hex.EncodeToString(s[:]))
	dir := t.TempDir()
	real := filepath.Join(dir, "upper-real")
	os.WriteFile(real, []byte("old"), 0o755)
	link := filepath.Join(dir, "upper")
	os.Symlink(real, link)
	if err := Apply(context.Background(), link); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(real); string(got) != "new" {
		t.Fatal("symlink target not updated")
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink was replaced by a file")
	}
}

func TestEnabled(t *testing.T) {
	for v, want := range map[string]bool{"dev": false, "": false, "abc123-dirty": false, "3733761": runtime.GOOS == "linux"} {
		if Enabled(v) != want {
			t.Errorf("Enabled(%q) = %v", v, !want)
		}
	}
}
