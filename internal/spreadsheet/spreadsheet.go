// Package spreadsheet 读取表格文件（xlsx / csv）为二维字符串数组。
//
// 只做「读」这一件事，且不引入任何第三方依赖：xlsx 本质是一个 zip，
// 里面 sheet1.xml + sharedStrings.xml 两个 XML 就够取出单元格文本了。
// 之所以不引 excelize，是因为服务端要保持 CGO_ENABLED=0 的纯静态构建，
// 也不想为了导入一个几十行的表去拉一整棵依赖树。
package spreadsheet

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// MaxRows 单次导入的行数上限，防止误传几十万行的表把内存打满。
const MaxRows = 5000

// Read 按扩展名选择解析方式，返回去掉空行后的二维表。
//
// 支持 .xlsx（Excel 2007+）与 .csv/.txt（逗号、制表符、分号分隔）。
// 老的 .xls（二进制 BIFF）不支持，需要另存为 xlsx 或 csv。
func Read(name string, data []byte) ([][]string, error) {
	if len(data) == 0 {
		return nil, errors.New("文件内容为空")
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".xlsx":
		return ReadXLSX(data)
	case ".csv", ".txt", ".tsv":
		return ReadCSV(data)
	case ".xls":
		return nil, errors.New("不支持旧版 .xls，请在 Excel 里另存为 .xlsx 或 .csv 后再导入")
	default:
		// 扩展名不认识时按内容猜：zip 头就是 xlsx
		if len(data) > 4 && string(data[:2]) == "PK" {
			return ReadXLSX(data)
		}
		return ReadCSV(data)
	}
}

/* ---------------- xlsx ---------------- */

// xlsxSharedStrings 对应 xl/sharedStrings.xml：<sst><si><t>文本</t></si>…
// 富文本单元格会把文本拆成多个 <r><t>，这里一并拼接。
type xlsxSharedStrings struct {
	SI []struct {
		T string `xml:"t"`
		R []struct {
			T string `xml:"t"`
		} `xml:"r"`
	} `xml:"si"`
}

// xlsxWorksheet 只取 sheetData/row/c 三层，够还原单元格文本。
type xlsxWorksheet struct {
	SheetData struct {
		Row []struct {
			C []struct {
				R  string `xml:"r,attr"` // 形如 B12
				T  string `xml:"t,attr"` // s=共享字符串 inlineStr=内联字符串
				V  string `xml:"v"`      // 值
				IS struct {
					T string `xml:"t"`
				} `xml:"is"`
			} `xml:"c"`
		} `xml:"row"`
	} `xml:"sheetData"`
}

// ReadXLSX 读取工作簿里第一个工作表的文本内容。
func ReadXLSX(data []byte) ([][]string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("文件不是有效的 xlsx：%w", err)
	}
	var sst xlsxSharedStrings
	var sheet *xlsxWorksheet
	for _, f := range zr.File {
		switch {
		case f.Name == "xl/sharedStrings.xml":
			if err := unmarshalZip(f, &sst); err != nil {
				return nil, err
			}
		case strings.HasPrefix(f.Name, "xl/worksheets/sheet") && strings.HasSuffix(f.Name, ".xml"):
			// 取文件名排序最靠前的那个工作表（sheet1 优先）
			if sheet == nil || f.Name == "xl/worksheets/sheet1.xml" {
				var ws xlsxWorksheet
				if err := unmarshalZip(f, &ws); err != nil {
					return nil, err
				}
				sheet = &ws
			}
		}
	}
	if sheet == nil {
		return nil, errors.New("xlsx 里没有找到工作表")
	}

	var str = func(i int) string {
		if i < 0 || i >= len(sst.SI) {
			return ""
		}
		si := sst.SI[i]
		if len(si.R) == 0 {
			return si.T
		}
		var sb strings.Builder
		sb.WriteString(si.T)
		for _, r := range si.R {
			sb.WriteString(r.T)
		}
		return sb.String()
	}

	out := make([][]string, 0, len(sheet.SheetData.Row))
	for _, row := range sheet.SheetData.Row {
		cells := map[int]string{}
		maxIdx := -1
		for seq, c := range row.C {
			idx := seq
			if c.R != "" {
				if i, ok := columnIndex(c.R); ok {
					idx = i
				}
			}
			var v string
			switch c.T {
			case "s":
				n, _ := strconv.Atoi(strings.TrimSpace(c.V))
				v = str(n)
			case "inlineStr":
				v = c.IS.T
			default:
				v = c.V
			}
			cells[idx] = strings.TrimSpace(v)
			if idx > maxIdx {
				maxIdx = idx
			}
		}
		if maxIdx < 0 {
			out = append(out, nil)
			continue
		}
		line := make([]string, maxIdx+1)
		for i, v := range cells {
			line[i] = v
		}
		out = append(out, line)
		if len(out) > MaxRows {
			return nil, fmt.Errorf("表格超过 %d 行，请拆分后再导入", MaxRows)
		}
	}
	return out, nil
}

// unmarshalZip 读取 zip 内单个 XML 文件并反序列化。
func unmarshalZip(f *zip.File, v any) error {
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("读取 %s 失败: %w", f.Name, err)
	}
	defer rc.Close()
	raw, err := io.ReadAll(io.LimitReader(rc, 64<<20))
	if err != nil {
		return fmt.Errorf("读取 %s 失败: %w", f.Name, err)
	}
	if err := xml.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("解析 %s 失败: %w", f.Name, err)
	}
	return nil
}

// columnIndex 把 "AB12" 里的列标转成 0 基下标（AB → 27）。
func columnIndex(ref string) (int, bool) {
	n := 0
	seen := false
	for _, r := range ref {
		if r >= 'A' && r <= 'Z' {
			n = n*26 + int(r-'A'+1)
			seen = true
			continue
		}
		if r >= 'a' && r <= 'z' {
			n = n*26 + int(r-'a'+1)
			seen = true
			continue
		}
		break
	}
	if !seen {
		return 0, false
	}
	return n - 1, true
}

/* ---------------- csv ---------------- */

// ReadCSV 读取 csv/tsv。分隔符自动判断（逗号 / 制表符 / 分号），
// 编码优先 UTF-8，不是合法 UTF-8 时按 GB18030 解码（Excel 另存为 CSV 常见）。
func ReadCSV(data []byte) ([][]string, error) {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	if !utf8.Valid(data) {
		if dec, err := simplifiedchinese.GB18030.NewDecoder().Bytes(data); err == nil {
			data = dec
		}
	}
	firstLine := data
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		firstLine = data[:i]
	}
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	r.TrimLeadingSpace = true
	switch {
	case bytes.Count(firstLine, []byte("\t")) > bytes.Count(firstLine, []byte(",")):
		r.Comma = '\t'
	case bytes.Count(firstLine, []byte(";")) > bytes.Count(firstLine, []byte(",")):
		r.Comma = ';'
	}
	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("解析表格失败: %w", err)
	}
	if len(rows) > MaxRows {
		return nil, fmt.Errorf("表格超过 %d 行，请拆分后再导入", MaxRows)
	}
	for i := range rows {
		for j := range rows[i] {
			rows[i][j] = strings.TrimSpace(rows[i][j])
		}
	}
	return rows, nil
}
