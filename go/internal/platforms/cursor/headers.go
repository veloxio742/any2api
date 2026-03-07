package cursor

import (
	"fmt"
	"math/rand"
	"runtime"
	"time"
)

type cursorBrowserProfile struct {
	Platform        string
	PlatformVersion string
	Architecture    string
	Bitness         string
	ChromeVersion   int
	UserAgent       string
}

type cursorHeaderGenerator struct {
	profile       cursorBrowserProfile
	chromeVersion int
	rng           *rand.Rand
}

var cursorChromeVersions = []int{120, 121, 122, 123, 124, 125, 126, 127, 128, 129, 130}

var cursorWindowsProfiles = []cursorBrowserProfile{
	{Platform: "Windows", PlatformVersion: "10.0.0", Architecture: "x86", Bitness: "64"},
	{Platform: "Windows", PlatformVersion: "11.0.0", Architecture: "x86", Bitness: "64"},
	{Platform: "Windows", PlatformVersion: "15.0.0", Architecture: "x86", Bitness: "64"},
}

var cursorMacOSProfiles = []cursorBrowserProfile{
	{Platform: "macOS", PlatformVersion: "13.0.0", Architecture: "arm", Bitness: "64"},
	{Platform: "macOS", PlatformVersion: "14.0.0", Architecture: "arm", Bitness: "64"},
	{Platform: "macOS", PlatformVersion: "15.0.0", Architecture: "arm", Bitness: "64"},
	{Platform: "macOS", PlatformVersion: "13.0.0", Architecture: "x86", Bitness: "64"},
	{Platform: "macOS", PlatformVersion: "14.0.0", Architecture: "x86", Bitness: "64"},
}

var cursorLinuxProfiles = []cursorBrowserProfile{
	{Platform: "Linux", PlatformVersion: "", Architecture: "x86", Bitness: "64"},
}

func newCursorHeaderGenerator() *cursorHeaderGenerator {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	g := &cursorHeaderGenerator{rng: rng}
	g.Refresh()
	return g
}

func (g *cursorHeaderGenerator) ChatHeaders(xIsHuman string) map[string]string {
	lang := g.randomChoice([]string{"en-US,en;q=0.9", "zh-CN,zh;q=0.9,en;q=0.8", "en-GB,en;q=0.9"})
	referer := g.randomChoice([]string{"https://cursor.com/en-US/learn/how-ai-models-work", "https://cursor.com/cn/learn/how-ai-models-work", "https://cursor.com/"})
	headers := map[string]string{
		"sec-ch-ua-platform": fmt.Sprintf(`"%s"`, g.profile.Platform),
		"x-path":             "/api/chat",
		"Referer":            referer,
		"referer":            referer,
		"sec-ch-ua":          g.secChUA(),
		"x-method":           "POST",
		"sec-ch-ua-mobile":   "?0",
		"x-is-human":         xIsHuman,
		"User-Agent":         g.profile.UserAgent,
		"content-type":       "application/json",
		"accept-language":    lang,
	}
	g.addOptionalHeaders(headers)
	return headers
}

func (g *cursorHeaderGenerator) ScriptHeaders() map[string]string {
	lang := g.randomChoice([]string{"en-US,en;q=0.9", "zh-CN,zh;q=0.9,en;q=0.8", "en-GB,en;q=0.9"})
	referer := g.randomChoice([]string{"https://cursor.com/cn/learn/how-ai-models-work", "https://cursor.com/en-US/learn/how-ai-models-work", "https://cursor.com/"})
	headers := map[string]string{
		"User-Agent":         g.profile.UserAgent,
		"sec-ch-ua-arch":     fmt.Sprintf(`"%s"`, g.profile.Architecture),
		"sec-ch-ua-platform": fmt.Sprintf(`"%s"`, g.profile.Platform),
		"sec-ch-ua":          g.secChUA(),
		"sec-ch-ua-bitness":  fmt.Sprintf(`"%s"`, g.profile.Bitness),
		"sec-ch-ua-mobile":   "?0",
		"sec-fetch-site":     "same-origin",
		"sec-fetch-mode":     "no-cors",
		"sec-fetch-dest":     "script",
		"Referer":            referer,
		"referer":            referer,
		"accept-language":    lang,
	}
	if g.profile.PlatformVersion != "" {
		headers["sec-ch-ua-platform-version"] = fmt.Sprintf(`"%s"`, g.profile.PlatformVersion)
	}
	return headers
}

func (g *cursorHeaderGenerator) Profile() cursorBrowserProfile {
	return g.profile
}

func (g *cursorHeaderGenerator) Refresh() {
	profiles := cursorWindowsProfiles
	switch runtime.GOOS {
	case "darwin":
		profiles = cursorMacOSProfiles
	case "linux":
		profiles = cursorLinuxProfiles
	}
	profile := profiles[g.rng.Intn(len(profiles))]
	chromeVersion := cursorChromeVersions[g.rng.Intn(len(cursorChromeVersions))]
	profile.ChromeVersion = chromeVersion
	profile.UserAgent = cursorUserAgent(profile)
	g.profile = profile
	g.chromeVersion = chromeVersion
}

func (g *cursorHeaderGenerator) addOptionalHeaders(headers map[string]string) {
	if g.profile.Architecture != "" {
		headers["sec-ch-ua-arch"] = fmt.Sprintf(`"%s"`, g.profile.Architecture)
	}
	if g.profile.Bitness != "" {
		headers["sec-ch-ua-bitness"] = fmt.Sprintf(`"%s"`, g.profile.Bitness)
	}
	if g.profile.PlatformVersion != "" {
		headers["sec-ch-ua-platform-version"] = fmt.Sprintf(`"%s"`, g.profile.PlatformVersion)
	}
}

func (g *cursorHeaderGenerator) secChUA() string {
	notABrand := 24 + g.rng.Intn(10)
	return fmt.Sprintf(`"Google Chrome";v="%d", "Chromium";v="%d", "Not(A:Brand";v="%d"`, g.chromeVersion, g.chromeVersion, notABrand)
}

func (g *cursorHeaderGenerator) randomChoice(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[g.rng.Intn(len(values))]
}

func cursorUserAgent(profile cursorBrowserProfile) string {
	switch profile.Platform {
	case "Windows":
		return fmt.Sprintf("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.0.0.0 Safari/537.36", profile.ChromeVersion)
	case "macOS":
		return fmt.Sprintf("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.0.0.0 Safari/537.36", profile.ChromeVersion)
	case "Linux":
		return fmt.Sprintf("Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.0.0.0 Safari/537.36", profile.ChromeVersion)
	default:
		return fmt.Sprintf("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.0.0.0 Safari/537.36", profile.ChromeVersion)
	}
}
