package main

import (
	"regexp"
	"strconv"
	"strings"
)

// Device types stored in visit_logs.device_type.
const (
	deviceDesktop = "desktop"
	deviceMobile  = "mobile"
	deviceTablet  = "tablet"
	deviceBot     = "bot"
	deviceUnknown = "unknown"
)

// clientHints are the browser's User-Agent Client Hints
// (navigator.userAgentData.getHighEntropyValues), sent by assets/js/visit.js.
// Like the User-Agent header they come from the visitor and are only informative.
type clientHints struct {
	Platform        string      `json:"platform"`        // "Windows", "Android", "macOS", …
	PlatformVersion string      `json:"platformVersion"` // "15.0.0" (Windows 11 when the major is ≥ 13)
	Model           string      `json:"model"`           // "SM-S918B" (Android only)
	Mobile          bool        `json:"mobile"`
	Brands          []hintBrand `json:"brands"`
	FullVersionList []hintBrand `json:"fullVersionList"`
	Architecture    string      `json:"architecture"` // "x86", "arm"
	Bitness         string      `json:"bitness"`      // "64"
}

type hintBrand struct {
	Brand   string `json:"brand"`
	Version string `json:"version"`
}

// deviceInfo is the readable summary shown in the admin panel.
type deviceInfo struct {
	Type    string // deviceDesktop, deviceMobile, …
	Device  string // "Samsung SM-S918B", "Apple iPhone"; "" for an ordinary PC
	OS      string // "Android 14", "Windows 11", "iOS 17.4"
	Browser string // "Chrome 130", "Safari 17.4", "Instagram (tətbiq)"
}

// parseDevice reads a User-Agent string plus optional client hints. touchPoints
// (navigator.maxTouchPoints) tells an iPad in desktop mode apart from a Mac.
func parseDevice(ua string, h clientHints, touchPoints int) deviceInfo {
	ua = strings.TrimSpace(ua)
	if ua == "" && h.Platform == "" {
		return deviceInfo{Type: deviceUnknown}
	}
	if name := botName(ua); name != "" {
		return deviceInfo{Type: deviceBot, Browser: name}
	}
	d := deviceInfo{Type: deviceUnknown, Browser: parseBrowser(ua, h)}
	platform := strings.ToLower(h.Platform)
	switch {
	case strings.Contains(ua, "iPhone") || strings.Contains(ua, "iPod"):
		d.Type, d.Device = deviceMobile, "Apple iPhone"
		if strings.Contains(ua, "iPod") {
			d.Device = "Apple iPod"
		}
		d.OS = joinVersion("iOS", dotted(versionAfter(ua, "OS ", 3)))
	case strings.Contains(ua, "iPad"):
		d.Type, d.Device = deviceTablet, "Apple iPad"
		d.OS = joinVersion("iPadOS", dotted(versionAfter(ua, "OS ", 3)))
	case strings.Contains(ua, "Android") || platform == "android":
		d.Type = deviceTablet
		if strings.Contains(ua, "Mobile") || h.Mobile {
			d.Type = deviceMobile
		}
		version := versionAfter(ua, "Android ", 2)
		if platform == "android" && h.PlatformVersion != "" {
			version = trimZeros(h.PlatformVersion)
		}
		d.OS = joinVersion("Android", version)
		d.Device = androidDevice(androidModel(ua, h))
	case strings.Contains(ua, "Windows") || platform == "windows":
		d.Type = deviceDesktop
		d.OS = windowsVersion(ua, h)
		if strings.Contains(ua, "Windows Phone") {
			d.Type, d.OS = deviceMobile, "Windows Phone"
		}
	case strings.Contains(ua, "Macintosh") || platform == "macos":
		if touchPoints > 1 { // iPadOS 13+ asks for desktop sites with a Mac User-Agent
			d.Type, d.Device = deviceTablet, "Apple iPad"
			d.OS = joinVersion("iPadOS", dotted(versionAfter(ua, "Version/", 3)))
			break
		}
		d.Type, d.Device = deviceDesktop, "Apple Mac"
		version := dotted(versionAfter(ua, "Mac OS X ", 3))
		if platform == "macos" && h.PlatformVersion != "" {
			version = trimZeros(h.PlatformVersion)
		}
		d.OS = joinVersion("macOS", version)
	case strings.Contains(ua, "CrOS") || platform == "chrome os" || platform == "chromeos":
		d.Type, d.OS = deviceDesktop, "ChromeOS"
	case strings.Contains(ua, "Linux") || strings.Contains(ua, "X11") || platform == "linux":
		d.Type, d.OS = deviceDesktop, "Linux"
		for _, distro := range []string{"Ubuntu", "Fedora", "Debian"} {
			if strings.Contains(ua, distro) {
				d.OS = distro
			}
		}
	default:
		d.OS = h.Platform
		if strings.Contains(ua, "Mobile") || h.Mobile {
			d.Type = deviceMobile
		}
	}
	return d
}

