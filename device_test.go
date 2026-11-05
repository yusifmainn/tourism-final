package main

import "testing"

func TestParseDevice(t *testing.T) {
	cases := []struct {
		name  string
		ua    string
		hints clientHints
		touch int
		want  deviceInfo
	}{
		{
			name: "iPhone Safari",
			ua:   "Mozilla/5.0 (iPhone; CPU iPhone OS 17_4_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4.1 Mobile/15E148 Safari/604.1",
			want: deviceInfo{Type: deviceMobile, Device: "Apple iPhone", OS: "iOS 17.4.1", Browser: "Safari 17.4"},
		},
		{
			name: "Android Chrome with client hints model",
			ua:   "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Mobile Safari/537.36",
			hints: clientHints{Platform: "Android", PlatformVersion: "14.0.0", Model: "SM-S918B", Mobile: true,
				Brands: []hintBrand{{"Chromium", "130.0.6723.58"}, {"Google Chrome", "130.0.6723.58"}, {"Not?A_Brand", "99.0.0.0"}}},
			want: deviceInfo{Type: deviceMobile, Device: "Samsung SM-S918B", OS: "Android 14", Browser: "Chrome 130"},
		},
		{
			name:  "Android Chrome reduced UA, Xiaomi model from hints",
			ua:    "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Mobile Safari/537.36",
			hints: clientHints{Platform: "Android", PlatformVersion: "13.0.0", Model: "23021RAAEG", Mobile: true},
			want:  deviceInfo{Type: deviceMobile, Device: "Xiaomi 23021RAAEG", OS: "Android 13", Browser: "Chrome 129"},
		},
		{
			name: "Samsung Internet",
			ua:   "Mozilla/5.0 (Linux; Android 14; SAMSUNG SM-A546B) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/25.0 Chrome/121.0.0.0 Mobile Safari/537.36",
			want: deviceInfo{Type: deviceMobile, Device: "Samsung SM-A546B", OS: "Android 14", Browser: "Samsung Internet 25"},
		},
		{
			name: "Android tablet without Mobile token",
			ua:   "Mozilla/5.0 (Linux; Android 13; SM-X205 Build/TP1A.220624.014) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
			want: deviceInfo{Type: deviceTablet, Device: "Samsung SM-X205", OS: "Android 13", Browser: "Chrome 120"},
		},
		{
			name: "Windows 11 via client hints",
			ua:   "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 Edg/130.0.0.0",
			hints: clientHints{Platform: "Windows", PlatformVersion: "15.0.0",
				Brands: []hintBrand{{"Microsoft Edge", "130.0.2849.56"}, {"Chromium", "130.0.6723.70"}}},
			want: deviceInfo{Type: deviceDesktop, OS: "Windows 11", Browser: "Edge 130"},
		},
		{
			name:  "Windows 10 via client hints",
			ua:    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36",
			hints: clientHints{Platform: "Windows", PlatformVersion: "10.0.0"},
			want:  deviceInfo{Type: deviceDesktop, OS: "Windows 10", Browser: "Chrome 130"},
		},
		{
			name: "Windows without hints (Firefox)",
			ua:   "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:132.0) Gecko/20100101 Firefox/132.0",
			want: deviceInfo{Type: deviceDesktop, OS: "Windows 10/11", Browser: "Firefox 132"},
		},
		{
			name: "macOS Safari",
			ua:   "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Safari/605.1.15",
			want: deviceInfo{Type: deviceDesktop, Device: "Apple Mac", OS: "macOS 10.15.7", Browser: "Safari 17.4"},
		},
		{
			name:  "iPad in desktop mode",
			ua:    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Safari/605.1.15",
			touch: 5,
			want:  deviceInfo{Type: deviceTablet, Device: "Apple iPad", OS: "iPadOS 17.4", Browser: "Safari 17.4"},
		},
		{
			name: "Firefox Linux",
			ua:   "Mozilla/5.0 (X11; Ubuntu; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0",
			want: deviceInfo{Type: deviceDesktop, OS: "Ubuntu", Browser: "Firefox 131"},
		},
		{
			name:  "Brave looks like Chrome but hints tell",
			ua:    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36",
			hints: clientHints{Platform: "Linux", Brands: []hintBrand{{"Brave", "130"}, {"Chromium", "130"}, {"Not?A_Brand", "99"}}},
			want:  deviceInfo{Type: deviceDesktop, OS: "Linux", Browser: "Brave 130"},
		},
		{
			name: "Googlebot",
			ua:   "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)",
			want: deviceInfo{Type: deviceBot, Browser: "Googlebot"},
		},
		{
			name: "Googlebot smartphone",
			ua:   "Mozilla/5.0 (Linux; Android 6.0.1; Nexus 5X Build/MMB29P) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.6723.69 Mobile Safari/537.36 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)",
			want: deviceInfo{Type: deviceBot, Browser: "Googlebot"},
		},
		{
			name: "generic crawler",
			ua:   "Mozilla/5.0 (compatible; ExampleSearchBot/1.0)",
			want: deviceInfo{Type: deviceBot, Browser: "Bot"},
		},
		{
			name: "CUBOT phone is not a bot",
			ua:   "Mozilla/5.0 (Linux; Android 11; CUBOT_X30) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Mobile Safari/537.36",
			want: deviceInfo{Type: deviceMobile, Device: "CUBOT_X30", OS: "Android 11", Browser: "Chrome 120"},
		},
		{
			name: "Instagram in-app browser",
			ua:   "Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148 Instagram 323.0.0.35.65 (iPhone15,2; iOS 17_4; en_US; en; scale=3.00; 1179x2556; 581191403)",
			want: deviceInfo{Type: deviceMobile, Device: "Apple iPhone", OS: "iOS 17.4", Browser: "Instagram (tətbiq)"},
		},
		{
			name: "Facebook in-app browser on Android",
			ua:   "Mozilla/5.0 (Linux; Android 13; Pixel 7 Build/TQ3A.230901.001; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/118.0.0.0 Mobile Safari/537.36 [FB_IAB/FB4A;FBAV/440.0.0.31.105;]",
			want: deviceInfo{Type: deviceMobile, Device: "Google Pixel 7", OS: "Android 13", Browser: "Facebook (tətbiq)"},
		},
		{
			name: "empty User-Agent",
			ua:   "",
			want: deviceInfo{Type: deviceUnknown},
		},
	}
	for _, tc := range cases {
		if got := parseDevice(tc.ua, tc.hints, tc.touch); got != tc.want {
			t.Errorf("%s:\n got %+v\nwant %+v", tc.name, got, tc.want)
		}
	}
}

func TestAndroidDevice(t *testing.T) {
	cases := map[string]string{
		"SM-S918B":         "Samsung SM-S918B",
		"SAMSUNG SM-A546B": "Samsung SM-A546B",
		"Redmi Note 12":    "Xiaomi Redmi Note 12",
		"POCO X5 Pro 5G":   "Xiaomi POCO X5 Pro 5G",
		"2201117TG":        "Xiaomi 2201117TG",
		"CPH2591":          "OPPO CPH2591",
		"RMX3771":          "realme RMX3771",
		"V2111":            "vivo V2111",
		"Pixel 8":          "Google Pixel 8",
		"motorola edge 40": "Motorola edge 40",
		"HUAWEI P30":       "Huawei P30",
		"Unknown Phone 1":  "Unknown Phone 1",
		"":                 "",
	}
	for model, want := range cases {
		if got := androidDevice(model); got != want {
			t.Errorf("androidDevice(%q) = %q, want %q", model, got, want)
		}
	}
}
