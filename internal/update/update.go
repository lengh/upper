// Package update keeps upper current. On launch it asks the download site
// for the latest version; if it differs, it downloads that build, verifies
// its SHA-256 against the published checksums and atomically replaces the
// running binary. The caller then exits, and the next launch runs the new
// version.
package update

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// BaseURL is where builds are published (overridable for tests).
var BaseURL = "https://lengh.github.io/upper"

// maxBinary bounds downloads so a broken server can't fill the disk.
const maxBinary = 64 << 20

var client = &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}}

// Enabled reports whether a build can update itself. Development builds
// (no version stamped in) never do.
func Enabled(current string) bool {
	return current != "" && current != "dev" && !strings.HasSuffix(current, "-dirty") &&
		runtime.GOOS == "linux" && (runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64")
}

func get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s: response too large", url)
	}
	return data, nil
}

// Latest returns the published version.
func Latest(ctx context.Context) (string, error) {
	data, err := get(ctx, BaseURL+"/dl/VERSION", 256)
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(data))
	if v == "" || strings.ContainsAny(v, " \n/") {
		return "", errors.New("malformed version file")
	}
	return v, nil
}

// Apply downloads the build for this machine, verifies it and replaces the
// binary at exe. The old binary stays in place if anything fails.
func Apply(ctx context.Context, exe string) error {
	file := "upper_linux_" + runtime.GOARCH + ".tar.gz"
	sums, err := get(ctx, BaseURL+"/dl/SHA256SUMS", 64<<10)
	if err != nil {
		return err
	}
	want := ""
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == file {
			want = f[0]
		}
	}
	if want == "" {
		return fmt.Errorf("no checksum published for %s", file)
	}

	archive, err := get(ctx, BaseURL+"/dl/"+file, maxBinary)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(archive)
	if hex.EncodeToString(sum[:]) != want {
		return errors.New("checksum mismatch: download corrupt or tampered with; nothing was changed")
	}
	bin, err := extract(archive)
	if err != nil {
		return err
	}
	return replace(exe, bin)
}

func extract(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			return nil, fmt.Errorf("binary not found in archive: %w", err)
		}
		if filepath.Base(h.Name) == "upper" && h.Typeflag == tar.TypeReg {
			return io.ReadAll(io.LimitReader(tr, maxBinary))
		}
	}
}

// replace writes the new binary next to the old one and renames it into
// place, which is atomic on the same filesystem: a crash mid-update leaves
// either the old or the new binary, never half of one.
func replace(exe string, bin []byte) error {
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".upper-update-*")
	if err != nil {
		return fmt.Errorf("can't write to %s (%w); rerun the install command instead", dir, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), exe)
}
