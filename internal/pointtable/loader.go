// Package pointtable 负责加载和管理 SIS 测点点表（CSV 格式）
package pointtable

import (
	"encoding/csv"
	"fmt"
	"os"
	"strings"
)

// Point 表示一个 SIS 测点
type Point struct {
	// PointName 测点完整名称，如 DDM.SIS.0DCS_00BHT03GT001XQ001
	PointName string
	// Alias 测点别名（可选），用于 MQTT 消息 key 展示
	Alias string
	// Description 测点描述（可选）
	Description string
	// Unit 工程单位（可选）
	Unit string
	// Enabled 是否启用该测点，默认 true
	Enabled bool
}

// Table 测点点表
type Table struct {
	Points []*Point
}

// LoadFromCSV 从 CSV 文件加载点表
//
// CSV 文件格式（首行为标题行，字段顺序不限，按标题名匹配）：
//
//	point_name,alias,description,unit,enabled
//	DDM.SIS.0DCS_00BHT03GT001XQ001,BHT03GT001_Current,主冷却水泵房变压器A高压断路器高压侧电流,A,true
//
// 必填字段：point_name
// 可选字段：alias / description / unit / enabled（默认 true）
func LoadFromCSV(path string) (*Table, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开点表文件失败 [%s]: %w", path, err)
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.TrimLeadingSpace = true
	r.Comment = '#'

	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("解析 CSV 失败: %w", err)
	}
	if len(records) < 2 {
		return nil, fmt.Errorf("点表为空或仅有标题行")
	}

	// 解析标题行，建立列索引
	header := records[0]
	colIdx := make(map[string]int, len(header))
	for i, h := range header {
		colIdx[strings.ToLower(strings.TrimSpace(h))] = i
	}

	mustHave := func(col string) (int, error) {
		idx, ok := colIdx[col]
		if !ok {
			return -1, fmt.Errorf("CSV 缺少必填列: %s", col)
		}
		return idx, nil
	}
	colGet := func(record []string, col string) string {
		idx, ok := colIdx[col]
		if !ok || idx >= len(record) {
			return ""
		}
		return strings.TrimSpace(record[idx])
	}

	nameIdx, err := mustHave("point_name")
	if err != nil {
		return nil, err
	}

	table := &Table{}
	for rowNum, row := range records[1:] {
		if len(row) == 0 {
			continue
		}
		if nameIdx >= len(row) {
			return nil, fmt.Errorf("第 %d 行列数不足", rowNum+2)
		}
		pointName := strings.TrimSpace(row[nameIdx])
		if pointName == "" {
			continue // 跳过空行
		}

		enabled := true
		if v := colGet(row, "enabled"); v != "" && strings.ToLower(v) == "false" {
			enabled = false
		}

		table.Points = append(table.Points, &Point{
			PointName:   pointName,
			Alias:       colGet(row, "alias"),
			Description: colGet(row, "description"),
			Unit:        colGet(row, "unit"),
			Enabled:     enabled,
		})
	}

	if len(table.Points) == 0 {
		return nil, fmt.Errorf("点表中没有有效测点")
	}

	return table, nil
}

// EnabledPoints 返回所有启用的测点
func (t *Table) EnabledPoints() []*Point {
	result := make([]*Point, 0, len(t.Points))
	for _, p := range t.Points {
		if p.Enabled {
			result = append(result, p)
		}
	}
	return result
}

// EnabledPointNames 返回所有启用测点的名称列表
func (t *Table) EnabledPointNames() []string {
	enabled := t.EnabledPoints()
	names := make([]string, len(enabled))
	for i, p := range enabled {
		names[i] = p.PointName
	}
	return names
}

// Total 返回测点总数（含禁用）
func (t *Table) Total() int {
	return len(t.Points)
}

// EnabledTotal 返回启用测点总数
func (t *Table) EnabledTotal() int {
	return len(t.EnabledPoints())
}

// PointMap 按 PointName 构建索引，方便快速查找
func (t *Table) PointMap() map[string]*Point {
	m := make(map[string]*Point, len(t.Points))
	for _, p := range t.Points {
		m[p.PointName] = p
	}
	return m
}

// BatchNames 将启用测点按 batchSize 分批，返回名称二维切片
func (t *Table) BatchNames(batchSize int) [][]string {
	names := t.EnabledPointNames()
	if batchSize <= 0 {
		return [][]string{names}
	}
	var batches [][]string
	for i := 0; i < len(names); i += batchSize {
		end := i + batchSize
		if end > len(names) {
			end = len(names)
		}
		batches = append(batches, names[i:end])
	}
	return batches
}