/* ---------- Bots ---------- */

var knownBots = []struct{ marker, name string }{
	{"googlebot", "Googlebot"}, {"google-inspectiontool", "Google Inspection Tool"},
	{"adsbot-google", "AdsBot-Google"}, {"bingbot", "Bingbot"}, {"yandexbot", "YandexBot"},
	{"yandex.com/bots", "Yandex bot"}, {"baiduspider", "Baiduspider"}, {"duckduckbot", "DuckDuckBot"},
	{"applebot", "Applebot"}, {"facebookexternalhit", "Facebook (keçid önizləməsi)"},
	{"meta-externalagent", "Meta bot"}, {"twitterbot", "Twitterbot"}, {"linkedinbot", "LinkedInBot"},
	{"telegrambot", "TelegramBot"}, {"whatsapp", "WhatsApp (keçid önizləməsi)"}, {"slackbot", "Slackbot"},
	{"discordbot", "Discordbot"}, {"ahrefsbot", "AhrefsBot"}, {"semrushbot", "SemrushBot"},
	{"mj12bot", "MJ12bot"}, {"petalbot", "PetalBot"}, {"gptbot", "GPTBot"}, {"claudebot", "ClaudeBot"},
	{"bytespider", "Bytespider"}, {"headlesschrome", "HeadlessChrome"}, {"lighthouse", "Lighthouse"},
	{"uptimerobot", "UptimeRobot"}, {"curl/", "curl"}, {"wget/", "Wget"}, {"python-requests", "python-requests"},
	{"python-urllib", "Python urllib"}, {"go-http-client", "Go HTTP client"}, {"java/", "Java"},
	{"axios/", "axios"}, {"node-fetch", "node-fetch"}, {"postmanruntime", "Postman"}, {"scrapy", "Scrapy"},
}

// genericBot catches the many smaller crawlers ("…bot/1.0", "…crawler", "…spider").
var genericBot = regexp.MustCompile(`(?i)(bot|crawler|spider)\b|\bcrawl`)

func botName(ua string) string {
	lower := strings.ToLower(ua)
	for _, b := range knownBots {
		if strings.Contains(lower, b.marker) {
			return b.name
		}
	}
	if genericBot.MatchString(strings.ReplaceAll(lower, "cubot", "")) { // CUBOT is a phone brand
		return "Bot"
	}
	return ""
}

/* ---------- Browsers ---------- */

