package httptransport

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	adminIDHeader    = "X-Admin-ID"
	customerIDHeader = "X-Customer-ID"
	maxJSONBodyBytes = 64 << 10
)

// apiErrorBody 是所有 HTTP 失败响应的统一外层结构。
type apiErrorBody struct {
	Error     apiErrorDetail `json:"error"`
	RequestID string         `json:"requestId"`
}

// apiErrorDetail 仅暴露稳定错误码和可展示消息，不包含内部 cause。
type apiErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeAPIError 将稳定业务错误与当前 request ID 一起写回客户端。
func writeAPIError(ctx *gin.Context, status int, code string, message string) {
	ctx.AbortWithStatusJSON(status, apiErrorBody{
		Error: apiErrorDetail{
			Code:    code,
			Message: message,
		},
		RequestID: ctx.GetString("request_id"),
	})
}

// requireHeaderIdentity 读取本地演示身份头；资源归属仍由服务端持久化关系决定。
func requireHeaderIdentity(
	ctx *gin.Context,
	header string,
	missingCode string,
) (string, bool) {
	identity := strings.TrimSpace(ctx.GetHeader(header))
	if identity == "" || len(identity) > 64 {
		writeAPIError(ctx, http.StatusUnauthorized, missingCode, "authentication is required")
		return "", false
	}
	return identity, true
}

// decodeJSONBody 拒绝未知字段和尾随 JSON，防止客户端拼写错误被静默忽略。
func decodeJSONBody(ctx *gin.Context, target any) error {
	return decodeJSONBodyWithLimit(ctx, target, maxJSONBodyBytes)
}

// decodeJSONBodyWithLimit 在解析前限制完整 JSON 请求体，供较大的 Markdown 内容使用。
func decodeJSONBodyWithLimit(ctx *gin.Context, target any, limit int64) error {
	content, err := io.ReadAll(io.LimitReader(ctx.Request.Body, limit+1))
	if err != nil {
		return err
	}
	if int64(len(content)) > limit {
		return errors.New("request body is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	err = decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("request body must contain one JSON value")
	}
	return err
}
