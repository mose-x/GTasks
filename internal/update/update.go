// Package update 实现 GTasks 的应用内自升级，策略与 SVC 项目一致：
// 拉 GitHub Releases 列表取最新的纯 X.Y.Z 稳定版（rc/beta 一律跳过），
// 按平台匹配裸二进制资产并用 sha256sums.txt 校验，进程退出后由后台脚本
// 原子替换二进制（保留 .bak 供回滚）再重新拉起。
//
// 与 SVC 的差异只有外围：GTasks 没有代理服务 / 多线程下载器 / GitHub 镜像，
// 网络一律走 http.DefaultTransport（尊重 http_proxy 等环境变量）。
package update

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// AppInfo 描述当前运行中的构建。UpdateURL 指向 GitHub Releases API 的
// latest 端点，检查时会去掉末尾的 /latest 改成拉列表。
type AppInfo struct {
	Version   string `json:"version"`
	License   string `json:"license"`
	RepoURL   string `json:"repoUrl"`
	UpdateURL string `json:"updateUrl"`
}

// UpdateInfo 是一次检查的结论。
type UpdateInfo struct {
	HasUpdate     bool   `json:"hasUpdate"`
	LatestVersion string `json:"latestVersion"`
	Changelog     string `json:"changelog"`
	DownloadURL   string `json:"downloadUrl"`
	Filename      string `json:"filename"`
	Sha256        string `json:"sha256"`
}

// Event 驱动页面里的升级对话框：Stage 由页面按词表本地化，Message 只放
// 技术细节（错误原文 / 字节数），不做用户文案。
type Event struct {
	Stage   string      `json:"stage"`
	Percent int         `json:"percent"`
	Message string      `json:"message,omitempty"`
	Info    *UpdateInfo `json:"info,omitempty"`
}

// Stage 取值，页面词表按这些键出文案。
const (
	StageChecking    = "checking"
	StageAvailable   = "available"
	StageUpToDate    = "uptodate"
	StageDownloading = "downloading"
	StageVerifying   = "verifying"
	StageApplying    = "applying"
	StageDownloaded  = "downloaded"
	StageError       = "error"
)

// Updater 串起检查 / 下载 / 应用 / 回滚四步。emit 与 quit 由宿主注入：
// emit 把事件推给注入 UI，quit 在替换脚本就位后退出当前进程。
type Updater struct {
	info AppInfo
	emit func(Event)
	quit func()

	mu      sync.Mutex
	busy    bool
	checked bool
	latest  UpdateInfo
	payload string
}

func NewUpdater(info AppInfo, emit func(Event), quit func()) *Updater {
	if emit == nil {
		emit = func(Event) {}
	}
	return &Updater{info: info, emit: emit, quit: quit}
}

func (u *Updater) Info() AppInfo { return u.info }

// Latest 返回最近一次检查结论；没检查过 ok 为 false。
func (u *Updater) Latest() (UpdateInfo, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.latest, u.checked
}

func (u *Updater) send(ev Event) { u.emit(ev) }

func (u *Updater) begin() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.busy {
		return fmt.Errorf("update already in progress")
	}
	u.busy = true
	return nil
}

func (u *Updater) end() {
	u.mu.Lock()
	u.busy = false
	u.mu.Unlock()
}

// Check 查询最新稳定版。网络不通 / 非 200 / 解析失败都返回 error，
// 由调用方把原文展示给用户；「没有稳定版可升」不算错误。
func (u *Updater) Check() (UpdateInfo, error) {
	if err := u.begin(); err != nil {
		return UpdateInfo{}, err
	}
	defer u.end()
	u.send(Event{Stage: StageChecking})
	info, err := u.check()
	if err != nil {
		u.send(Event{Stage: StageError, Message: err.Error()})
		return UpdateInfo{}, err
	}
	u.mu.Lock()
	u.latest, u.checked = info, true
	u.mu.Unlock()
	report := info
	if info.HasUpdate {
		u.send(Event{Stage: StageAvailable, Info: &report})
	} else {
		u.send(Event{Stage: StageUpToDate, Info: &report})
	}
	return info, nil
}