func parseBrowser(ua string, h clientHints) string {
	switch {
	case strings.Contains(ua, "Instagram"):
		return "Instagram (tətbiq)"
	case strings.Contains(ua, "FBAN/") || strings.Contains(ua, "FBAV/") || strings.Contains(ua, "FB_IAB"):
		return "Facebook (tətbiq)"
	case strings.Contains(ua, "TikTok") || strings.Contains(ua, "musical_ly") || strings.Contains(ua, "BytedanceWebview"):
		return "TikTok (tətbiq)"
	}
	tokens := []struct{ token, name string }{
		{"EdgA/", "Edge"}, {"EdgiOS/", "Edge"}, {"Edg/", "Edge"},
		{"OPR/", "Opera"}, {"OPT/", "Opera"}, {"OPiOS/", "Opera"},
		{"YaBrowser/", "Yandex Browser"}, {"SamsungBrowser/", "Samsung Internet"},
		{"UCBrowser/", "UC Browser"}, {"Firefox/", "Firefox"}, {"FxiOS/", "Firefox"},
		{"CriOS/", "Chrome"},
	}
	for _, t := range tokens {
		if strings.Contains(ua, t.token) {
			return joinVersion(t.name, versionAfter(ua, t.token, 1))
		}
	}
	if name, version := hintBrowser(h); name != "" && name != "Chrome" && name != "Chromium" {
		return joinVersion(name, version) // e.g. Brave, whose User-Agent looks like Chrome
	}
	switch {
	case strings.Contains(ua, "; wv)"):
		return "Android WebView"
	case strings.Contains(ua, "Chrome/"):
		if name, version := hintBrowser(h); name == "Chrome" && version != "" {
			return joinVersion(name, version)
		}
		return joinVersion("Chrome", versionAfter(ua, "Chrome/", 1))
	case strings.Contains(ua, "Chromium/"):
		return joinVersion("Chromium", versionAfter(ua, "Chromium/", 1))
	case strings.Contains(ua, "Safari/") && strings.Contains(ua, "Version/"):
		return joinVersion("Safari", versionAfter(ua, "Version/", 2))
	case strings.Contains(ua, "Trident/") || strings.Contains(ua, "MSIE "):
		return "Internet Explorer"
	case strings.Contains(ua, "iPhone") || strings.Contains(ua, "iPad"):
		return "WebView" // an app's built-in browser on iOS (no Safari token)
	}
	if name, version := hintBrowser(h); name != "" {
		return joinVersion(name, version)
	}
	return ""
}

// hintBrowser picks the real browser out of the client hint brands, ignoring the
// deliberately fake "Not A Brand" entries.
func hintBrowser(h clientHints) (string, string) {
	brands := h.FullVersionList
	if len(brands) == 0 {
		brands = h.Brands
	}
	names := []struct{ brand, name string }{
		{"Microsoft Edge", "Edge"}, {"Opera", "Opera"}, {"Brave", "Brave"}, {"YaBrowser", "Yandex Browser"},
		{"Yandex", "Yandex Browser"}, {"Samsung Internet", "Samsung Internet"}, {"Vivaldi", "Vivaldi"},
		{"Google Chrome", "Chrome"}, {"Chromium", "Chromium"},
	}
	for _, n := range names {
		for _, b := range brands {
			if strings.HasPrefix(b.Brand, n.brand) {
				return n.name, firstParts(b.Version, 1)
			}
		}
	}
	return "", ""
}

/* ---------- Operating systems and devices ---------- */

func windowsVersion(ua string, h clientHints) string {
	if strings.EqualFold(h.Platform, "Windows") && h.PlatformVersion != "" {
		major, err := strconv.Atoi(firstParts(h.PlatformVersion, 1))
		switch {
		case err != nil:
		case major >= 13: // client hints: 13 and above is Windows 11
			return "Windows 11"
		case major >= 1:
			return "Windows 10"
		}
	}
	switch versionAfter(ua, "Windows NT ", 2) {
	case "10.0":
		return "Windows 10/11" // both say 10.0; only client hints (sent by visit.js) tell them apart
	case "6.3":
		return "Windows 8.1"
	case "6.2":
		return "Windows 8"
	case "6.1":
		return "Windows 7"
	}
	return "Windows"
}

// androidModel finds the phone model in the client hints or in the User-Agent
// ("Linux; Android 14; SM-S918B Build/…"). Modern Chrome sends just "K" there.
func androidModel(ua string, h clientHints) string {
	if m := strings.TrimSpace(h.Model); m != "" {
		return m
	}
	i := strings.Index(ua, "Android")
	if i < 0 {
		return ""
	}
	segment := ua[i:]
	if end := strings.IndexByte(segment, ')'); end >= 0 {
		segment = segment[:end]
	}
	parts := strings.Split(segment, ";")
	for _, p := range parts[1:] {
		p = strings.TrimSpace(p)
		if b := strings.Index(p, " Build/"); b >= 0 {
			p = p[:b]
		}
		p = strings.TrimSuffix(p, " Build")
		switch {
		case p == "" || p == "K" || p == "U" || p == "wv" || p == "Mobile" || p == "Tablet":
			continue
		case strings.HasPrefix(p, "rv:") || localePattern.MatchString(p):
			continue
		}
		return p
	}
	return ""
}

