package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Response 是整个 handlers 层统一使用的 API 响应结构。
//
// 当前后端接口默认都返回：
// - code：业务状态码，0 表示成功，1 表示失败；
// - msg：本次请求的说明文本或错误信息；
// - data：成功结果或附带数据。
//
// 这样前端可以用统一的数据结构解析不同接口响应，而不需要为每条接口单独适配。
type Response struct {
	Code int         `json:"code"` // 0-成功，1-失败
	Msg  string      `json:"msg"`  // 错误描述
	Data interface{} `json:"data"` // 数据
}

// JsonResponse 按统一结构输出 HTTP 200 响应。
//
// 当前项目采用“HTTP 状态码尽量简单 + 业务 code 表达成功失败”的约定，
// 因此这里默认总是返回 200，真正的业务成功或失败由 Response.Code 区分。
func JsonResponse(c *gin.Context, code int, msg string, data interface{}) {
	c.JSON(http.StatusOK, Response{
		Code: code,
		Msg:  msg,
		Data: data,
	})
}

// SuccessCode 和 FailureCode 是 handlers 层统一使用的业务状态码常量。
const (
	SuccessCode = 0
	FailureCode = 1
)

// appEnv 保存当前应用运行环境。
//
// 它由启动阶段调用 InitAppEnv 注入，目前主要用于给后续扩展保留统一的环境感知入口。
var appEnv = "pro"

// InitAppEnv 在服务初始化阶段写入当前运行环境。
//
// 当传入值为空时保持默认值不变，避免误把环境配置覆盖为空字符串。
func InitAppEnv(env string) {
	if env != "" {
		appEnv = env
	}
}
