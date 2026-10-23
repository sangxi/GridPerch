package config

// 授权验签公钥（Ed25519）—— 可以有多把。
//
// 这里放的是「授权中心授权签名私钥」对应的公钥，由授权中心生成：
//
//	./wgc-hub -print-license-pubkey -license-key-file data/license-key.pem
//	# 或后台「授权管理」页顶部复制
//
// 公钥是**可以公开**的：它只能验证签名，不能生成签名。就算有人把本程序反编译，
// 也只能拿到这一串公钥 —— 伪造不出任何授权码。
//
// 换密钥需要改这里并**重新编译**：公钥是编译进二进制的，不是配置文件，
// 所以运行时无法被替换成别人的公钥。
//
// ⚠️ 换密钥时**必须把旧公钥挪进 LicenseLegacyPublicKeys**，不能直接覆盖：
// 那批用旧私钥签发的存量授权码否则会一夜之间全部验签失败（客户手上还有效的码
// 突然用不了）。验签器是逐把试的，任意一把通过即认，多留一把没有副作用。

// LicensePublicKey 现行验签公钥（授权中心当前签发所用私钥对应的那把）。
// 取授权中心后台「授权管理」页顶部显示的公钥。
const LicensePublicKey = "FRrqtqc2rshOGQDw63ctoh0NiWLSoGKa7E2wUYrgeuo="

// LicenseKeyFingerprint 是上面那把公钥的指纹，形如 A9CB-C5B2-C14D-B5D9。
// 它只用来在启动时自检「公钥有没有抄错」——填错会在启动日志里直接报出来，
// 而不是等到客户导入授权码时才一脸茫然。留空则跳过这项检查。
const LicenseKeyFingerprint = "A9CB-C5B2-C14D-B5D9"

// LicenseLegacyPublicKeys 历史公钥，继续接受，避免存量授权码失效。
// 换密钥时把上一把挪进来即可，不要直接删。
var LicenseLegacyPublicKeys = []string{
	// 更名前的授权中心（wgc-hub @ host-10-0-0-94）用的公钥，指纹 278D-4852-7191-E7CE
	"uZhJVTm2UXxTuOmL3RXvbcokxdU+AIpxqC8PU799I2U=",
}

// LicensePublicKeys 返回全部验签公钥，现行在前。启动时据此构造验签器。
func LicensePublicKeys() []string {
	out := make([]string, 0, 1+len(LicenseLegacyPublicKeys))
	if LicensePublicKey != "" {
		out = append(out, LicensePublicKey)
	}
	return append(out, LicenseLegacyPublicKeys...)
}
