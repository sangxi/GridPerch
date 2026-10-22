package probe

import (
	"bufio"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql" // 注册 mysql 驱动
)

//nolint:gochecknoinits // 集中注册数据库与中间件类探测
func init() {
	Register("mysql", probeMySQL)
	Register("redis", probeRedis)
	Register("kafka", probeKafka)
	Register("rabbitmq", probeRabbitMQ)
	Register("elasticsearch", probeElasticsearch)
	Register("k8s", probeKubernetes)
}

// probeMySQL 使用真实 MySQL 协议建立连接并执行一条 SQL，验证实例可用性。
func probeMySQL(ctx context.Context, t Target, timeout time.Duration) Result {
	user := t.Str("username", t.Str("user", "root"))
	password := t.Str("password", t.Str("secret", ""))
	host := t.Str("host", t.Str("ip", ""))
	if host == "" {
		return fail("", "缺少参数 host")
	}
	dbName := t.Str("db", t.Str("database", ""))
	addr := t.Addr(defaultMySQL)
	dsn := fmt.Sprintf("%s:%s@tcp(%s)/%s?timeout=%ds&readTimeout=%ds",
		user, password, addr, dbName, int(timeout.Seconds()), int(timeout.Seconds()))
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return fail("", fmt.Sprintf("初始化连接失败: %v", err))
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	start := time.Now()
	if err := db.PingContext(ctx); err != nil {
		return fail("", fmt.Sprintf("MySQL %s 连接或认证失败: %v", addr, err))
	}
	query := t.Str("sql", "SELECT VERSION()")
	var value string
	if err := db.QueryRowContext(ctx, query).Scan(&value); err != nil {
		return fail("", fmt.Sprintf("执行 %q 失败: %v", query, err))
	}
	cost := time.Since(start)
	return ok(fmt.Sprintf("%d", cost.Milliseconds()),
		fmt.Sprintf("MySQL %s 可用，%q = %s，用时 %dms", addr, query, truncate(value, 60), cost.Milliseconds()))
}

// redisReply 读取一条 RESP 协议响应。
func redisReply(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return "", io.EOF
	}
	switch line[0] {
	case '+', ':':
		return line[1:], nil
	case '-':
		return "", fmt.Errorf("%s", line[1:])
	case '$':
		n, err := strconv.Atoi(line[1:])
		if err != nil {
			return "", err
		}
		if n < 0 {
			return "", nil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", err
		}
		return string(buf[:n]), nil
	case '*':
		n, err := strconv.Atoi(line[1:])
		if err != nil {
			return "", err
		}
		var parts []string
		for i := 0; i < n; i++ {
			v, err := redisReply(r)
			if err != nil {
				return "", err
			}
			parts = append(parts, v)
		}
		return strings.Join(parts, " "), nil
	default:
		return "", fmt.Errorf("未知 RESP 响应: %s", line)
	}
}

// probeRedis 通过 RESP 协议检查 Redis 存活，并返回关键运行数据。
func probeRedis(ctx context.Context, t Target, timeout time.Duration) Result {
	addr := t.Addr(defaultRedis)
	d := &net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fail("", fmt.Sprintf("连接 Redis %s 失败: %v", addr, err))
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	r := bufio.NewReader(conn)
	start := time.Now()
	write := func(cmd ...string) error {
		if _, err := conn.Write([]byte(fmt.Sprintf("*%d\r\n", len(cmd)))); err != nil {
			return err
		}
		for _, c := range cmd {
			if _, err := conn.Write([]byte(fmt.Sprintf("$%d\r\n%s\r\n", len(c), c))); err != nil {
				return err
			}
		}
		return nil
	}
	password := t.Str("password", t.Str("secret", ""))
	if password != "" {
		if err := write("AUTH", password); err != nil {
			return fail("", err.Error())
		}
		if _, err := redisReply(r); err != nil {
			return fail("", "Redis 认证失败: "+err.Error())
		}
	}
	if err := write("PING"); err != nil {
		return fail("", err.Error())
	}
	pong, err := redisReply(r)
	if err != nil {
		return fail("", "PING 失败: "+err.Error())
	}
	if !strings.EqualFold(strings.TrimSpace(pong), "PONG") {
		return fail("", "PING 返回异常: "+pong)
	}
	if err := write("INFO", "server"); err == nil {
		info, err := redisReply(r)
		if err == nil {
			version := fieldOf(info, "redis_version")
			cost := time.Since(start)
			return ok(fmt.Sprintf("%d", cost.Milliseconds()),
				fmt.Sprintf("Redis %s 正常，版本 %s，响应 %dms", addr, version, cost.Milliseconds()))
		}
	}
	cost := time.Since(start)
	return ok(fmt.Sprintf("%d", cost.Milliseconds()), fmt.Sprintf("Redis %s 正常，响应 %dms", addr, cost.Milliseconds()))
}

