package aichat

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"

	"github.com/gin-gonic/gin"
)

// writeSSEHeaders 写入标准的 SSE 响应头，确保客户端按流式方式消费响应。
//
// 这里还显式关闭了代理缓冲，避免中间层把打字机效果攒成整块后再返回。
func writeSSEHeaders(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
}

// writeSSEPayload 按 SSE 协议写入一条 data 事件并立即 flush。
//
// payload 应当已经是可直接发给前端的字符串，本函数只负责协议层包装。
func writeSSEPayload(c *gin.Context, payload string) {
	_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", payload)
	c.Writer.Flush()
}

// writeSSEDone 输出 [DONE] 结束标记，通知前端本次流式响应已经结束。
func writeSSEDone(c *gin.Context) {
	_, _ = fmt.Fprint(c.Writer, "data: [DONE]\n\n")
	c.Writer.Flush()
}

// streamAssistantTextResult 用本地拼装的方式模拟 OpenAI 风格的增量流输出。
//
// 它主要服务于“不需要真的调用上游模型，但仍希望前端按统一 SSE 协议消费”的场景，
// 比如测试用例帮助文案、创建成功提示等本地直接构造的响应。
func streamAssistantTextResult(c *gin.Context, message string) {
	writeSSEHeaders(c)

	for _, chunk := range splitMessageChunks(message, 48) {
		payload := map[string]interface{}{
			"choices": []map[string]interface{}{{
				"delta": map[string]string{"content": chunk},
			}},
		}
		encoded, _ := json.Marshal(payload)
		writeSSEPayload(c, string(encoded))
	}

	writeSSEDone(c)
}

// streamUpstreamResponse 逐行转发上游模型返回的 SSE 内容。
//
// 当前实现故意不做结构化重组，只承担透传职责，
// 这样协议漂移风险更低，也能最大限度保持和上游返回的一致性。
func streamUpstreamResponse(c *gin.Context, body io.ReadCloser) error {
	defer body.Close()

	writeSSEHeaders(c)

	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		_, _ = fmt.Fprintf(c.Writer, "%s\n", scanner.Text())
		c.Writer.Flush()
	}

	return scanner.Err()
}

// splitMessageChunks 按 rune 长度切分文本，用于构造稳定的增量返回片段。
//
// 使用 rune 而不是 byte，可以避免中文被截断为乱码。
func splitMessageChunks(message string, chunkSize int) []string {
	runes := []rune(message)
	if len(runes) == 0 {
		return []string{""}
	}
	if chunkSize <= 0 {
		chunkSize = len(runes)
	}

	chunks := make([]string, 0, (len(runes)+chunkSize-1)/chunkSize)
	for start := 0; start < len(runes); start += chunkSize {
		end := start + chunkSize
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[start:end]))
	}

	return chunks
}
