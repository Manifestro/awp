package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		left, right string
		want        int
	}{{"0.2.0-alpha.1", "0.1.0-alpha.1", 1}, {"0.2.0-alpha.1", "0.2.0", -1}, {"1.0.0", "1.0.0", 0}}
	for _, test := range tests {
		got, err := CompareVersions(test.left, test.right)
		if err != nil || got != test.want {
			t.Fatalf("CompareVersions(%s,%s)=%d,%v want %d", test.left, test.right, got, err, test.want)
		}
	}
}

func TestCheckAndInstallVerifiedRelease(t *testing.T) {
	binary := []byte("new-awp-binary")
	archive := makeArchive(t, binary)
	sum := sha256.Sum256(archive)
	version := "0.2.0-alpha.1"
	archiveName := fmt.Sprintf("awp_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/repos/test/repo/releases/latest":
			writer.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(writer, `{"tag_name":"v%s","html_url":"https://example.test/release"}`, version)
		case filepath.Base(request.URL.Path) == archiveName:
			writer.Write(archive)
		case filepath.Base(request.URL.Path) == "awp_"+version+"_checksums.txt":
			fmt.Fprintf(writer, "%s  %s\n", hex.EncodeToString(sum[:]), archiveName)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client := Client{HTTPClient: server.Client(), Repository: "test/repo", APIBase: server.URL, DownloadBase: server.URL}
	status, err := client.Check(context.Background(), "0.1.0-alpha.1")
	if err != nil {
		t.Fatal(err)
	}
	if !status.UpdateAvailable || status.LatestVersion != version {
		t.Fatalf("status=%#v", status)
	}
	target := filepath.Join(t.TempDir(), "awp")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := client.Install(context.Background(), version, target); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, binary) {
		t.Fatalf("installed=%q", got)
	}
}

func makeArchive(t *testing.T, binary []byte) []byte {
	t.Helper()
	var output bytes.Buffer
	gz := gzip.NewWriter(&output)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "awp", Mode: 0o755, Size: int64(len(binary)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(binary); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
