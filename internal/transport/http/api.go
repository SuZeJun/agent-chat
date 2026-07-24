package httptransport

import (
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

type apiErrorBody struct {
	Error     apiErrorDetail `json:"error"`
	RequestID string         `json:"requestId"`
}

type apiErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeAPIError(ctx *gin.Context, status int, code string, message string) {
	ctx.AbortWithStatusJSON(status, apiErrorBody{
		Error: apiErrorDetail{
			Code:    code,
			Message: message,
		},
		RequestID: ctx.GetString("request_id"),
	})
}

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

func decodeJSONBody(ctx *gin.Context, target any) error {
	reader := io.LimitReader(ctx.Request.Body, maxJSONBodyBytes+1)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("request body must contain one JSON value")
	}
	return err
}
