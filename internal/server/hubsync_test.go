package server

import (
	"bytes"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/sangxi/gridperch/internal/config"
)

// newHubResp 构造一个带请求上下文的响应，便于断言 describeHubStatus 的分类结果。
func newHubResp(status int, ct, server string, body string) *http.Response {
	u, _ := url.Parse("https://hub.example.com/api/v1/check?token=secret&machine=abc")
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{ct}, "Server": []string{server}},
		Body:       http.NoBody,
		Request:    &http.Request{Method: http.MethodGet, URL: u},
		Status:     http.StatusText(status),
	}
}

// TestDescribeHubStatusGateway 502/503/504 必须归因到「网关连不上上游」，
// 而不是旧实现里那句「-hub 指向的可能不是接口地址」——
// 反代的错误页也是 text/html，按内容类型归因会把人引到完全错误的方向。
func TestDescribeHubStatusGateway(t *testing.T) {
	st := &Server{cfg: &config.Server{HubURL: "https://hub.example.com", HubToken: "secret"}}
	body := `<html><head><title>502 Bad Gateway</title></head><body><center><h1>502 Bad Gateway</h1></center><hr><center>nginx</center></body></html>`
	for _, code := range []int{502, 503, 504} {
		err := st.describeHubStatus(newHubResp(code, "text/html", "nginx", body), []byte(body))
		msg := err.Error()
		if !strings.Contains(msg, "网关错误") {
			t.Errorf("HTTP %d 应判定为网关错误，实际：%s", code, msg)
		}
		if !strings.Contains(msg, "不是 -hub 地址写错") {
			t.Errorf("HTTP %d 应明确排除地址写错，实际：%s", code, msg)
		}
		if !strings.Contains(msg, "502 Bad Gateway") {
			t.Errorf("HTTP %d 应带出页面标题便于定位，实际：%s", code, msg)
		}
	}
}

// TestDescribeHubStatusHTML 非网关状态码 + 网页，才归因为「落到了静态站点」。
func TestDescribeHubStatusHTML(t *testing.T) {
	st := &Server{cfg: &config.Server{HubURL: "https://hub.example.com", HubToken: "secret"}}
	body := `<html><head><title>某某公司官网</title></head><body>x</body></html>`
	err := st.describeHubStatus(newHubResp(200, "text/html; charset=utf-8", "nginx", body), []byte(body))
	msg := err.Error()
	if !strings.Contains(msg, "静态站点") {
		t.Errorf("应判定为命中了站点，实际：%s", msg)
	}
	if !strings.Contains(msg, "某某公司官网") {
		t.Errorf("应带出页面标题，实际：%s", msg)
	}
}

// TestDescribeHubStatus404 网页型 404 与 JSON 型 404 要给不同建议：
// 前者是路径不对，后者是中心没有这个接口。
func TestDescribeHubStatus404(t *testing.T) {
	st := &Server{cfg: &config.Server{HubURL: "https://hub.example.com", HubToken: "secret"}}

	htmlBody := `<html><head><title>404 Not Found</title></head></html>`
	msg := st.describeHubStatus(newHubResp(404, "text/html", "nginx", htmlBody), []byte(htmlBody)).Error()
	if !strings.Contains(msg, "子路径") {
		t.Errorf("网页型 404 应提示子路径问题，实际：%s", msg)
	}

	jsonBody := `{"ok":false,"msg":"not found"}`
	msg = st.describeHubStatus(newHubResp(404, "application/json", "", jsonBody), []byte(jsonBody)).Error()
	if !strings.Contains(msg, "不存在") {
		t.Errorf("JSON 型 404 应提示接口不存在，实际：%s", msg)
	}
}

// TestDescribeHubStatusRedactsToken 错误信息会写日志、也会回显到控制台，
// 里面的 token 查询参数必须被抹掉。
func TestDescribeHubStatusRedactsToken(t *testing.T) {
	st := &Server{cfg: &config.Server{HubURL: "https://hub.example.com", HubToken: "secret"}}
	body := `<html><head><title>x</title></head></html>`
	msg := st.describeHubStatus(newHubResp(502, "text/html", "nginx", body), []byte(body)).Error()
	if strings.Contains(msg, "secret") {
		t.Fatalf("错误信息泄露了令牌：%s", msg)
	}
}

// TestHTMLTitle 标题兜底：抠不到就返回空串，不能 panic。
func TestHTMLTitle(t *testing.T) {
	if got := htmlTitle([]byte("<html><head><title>  A \n B </title></head></html>")); got != "A B" {
		t.Errorf("标题应折叠空白，得到 %q", got)
	}
	if got := htmlTitle([]byte("plain text")); got != "" {
		t.Errorf("非 HTML 应返回空串，得到 %q", got)
	}
	if got := htmlTitle([]byte("<html><title></title></html>")); got != "" {
		t.Errorf("空标题应返回空串，得到 %q", got)
	}
	if got := htmlTitle(bytes.Repeat([]byte("x"), 100<<10)); got != "" {
		t.Errorf("超长内容应返回空串，得到 %q", got)
	}
}
