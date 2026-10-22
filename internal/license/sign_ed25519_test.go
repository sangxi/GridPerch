package license

import (
	"strings"
	"testing"
)

const (
	vectorPub  = "4nUyt6dD+KTXifig8l7CBEZV6FtLJ7PlNS5Ex4nRi+s="
	vectorFP   = "FD3F-93C6-3C9B-CF70"
	vectorCode = "GRIDPERCH2-eyJrZXkiOiJPUFNNLTIwMjYwMTAxLVRFU1QiLCJjdXN0b21lciI6IuekuuS-i-enkeaKgOaciemZkOWFrOWPuCIsImVkaXRpb24iOiJwcm8iLCJtYXhIb3N0cyI6MjAwLCJtYXhVc2VycyI6MzAsImZlYXR1cmVzIjpbInNjcmVlbnMiLCJvcGVuYXBpIl0sImlzc3VlZEF0IjoiMjAyNi0wMS0wMSIsIm5vdEFmdGVyIjoiMjA5OS0xMi0zMSJ9.3n07f7Qtjes-Mek7CvV2g1eDp907Kb2C044lGa2pN7--Cwy_FoMrg4vFHnuQBi7fZji0LKth1Pb4wnBjLgyyCw"
)

func vectorVerifier(t *testing.T) Verifier {
	t.Helper()
	pub, err := ParsePublicKey(vectorPub)
	if err != nil {
		t.Fatalf("解析测试向量公钥失败：%v", err)
	}
	return NewVerifier(pub, "legacy-secret")
}

func TestVectorVerify(t *testing.T) {
	v := vectorVerifier(t)
	if got := Fingerprint(v.PublicKey()); got != vectorFP {
		t.Fatalf("公钥指纹不一致：%s", got)
	}
	l, err := v.Verify(vectorCode)
	if err != nil {
		t.Fatalf("固化向量应校验通过：%v", err)
	}
	if l.Customer != "示例科技有限公司" || l.Edition != "pro" || l.MaxHosts != 200 {
		t.Fatalf("载荷还原不一致：%+v", l)
	}
	if v.Format(vectorCode) != "ed25519" {
		t.Fatalf("应识别为 ed25519")
	}
}

func TestVectorTamperRejected(t *testing.T) {
	v := vectorVerifier(t)
	i := strings.LastIndex(vectorCode, ".")
	body, sig := vectorCode[:i], vectorCode[i+1:]
	bad := body[:20] + string(vectorFlip(body[20])) + body[21:] + "." + sig
	if _, err := v.Verify(bad); err == nil {
		t.Fatal("篡改后的授权码应被拒绝")
	}
	// 换一把公钥也必须拒绝
	other, _ := ParsePublicKey("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if _, err := NewVerifier(other, "").Verify(vectorCode); err == nil {
		t.Fatal("错误公钥应验签失败")
	}
}

func vectorFlip(c byte) byte {
	if c == 'A' {
		return 'B'
	}
	return 'A'
}

func TestOldFormatStillReadable(t *testing.T) {
	v := vectorVerifier(t)
	// 旧格式：OPSM1-/WGC- 前缀仍按 HMAC 校验
	body := "eyJrZXkiOiJXRy0yMDI2MDEwMS1MRUdBQ1kifQ"
	code := PrefixV1 + body + "." + hmacSign(body, "legacy-secret")
	if _, err := v.Verify(code); err != nil {
		t.Fatalf("旧格式授权码应仍可校验：%v", err)
	}
	if _, err := v.Verify(prefixLegacy + body + "." + hmacSign(body, "legacy-secret")); err != nil {
		t.Fatalf("历史 WGC- 前缀应仍可校验：%v", err)
	}
	// 本工程初版前缀 OPSM-（重命名前的存量码）同样按 HMAC 校验
	old := prefixLegacyV0 + body + "." + hmacSign(body, "legacy-secret")
	if _, err := v.Verify(old); err != nil {
		t.Fatalf("历史 OPSM- 前缀应仍可校验：%v", err)
	}
	if v.Format(old) != "hmac" {
		t.Fatalf("OPSM- 应识别为 hmac")
	}
	// 没配旧密钥时必须明确拒绝，而不是静默通过
	only := NewVerifier(v.PublicKey(), "")
	if _, err := only.Verify(code); err == nil {
		t.Fatal("未配置旧版密钥时不应接受旧格式授权码")
	}
}

// TestRenamedPrefixStillReadable 品牌更名后签发前缀改为 GRIDPERCH2-，
// 但客户手里已经签发出去的 OPSM2- 授权码必须继续有效，不能要求换码。
func TestRenamedPrefixStillReadable(t *testing.T) {
	v := vectorVerifier(t)
	legacy := prefixLegacyV2 + strings.TrimPrefix(vectorCode, PrefixV2)
	l, err := v.Verify(legacy)
	if err != nil {
		t.Fatalf("更名前签发的 OPSM2- 授权码应继续有效：%v", err)
	}
	if l.Edition != "pro" || l.MaxHosts != 200 {
		t.Fatalf("载荷还原不一致：%+v", l)
	}
	if v.Format(legacy) != "ed25519" {
		t.Fatal("OPSM2- 应识别为 ed25519")
	}
}

// TestLegacyHmacSecretAccepted 更名前后各自有一个 HMAC 默认密钥，
// 用旧密钥签发的存量授权码，新校验器必须仍能验通。
func TestLegacyHmacSecretAccepted(t *testing.T) {
	pub, err := ParsePublicKey(vectorPub)
	if err != nil {
		t.Fatalf("解析测试向量公钥失败：%v", err)
	}
	v := NewVerifier(pub, "new-secret", "old-secret")
	body := "eyJrZXkiOiJXRy0yMDI2MDEwMS1MRUdBQ1kifQ"
	for _, secret := range []string{"old-secret", "new-secret"} {
		code := PrefixV1 + body + "." + hmacSign(body, secret)
		if _, err := v.Verify(code); err != nil {
			t.Fatalf("使用 %s 签发的授权码应可校验：%v", secret, err)
		}
	}
	if _, err := v.Verify(PrefixV1 + body + "." + hmacSign(body, "other-secret")); err == nil {
		t.Fatal("未知密钥签发的授权码应被拒绝")
	}
}
