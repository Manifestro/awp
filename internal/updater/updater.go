package updater

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Manifestro/awp/internal/config"
)

const PolicyVersion = "0.1"

type Release struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
}

type Status struct {
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version"`
	UpdateAvailable bool   `json:"update_available"`
	ReleaseURL      string `json:"release_url"`
}

type Policy struct {
	Version              string `json:"version"`
	Enabled              bool   `json:"enabled"`
	IntervalHours        int    `json:"interval_hours"`
	LastCheckedAt        string `json:"last_checked_at,omitempty"`
	LastInstalledVersion string `json:"last_installed_version,omitempty"`
}

type Client struct {
	HTTPClient   *http.Client
	Repository   string
	APIBase      string
	DownloadBase string
}

func DefaultClient() Client {
	repository := os.Getenv("AWP_REPOSITORY")
	if repository == "" {
		repository = "Manifestro/awp"
	}
	return Client{HTTPClient: &http.Client{Timeout: 30 * time.Second}, Repository: repository, APIBase: "https://api.github.com", DownloadBase: "https://github.com"}
}

func (client Client) Check(ctx context.Context, currentVersion string) (Status, error) {
	release, err := client.latest(ctx)
	if err != nil {
		return Status{}, err
	}
	latest := strings.TrimPrefix(release.TagName, "v")
	if _, err := parseVersion(latest); err != nil {
		return Status{}, fmt.Errorf("invalid latest release version: %w", err)
	}
	available := currentVersion == "" || strings.HasSuffix(currentVersion, "-dev")
	if !available {
		comparison, compareErr := CompareVersions(latest, strings.TrimPrefix(currentVersion, "v"))
		if compareErr != nil {
			return Status{}, compareErr
		}
		available = comparison > 0
	}
	return Status{CurrentVersion: currentVersion, LatestVersion: latest, UpdateAvailable: available, ReleaseURL: release.HTMLURL}, nil
}

func (client Client) Install(ctx context.Context, version, executable string) error {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return fmt.Errorf("self-update is not supported on %s", runtime.GOOS)
	}
	architecture := runtime.GOARCH
	if architecture != "amd64" && architecture != "arm64" {
		return fmt.Errorf("self-update is not supported on architecture %s", architecture)
	}
	version = strings.TrimPrefix(version, "v")
	if _, err := parseVersion(version); err != nil {
		return err
	}
	tag := "v" + version
	archiveName := fmt.Sprintf("awp_%s_%s_%s.tar.gz", version, runtime.GOOS, architecture)
	checksumName := fmt.Sprintf("awp_%s_checksums.txt", version)
	base := fmt.Sprintf("%s/%s/releases/download/%s", strings.TrimRight(client.DownloadBase, "/"), client.Repository, tag)
	archive, err := client.download(ctx, base+"/"+archiveName)
	if err != nil {
		return err
	}
	checksums, err := client.download(ctx, base+"/"+checksumName)
	if err != nil {
		return err
	}
	expected, err := findChecksum(checksums, archiveName)
	if err != nil {
		return err
	}
	actual := sha256.Sum256(archive)
	if hex.EncodeToString(actual[:]) != expected {
		return errors.New("release checksum verification failed")
	}
	binary, err := extractBinary(archive)
	if err != nil {
		return err
	}
	if executable == "" {
		executable, err = os.Executable()
		if err != nil {
			return err
		}
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return err
	}
	directory := filepath.Dir(executable)
	temporary, err := os.CreateTemp(directory, ".awp-update-*")
	if err != nil {
		return fmt.Errorf("create update beside %s: %w", executable, err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if _, err := temporary.Write(binary); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Chmod(0o755); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, executable); err != nil {
		return fmt.Errorf("replace AWP executable: %w", err)
	}
	return nil
}

func (client Client) latest(ctx context.Context) (Release, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", strings.TrimRight(client.APIBase, "/"), client.Repository)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "awp-updater")
	response, err := client.httpClient().Do(request)
	if err != nil {
		return Release{}, fmt.Errorf("check latest AWP release: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("check latest AWP release: HTTP %d", response.StatusCode)
	}
	var release Release
	if err := json.NewDecoder(io.LimitReader(response.Body, 1024*1024)).Decode(&release); err != nil {
		return Release{}, err
	}
	if release.TagName == "" {
		return Release{}, errors.New("latest release has no tag")
	}
	return release, nil
}

func (client Client) download(ctx context.Context, url string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "awp-updater")
	response, err := client.httpClient().Do(request)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: HTTP %d", url, response.StatusCode)
	}
	const maxAssetBytes = 64 * 1024 * 1024
	contents, err := io.ReadAll(io.LimitReader(response.Body, maxAssetBytes+1))
	if err != nil {
		return nil, err
	}
	if len(contents) > maxAssetBytes {
		return nil, errors.New("release asset exceeds 64 MiB")
	}
	return contents, nil
}

