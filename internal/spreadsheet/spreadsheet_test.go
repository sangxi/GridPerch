package spreadsheet

import (
	"archive/zip"
	"bytes"
	"testing"
)

// buildXLSX 拼一个最小可用的 xlsx：两个 XML 就够读出单元格文本。
func buildXLSX(t *testing.T, sst, sheet string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"xl/sharedStrings.xml":     sst,
		"xl/worksheets/sheet1.xml": sheet,
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("创建 zip 条目失败: %v", err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("写入 zip 条目失败: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("关闭 zip 失败: %v", err)
	}
	return buf.Bytes()
}

// TestReadXLSX 验证共享字符串、内联字符串、空单元格与列标解析。
func TestReadXLSX(t *testing.T) {
	sst := `<?xml version="1.0"?><sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
		<si><t>地址</t></si><si><t>账号</t></si><si><t>密码</t></si><si><t>核心库</t></si></sst>`
	sheet := `<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
		<sheetData>
		  <row r="1">
		    <c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c><c r="C1" t="s"><v>2</v></c>
		  </row>
		  <row r="2">
		    <c r="A2" t="s"><v>3</v></c><c r="B2" t="inlineStr"><is><t>root</t></is></c><c r="C2"><v>123456</v></c>
		  </row>
		  <row r="3">
		    <c r="B3" t="inlineStr"><is><t>gap</t></is></c>
		  </row>
		</sheetData></worksheet>`
	rows, err := ReadXLSX(buildXLSX(t, sst, sheet))
	if err != nil {
		t.Fatalf("解析 xlsx 失败: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("应有 3 行，实际 %d", len(rows))
	}
	if rows[0][0] != "地址" || rows[0][2] != "密码" {
		t.Fatalf("表头解析错误: %#v", rows[0])
	}
	if rows[1][0] != "核心库" || rows[1][1] != "root" || rows[1][2] != "123456" {
		t.Fatalf("数据行解析错误: %#v", rows[1])
	}
	// B3 缺失 A3：下标要按列标还原，不能被挤到第 0 列
	if len(rows[2]) < 2 || rows[2][0] != "" || rows[2][1] != "gap" {
		t.Fatalf("空列未按列标补齐: %#v", rows[2])
	}
}

// TestColumnIndex 验证列标换算，含双字母列。
func TestColumnIndex(t *testing.T) {
	cases := map[string]int{"A1": 0, "B12": 1, "Z9": 25, "AA1": 26, "AB100": 27}
	for ref, want := range cases {
		got, ok := columnIndex(ref)
		if !ok || got != want {
			t.Errorf("columnIndex(%q)=(%d,%v)，期望 %d", ref, got, ok, want)
		}
	}
	if _, ok := columnIndex("12"); ok {
		t.Errorf("没有列标的引用不应解析成功")
	}
}

// TestReadCSV 验证 BOM、制表符分隔与字段裁剪。
func TestReadCSV(t *testing.T) {
	data := []byte("\xEF\xBB\xBF地址\t账号\t密码\r\n10.0.0.1\troot\tp@ss\r\n")
	rows, err := ReadCSV(data)
	if err != nil {
		t.Fatalf("解析 csv 失败: %v", err)
	}
	if len(rows) != 2 || rows[0][0] != "地址" || rows[1][2] != "p@ss" {
		t.Fatalf("csv 解析结果不对: %#v", rows)
	}
}

// TestReadDispatch 验证按扩展名分派与 .xls 的明确提示。
func TestReadDispatch(t *testing.T) {
	if _, err := Read("a.xls", []byte("x")); err == nil {
		t.Errorf(".xls 应返回可读的提示而不是硬解析")
	}
	if _, err := Read("a.csv", []byte("a,b\n1,2\n")); err != nil {
		t.Errorf("csv 分派失败: %v", err)
	}
}
