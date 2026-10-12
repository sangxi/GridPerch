package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// hwmonRoot 是 Linux 内核把温度传感器暴露到用户态的位置。
const hwmonRoot = "/sys/class/hwmon"

// readHwmonTemps 遍历 hwmon 下的温度通道，返回 CPU 相关传感器里的最高温度。
//
// 之所以直接读 sysfs 而不是依赖 lm-sensors：内核已经把主板 SuperIO 芯片
// 的温度映射成标准 hwmon 设备，读文件几乎零开销、无外部命令依赖，
// 在精简镜像与容器里也能工作。
func readHwmonTemps() (float64, error) {
	entries, err := os.ReadDir(hwmonRoot)
	if err != nil {
		return 0, err
	}
	var maxTemp float64
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(hwmonRoot, e.Name())
		// 设备名文件决定这个 hwmon 是不是 CPU 温度源（coretemp / k10temp / cpu_thermal 等）
		if name, err := os.ReadFile(filepath.Join(dir, "name")); err == nil {
			n := strings.ToLower(strings.TrimSpace(string(name)))
			if !strings.Contains(n, "coretemp") && !strings.Contains(n, "k10temp") &&
				!strings.Contains(n, "cpu") && !strings.Contains(n, "zenpower") {
				continue
			}
		}
		// tempN_input 是温度值，tempN_label 是通道用途（Package id 0 / Core 0 ...）
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if !strings.HasPrefix(f.Name(), "temp") || !strings.HasSuffix(f.Name(), "_input") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(dir, f.Name()))
			if err != nil {
				continue
			}
			// hwmon 温度单位是毫摄氏度
			var milli int64
			if _, err := fmtSscan(strings.TrimSpace(string(raw)), &milli); err != nil {
				continue
			}
			c := float64(milli) / 1000
			if c > 0 && c < 200 && c > maxTemp {
				maxTemp = c
			}
		}
	}
	return maxTemp, nil
}

// fmtSscan 包装 fmt.Sscan，让调用处少写一个 error 判断。
func fmtSscan(s string, v any) (int, error) { return fmt.Sscan(s, v) }
