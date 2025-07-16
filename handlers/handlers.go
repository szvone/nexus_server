package handlers

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"nexus_server/database"
	"nexus_server/models"
	"time"

	"github.com/gin-gonic/gin"
)

type TaskHandler struct {
	db *database.Database
}

func NewTaskHandler(db *database.Database) *TaskHandler {
	return &TaskHandler{db: db}
}

func (h *TaskHandler) CreateTask(c *gin.Context) {
	var task models.TaskData
	if err := c.ShouldBindJSON(&task); err != nil {
		sendErrorResponse(c, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	if task.TaskID == "" {
		sendErrorResponse(c, http.StatusBadRequest, "Task ID is required")
		return
	}

	if err := h.db.CreateTask(task); err != nil {
		handleDBError(c, err)
		return
	}

	c.JSON(http.StatusCreated, task)
}

func (h *TaskHandler) GetTask(c *gin.Context) {
	taskID := c.Param("task_id")
	if taskID == "" {
		sendErrorResponse(c, http.StatusBadRequest, "Task ID is required")
		return
	}

	task, err := h.db.GetTask(taskID)
	if handleDBError(c, err) {
		return
	}

	c.JSON(http.StatusOK, task)
}

func (h *TaskHandler) UpdateTask(c *gin.Context) {
	taskID := c.Param("task_id")
	if taskID == "" {
		sendErrorResponse(c, http.StatusBadRequest, "Task ID is required")
		return
	}

	var task models.TaskData
	if err := c.ShouldBindJSON(&task); err != nil {
		sendErrorResponse(c, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	if task.TaskID != "" && task.TaskID != taskID {
		sendErrorResponse(c, http.StatusBadRequest, "Task ID mismatch")
		return
	}

	task.TaskID = taskID
	if err := h.db.UpdateTask(task); handleDBError(c, err) {
		return
	}

	c.JSON(http.StatusOK, task)
}

func (h *TaskHandler) DeleteTask(c *gin.Context) {

	if err := h.db.DeleteTask(); handleDBError(c, err) {
		return
	}

	c.Status(http.StatusNoContent)
}

func (h *TaskHandler) ListTasks(c *gin.Context) {
	tasks, err := h.db.GetAllTasks()
	if handleDBError(c, err) {
		return
	}

	if tasks == nil {
		tasks = []models.TaskData{}
	}

	c.JSON(http.StatusOK, tasks)
}

// 提取一个新任务
func (h *TaskHandler) PickTask(c *gin.Context) {
	task, err := h.db.PickAvailableTask()
	if err != nil {
		sendErrorResponse(c, http.StatusInternalServerError, "Failed to pick task: "+err.Error())
		return
	}

	if task == nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "No available tasks"})
		return
	}

	c.JSON(http.StatusOK, task)
}

// 提取一个新任务
func (h *TaskHandler) GetTaskStats(c *gin.Context) {
	task, err := h.db.GetTaskStats()
	if err != nil {
		sendErrorResponse(c, http.StatusInternalServerError, "Failed to pick task: "+err.Error())
		return
	}

	if task == nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "No available tasks"})
		return
	}

	c.JSON(http.StatusOK, task)
}

// 提交任务结果
func (h *TaskHandler) SubmitResult(c *gin.Context) {

	var submission models.TaskResultSubmission
	if err := c.ShouldBindJSON(&submission); err != nil {
		sendErrorResponse(c, http.StatusBadRequest, "Invalid request format: "+err.Error())
		return
	}

	if err := h.db.SubmitTaskResult(submission); err != nil {
		statusCode := http.StatusInternalServerError
		if errors.Is(err, sql.ErrNoRows) {
			statusCode = http.StatusNotFound
		} else if err.Error() == "task is not locked" {
			statusCode = http.StatusConflict
		} else if err.Error() == "task lock has expired" {
			statusCode = http.StatusGone
		}

		sendErrorResponse(c, statusCode, "Failed to submit result: "+err.Error())
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": fmt.Sprintf("Task %s completed successfully", submission.TaskID),
		"credits": submission.Credits,
	})
}

func (h *TaskHandler) ClientHeart(c *gin.Context) {
	// 首先捕获原始请求体
	rawBody, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Failed to read request body",
			"raw":   "", // 无法读取时设为空
		})
		return
	}

	// 重置请求体供后续绑定使用
	c.Request.Body = io.NopCloser(bytes.NewBuffer(rawBody))

	var req struct {
		UUID   string `json:"client_uuid" binding:"required"`
		Key    string `json:"client_key" binding:"required"`
		CPU    int    `json:"cpu" `
		Memory int    `json:"memory" `
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		// 获取原始文本并转为字符串（避免二进制数据问题）
		rawText := string(rawBody)
		if rawText == "" {
			rawText = base64.StdEncoding.EncodeToString(rawBody)
		}

		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "Invalid request",
			"details": err.Error(), // 绑定错误详情
			"raw":     rawText,     // 原始POST数据
		})
		return
	}

	client := models.Client{
		UUID:      req.UUID,
		Key:       req.Key,
		IP:        c.ClientIP(),      // 获取客户端IP
		HeartTime: time.Now().Unix(), // 使用服务器当前时间戳
		CPU:       req.CPU,
		Memory:    req.Memory,
	}

	if err := h.db.UpsertClient(client); err != nil {
		handleDBError(c, err)
		return
	}

	c.JSON(http.StatusCreated, client)
}

func (h *TaskHandler) GetClientStates(c *gin.Context) {
	clients, err := h.db.GetClientStates()
	if handleDBError(c, err) {
		return
	}
	c.JSON(http.StatusOK, clients)
}

func sendErrorResponse(c *gin.Context, statusCode int, message string) {
	println(message)
	c.JSON(statusCode, gin.H{"error": message})
}

func handleDBError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, sql.ErrNoRows) {
		sendErrorResponse(c, http.StatusNotFound, err.Error())
		return true
	}

	sendErrorResponse(c, http.StatusInternalServerError, "Database error: "+err.Error())
	return true
}
