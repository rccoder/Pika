package bilibili

import "strings"

const (
	ParseModeKey          = "parse_mode"
	ParseModeOn           = "on"
	ParseModeMusicKichiku = "music_kichiku"
	ParseModeOff          = "off"
	SearchFilterKey       = "search_filter_kichiku"
	SearchFilterOn        = "on"
	SearchFilterOff       = "off"
)

func normalizeParseMode(mode string) string {
	switch strings.TrimSpace(mode) {
	case ParseModeOn, ParseModeMusicKichiku, ParseModeOff:
		return strings.TrimSpace(mode)
	default:
		return ParseModeOn
	}
}