// fieldOf 从 Redis INFO 文本中提取字段。
func fieldOf(info, key string) string {
	for _, line := range strings.Split(info, "\r\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, key+":") {
			return strings.TrimSpace(strings.TrimPrefix(line, key+":"))
		}
	}
	return "-"
}

// probeKafka 通过 ApiVersions 请求验证 Kafka Broker 可用。
func probeKafka(ctx context.Context, t Target, timeout time.Duration) Result {
	addr := t.Addr(defaultKafka)
	d := &net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fail("", fmt.Sprintf("连接 Kafka %s 失败: %v", addr, err))
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	clientID := "GridPerch"
	body := make([]byte, 0, 32)
	body = appendInt16(body, 18) // ApiKey: ApiVersions
	body = appendInt16(body, 0)  // ApiVersion
	body = appendInt32(body, 1)  // CorrelationId
	body = appendString(body, clientID)
	req := make([]byte, 4)
	binary.BigEndian.PutUint32(req, uint32(len(body)))
	req = append(req, body...)
	start := time.Now()
	if _, err := conn.Write(req); err != nil {
		return fail("", fmt.Sprintf("发送请求失败: %v", err))
	}
	sizeBuf := make([]byte, 4)
	if _, err := io.ReadFull(conn, sizeBuf); err != nil {
		return fail("", fmt.Sprintf("读取响应失败: %v", err))
	}
	size := int(binary.BigEndian.Uint32(sizeBuf))
	if size <= 0 || size > 1<<20 {
		return fail("", fmt.Sprintf("响应长度异常: %d", size))
	}
	resp := make([]byte, size)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return fail("", fmt.Sprintf("读取响应体失败: %v", err))
	}
	cost := time.Since(start)
	if len(resp) < 6 {
		return fail("", "响应体过短")
	}
	errCode := int16(binary.BigEndian.Uint16(resp[4:6]))
	if errCode != 0 {
		return fail("", fmt.Sprintf("Broker 返回错误码 %d", errCode))
	}
	count := 0
	if len(resp) >= 10 {
		count = int(binary.BigEndian.Uint32(resp[6:10]))
	}
	return ok(fmt.Sprintf("%d", cost.Milliseconds()),
		fmt.Sprintf("Kafka %s 正常，支持 %d 个 API 版本，响应 %dms", addr, count, cost.Milliseconds()))
}

func appendInt16(b []byte, v int16) []byte {
	return append(b, byte(v>>8), byte(v))
}

