// 生成内置授权令牌的混淆存储。
// 用法: node gen-hubtoken.js "<明文令牌>"
// 输出: internal/config/hubtoken.go
const fs = require('fs');
const path = require('path');

const token = process.argv[2];
if (!token) { console.error('缺参数: 明文令牌'); process.exit(1); }

// 固定掩码（16 字节）。与编码数据分开存放，避免同段落出现。
const mask = [0x9E, 0x3B, 0xD5, 0x71, 0x2C, 0xA8, 0x4F, 0x16, 0xE3, 0x5D, 0x87, 0x39, 0xB2, 0x6A, 0xCF, 0x04];

const ROT = 3; // 每字节循环左移位数

function rotl8(v, n) { return ((v << n) | (v >>> (8 - n))) & 0xFF; }
function rotr8(v, n) { return ((v >>> n) | (v << (8 - n))) & 0xFF; }

const plain = Buffer.from(token, 'utf8');

// 补齐到 4 字节整数倍
const padLen = (4 - (plain.length % 4)) % 4;
const padded = Buffer.concat([plain, Buffer.alloc(padLen, 0)]);

const enc = Buffer.alloc(padded.length);
for (let i = 0; i < padded.length; i++) {
  enc[i] = rotl8(padded[i] ^ mask[i % mask.length], ROT);
}

// 打包成 uint32 小端
const words = [];
for (let i = 0; i < enc.length; i += 4) {
  words.push(enc.readUInt32LE(i));
}

// 自校验：反解必须还原出原文
const back = [];
for (const w of words) {
  const b = Buffer.alloc(4);
  b.writeUInt32LE(w >>> 0, 0);
  for (let j = 0; j < 4; j++) back.push(rotr8(b[j], ROT) ^ mask[(back.length) % mask.length]);
}
const decoded = Buffer.from(back).toString('utf8').replace(/\0+$/, '');
if (decoded !== token) { console.error('自校验失败: 还原出 "' + decoded + '"'); process.exit(1); }
console.log('自校验通过，还原长度', decoded.length, '（原文', token.length, '）');

const hex = (n) => '0x' + n.toString(16).toUpperCase().padStart(8, '0');
const wordLines = [];
for (let i = 0; i < words.length; i += 6) {
  wordLines.push('\t' + words.slice(i, i + 6).map(hex).join(', ') + ',');
}
const maskLines = [];
for (let i = 0; i < mask.length; i += 8) {
  maskLines.push('\t' + mask.slice(i, i + 8).map(b => '0x' + b.toString(16).toUpperCase().padStart(2, '0')).join(', ') + ',');
}

const go = `package config

// 授权中心的内置拉取令牌，以混淆形式存放，不以明文出现在源码与二进制里。
//
// 存放方式：把令牌字节与一段固定掩码逐字节异或，再循环左移 ${ROT} 位，
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
${wordLines.join('\n')}
}

// hubTokenMask 参与还原的掩码。与 hubTokenBlob 分开声明，避免两者
// 相邻出现在同一个数据段里，被一次性 dump 出来。
var hubTokenMask = [...]byte{
${maskLines.join('\n')}
}

const hubTokenRot = ${ROT}

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
`;

const target = path.join(process.cwd(), 'internal', 'config', 'hubtoken.go');
fs.writeFileSync(target, go, 'utf8');
console.log('已写入', target);
console.log('词数', words.length, '掩码字节', mask.length);
