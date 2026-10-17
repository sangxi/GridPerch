// Package license 的验签实现：Ed25519（现行）与 HMAC-SHA256（历史兼容）。
//
// 授权码结构与授权中心侧完全一致：
//
//	<前缀><base64url(JSON 载荷)>.<base64url(签名)>
//
// 签名对象是点号前的那段 base64url 字符串本身，而不是原始 JSON —— 这样验签
// 不必先解析 JSON，也就不会因为编码或字段顺序的差异产生歧义。
//
// 本文件只实现「验」，没有「签」：
//   - STRIX2- 用 Ed25519 公钥验签。公钥只能验、不能签，所以即使二进制被逆向，
//     也伪造不出授权码。
//   - OPSM1- / OPSM- / WGC- 用 HMAC-SHA256 对称密钥验签，只为兼容存量授权码而保留。
package license

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// 授权码前缀。这里只做识别，不产生新码。
const (
	// PrefixV2 现行格式，Ed25519 签名，由授权中心签发。
	PrefixV2 = "STRIX2-"
	// prefixLegacyV2 更名前的现行前缀，同样是 Ed25519 签名，只读兼容存量授权码。
	// 授权中心改成签发 STRIX2- 之后，老用户手里的 OPSM2- 码仍然能验，不必换码。
	prefixLegacyV2 = "OPSM2-"
	// PrefixV1 旧格式，HMAC-SHA256 签名。
	PrefixV1 = "OPSM1-"
	// prefixLegacy 历史前缀，语义等价于 PrefixV1。
	prefixLegacy = "WGC-"
	// prefixLegacyV0 本工程最早的前缀（更名前的初版），同样按 HMAC 校验。
	prefixLegacyV0 = "OPSM-"
)

// ErrNotSignedByUs 授权码不是本平台签发的。
var ErrNotSignedByUs = errors.New("授权码签名校验失败，可能不是本平台签发或已被篡改")

// PublicKeyString 把公钥编码成 base64 文本，用于打日志与核对。
func PublicKeyString(pub ed25519.PublicKey) string {
	return base64.StdEncoding.EncodeToString(pub)
}

// ParsePublicKey 解析 base64 公钥文本。允许带换行与空格，方便直接粘贴。
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		b.WriteString(line)
	}
	blob, err := base64.StdEncoding.DecodeString(b.String())
	if err != nil {
		return nil, errors.New("公钥不是合法的 base64")
	}
	if len(blob) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("公钥长度不正确（得到 %d 字节，应为 %d）", len(blob), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(blob), nil
}

// Fingerprint 返回公钥指纹，形如 A1B2-C3D4-E5F6-7890。
// 启动日志里打它，与授权中心启动日志对一眼就知道公钥有没有配错。
func Fingerprint(pub ed25519.PublicKey) string {
	if len(pub) != ed25519.PublicKeySize {
		return ""
	}
	sum := sha256.Sum256(pub)
	hexs := fmt.Sprintf("%x", sum[:8])
	parts := make([]string, 0, 4)
	for i := 0; i+4 <= len(hexs); i += 4 {
		parts = append(parts, hexs[i:i+4])
	}
	return strings.ToUpper(strings.Join(parts, "-"))
}

// Verifier 是校验方持有的校验材料：新版用公钥，旧版用对称密钥。
type Verifier struct {
	pub    ed25519.PublicKey
	legacy []string
}

// NewVerifier 构造校验器。pub 为 Ed25519 公钥（必填，否则拒绝新格式授权码）；
// legacySecret 为旧版 HMAC 密钥，可传多个（更名前后各一个默认值），
// 全部为空表示不再接受旧格式授权码。
func NewVerifier(pub ed25519.PublicKey, legacySecret ...string) Verifier {
	secrets := make([]string, 0, len(legacySecret))
	for _, s := range legacySecret {
		if s = strings.TrimSpace(s); s != "" {
			secrets = append(secrets, s)
		}
	}
	return Verifier{pub: pub, legacy: secrets}
}

// Ready 判断校验器是否可用于校验新版授权码。
func (v Verifier) Ready() bool { return len(v.pub) == ed25519.PublicKeySize }