// githubRelease 只取升级流程用得上的字段。
type githubRelease struct {
	TagName string        `json:"tag_name"`
	Body    string        `json:"body"`
	Assets  []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// stableVersionReg 只认纯 X.Y.Z：带 -rc1 / -beta 之类后缀的 tag 一律跳过，
// 保证升级器永远不会把非稳定版推给用户（与 GitHub 是否标 pre-release 无关）。
var stableVersionReg = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

func (u *Updater) check() (UpdateInfo, error) {
	if u.info.UpdateURL == "" {
		return UpdateInfo{}, fmt.Errorf("update URL is not configured")
	}
	client := &http.Client{Timeout: 20 * time.Second}
	listURL := strings.TrimSuffix(u.info.UpdateURL, "/latest")
	req, err := http.NewRequest(http.MethodGet, listURL, nil)
	if err != nil {
		return UpdateInfo{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "GTasks")
	q := req.URL.Query()
	q.Set("per_page", "30")
	req.URL.RawQuery = q.Encode()

	resp, err := client.Do(req)
	if err != nil {
		return UpdateInfo{}, fmt.Errorf("network error, unable to reach update server: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return UpdateInfo{}, fmt.Errorf("update server returned HTTP %d", resp.StatusCode)
	}
	var releases []githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return UpdateInfo{}, fmt.Errorf("failed to parse release info: %w", err)
	}

	var release *githubRelease
	for i := range releases {
		if stableVersionReg.MatchString(strings.TrimPrefix(releases[i].TagName, "v")) {
			release = &releases[i]
			break
		}
	}
	if release == nil {
		return UpdateInfo{HasUpdate: false}, nil
	}

	info := UpdateInfo{LatestVersion: strings.TrimPrefix(release.TagName, "v")}
	if CompareVersions(info.LatestVersion, u.info.Version) <= 0 {
		return info, nil
	}
	asset, ok := matchPlatformAsset(release.Assets, info.LatestVersion)
	if !ok {
		return UpdateInfo{}, fmt.Errorf("release %s has no asset for %s/%s",
			info.LatestVersion, runtime.GOOS, runtime.GOARCH)
	}
	info.HasUpdate = true
	info.Changelog = release.Body
	info.DownloadURL = asset.BrowserDownloadURL
	info.Filename = asset.Name
	if sha := fetchAssetSha256(client, release.Assets, asset.Name); sha != "" {
		info.Sha256 = sha
	}
	return info, nil
}

// matchPlatformAsset 先按命名约定精确匹配 gtasks-<ver>-<os>-<arch><ext>，
// 匹配不到再退化成「包含 os/arch 且不在排除后缀里」的扫描。资产命名见
// scripts/build-*.sh：macos 自升级用裸 .bin（不是 .dmg），linux 用无扩展名
// 裸二进制（不是 .deb/.rpm/.tar.gz），windows 用裸 .exe。
func matchPlatformAsset(assets []githubAsset, version string) (githubAsset, bool) {
	osToken := map[string]string{"windows": "windows", "darwin": "macos", "linux": "linux"}[runtime.GOOS]
	archToken := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
	if osToken == "" || archToken == "" {
		return githubAsset{}, false
	}
	ext := map[string]string{"darwin": ".bin", "windows": ".exe", "linux": ""}[runtime.GOOS]
	want := strings.ToLower(fmt.Sprintf("gtasks-%s-%s-%s%s", version, osToken, archToken, ext))
	for _, a := range assets {
		if strings.ToLower(a.Name) == want {
			return a, true
		}
	}
	for _, a := range assets {
		name := strings.ToLower(a.Name)
		if !strings.Contains(name, osToken) || !strings.Contains(name, archToken) {
			continue
		}
		if isExcludedAsset(name) {
			continue
		}
		return a, true
	}
	return githubAsset{}, false
}

// isExcludedAsset 列出自升级不能选的资产后缀：安装包 / 压缩包 / 校验清单。
// 自升级只能拿裸二进制原地替换，安装包装不上、压缩包还得解。
func isExcludedAsset(name string) bool {
	switch {
	case strings.HasSuffix(name, ".dmg"),
		strings.HasSuffix(name, ".deb"),
		strings.HasSuffix(name, ".rpm"),
		strings.HasSuffix(name, ".tar.gz"),
		strings.HasSuffix(name, ".txt"),
		strings.HasSuffix(name, "-installer.exe"):
		return true
	}
	return false
}

// fetchAssetSha256 从 sha256sums.txt 里取 filename 对应的哈希；清单缺失或
// 没列这个文件时返回空串，Download 随之跳过校验（兼容老 release）。
func fetchAssetSha256(client *http.Client, assets []githubAsset, filename string) string {
	var sumsURL string
	for _, a := range assets {
		if a.Name == "sha256sums.txt" {
			sumsURL = a.BrowserDownloadURL
			break
		}
	}
	if sumsURL == "" {
		return ""
	}
	resp, err := client.Get(sumsURL)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == filename {
			return fields[0]
		}
	}
	return ""
}

// Download 把已检查出的资产拉到临时文件并校验 SHA256，进度通过 Event 上报。
// 校验失败会删掉临时文件，Apply 不可能捡到坏包。
func (u *Updater) Download() error {
	if err := u.begin(); err != nil {
		return err
	}
	defer u.end()
	info, ok := u.Latest()
	if !ok || !info.HasUpdate || info.DownloadURL == "" {
		return fmt.Errorf("no checked update available, run check first")
	}

	u.send(Event{Stage: StageDownloading})
	f, err := os.CreateTemp("", "gtasks_update_payload_*")
	if err != nil {
		u.send(Event{Stage: StageError, Message: err.Error()})
		return fmt.Errorf("failed to create temp payload: %w", err)
	}
	payload := f.Name()

	// 下载不设整体超时：资产十几兆，慢线路（直连 CDN 几十 KB/s）下整体超时
	// 必然在收尾前掐断；卡死由 TCP 层自己报错兜底。
	client := &http.Client{}
	resp, err := client.Get(info.DownloadURL)
	if err != nil {
		f.Close()
		os.Remove(payload)
		u.send(Event{Stage: StageError, Message: err.Error()})
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		f.Close()
		os.Remove(payload)
		msg := fmt.Sprintf("download returned HTTP %d", resp.StatusCode)
		u.send(Event{Stage: StageError, Message: msg})
		return fmt.Errorf("%s", msg)
	}

	total := resp.ContentLength
	var downloaded int64
	lastEmit := time.Now()
	buf := make([]byte, 64*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := f.Write(buf[:n]); writeErr != nil {
				f.Close()
				os.Remove(payload)
				u.send(Event{Stage: StageError, Message: writeErr.Error()})
				return fmt.Errorf("failed to write payload: %w", writeErr)
			}
			downloaded += int64(n)
			if time.Since(lastEmit) > 200*time.Millisecond {
				lastEmit = time.Now()
				u.send(progressEvent(downloaded, total))
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			f.Close()
			os.Remove(payload)
			u.send(Event{Stage: StageError, Message: readErr.Error()})
			return fmt.Errorf("download failed: %w", readErr)
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(payload)
		u.send(Event{Stage: StageError, Message: err.Error()})
		return fmt.Errorf("failed to finalize payload: %w", err)
	}
	u.send(progressEvent(downloaded, total))

	if info.Sha256 != "" {
		u.send(Event{Stage: StageVerifying, Percent: 100})
		actual, err := sha256OfFile(payload)
		if err != nil {
			os.Remove(payload)
			u.send(Event{Stage: StageError, Message: err.Error()})
			return fmt.Errorf("failed to hash downloaded file: %w", err)
		}
		if !sha256Matches(actual, info.Sha256) {
			os.Remove(payload)
			msg := fmt.Sprintf("checksum mismatch: expected %s, got %s", info.Sha256, actual)
			u.send(Event{Stage: StageError, Message: msg})
			return fmt.Errorf("%s", msg)
		}
	}

	u.mu.Lock()
	if u.payload != "" {
		os.Remove(u.payload)
	}
	u.payload = payload
	u.mu.Unlock()
	done := info
	u.send(Event{Stage: StageDownloaded, Percent: 100, Info: &done})
	return nil
}

func progressEvent(downloaded, total int64) Event {
	percent := 0
	if total > 0 {
		percent = int(downloaded * 100 / total)
	}
	msg := fmt.Sprintf("%.1fMB", float64(downloaded)/(1024*1024))
	if total > 0 {
		msg = fmt.Sprintf("%.1fMB / %.1fMB", float64(downloaded)/(1024*1024), float64(total)/(1024*1024))
	}
	return Event{Stage: StageDownloading, Percent: percent, Message: msg}
}

// payloadPath 取下载好的临时包；没有则报错。
func (u *Updater) payloadPath() (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.payload == "" {
		return "", fmt.Errorf("no downloaded payload, run download first")
	}
	if _, err := os.Stat(u.payload); err != nil {
		return "", fmt.Errorf("downloaded payload is gone: %w", err)
	}
	return u.payload, nil
}

func sha256OfFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// sha256Matches 忽略大小写比较：清单里偶尔是大写十六进制。
func sha256Matches(actual, expected string) bool {
	return strings.EqualFold(strings.TrimSpace(actual), strings.TrimSpace(expected))
}

// CompareVersions 比较两个 X.Y.Z 版本号，返回 1 / 0 / -1。
// 缺位按 0 处理，非数字段按 0 处理（脏 tag 不会让升级流程崩掉）。
func CompareVersions(a, b string) int {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		av, bv := 0, 0
		if i < len(as) {
			av, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			bv, _ = strconv.Atoi(bs[i])
		}
		if av != bv {
			if av > bv {
				return 1
			}
			return -1
		}
	}
	return 0
}
