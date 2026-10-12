package config

// 授权中心的内置拉取令牌，以混淆形式存放，不以明文出现在源码与二进制里。
//
// 存放方式：把令牌字节与一段固定掩码逐字节异或，再循环左移 3 位，
// 最后按小端打包成 uint32 数组；掩码单独放在另一个变量里。
// 这样用 strings 之类的工具扫描可执行文件不会直接捞出令牌。
//
// 这属于「混淆」而非「加密」。
// 客户端必须把明文令牌发给授权中心做比对，因此令牌在运行时一定能被还原出来
// —— 只要拿到二进制并肯花时间调试，就能取到。它的作用是把随手逆向的成本从
// 「strings 一下」提高到「需要读懂还原逻辑并下断点」。
//
// 若确实要让拿到二进制的人取不到凭证，需要在授权中心侧改造：
// 客户端只保存公钥，用签名挑战的方式证明身份，而不是共享一个对称令牌。
var hubTokenBlob = [...]uint32{
	0xA1E4939E, 0xAA29D403, 0xC2FFE005, 0x6AC559AE, 0xC195D3E6, 0x09B02FCB,
	0x831F4945, 0x6A3439B6, 0x8BAEBBDE,
}

// hubTokenMask 参与还原的掩码。与 hubTokenBlob 分开声明，避免两者
// 相邻出现在同一个数据段里，被一次性 dump 出来。
var hubTokenMask = [...]byte{
	0x9E, 0x3B, 0xD5, 0x71, 0x2C, 0xA8, 0x4F, 0x16,
	0xE3, 0x5D, 0x87, 0x39, 0xB2, 0x6A, 0xCF, 0x04,
}

const hubTokenRot = 3

// defaultHubToken 还原出内置的授权中心拉取令牌。
// 每一步都做边界判断，数据被改动时返回空串而不是 panic。
func defaultHubToken() string {
	if len(hubTokenBlob) == 0 || len(hubTokenMask) == 0 {
		return ""
	}
	out := make([]byte, 0, len(hubTokenBlob)*4)
	var tmp [4]byte
	for i, w := range hubTokenBlob {
		tmp[0] = byte(w)
		tmp[1] = byte(w >> 8)
		tmp[2] = byte(w >> 16)
		tmp[3] = byte(w >> 24)
		for j := 0; j < 4; j++ {
			k := i*4 + j
			v := tmp[j]
			// 循环右移，与生成时的左移对称
			v = (v >> hubTokenRot) | (v << (8 - hubTokenRot))
			out = append(out, v^hubTokenMask[k%len(hubTokenMask)])
		}
	}
	// 去掉补齐用的 0 字节
	for len(out) > 0 && out[len(out)-1] == 0 {
		out = out[:len(out)-1]
	}
	return string(out)
}

// UsesBuiltinHubToken 报告当前生效的令牌是否就是内置令牌。
//
// 控制台只展示令牌「来源」（内置 / 自定义 / 未设置），从不回显令牌内容本身，
// 避免把凭证通过接口响应或页面源码交出去。
func (c *Server) UsesBuiltinHubToken() bool {
	if c == nil || c.HubToken == "" {
		return false
	}
	return c.HubToken == defaultHubToken()
}
