package models

import (
	"database/sql"
	"time"
)

type TaskData struct {
	ProgramID    string         `json:"programId"`
	PublicInputs string         `json:"publicInputs"`
	TaskID       string         `json:"taskId"`
	SignKey      string         `json:"signKey"`
	Status       TaskStatus     `json:"status"`
	Result       sql.NullString `json:"result"` // 支持 NULL 值
	TaskType     int            `json:"taskType"`
	LockedAt     sql.NullTime   `json:"lockedAt"` // 支持 NULL 值
	CreatedAt    time.Time      `json:"createdAt"`
	AddAt        time.Time      `json:"addAt"`
	Wallet       string         `json:"wallet"`
}

// 任务状态枚举
type TaskStatus string

const (
	TaskPending   TaskStatus = "pending"   // 等待处理
	TaskLocked    TaskStatus = "locked"    // 已被锁定
	TaskCompleted TaskStatus = "completed" // 已完成
)

// 任务结果提交请求
type TaskResultSubmission struct {
	TaskID string `json:"taskId" binding:"required"` // 任务ID
	Result string `json:"result" binding:"required"` // 任务结果
}

// 定义API响应结构体
type TaskStats struct {
	TotalTasks      int64   `json:"total_tasks"`
	PendingTasks    int64   `json:"pending_tasks"`
	ProcessingTasks int64   `json:"processing_tasks"`
	CompletedTasks  int64   `json:"completed_tasks"`
	SuccessfulTasks int64   `json:"successful_tasks"`
	FailedTasks     int64   `json:"failed_tasks"`
	ProcessingRate  float64 `json:"processing_rate"`  // 任务/分钟
	AvgProcessTime  float64 `json:"avg_process_time"` // 单位：秒
}

// 定义数据结构
type Client struct {
	UUID      string `json:"client_uuid"`
	Key       string `json:"client_key"`
	IP        string `json:"client_ip"`
	HeartTime int64  `json:"heart_time"`
	CPU       int    `json:"cpu"`
	Memory    int    `json:"memory"`
}

type ClientState struct {
	ClientIp    string `json:"client_ip"`
	ClientKey   string `json:"client_key"`
	ClientCount int    `json:"client_count"`
	LastUpdated int64  `json:"last_updated"`
	CPU         int    `json:"cpu"`
	Memory      int    `json:"memory"`
}
