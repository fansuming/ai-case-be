package database

import (
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

type AiTestCaseRecord struct {
	ID             int64      `gorm:"column:id;primaryKey;autoIncrement"`
	Title          string     `gorm:"column:title;type:varchar(255);not null"`
	CaseTitle      string     `gorm:"column:case_title;type:varchar(255);not null"`
	Preconditions  string     `gorm:"column:preconditions;type:text"`
	Steps          string     `gorm:"column:steps;type:text"`
	ExpectedResult string     `gorm:"column:expected_result;type:text"`
	Priority       string     `gorm:"column:priority;type:varchar(32);not null;default:中"`
	Status         string     `gorm:"column:status;type:varchar(32);not null;default:created"`
	Source         string     `gorm:"column:source;type:varchar(32);not null;default:ai"`
	Provider       string     `gorm:"column:provider;type:varchar(32);not null;default:codex"`
	Creator        string     `gorm:"column:creator;type:varchar(64)"`
	CreatedAt      *time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt      *time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 显式指定 AI 测试用例记录在数据库中的表名。
//
// 这样可以避免 Gorm 按默认复数推断产生偏差，确保迁移和查询始终落到既定表结构。
func (AiTestCaseRecord) TableName() string {
	return "ai_test_case_records"
}

type AiTestCaseItem struct {
	ID             int64    `json:"id"`
	Title          string   `json:"title"`
	CaseTitle      string   `json:"caseTitle"`
	Preconditions  []string `json:"preconditions"`
	Steps          []string `json:"steps"`
	ExpectedResult string   `json:"expectedResult"`
	Priority       string   `json:"priority"`
	Status         string   `json:"status"`
	Source         string   `json:"source"`
	Provider       string   `json:"provider"`
	Creator        string   `json:"creator"`
	CreatedAt      string   `json:"createdAt"`
	UpdatedAt      string   `json:"updatedAt"`
}

var (
	ensureAiTestCaseTableOnce sync.Once
	ensureAiTestCaseTableErr  error
)

// EnsureAiTestCaseTable 确保 AI 测试用例表在当前进程生命周期内只做一次迁移检查。
//
// 该函数既负责在首次访问时触发表结构创建，也负责在数据库未初始化时尽早返回错误，
// 避免后续创建和查询逻辑在空 DB 上继续执行。
func EnsureAiTestCaseTable() error {
	ensureAiTestCaseTableOnce.Do(func() {
		if DB == nil {
			ensureAiTestCaseTableErr = gorm.ErrInvalidDB
			return
		}
		ensureAiTestCaseTableErr = DB.AutoMigrate(&AiTestCaseRecord{})
	})
	return ensureAiTestCaseTableErr
}

// CreateAiTestCase 对 AI 生成的测试用例记录做标准化后写入数据库。
//
// 写入前会统一清洗标题、优先级、步骤等字段，并补齐默认值，
// 保证不同入口生成的数据在表内保持一致格式。
func CreateAiTestCase(record AiTestCaseRecord) (AiTestCaseItem, error) {
	if err := EnsureAiTestCaseTable(); err != nil {
		return AiTestCaseItem{}, err
	}

	record.Title = strings.TrimSpace(record.Title)
	record.CaseTitle = strings.TrimSpace(record.CaseTitle)
	record.ExpectedResult = strings.TrimSpace(record.ExpectedResult)
	record.Priority = strings.TrimSpace(record.Priority)
	record.Status = strings.TrimSpace(record.Status)
	record.Source = strings.TrimSpace(record.Source)
	record.Provider = strings.TrimSpace(record.Provider)
	record.Creator = strings.TrimSpace(record.Creator)
	record.Preconditions = normalizeAiTestCaseText(record.Preconditions)
	record.Steps = normalizeAiTestCaseText(record.Steps)

	if record.Priority == "" {
		record.Priority = "中"
	}
	if record.Status == "" {
		record.Status = "created"
	}
	if record.Source == "" {
		record.Source = "ai"
	}
	if record.Provider == "" {
		record.Provider = "codex"
	}

	if err := DB.Create(&record).Error; err != nil {
		return AiTestCaseItem{}, err
	}

	return convertAiTestCaseItem(record), nil
}

// ListAiTestCases 按分页和标题关键字查询已保存的 AI 测试用例。
//
// 该函数同时返回列表数据和总数，便于前端直接构建分页表格；
// 当 page 或 pageSize 非法时，会自动回退到安全默认值。
func ListAiTestCases(page, pageSize int, title string) ([]AiTestCaseItem, int64, error) {
	if err := EnsureAiTestCaseTable(); err != nil {
		return nil, 0, err
	}
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 10
	}

	query := DB.Model(&AiTestCaseRecord{})
	title = strings.TrimSpace(title)
	if title != "" {
		likeTitle := "%" + strings.ToLower(title) + "%"
		query = query.Where("LOWER(title) LIKE ? OR LOWER(case_title) LIKE ?", likeTitle, likeTitle)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var rows []AiTestCaseRecord
	if err := query.
		Order("created_at DESC").
		Order("id DESC").
		Limit(pageSize).
		Offset((page - 1) * pageSize).
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}

	items := make([]AiTestCaseItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, convertAiTestCaseItem(row))
	}

	return items, total, nil
}

// convertAiTestCaseItem 把数据库记录转换成前端接口使用的响应结构。
//
// 这里会把换行存储的文本字段拆成数组，同时把时间统一格式化成字符串，
// 避免 handler 层再重复做展示结构转换。
func convertAiTestCaseItem(record AiTestCaseRecord) AiTestCaseItem {
	createdAt := ""
	updatedAt := ""
	if record.CreatedAt != nil {
		createdAt = record.CreatedAt.Format("2006-01-02 15:04:05")
	}
	if record.UpdatedAt != nil {
		updatedAt = record.UpdatedAt.Format("2006-01-02 15:04:05")
	}

	return AiTestCaseItem{
		ID:             record.ID,
		Title:          record.Title,
		CaseTitle:      record.CaseTitle,
		Preconditions:  splitAiTestCaseLines(record.Preconditions),
		Steps:          splitAiTestCaseLines(record.Steps),
		ExpectedResult: record.ExpectedResult,
		Priority:       record.Priority,
		Status:         record.Status,
		Source:         record.Source,
		Provider:       record.Provider,
		Creator:        record.Creator,
		CreatedAt:      createdAt,
		UpdatedAt:      updatedAt,
	}
}

// normalizeAiTestCaseText 规范化多行文本字段，去掉编号和空白后再按换行持久化。
//
// 这样数据库中保存的是干净、可重复拆分的文本内容，便于后续查询和展示复用。
func normalizeAiTestCaseText(value string) string {
	lines := splitAiTestCaseLines(value)
	return strings.Join(lines, "\n")
}

// splitAiTestCaseLines 把前端或模型输出的多行步骤文本切分成干净的字符串数组。
//
// 它会统一处理 CRLF、去掉常见的编号前缀，并过滤空行，
// 适合同时服务于入库清洗和响应反序列化两个场景。
func splitAiTestCaseLines(value string) []string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return []string{}
	}

	rawLines := strings.Split(strings.ReplaceAll(trimmed, "\r\n", "\n"), "\n")
	lines := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		normalized := strings.TrimSpace(line)
		normalized = strings.TrimLeft(normalized, "-0123456789. ")
		normalized = strings.TrimSpace(normalized)
		if normalized == "" {
			continue
		}
		lines = append(lines, normalized)
	}
	return lines
}
