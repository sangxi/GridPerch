package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

/*
网页终端：浏览器 ⇄ 平台 ⇄ SSH 目标机。

没有引入 xterm.js 之类的终端仿真库：
  - 前端资源全部编译进二进制，现场经常没有外网，不能依赖 CDN；
  - 这里只需要一个能跑命令、看得见输出的交互界面，不需要完整的
    VT100 屏幕缓冲、颜色与鼠标事件。前端按 \r、\n、\b 做行级渲染，
    其余 ANSI 控制序列直接丢弃。

数据传输用「分段文本流」而不是 WebSocket：
  open    POST /api/device-accounts/{id}/ssh/open    → {sid}
  stream  GET  .../ssh/stream?sid=xxx                → text/plain 持续输出
  input   POST .../ssh/input  {sid,data}             → 写入 stdin
  resize  POST .../ssh/resize {sid,cols,rows}
  close   DELETE .../ssh/session?sid=xxx
HTTP/1.1 分块传输足够用，也省掉一套 WebSocket 握手与心跳。
*/

// sshTermIdle 无操作多久后回收会话，避免关掉浏览器后连接一直挂着。
const sshTermIdle = 30 * time.Minute

// sshTermSession 一个 SSH 会话：输出缓冲 + 阻塞读取，供 HTTP 流消费。
type sshTermSession struct {
	id     string
	host   string
	client *ssh.Client
	sess   *ssh.Session
	stdin  io.WriteCloser

	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte
	closed bool
	err    error
	last   time.Time
}

// Read 阻塞直到有输出或会话关闭，供流式响应循环调用。
func (t *sshTermSession) Read(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for len(t.buf) == 0 && !t.closed {
		t.cond.Wait()
	}
	if len(t.buf) == 0 {
		if t.err != nil {
			return 0, t.err
		}
		return 0, io.EOF
	}
	n := copy(p, t.buf)
	t.buf = t.buf[n:]
	if len(t.buf) == 0 {
		t.buf = nil
	}
	t.last = time.Now()
	return n, nil
}

// feed 把 SSH 端输出塞进缓冲区并唤醒等待者。
func (t *sshTermSession) feed(p []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	// 客户端卡住不看时不能无限涨：只保留最近 2MB
	if len(t.buf) > 4<<20 {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-2<<20:]...)
	}
	t.buf = append(t.buf, p...)
	t.cond.Broadcast()
}

// close 关闭会话，重复调用安全。
func (t *sshTermSession) close(err error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	if err != nil && !errors.Is(err, io.EOF) {
		t.err = err
	}
	t.cond.Broadcast()
	t.mu.Unlock()
	if t.stdin != nil {
		_ = t.stdin.Close()
	}
	if t.sess != nil {
		_ = t.sess.Close()
	}
	if t.client != nil {
		_ = t.client.Close()
	}
}

func (t *sshTermSession) idle() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return time.Since(t.last) > sshTermIdle
}

func (s *Server) termPut(t *sshTermSession) {
	s.termMu.Lock()
	defer s.termMu.Unlock()
	if s.termSess == nil {
		s.termSess = map[string]*sshTermSession{}
	}
	s.termSess[t.id] = t
}

func (s *Server) termGet(id string) *sshTermSession {
	s.termMu.Lock()
	defer s.termMu.Unlock()
	return s.termSess[id]
}

func (s *Server) termDrop(id string) {
	s.termMu.Lock()
	t := s.termSess[id]
	delete(s.termSess, id)
	s.termMu.Unlock()
	if t != nil {
		t.close(nil)
	}
}

// sweepTerms 顺手清理闲置会话。
func (s *Server) sweepTerms() {
	s.termMu.Lock()
	ids := make([]string, 0, len(s.termSess))
	for id, t := range s.termSess {
		if t.idle() {
			ids = append(ids, id)
		}
	}
	s.termMu.Unlock()
	for _, id := range ids {
		s.termDrop(id)
	}
}

// newTermID 生成不可猜测的会话号。
func newTermID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b)
}

