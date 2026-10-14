package singbox

func ruleSetsRU() []map[string]any {
	return []map[string]any{
		{
			"type":            "remote",
			"tag":             "geosite-ru",
			"format":          "binary",
			"url":             "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-ru.srs",
			"update_interval": "24h",
		},
		{
			"type":            "remote",
			"tag":             "geoip-ru",
			"format":          "binary",
			"url":             "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-ru.srs",
			"update_interval": "24h",
		},
	}
}