// PublicKey 返回内置公钥。
func (v Verifier) PublicKey() ed25519.PublicKey { return v.pub }

// Fingerprint 返回内置公钥指纹。
func (v Verifier) Fingerprint() string { return Fingerprint(v.pub) }

// Format 返回授权码的格式标识：ed25519 / hmac / unknown，供状态与日志展示。
func (v Verifier) Format(raw string) string {
	switch codePrefix(raw) {
	case PrefixV2, prefixLegacyV2:
		return "ed25519"
	case PrefixV1, prefixLegacy, prefixLegacyV0:
		return "hmac"
	default:
		return "unknown"
	}
}

// Verify 校验授权码并解出授权信息。按前缀分流到 Ed25519 或 HMAC。
func (v Verifier) Verify(raw string) (*License, error) {
	code := compact(raw)
	if code == "" {
		return nil, errors.New("授权码为空")
	}
	switch codePrefix(code) {
	case PrefixV2, prefixLegacyV2:
		return v.verifyEd25519(code)
	case PrefixV1, prefixLegacy, prefixLegacyV0:
		if len(v.legacy) == 0 {
			return nil, errors.New("这是旧格式授权码，本机未配置旧版密钥，请改用授权中心重新签发的授权码")
		}
		return v.verifyHMAC(code)
	default:
		return nil, errors.New("授权码格式不正确：前缀无法识别，应形如 " + PrefixV2 + "xxxx.xxxx")
	}
}

func (v Verifier) verifyEd25519(code string) (*License, error) {
	if !v.Ready() {
		return nil, errors.New("本机未内置授权验签公钥，无法校验新格式授权码")
	}
	body, sig, err := splitCode(code)
	if err != nil {
		return nil, err
	}
	blob, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return nil, errors.New("授权码签名无法解析")
	}
	if len(blob) != ed25519.SignatureSize || !ed25519.Verify(v.pub, []byte(body), blob) {
		return nil, ErrNotSignedByUs
	}
	return decodePayload(body)
}

func (v Verifier) verifyHMAC(code string) (*License, error) {
	body, sig, err := splitCode(code)
	if err != nil {
		return nil, err
	}
	// 逐个密钥试：更名前后各有一个默认密钥，存量码用旧的签，新码用新的签。
	for _, secret := range v.legacy {
		if hmac.Equal([]byte(hmacSign(body, secret)), []byte(sig)) {
			return decodePayload(body)
		}
	}
	return nil, ErrNotSignedByUs
}

// codePrefix 返回授权码前缀；无法识别时返回空串。
func codePrefix(code string) string {
	code = compact(code)
	for _, p := range []string{PrefixV2, prefixLegacyV2, PrefixV1, prefixLegacy, prefixLegacyV0} {
		if strings.HasPrefix(code, p) {
			return p
		}
	}
	return ""
}

// splitCode 去掉前缀后按「最后一个点号」切成载荷与签名两段。
func splitCode(code string) (body, sig string, err error) {
	p := codePrefix(code)
	if p == "" {
		return "", "", errors.New("授权码格式不正确：前缀无法识别")
	}
	rest := code[len(p):]
	i := strings.LastIndex(rest, ".")
	if i <= 0 || i == len(rest)-1 {
		return "", "", errors.New("授权码格式不正确：缺少点号分隔的签名")
	}
	return rest[:i], rest[i+1:], nil
}

func decodePayload(body string) (*License, error) {
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return nil, errors.New("授权码内容无法解析")
	}
	var l License
	if err := json.Unmarshal(payload, &l); err != nil {
		return nil, errors.New("授权码内容无法解析")
	}
	return &l, nil
}

// compact 去掉授权码里的全部空白字符，兼容从网页复制时带入的换行与缩进。
// 空白字符不参与签名（载荷是 base64url，本身不含空白），去掉不影响校验结果。
func compact(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
}

// hmacSign 旧格式签名：HMAC-SHA256(secret, body) 的 base64url。只用于校验旧码。
func hmacSign(body, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