// handleSSHOpen 建立到目标设备的 SSH 连接并申请一个交互式终端。
func (s *Server) handleSSHOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "msg": "不支持的方法"})
		return
	}
	var req struct {
		Cols int `json:"cols"`
		Rows int `json:"rows"`
		// Password 是「当前登录账号」的口令，只用于二次确认：
		// 拿别人的登录态开 shell 是最容易被忽略的越权，必须再验一次人。
		Password string `json:"password"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req)
	if !s.confirmCurrentUser(r, req.Password) {
		writeJSONStatus(w, http.StatusForbidden, map[string]any{"ok": false, "msg": "当前账号密码不正确，无法打开终端"})
		return
	}
	if req.Cols <= 0 || req.Cols > 400 {
		req.Cols = 100
	}
	if req.Rows <= 0 || req.Rows > 200 {
		req.Rows = 30
	}
	acc, err := s.store.DeviceCredential(r.PathValue("id"))
	if err != nil {
		writeJSONStatus(w, http.StatusNotFound, map[string]any{"ok": false, "msg": "设备账号不存在"})
		return
	}
	if strings.ToLower(acc.Protocol) != "ssh" && strings.ToLower(acc.Protocol) != "sftp" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "该账号协议是「" + acc.Protocol + "」，网页终端只支持 SSH"})
		return
	}
	if acc.Host == "" || acc.Username == "" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "该账号缺少地址或用户名"})
		return
	}
	if acc.Secret == "" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "该账号没有保存口令，网页终端暂不支持密钥登录"})
		return
	}

	s.sweepTerms()

	cfg := &ssh.ClientConfig{
		User: acc.Username,
		Auth: []ssh.AuthMethod{ssh.Password(acc.Secret)},
		// 内网运维工具的直连终端：信任首次见到的主机密钥。
		// 需要严格校验时应改为读取 known_hosts，而不是在网页里弹指纹确认。
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         12 * time.Second,
	}
	addr := net.JoinHostPort(acc.Host, strconv.Itoa(acc.Port))
	conn, err := net.DialTimeout("tcp", addr, 12*time.Second)
	if err != nil {
		writeJSONStatus(w, http.StatusBadGateway, map[string]any{"ok": false, "msg": "连接 " + addr + " 失败：" + err.Error()})
		return
	}
	cc, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		_ = conn.Close()
		writeJSONStatus(w, http.StatusBadGateway, map[string]any{"ok": false, "msg": "SSH 认证失败：" + err.Error()})
		return
	}
	client := ssh.NewClient(cc, chans, reqs)

	sess, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		writeJSONStatus(w, http.StatusBadGateway, map[string]any{"ok": false, "msg": "创建会话失败：" + err.Error()})
		return
	}
	modes := ssh.TerminalModes{
		ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400,
	}
	if err := sess.RequestPty("xterm-256color", req.Rows, req.Cols, modes); err != nil {
		_ = sess.Close()
		_ = client.Close()
		writeJSONStatus(w, http.StatusBadGateway, map[string]any{"ok": false, "msg": "终端初始化失败：" + err.Error()})
		return
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		_ = sess.Close()
		_ = client.Close()
		writeJSONStatus(w, http.StatusBadGateway, map[string]any{"ok": false, "msg": "终端初始化失败：" + err.Error()})
		return
	}
	t := &sshTermSession{id: newTermID(), host: addr, client: client, sess: sess, stdin: stdin, last: time.Now()}
	t.cond = sync.NewCond(&t.mu)
	stdout, _ := sess.StdoutPipe()
	stderr, _ := sess.StderrPipe()
	if stdout != nil {
		go func() {
			buf := make([]byte, 8192)
			for {
				n, err := stdout.Read(buf)
				if n > 0 {
					t.feed(buf[:n])
				}
				if err != nil {
					return
				}
			}
		}()
	}
	if stderr != nil {
		go func() {
			buf := make([]byte, 4096)
			for {
				n, err := stderr.Read(buf)
				if n > 0 {
					t.feed(buf[:n])
				}
				if err != nil {
					return
				}
			}
		}()
	}
	if err := sess.Shell(); err != nil {
		t.close(err)
		writeJSONStatus(w, http.StatusBadGateway, map[string]any{"ok": false, "msg": "启动 shell 失败：" + err.Error()})
		return
	}
	go func() {
		_ = sess.Wait()
		t.close(nil)
	}()
	s.termPut(t)
	writeJSON(w, map[string]any{"ok": true, "sid": t.id, "target": addr, "user": acc.Username})
}

// handleSSHStream 把终端输出持续写给浏览器。断开后立即回收会话，避免悬挂连接。
func (s *Server) handleSSHStream(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("sid")
	t := s.termGet(sid)
	if t == nil {
		writeJSONStatus(w, http.StatusNotFound, map[string]any{"ok": false, "msg": "会话不存在或已结束"})
		return
	}
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no") // nginx 反代时别攒包
	w.WriteHeader(http.StatusOK)
	if flusher != nil {
		flusher.Flush()
	}
	buf := make([]byte, 8192)
	for {
		select {
		case <-r.Context().Done():
			// 浏览器关页面/刷新：直接回收，不让 SSH 连接空转
			s.termDrop(sid)
			return
		default:
		}
		n, err := t.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				s.termDrop(sid)
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			s.termDrop(sid)
			return
		}
	}
}

// handleSSHInput 把按键写入目标机 stdin。
func (s *Server) handleSSHInput(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "msg": "不支持的方法"})
		return
	}
	var req struct {
		SID  string `json:"sid"`
		Data string `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "参数解析失败"})
		return
	}
	t := s.termGet(req.SID)
	if t == nil {
		writeJSONStatus(w, http.StatusNotFound, map[string]any{"ok": false, "msg": "会话不存在或已结束"})
		return
	}
	if req.Data == "" {
		writeJSON(w, map[string]any{"ok": true})
		return
	}
	if _, err := t.stdin.Write([]byte(req.Data)); err != nil {
		s.termDrop(req.SID)
		writeJSONStatus(w, http.StatusGone, map[string]any{"ok": false, "msg": "会话已关闭：" + err.Error()})
		return
	}
	t.mu.Lock()
	t.last = time.Now()
	t.mu.Unlock()
	writeJSON(w, map[string]any{"ok": true})
}