var (
	localePattern = regexp.MustCompile(`^[a-z]{2}([-_][a-zA-Z]{2})?$`)
	xiaomiNumeric = regexp.MustCompile(`^2\d{4,}`) // e.g. 2201117TG, 23021RAAEG
)

// modelBrands maps model name prefixes to the maker (compared without case).
var modelBrands = []struct{ prefix, brand string }{
	{"SAMSUNG", "Samsung"}, {"SM-", "Samsung"}, {"GT-", "Samsung"}, {"Galaxy", "Samsung"},
	{"Pixel", "Google"}, {"Nexus", "Google"},
	{"Xiaomi", "Xiaomi"}, {"Redmi", "Xiaomi"}, {"POCO", "Xiaomi"}, {"Mi ", "Xiaomi"}, {"M2", "Xiaomi"},
	{"CPH", "OPPO"}, {"OPPO", "OPPO"}, {"RMX", "realme"}, {"realme", "realme"},
	{"vivo", "vivo"}, {"V2", "vivo"}, {"OnePlus", "OnePlus"}, {"HUAWEI", "Huawei"}, {"HONOR", "Honor"},
	{"motorola", "Motorola"}, {"moto", "Motorola"}, {"Nokia", "Nokia"}, {"TECNO", "TECNO"},
	{"Infinix", "Infinix"}, {"itel", "itel"}, {"LM-", "LG"}, {"LG-", "LG"}, {"XQ-", "Sony"},
	{"Lenovo", "Lenovo"}, {"ZTE", "ZTE"}, {"Nothing", "Nothing"},
}

// androidDevice turns a model code into "Maker model", e.g. "SM-S918B" -> "Samsung SM-S918B".
func androidDevice(model string) string {
	if model == "" {
		return ""
	}
	upper := strings.ToUpper(model)
	brand := ""
	for _, mb := range modelBrands {
		if strings.HasPrefix(upper, strings.ToUpper(mb.prefix)) {
			brand = mb.brand
			break
		}
	}
	if brand == "" && xiaomiNumeric.MatchString(model) {
		brand = "Xiaomi"
	}
	if brand == "" {
		return model
	}
	if strings.HasPrefix(upper, strings.ToUpper(brand)) { // "SAMSUNG SM-S918B", "motorola edge 40"
		model = strings.TrimLeft(model[len(brand):], " _-")
	}
	if model == "" {
		return brand
	}
	return brand + " " + model
}

/* ---------- Version helpers ---------- */

// versionAfter returns the version number that follows token, keeping at most
// `parts` components: versionAfter("Chrome/130.0.6723.58", "Chrome/", 1) = "130".
// Underscores (iOS "17_4_1") count as separators and are kept.
func versionAfter(ua, token string, parts int) string {
	i := strings.Index(ua, token)
	if i < 0 {
		return ""
	}
	rest := ua[i+len(token):]
	end := 0
	for end < len(rest) && (rest[end] >= '0' && rest[end] <= '9' || rest[end] == '.' || rest[end] == '_') {
		end++
	}
	return firstParts(strings.Trim(rest[:end], "._"), parts)
}

// firstParts keeps the first n dot- or underscore-separated components.
func firstParts(version string, n int) string {
	count := 0
	for i := 0; i < len(version); i++ {
		if version[i] == '.' || version[i] == '_' {
			count++
			if count == n {
				return version[:i]
			}
		}
	}
	return version
}

// dotted writes iOS-style "17_4" as "17.4".
func dotted(version string) string { return strings.ReplaceAll(version, "_", ".") }

// trimZeros shortens "14.0.0" to "14" and "14.5.0" to "14.5".
func trimZeros(version string) string {
	for strings.HasSuffix(version, ".0") {
		version = strings.TrimSuffix(version, ".0")
	}
	return version
}

func joinVersion(name, version string) string {
	if version == "" {
		return name
	}
	return name + " " + version
}
