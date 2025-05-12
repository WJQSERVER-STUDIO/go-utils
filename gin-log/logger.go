package logm

import (
	"time"

	"github.com/WJQSERVER-STUDIO/go-utils/logger"
	"github.com/gin-gonic/gin"
)

var (
	logw       = logger.Logw
	logDump    = logger.LogDump
	logDebug   = logger.LogDebug
	logInfo    = logger.LogInfo
	logWarning = logger.LogWarning
	logError   = logger.LogError
)

// 日志中间件
func Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		startTime := time.Now()

		c.Next()

		endTime := time.Now()
		timingResults := endTime.Sub(startTime)

		logInfo("%s %s %s %s %s %d %s ", c.ClientIP(), c.Request.Method, c.Request.Header.Get("Protocol"), c.Request.URL.Path, c.Request.UserAgent(), c.Writer.Status(), timingResults)
	}
}
