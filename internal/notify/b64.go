package notify

import "encoding/base64"

// base64Wrap 将内容按邮件规范每 76 字节换行，避免部分 SMTP 服务器拒绝超长行。
func base64Wrap(s string) string {
	raw := base64.StdEncoding.EncodeToString([]byte(s))
	var sb string
	for len(raw) > 76 {
		sb += raw[:76] + "\r\n"
		raw = raw[76:]
	}
	return sb + raw
}
