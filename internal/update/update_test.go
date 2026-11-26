package update

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.1", "1.0.0", 1},
		{"1.0.0", "1.0.1", -1},
		{"2.0.0", "1.9.9", 1},
		{"1.10.0", "1.9.0", 1},
		{"1.0", "1.0.0", 0},
		{"x.y.z", "1.0.0", -1},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestSha256Matches(t *testing.T) {
	if !sha256Matches(" ABC123 ", "abc123") {
		t.Error("expected case-insensitive match")
	}
	if sha256Matches("abc", "abd") {
		t.Error("expected mismatch")
	}
}

func asset(name string) githubAsset {
	return githubAsset{Name: name, BrowserDownloadURL: "https://example.invalid/" + name}
}

func TestMatchPlatformAssetPrefersExactName(t *testing.T) {
	assets := []githubAsset{
		asset("gtasks-1.2.0-macos-x64.dmg"),
		asset("gtasks-1.2.0-macos-x64.bin"),
		asset("sha256sums.txt"),
	}
	got, ok := matchPlatformAsset(assets, "1.2.0")
	if runtime.GOOS != "darwin" || runtime.GOARCH != "amd64" {
		t.Skip("exact-name case targets darwin/amd64 naming")
	}
	if !ok || got.Name != "gtasks-1.2.0-macos-x64.bin" {
		t.Fatalf("got %+v ok=%v, want the bare .bin", got, ok)
	}
}

func TestMatchPlatformAssetExcludesInstallers(t *testing.T) {
	// 与当前平台无关的命名也要能被排除逻辑覆盖：直接校验排除后缀表。
	excluded := []string{"a.dmg", "a.deb", "a.rpm", "a.tar.gz", "a.txt", "a-installer.exe"}
	for _, name := range excluded {
		if !isExcludedAsset(name) {
			t.Errorf("%s should be excluded from self-update assets", name)
		}
	}
	if isExcludedAsset("gtasks-1.0.0-linux-x64") {
		t.Error("bare linux binary must stay selectable")
	}
}

func TestCheckPicksNewestStableAndSkipsPrerelease(t *testing.T) {
	payload := `[
	  {"tag_name":"v1.1.0-rc1","assets":[]},
	  {"tag_name":"v1.0.5","body":"fixes","assets":[
	     {"name":"sha256sums.txt","browser_download_url":"SUMS"},
	     {"name":"ASSET","browser_download_url":"DL"}
	  ]}
	]`
	osToken := map[string]string{"windows": "windows", "darwin": "macos", "linux": "linux"}[runtime.GOOS]
	archToken := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
	ext := map[string]string{"darwin": ".bin", "windows": ".exe", "linux": ""}[runtime.GOOS]
	assetName := fmt.Sprintf("gtasks-1.0.5-%s-%s%s", osToken, archToken, ext)
	sumsLine := "deadbeef  " + assetName + "\n"
	payload = strings.ReplaceAll(payload, "ASSET", assetName)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sums") {
			fmt.Fprint(w, sumsLine)
			return
		}
		fmt.Fprint(w, strings.ReplaceAll(payload, "SUMS", "http://"+r.Host+"/sums"))
	}))
	defer server.Close()

	u := NewUpdater(AppInfo{Version: "1.0.0", UpdateURL: server.URL + "/latest"}, nil, nil)
	info, err := u.Check()
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !info.HasUpdate || info.LatestVersion != "1.0.5" {
		t.Fatalf("got %+v, want stable 1.0.5", info)
	}
	if info.Filename != assetName {
		t.Errorf("filename = %q, want %q", info.Filename, assetName)
	}
	if info.Sha256 != "deadbeef" {
		t.Errorf("sha256 = %q, want deadbeef", info.Sha256)
	}
	if info.Changelog != "fixes" {
		t.Errorf("changelog = %q", info.Changelog)
	}
}

