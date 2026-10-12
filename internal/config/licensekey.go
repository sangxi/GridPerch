package config

// 授权验签公钥（Ed25519）。
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
// 当前值取自授权中心（host-10-0-0-94:/www/wwwroot/wgc-hub/wgc-hub）的
// `-print-license-pubkey -license-key-file data/license-key.pem` 输出。
const LicensePublicKey = "uZhJVTm2UXxTuOmL3RXvbcokxdU+AIpxqC8PU799I2U="

// LicenseKeyFingerprint 是上面那把公钥的指纹，形如 FD3F-93C6-3C9B-CF70。
// 它只用来在启动时自检「公钥有没有抄错」——填错会在启动日志里直接报出来，
// 而不是等到客户导入授权码时才一脸茫然。留空则跳过这项检查。
const LicenseKeyFingerprint = "278D-4852-7191-E7CE"