// handleSSHResize 同步浏览器窗口尺寸，让 vim/top 这类全屏程序排版正确。
func (s *Server) handleSSHResize(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SID  string `json:"sid"`
		Cols int    `json:"cols"`
		Rows int    `json:"rows"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "参数解析失败"})
		return
	}
	t := s.termGet(req.SID)
	if t == nil {
		writeJSONStatus(w, http.StatusNotFound, map[string]any{"ok": false, "msg": "会话不存在或已结束"})
		return
	}
	if req.Cols > 0 && req.Rows > 0 {
		if err := t.sess.WindowChange(req.Rows, req.Cols); err != nil {
			writeJSONStatus(w, http.StatusBadGateway, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleSSHClose 主动关闭会话（用户点「断开」）。
func (s *Server) handleSSHClose(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("sid")
	s.termDrop(sid)
	writeJSON(w, map[string]any{"ok": true})
}

// rdpFile 生成 Windows 远程桌面连接文件内容。
//
// 浏览器不能直接发起 RDP，通行做法是给一个 .rdp 文件：双击由 mstsc 打开，
// 地址、端口、用户名都带上，口令仍然由 Windows 自己询问，不落盘。
func rdpFile(host string, port int, username string) string {
	if port <= 0 {
		port = 3389
	}
	var sb strings.Builder
	sb.WriteString("screen mode id:i:2\r\n")
	sb.WriteString("use multimon:i:0\r\n")
	sb.WriteString("desktopwidth:i:1920\r\n")
	sb.WriteString("desktopheight:i:1080\r\n")
	sb.WriteString("session bpp:i:32\r\n")
	sb.WriteString("full address:s:" + net.JoinHostPort(host, strconv.Itoa(port)) + "\r\n")
	if username != "" {
		sb.WriteString("username:s:" + username + "\r\n")
	}
	sb.WriteString("prompt for credentials:i:1\r\n")
	sb.WriteString("authentication level:i:2\r\n")
	sb.WriteString("enablecredsspsupport:i:1\r\n")
	sb.WriteString("redirectclipboard:i:1\r\n")
	sb.WriteString("audiomode:i:0\r\n")
	sb.WriteString("autoreconnection enabled:i:1\r\n")
	sb.WriteString("bandwidthautodetect:i:1\r\n")
	sb.WriteString("networkautodetect:i:1\r\n")
	sb.WriteString("connection type:i:7\r\n")
	return sb.String()
}

// handleDeviceRDP 下载 .rdp 连接文件。
func (s *Server) handleDeviceRDP(w http.ResponseWriter, r *http.Request) {
	acc, err := s.store.DeviceCredential(r.PathValue("id"))
	if err != nil {
		writeJSONStatus(w, http.StatusNotFound, map[string]any{"ok": false, "msg": "设备账号不存在"})
		return
	}
	if acc.Host == "" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "该账号缺少地址"})
		return
	}
	// 端口按「这条账号是不是远程桌面账号」来取，不能直接照搬账号里的端口：
	// 同一个 Windows 主机往往既有 SSH 账号（22）又有 RDP，照搬会把 .rdp
	// 写成 IP:22，mstsc 连过去必然失败（截图里就是这个现象）。
	port := 3389
	if strings.EqualFold(acc.Protocol, "rdp") && acc.Port > 0 {
		port = acc.Port
	}
	body := rdpFile(acc.Host, port, acc.Username)
	name := strings.TrimSpace(acc.Name)
	if name == "" {
		name = acc.Host
	}
	name = strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_").Replace(name)
	w.Header().Set("Content-Type", "application/x-rdp; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q; filename*=UTF-8''%s",
		"connection.rdp", percentEncode(name+".rdp")))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(body))
}

// percentEncode 生成 RFC 5987 文件名编码（空格必须是 %20，不能是 +）。
func percentEncode(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}
