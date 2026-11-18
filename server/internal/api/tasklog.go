package api

import (
	"fmt"
	"strings"
)

// 单步日志的内存上限：头部保留起始上下文，尾部保留最新进展。
// 中间部分丢弃并标注省略字节数，既保住排障需要的关键信息，又不会把数据库撑爆。
const (
	stepLogHeadBytes = 8 << 10  // 头部保留 8KB
	stepLogTailBytes = 24 << 10 // 尾部保留 24KB
	stepLogLineBytes = 4 << 10  // 单行上限 4KB
)

// stepLog 是有界的日志累积器，按行追加并按头/尾两段保留。
type stepLog struct {
	head      []string
	headBytes int
	tail      []string
	tailBytes int
	dropped   int
}

// add 追加一行日志；超出头部配额后转入尾部，尾部超限时从最旧的整行开始丢弃。
func (l *stepLog) add(line string) {
	line = clampLine(line, stepLogLineBytes)
	n := len(line) + 1
	if l.headBytes+n <= stepLogHeadBytes {
		l.head = append(l.head, line)
		l.headBytes += n
		return
	}
	l.tail = append(l.tail, line)
	l.tailBytes += n
	// 至少保留一行，避免单行超限时把刚写入的内容也丢掉。
	for l.tailBytes > stepLogTailBytes && len(l.tail) > 1 {
		l.dropped += len(l.tail[0]) + 1
		l.tailBytes -= len(l.tail[0]) + 1
		l.tail = l.tail[1:]
	}
}

// String 渲染为可直接入库的文本；有丢弃时插入一行说明，让阅读者知道中间被截断。
func (l *stepLog) String() string {
	var b strings.Builder
	for _, s := range l.head {
		b.WriteString(s)
		b.WriteByte('\n')
	}
	if l.dropped > 0 {
		fmt.Fprintf(&b, "…（中间 %d 字节日志已省略，单步最多保留 %d KB）…\n",
			l.dropped, (stepLogHeadBytes+stepLogTailBytes)>>10)
	}
	for _, s := range l.tail {
		b.WriteString(s)
		b.WriteByte('\n')
	}
	return b.String()
}

// clampLine 按字节上限截断单行，并回退到 UTF-8 字符边界，避免出现半个汉字。
func clampLine(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…（本行过长已截断）"
}
