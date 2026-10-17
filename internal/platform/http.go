package platform

import (
	"github.com/docker-manager/docker-manager/internal/security"
	"github.com/docker-manager/docker-manager/internal/task"
	"github.com/gin-gonic/gin"
	"net/http"
)

func Fail(c *gin.Context, status int, code string, err error) {
	message := code
	if err != nil {
		message = security.Redact(err.Error())
	}
	c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"code": code, "message": message, "trace_id": c.GetString("request_id")}})
}
func Actor(c *gin.Context) string { v, _ := c.Get("actor"); s, _ := v.(string); return s }
func Submit(c *gin.Context, a *App, kind, target string, cancel bool, fn func(*task.Run) error) {
	t, e := a.Tasks.Submit(c.Request.Context(), kind, target, Actor(c), c.GetHeader("Idempotency-Key"), cancel, fn)
	if e != nil {
		Fail(c, http.StatusConflict, "TASK_CONFLICT", e)
		return
	}
	c.JSON(http.StatusAccepted, t)
}
