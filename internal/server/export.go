package server

import (
	"archive/zip"
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// handleExport 按类别导出数据，支持 csv 与 xlsx 两种格式。
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	format := r.URL.Query().Get("format")
	var sheet, file string
	var rows [][]string

	switch kind {
	case "hosts":
		hosts, err := s.store.ListHosts()
		if err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		sheet, file = "主机清单", "hosts"
		rows = [][]string{{"主机", "IP", "操作系统", "状态", "CPU%", "内存%", "核数", "CPU型号", "最后上报"}}
		for _, h := range hosts {
			rows = append(rows, []string{h.Hostname, h.IP, h.Version, hostStateCN(h.State),
				strconv.FormatFloat(h.CpuPer, 'f', 1, 64), strconv.FormatFloat(h.MemPer, 'f', 1, 64),
				h.CpuCoreNum, h.CpuXh, h.LastSeen})
		}
	case "alerts":
		alerts, err := s.store.Alerts(1000)
		if err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		sheet, file = "告警记录", "alerts"
		rows = [][]string{{"主机", "类型", "级别", "数值", "内容", "状态", "时间", "认领人", "认领时间", "处理备注"}}
		for _, a := range alerts {
			rows = append(rows, []string{a.Hostname, a.Type, a.Level, a.Value, a.Content,
				alertStateCN(a.State), a.CreateTime, a.AckBy, a.AckTime, a.Note})
		}
	case "tasks":
		tasks, err := s.store.ListTasks()
		if err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		sheet, file = "监控项", "tasks"
		rows = [][]string{{"名称", "类型", "执行端", "作用主机", "间隔(秒)", "超时(秒)", "状态", "目标配置", "自动恢复命令"}}
		for _, t := range tasks {
			enabled := "已启用"
			if !t.Enabled {
				enabled = "已停用"
			}
			rows = append(rows, []string{t.Name, t.Type, runnerCN(t.Runner), t.Hosts,
				strconv.Itoa(t.IntervalSec), strconv.Itoa(t.TimeoutSec), enabled, t.Target, t.HealCmd})
		}
	case "monitors":
		states, err := s.store.ListMonitorStates("")
		if err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		sheet, file = "监控状态", "monitors"
		rows = [][]string{{"监控项", "类型", "目标/主机", "状态", "数值", "说明", "检查时间"}}
		for _, st := range states {
			rows = append(rows, []string{st.Name, st.Type, st.Hostname, monitorStateCN(st.Status), st.Value, st.Msg, st.CheckTime})
		}
	case "commands":
		list, err := s.store.ListCommands(r.URL.Query().Get("host"), 1000)
		if err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		sheet, file = "指令记录", "commands"
		rows = [][]string{{"目标主机", "指令", "执行人", "状态", "退出码", "时间", "输出"}}
		for _, c := range list {
			rows = append(rows, []string{c.Host, c.Cmd, c.User, commandStatusCN(c.Status), strconv.Itoa(c.ExitCode), c.CreateTime, c.Output})
		}
	case "assets":
		s.handleAssetExport(w, r)
		return
	default:
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "未知的导出类别：" + kind})
		return
	}
	writeTableFile(w, sheet, file, rows, format)
}

func hostStateCN(v string) string {
	if v == "1" {
		return "在线"
	}
	return "离线"
}

func alertStateCN(v string) string {
	if v == "1" {
		return "未处理"
	}
	return "已恢复"
}

func monitorStateCN(v string) string {
	if v == "2" {
		return "异常"
	}
	return "正常"
}

func commandStatusCN(v string) string {
	switch v {
	case "pending":
		return "待执行"
	case "running":
		return "执行中"
	case "done":
		return "成功"
	case "failed":
		return "失败"
	case "timeout":
		return "超时"
	default:
		return v
	}
}

func runnerCN(v string) string {
	if v == "server" {
		return "服务端探测"
	}
	return "采集端执行"
}

// writeTableFile 按格式输出表格文件，默认导出 CSV。
func writeTableFile(w http.ResponseWriter, sheet, name string, rows [][]string, format string) {
	stamp := time.Now().Format("20060102-150405")
	if strings.EqualFold(format, "xlsx") {
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s-%s.xlsx", name, stamp))
		if err := writeXLSX(w, sheet, rows); err != nil {
			http.Error(w, "生成 Excel 失败: "+err.Error(), http.StatusInternalServerError)
		}
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s-%s.csv", name, stamp))
	writer := csv.NewWriter(w)
	// 写入 UTF-8 BOM，避免 Excel 打开中文乱码
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
	_ = writer.WriteAll(rows)
	writer.Flush()
}

/* ---------------- 极简 XLSX 生成 ---------------- */

// writeXLSX 使用标准库生成一个单表单 xlsx 工作簿，避免引入第三方依赖。
func writeXLSX(w http.ResponseWriter, sheet string, rows [][]string) error {
	zw := zip.NewWriter(w)
	defer zw.Close()

	files := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>
<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>
<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>
</Types>`,
		"_rels/.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
</Relationships>`,
		"xl/workbook.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<sheets><sheet name="` + escapeXML(sheet) + `" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>
</Relationships>`,
		"xl/styles.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
<fonts count="1"><font><sz val="11"/><name val="Calibri"/></font></fonts>
<fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills>
<borders count="1"><border/></borders>
<cellStyleXfs count="1"><xf/></cellStyleXfs>
<cellXfs count="1"><xf xfId="0"/></cellXfs>
</styleSheet>`,
		"xl/worksheets/sheet1.xml": buildSheet(sheet, rows),
	}
	for name, content := range files {
		f, err := zw.Create(name)
		if err != nil {
			return err
		}
		if _, err := f.Write([]byte(content)); err != nil {
			return err
		}
	}
	return nil
}

// buildSheet 构造 worksheet XML，数值型单元格不加字符串标记。
func buildSheet(sheet string, rows [][]string) string {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	sb.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)
	sb.WriteString(`<sheetData>`)
	for i, row := range rows {
		sb.WriteString(fmt.Sprintf(`<row r="%d">`, i+1))
		for j, cell := range row {
			ref := columnName(j) + strconv.Itoa(i+1)
			if isNumeric(cell) {
				sb.WriteString(fmt.Sprintf(`<c r="%s"><v>%s</v></c>`, ref, escapeXML(cell)))
				continue
			}
			sb.WriteString(fmt.Sprintf(`<c r="%s" t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>`, ref, escapeXML(cell)))
		}
		sb.WriteString(`</row>`)
	}
	sb.WriteString(`</sheetData></worksheet>`)
	return sb.String()
}

// isNumeric 判断单元格是否应输出为数值。
func isNumeric(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

// columnName 将列序号转换为 Excel 列名（A、B、...、AA）。
func columnName(index int) string {
	var out string
	index++
	for index > 0 {
		rem := (index - 1) % 26
		out = string(rune('A'+rem)) + out
		index = (index - 1) / 26
	}
	return out
}

// escapeXML 转义 XML 保留字符。
func escapeXML(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&apos;",
		"\x00", "", "\r", "")
	return r.Replace(s)
}