func (client Client) httpClient() *http.Client {
	if client.HTTPClient != nil {
		return client.HTTPClient
	}
	return http.DefaultClient
}

func findChecksum(contents []byte, name string) (string, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(contents)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			if len(fields[0]) != 64 {
				return "", errors.New("invalid SHA-256 checksum")
			}
			if _, err := hex.DecodeString(fields[0]); err != nil {
				return "", errors.New("invalid SHA-256 checksum")
			}
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("release checksum is missing for %s", name)
}

func extractBinary(archive []byte) ([]byte, error) {
	gzipReader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	header, err := tarReader.Next()
	if err != nil {
		return nil, errors.New("release archive is empty")
	}
	if header.Name != "awp" || header.Typeflag != tar.TypeReg {
		return nil, errors.New("release archive must contain only the awp binary")
	}
	if header.Size < 1 || header.Size > 64*1024*1024 {
		return nil, errors.New("invalid awp binary size")
	}
	binary, err := io.ReadAll(io.LimitReader(tarReader, header.Size+1))
	if err != nil || int64(len(binary)) != header.Size {
		return nil, errors.New("read awp binary from release archive")
	}
	if _, err := tarReader.Next(); !errors.Is(err, io.EOF) {
		return nil, errors.New("release archive contains unexpected files")
	}
	return binary, nil
}

func PolicyPath(configPath, explicit string) (string, error) {
	if explicit != "" {
		return filepath.Abs(explicit)
	}
	resolved, err := config.Path(configPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(resolved), "update.json"), nil
}

func DefaultPolicy() Policy { return Policy{Version: PolicyVersion, IntervalHours: 24} }

func LoadPolicy(path string) (Policy, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultPolicy(), nil
	}
	if err != nil {
		return Policy{}, err
	}
	defer file.Close()
	policy := DefaultPolicy()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return Policy{}, err
	}
	if policy.Version != PolicyVersion || policy.IntervalHours < 1 {
		return Policy{}, errors.New("invalid update policy")
	}
	return policy, nil
}

func SavePolicy(path string, policy Policy) error {
	if policy.Version != PolicyVersion || policy.IntervalHours < 1 {
		return errors.New("invalid update policy")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".update-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(policy); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func Due(policy Policy, now time.Time) bool {
	if !policy.Enabled {
		return false
	}
	last, err := time.Parse(time.RFC3339Nano, policy.LastCheckedAt)
	return err != nil || now.Sub(last) >= time.Duration(policy.IntervalHours)*time.Hour
}

type parsedVersion struct {
	major, minor, patch int
	prerelease          []string
}

func CompareVersions(left, right string) (int, error) {
	a, err := parseVersion(left)
	if err != nil {
		return 0, err
	}
	b, err := parseVersion(right)
	if err != nil {
		return 0, err
	}
	for _, pair := range [][2]int{{a.major, b.major}, {a.minor, b.minor}, {a.patch, b.patch}} {
		if pair[0] < pair[1] {
			return -1, nil
		}
		if pair[0] > pair[1] {
			return 1, nil
		}
	}
	if len(a.prerelease) == 0 && len(b.prerelease) > 0 {
		return 1, nil
	}
	if len(a.prerelease) > 0 && len(b.prerelease) == 0 {
		return -1, nil
	}
	for i := 0; i < len(a.prerelease) || i < len(b.prerelease); i++ {
		if i >= len(a.prerelease) {
			return -1, nil
		}
		if i >= len(b.prerelease) {
			return 1, nil
		}
		x, y := a.prerelease[i], b.prerelease[i]
		xi, xe := strconv.Atoi(x)
		yi, ye := strconv.Atoi(y)
		if xe == nil && ye == nil {
			if xi < yi {
				return -1, nil
			}
			if xi > yi {
				return 1, nil
			}
			continue
		}
		if xe == nil {
			return -1, nil
		}
		if ye == nil {
			return 1, nil
		}
		if x < y {
			return -1, nil
		}
		if x > y {
			return 1, nil
		}
	}
	return 0, nil
}

func parseVersion(value string) (parsedVersion, error) {
	value = strings.TrimPrefix(value, "v")
	value = strings.SplitN(value, "+", 2)[0]
	parts := strings.SplitN(value, "-", 2)
	core := strings.Split(parts[0], ".")
	if len(core) != 3 {
		return parsedVersion{}, fmt.Errorf("version %q is not semantic versioning", value)
	}
	numbers := make([]int, 3)
	for i, item := range core {
		number, err := strconv.Atoi(item)
		if err != nil || number < 0 {
			return parsedVersion{}, fmt.Errorf("invalid version %q", value)
		}
		numbers[i] = number
	}
	parsed := parsedVersion{major: numbers[0], minor: numbers[1], patch: numbers[2]}
	if len(parts) == 2 {
		if parts[1] == "" {
			return parsedVersion{}, fmt.Errorf("invalid version %q", value)
		}
		parsed.prerelease = strings.Split(parts[1], ".")
	}
	return parsed, nil
}