func TestCheckReportsUpToDate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"tag_name":"v1.0.0","assets":[]}]`)
	}))
	defer server.Close()
	u := NewUpdater(AppInfo{Version: "1.0.0", UpdateURL: server.URL + "/latest"}, nil, nil)
	info, err := u.Check()
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if info.HasUpdate {
		t.Errorf("same version must not report an update: %+v", info)
	}
}

func TestCheckNetworkErrorSurfaced(t *testing.T) {
	u := NewUpdater(AppInfo{Version: "1.0.0", UpdateURL: "http://127.0.0.1:1/latest"}, nil, nil)
	if _, err := u.Check(); err == nil || !strings.Contains(err.Error(), "network error") {
		t.Fatalf("want a network error, got %v", err)
	}
}

func TestDownloadVerifiesChecksum(t *testing.T) {
	body := []byte("gtasks-binary-payload")
	sum := sha256.Sum256(body)
	good := hex.EncodeToString(sum[:])

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer server.Close()

	u := NewUpdater(AppInfo{Version: "1.0.0"}, nil, nil)
	u.mu.Lock()
	u.latest = UpdateInfo{HasUpdate: true, LatestVersion: "1.0.1", DownloadURL: server.URL + "/bin", Sha256: good}
	u.checked = true
	u.mu.Unlock()
	if err := u.Download(); err != nil {
		t.Fatalf("Download with good checksum: %v", err)
	}
	payload, err := u.payloadPath()
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	got, err := sha256OfFile(payload)
	if err != nil || got != good {
		t.Fatalf("payload hash = %q err=%v, want %q", got, err, good)
	}

	u2 := NewUpdater(AppInfo{Version: "1.0.0"}, nil, nil)
	u2.mu.Lock()
	u2.latest = UpdateInfo{HasUpdate: true, LatestVersion: "1.0.1", DownloadURL: server.URL + "/bin", Sha256: strings.Repeat("0", 64)}
	u2.checked = true
	u2.mu.Unlock()
	if err := u2.Download(); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("want checksum mismatch, got %v", err)
	}
	if _, err := u2.payloadPath(); err == nil {
		t.Fatal("payload must be removed after a checksum mismatch")
	}
}

func TestCheckFallsBackToWebWhenRateLimited(t *testing.T) {
	osToken := map[string]string{"windows": "windows", "darwin": "macos", "linux": "linux"}[runtime.GOOS]
	archToken := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
	ext := map[string]string{"darwin": ".bin", "windows": ".exe", "linux": ""}[runtime.GOOS]
	assetName := fmt.Sprintf("gtasks-1.0.5-%s-%s%s", osToken, archToken, ext)
	sumsLine := "deadbeef  " + assetName + "\n"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/o/r/releases"):
			// API 通道：限流。
			w.WriteHeader(http.StatusForbidden)
		case r.URL.Path == "/o/r/releases/latest":
			http.Redirect(w, r, "/o/r/releases/tag/v1.0.5", http.StatusFound)
		case strings.HasSuffix(r.URL.Path, "sha256sums.txt"):
			fmt.Fprint(w, sumsLine)
		default:
			fmt.Fprint(w, "payload")
		}
	}))
	defer server.Close()

	u := NewUpdater(AppInfo{Version: "1.0.0", UpdateURL: server.URL + "/repos/o/r/releases/latest"}, nil, nil)
	info, err := u.Check()
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !info.HasUpdate || info.LatestVersion != "1.0.5" {
		t.Fatalf("got %+v, want stable 1.0.5 via web fallback", info)
	}
	if info.Filename != assetName {
		t.Errorf("filename = %q, want %q", info.Filename, assetName)
	}
	if info.Sha256 != "deadbeef" {
		t.Errorf("sha256 = %q, want deadbeef", info.Sha256)
	}
	if !strings.HasSuffix(info.DownloadURL, "/releases/download/v1.0.5/"+assetName) {
		t.Errorf("download url = %q, want the convention-built asset URL", info.DownloadURL)
	}
}

func TestCheckRateLimitedWithoutDerivableWebURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	u := NewUpdater(AppInfo{Version: "1.0.0", UpdateURL: server.URL + "/latest"}, nil, nil)
	_, err := u.Check()
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("want a rate-limit error, got %v", err)
	}
}
