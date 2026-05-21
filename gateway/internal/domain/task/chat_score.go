package task

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// ChatScoreThreshold 是判定"闲聊"的阈值。>= 此值视为闲聊。
const ChatScoreThreshold = 0.6

// ComputeChatScore 计算单条消息的闲聊分数 (0–1)。
// 纯本地计算，不调外部服务。
func ComputeChatScore(text string) float64 {
	var score float64

	// Signal 1: 长度过短
	charCount := utf8.RuneCountInString(text)
	if charCount < 50 {
		score += 0.3
	} else if charCount < 100 {
		score += 0.1
	}

	// Signal 2: 套话 regex
	if isPleasantry(text) {
		score += 0.4
	}

	if score > 1 {
		score = 1
	}
	return score
}

// 套话检测 patterns
var pleasantryPatterns = []*regexp.Regexp{
	// 英文
	regexp.MustCompile(`(?i)\b(thanks?|thank you|cheers|bye|goodbye|take care|see you|have a (good|great|nice)|happy coding|no worries|sounds good|got it|noted|will do|roger|acknowledged)\b`),
	// 纯确认
	regexp.MustCompile(`(?i)^(ok|okay|sure|yep|yeah|yes|no|nope|done|ack|lgtm|sgtm)\s*[!.]*$`),
	// 中文
	regexp.MustCompile(`^(收到|好的|了解|明白|没问题|辛苦了|感谢|谢谢|拜拜|再见|加油|保持联系|随时找我|有问题随时|互相学习|一起进步|祝顺利).{0,20}$`),
}

func isPleasantry(text string) bool {
	trimmed := strings.TrimSpace(text)
	if utf8.RuneCountInString(trimmed) > 200 {
		return false
	}
	for _, re := range pleasantryPatterns {
		if re.MatchString(trimmed) {
			return true
		}
	}
	return false
}

// extractPartsText 从 message parts 中提取纯文本（用于 chat_score 计算）。
func extractPartsText(parts []Part) string {
	var sb strings.Builder
	for _, p := range parts {
		if p.Kind == "text" && p.Text != "" {
			if sb.Len() > 0 {
				sb.WriteByte(' ')
			}
			sb.WriteString(p.Text)
		}
	}
	return sb.String()
}