func appendInt32(b []byte, v int32) []byte {
	return append(b, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

func appendString(b []byte, s string) []byte {
	b = appendInt16(b, int16(len(s)))
	return append(b, []byte(s)...)
}

// probeRabbitMQ 调用 RabbitMQ Management HTTP API 检查节点与堆积消息数。
func probeRabbitMQ(ctx context.Context, t Target, timeout time.Duration) Result {
	addr := t.Addr(defaultRabbit)
	scheme := "http"
	if t.Str("https", "false") == "true" {
		scheme = "https"
	}
	url := fmt.Sprintf("%s://%s/api/overview", scheme, addr)
	var data map[string]any
	if err := httpJSON(ctx, url, t, timeout, &data); err != nil {
		return fail("", fmt.Sprintf("RabbitMQ Management API 访问失败: %v", err))
	}
	version, _ := data["rabbitmq_version"].(string)
	queued := "-"
	if v, ok := data["queue_totals"]; ok {
		if m, ok := v.(map[string]any); ok {
			if n, ok := m["messages"].(float64); ok {
				queued = strconv.Itoa(int(n))
			}
		}
	}
	return ok(queued, fmt.Sprintf("RabbitMQ %s 正常，版本 %s，当前队列消息 %s 条", addr, defaultStr(version, "-"), queued))
}

// probeElasticsearch 检查 Elasticsearch 集群健康状态。
func probeElasticsearch(ctx context.Context, t Target, timeout time.Duration) Result {
	addr := t.Addr(defaultES)
	scheme := "http"
	if t.Str("https", "false") == "true" {
		scheme = "https"
	}
	health := map[string]any{}
	if err := httpJSON(ctx, fmt.Sprintf("%s://%s/_cluster/health", scheme, addr), t, timeout, &health); err != nil {
		return fail("", fmt.Sprintf("Elasticsearch 集群健康接口访问失败: %v", err))
	}
	status, _ := health["status"].(string)
	nodes := "-"
	if n, ok := health["number_of_nodes"].(float64); ok {
		nodes = strconv.Itoa(int(n))
	}
	if status == "red" {
		return fail(status, fmt.Sprintf("Elasticsearch %s 集群状态 red，节点数 %s", addr, nodes))
	}
	return ok(status, fmt.Sprintf("Elasticsearch %s 状态 %s，节点数 %s", addr, status, nodes))
}

// probeKubernetes 访问 K8S APIServer 统计节点与 Pod 状态。
func probeKubernetes(ctx context.Context, t Target, timeout time.Duration) Result {
	addr := t.Addr("6443")
	scheme := "https"
	if t.Str("https", "true") == "false" {
		scheme = "http"
	}
	token := t.Str("token", "")
	if token == "" {
		return fail("", "缺少参数 token（ServiceAccount Token 或 Bearer Token）")
	}
	var nodes struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	url := fmt.Sprintf("%s://%s/api/v1/nodes", scheme, addr)
	if err := httpJSON(ctx, url, Target{"token": token, "skipTls": t.Str("skipTls", "true")}, timeout, &nodes); err != nil {
		return fail("", fmt.Sprintf("访问 APIServer 失败: %v", err))
	}
	total, ready := len(nodes.Items), 0
	for _, n := range nodes.Items {
		for _, c := range n.Status.Conditions {
			if c.Type == "Ready" && c.Status == "True" {
				ready++
			}
		}
	}
	if total == 0 {
		return fail("0", "APIServer 未返回任何节点，请检查 token 权限")
	}
	res := ok(fmt.Sprintf("%d/%d", ready, total),
		fmt.Sprintf("K8S %s 集群正常，就绪节点 %d/%d", addr, ready, total))
	if ready < total {
		// 存在未就绪节点视为异常，便于在控制台与告警中直接暴露
		res.Status = StatusFail
		res.Msg = fmt.Sprintf("K8S %s 存在未就绪节点，就绪 %d/%d", addr, ready, total)
	}
	return res
}

// httpJSON 发起带 Basic/Bearer 鉴权的 JSON 请求并解析响应。
func httpJSON(ctx context.Context, url string, t Target, timeout time.Duration, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if user := t.Str("username", t.Str("user", "")); user != "" {
		req.SetBasicAuth(user, t.Str("password", t.Str("secret", "")))
	}
	if token := t.Str("token", ""); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: timeout, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: t.Str("skipTls", "false") == "true"},
	}}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 160))
	}
	return json.Unmarshal(body, out)
}

func defaultStr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
