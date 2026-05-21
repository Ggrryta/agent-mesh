// Package publication 实现 Market：用户把自己的 agent 发布成可被订阅的 publication。
package publication

import (
	"errors"
	"strings"
	"time"
)

// Publication 是 market 上一条公开发布的 agent 模板。
type Publication struct {
	ID                   int64
	PublisherUID         int64
	SourceAgentID        string
	Title                string
	Summary              string
	SystemPromptTemplate string
	Category             string
	Tags                 []string
	DownloadCount        int64
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// Subscription 表示某个用户 fork 了某个 publication 到自己名下的新 agent_id。
type Subscription struct {
	ID             int64
	UID            int64
	PublicationID  int64
	ForkedAgentID  string
	CreatedAt      time.Time
}

// MaxTitleLen / MaxSummaryLen 跟 schema 对齐，超长直接拒绝。
const (
	MaxTitleLen   = 120
	MaxSummaryLen = 500
	MaxTagsTotal  = 200
)

var (
	ErrTitleRequired       = errors.New("publication: title required")
	ErrTitleTooLong        = errors.New("publication: title too long")
	ErrSummaryTooLong      = errors.New("publication: summary too long")
	ErrTagsTooLong         = errors.New("publication: tags too long")
	ErrPublicationNotFound = errors.New("publication: not found")
	ErrAlreadySubscribed   = errors.New("publication: already subscribed")
	ErrCannotForkOwn       = errors.New("publication: cannot fork your own publication")
)

// Filter 用于列表查询。零值都表示不过滤。
type Filter struct {
	Category     string
	Search       string // 模糊匹配 title / summary / tags
	PublisherUID int64
	Limit        int
	Offset       int
}

// SerializeTags / ParseTags 在 DB 层用逗号分隔字符串，业务层暴露 []string。
func SerializeTags(tags []string) string {
	clean := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t != "" {
			clean = append(clean, t)
		}
	}
	return strings.Join(clean, ",")
}

func ParseTags(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
