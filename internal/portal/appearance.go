package portal

import (
	"net/url"
	"regexp"
	"strings"
)

var cssImageURL = regexp.MustCompile(`(?i)^url\(\s*['"]?([^'"()]+)['"]?\s*\)\s*(?:!important)?\s*;?$`)

func backgroundURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if m := cssImageURL.FindStringSubmatch(value); m != nil {
		value = strings.TrimSpace(m[1])
	}
	value = strings.ReplaceAll(value, `\:`, ":")
	if strings.HasPrefix(value, "/portal/v1/media/") && portalID.MatchString(mediaID(value)) {
		return value, nil
	}
	if len(value) > 2048 || strings.ContainsAny(value, "\r\n\x00\"'<>\\") {
		return "", bad("背景应填写 HTTPS 图片地址或本站上传图片")
	}
	u, e := url.Parse(value)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return "", bad("外链背景仅支持完整 HTTPS 图片地址")
	}
	return u.String(), nil
}
