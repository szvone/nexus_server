package database

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"nexus_server/models"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type Database struct {
	db *sql.DB
}

func NewDatabase() (*Database, error) {
	db, err := sql.Open("sqlite3", "tasks.db?_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	// 启用WAL模式
	if _, err := db.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		return nil, fmt.Errorf("failed to set WAL mode: %v", err)
	}
	if err := createTables(db); err != nil {
		return nil, err
	}

	// 启动后台任务清理超时锁定的任务
	go func() {
		for {
			if err := releaseExpiredLocks(db); err != nil {
				log.Printf("Error releasing expired locks: %v", err)
			}
			time.Sleep(1 * time.Minute)

		}
	}()
	// 启动后台任务清理超时客户端
	go func() {
		for {
			if err := releaseExpiredClient(db); err != nil {
				log.Printf("Error releasing expired Client: %v", err)
			}
			time.Sleep(10 * time.Second)

		}
	}()

	return &Database{db: db}, nil
}

func (d *Database) Close() error {
	return d.db.Close()
}

func createTables(db *sql.DB) error {
	createTable := `
	CREATE TABLE IF NOT EXISTS tasks (
		program_id VARCHAR(50) NOT NULL,
		public_inputs TEXT NOT NULL,
		task_id VARCHAR(50) PRIMARY KEY,
		sign_key TEXT NOT NULL,
		status VARCHAR(10) NOT NULL DEFAULT 'pending',
		result TEXT DEFAULT NULL,
		credits INTEGER DEFAULT 0,
		locked_at INTEGER DEFAULT NULL,
		created_at INTEGER NOT NULL
	);`

	if _, err := db.Exec(createTable); err != nil {
		return fmt.Errorf("failed to create table: %w", err)
	}

	createClientTable := `
	CREATE TABLE IF NOT EXISTS client (
		client_uuid VARCHAR(50) NOT NULL PRIMARY KEY,
		client_key VARCHAR(50) NOT NULL,
		Client_ip VARCHAR(50) NOT NULL,
		heart_time INTEGER DEFAULT 0,
		cpu INTEGER DEFAULT 0,
		memory INTEGER DEFAULT 0
	);`

	if _, err := db.Exec(createClientTable); err != nil {
		return fmt.Errorf("failed to create table: %w", err)
	}
	return nil
}

// 计算所有统计数据
func (d *Database) GetTaskStats() (*models.TaskStats, error) {
	// 初始化结构体
	stats := &models.TaskStats{}

	// 任务总量统计
	totalQuery := `SELECT COUNT(*) FROM tasks`
	if err := d.db.QueryRow(totalQuery).Scan(&stats.TotalTasks); err != nil {
		return nil, fmt.Errorf("total query failed: %w", err)
	}

	// 各状态任务计数（单次查询获取所有状态）
	statusQuery := `
		SELECT 
			SUM(CASE WHEN status = 'pending' THEN 1 ELSE 0 END) AS pending,
			SUM(CASE WHEN status = 'locked' THEN 1 ELSE 0 END) AS processing,
			SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END) AS completed,
			SUM(CASE WHEN status = 'completed' AND result = 'success' THEN 1 ELSE 0 END) AS successful
		FROM tasks`

	var pending, processing, completed, successful sql.NullInt64
	if err := d.db.QueryRow(statusQuery).Scan(&pending, &processing, &completed, &successful); err != nil {
		return nil, fmt.Errorf("status query failed: %w", err)
	}

	stats.PendingTasks = pending.Int64
	stats.ProcessingTasks = processing.Int64
	stats.CompletedTasks = completed.Int64
	stats.SuccessfulTasks = successful.Int64
	stats.FailedTasks = completed.Int64 - successful.Int64

	// 处理速度（最近10分钟完成任务数/10）
	rateQuery := `
		SELECT COUNT(*) 
		FROM tasks 
		WHERE status = 'completed'
		AND locked_at > ?`

	var recentCompletions int64
	tenMinAgo := time.Now().Add(-10 * time.Minute).Unix()
	if err := d.db.QueryRow(rateQuery, tenMinAgo).Scan(&recentCompletions); err != nil {
		return nil, fmt.Errorf("rate query failed: %w", err)
	}
	stats.ProcessingRate = float64(recentCompletions) / 10.0

	// 平均处理时间（只计算completed状态）
	avgTimeQuery := `
		SELECT AVG(locked_at - created_at)
		FROM tasks
		WHERE status = 'completed'
		AND locked_at > created_at` // 防止负值

	var avgTime sql.NullFloat64
	if err := d.db.QueryRow(avgTimeQuery).Scan(&avgTime); err != nil {
		return nil, fmt.Errorf("avg time query failed: %w", err)
	}

	if avgTime.Valid {
		stats.AvgProcessTime = avgTime.Float64
	} else {
		stats.AvgProcessTime = 0 // 无完成任务时默认为0
	}

	return stats, nil
}

// 释放超时锁定的任务
func releaseExpiredLocks(db *sql.DB) error {
	c := int64(0)
	timeout := time.Now().Add(-5 * time.Minute).Unix()
	timeoutCount, err := db.Exec("UPDATE tasks SET status = 'pending', locked_at = NULL WHERE status = 'locked' AND locked_at <= ?", timeout)
	if err != nil {
		return fmt.Errorf("failed to release expired locks: %w", err)
	}
	c1, _ := timeoutCount.RowsAffected()
	c = c + c1
	// log.Println("重置超时任务个数：", c1)

	rateCount, err := db.Exec("UPDATE tasks SET status = 'pending', locked_at = NULL, result = NULL WHERE result like '%:429}%' AND locked_at <= ?", timeout)
	if err != nil {
		return fmt.Errorf("failed to release expired locks: %w", err)
	}

	c2, _ := rateCount.RowsAffected()
	c = c + c2

	// log.Println("重置code：429任务个数：", c2)

	errSendCount, err := db.Exec("UPDATE tasks SET status = 'pending', locked_at = NULL, result = NULL WHERE result like '%rror sending request for url %' AND locked_at <= ?", timeout)
	if err != nil {
		return fmt.Errorf("failed to release expired locks: %w", err)
	}

	c3, _ := errSendCount.RowsAffected()
	// log.Println("重置请求超时任务个数：", c3)
	c = c + c3

	httperrSendCount, err := db.Exec("UPDATE tasks SET status = 'pending', locked_at = NULL, result = NULL WHERE result like '%HTTP error with status 502: %' AND locked_at <= ?", timeout)
	if err != nil {
		return fmt.Errorf("failed to release expired locks: %w", err)
	}

	c4, _ := httperrSendCount.RowsAffected()

	c = c + c4
	if c > 0 {
		log.Println("已重置任务状态，数量：", c)
	}

	return nil
}

// 释放超时客户端
func releaseExpiredClient(db *sql.DB) error {
	timeout := time.Now().Add(-30 * time.Second).Unix()
	timeoutCount, err := db.Exec("DELETE FROM client WHERE heart_time < ?", timeout)
	if err != nil {
		return fmt.Errorf("failed to release expired client: %w", err)
	}
	c, _ := timeoutCount.RowsAffected()

	if c > 0 {
		log.Println("已清理离线客户端，数量：", c)
	}

	return nil
}
func (d *Database) CreateTask(task models.TaskData) error {
	// 确保任务创建时间为当前时间
	task.CreatedAt = time.Now()

	stmt, err := d.db.Prepare(`INSERT INTO tasks(
		program_id, public_inputs, task_id, sign_key, 
		status, result, credits, locked_at, created_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare statement failed: %w", err)
	}
	defer stmt.Close()

	_, err = stmt.Exec(
		task.ProgramID, task.PublicInputs, task.TaskID, task.SignKey,
		models.TaskPending, nil, 0, nil, task.CreatedAt.Unix(),
	)
	return err
}

func (d *Database) GetTaskCount(status string) int {
	var count int
	err := d.db.QueryRow("SELECT COUNT(*) FROM tasks WHERE status = ?", status).Scan(&count)
	if err != nil {
		log.Fatalf("Failed to execute query: %v", err)
		return -1
	}
	return count
}

func (d *Database) GetTask(taskID string) (*models.TaskData, error) {
	stmt, err := d.db.Prepare("SELECT * FROM tasks WHERE task_id = ?")
	if err != nil {
		return nil, fmt.Errorf("prepare statement failed: %w", err)
	}
	defer stmt.Close()

	task, err := scanTask(stmt.QueryRow(taskID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, err
	}
	return task, nil
}

func (d *Database) UpdateTask(task models.TaskData) error {
	stmt, err := d.db.Prepare(`UPDATE tasks SET 
		program_id = ?, public_inputs = ?, sign_key = ?, 
		status = ?, result = ?, credits = ?, locked_at = ?
	WHERE task_id = ?`)
	if err != nil {
		return fmt.Errorf("prepare statement failed: %w", err)
	}
	defer stmt.Close()

	res, err := stmt.Exec(
		task.ProgramID, task.PublicInputs, task.SignKey,
		task.Status, task.Result.String, task.Credits, task.LockedAt.Time.Unix(),
		task.TaskID,
	)
	if err != nil {
		return err
	}

	if rows, _ := res.RowsAffected(); rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (d *Database) DeleteTask(taskID string) error {
	res, err := d.db.Exec("DELETE FROM tasks WHERE task_id = ?", taskID)
	if err != nil {
		return err
	}

	if rows, _ := res.RowsAffected(); rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (d *Database) GetAllTasks() ([]models.TaskData, error) {
	rows, err := d.db.Query("SELECT * FROM tasks")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanTasks(rows)
}

// 提取一个未处理的任务
func (d *Database) PickAvailableTask() (*models.TaskData, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin transaction failed: %w", err)
	}
	defer tx.Rollback()

	// 获取最早创建的待处理任务(一分钟前的任务)
	row := tx.QueryRow(`
		SELECT * 
		FROM tasks 
		WHERE status = 'pending' and created_at <= ?
		ORDER BY created_at ASC 
		LIMIT 1
	`, time.Now().Add(-30*time.Second).Unix())

	task, err := scanTask(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil // 没有待处理任务
		}
		return nil, err
	}

	// 锁定任务
	now := time.Now()
	task.Status = models.TaskLocked
	task.LockedAt = sql.NullTime{Time: now, Valid: true}

	res, err := tx.Exec(`
		UPDATE tasks 
		SET status = 'locked', locked_at = ?
		WHERE task_id = ?`,
		now.Unix(), task.TaskID,
	)
	if err != nil {
		return nil, fmt.Errorf("update task status failed: %w", err)
	}

	if rows, _ := res.RowsAffected(); rows == 0 {
		return nil, errors.New("task already taken")
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaction failed: %w", err)
	}

	return task, nil
}

// 提交任务结果
func (d *Database) SubmitTaskResult(submission models.TaskResultSubmission) error {
	tx, err := d.db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction failed: %w", err)
	}
	defer tx.Rollback()

	// 获取任务
	row := tx.QueryRow(`
		SELECT status, locked_at 
		FROM tasks 
		WHERE task_id = ? `,
		submission.TaskID,
	)

	var status models.TaskStatus
	var lockedAt sql.NullInt64
	err = row.Scan(&status, &lockedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("task not found: %w", err)
		}
		return fmt.Errorf("task scan failed: %w", err)
	}

	// 验证任务状态
	if status != models.TaskLocked {
		return fmt.Errorf("task is not locked: current status %s", status)
	}

	// 验证锁定时间是否超时
	if !lockedAt.Valid {
		return fmt.Errorf("task lock is invalid")
	}

	lockTime := time.Unix(lockedAt.Int64, 0)
	if time.Since(lockTime) > 5*time.Minute {
		return fmt.Errorf("task lock has expired")
	}

	// 更新任务为完成状态
	_, err = tx.Exec(`
		UPDATE tasks 
		SET status = 'completed', 
		    result = ?, 
		    credits = ?, 
		    locked_at = ?, 
		    created_at = ?
		WHERE task_id = ?`,
		submission.Result, submission.Credits, time.Now().Unix(), lockedAt, submission.TaskID,
	)
	if err != nil {
		return fmt.Errorf("update task result failed: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction failed: %w", err)
	}

	return nil
}

// 辅助函数：扫描单个任务
func scanTask(row *sql.Row) (*models.TaskData, error) {
	var task models.TaskData
	var result sql.NullString
	var lockedAt sql.NullInt64
	var credits sql.NullInt64
	var createdAt int64

	err := row.Scan(
		&task.ProgramID,
		&task.PublicInputs,
		&task.TaskID,
		&task.SignKey,
		&task.Status,
		&result,
		&credits,
		&lockedAt,
		&createdAt,
	)
	if err != nil {
		return nil, err
	}

	// 处理可能为 NULL 的字段
	task.Result = sql.NullString{String: result.String, Valid: result.Valid}
	task.Credits = int(credits.Int64)

	if lockedAt.Valid {
		task.LockedAt = sql.NullTime{Time: time.Unix(lockedAt.Int64, 0), Valid: true}
	}

	task.CreatedAt = time.Unix(createdAt, 0)

	return &task, nil
}

// 辅助函数：扫描多个任务
func scanTasks(rows *sql.Rows) ([]models.TaskData, error) {
	var tasks []models.TaskData
	for rows.Next() {
		var task models.TaskData
		var result sql.NullString
		var credits sql.NullInt64
		var lockedAt sql.NullInt64
		var createdAt int64

		if err := rows.Scan(
			&task.ProgramID,
			&task.PublicInputs,
			&task.TaskID,
			&task.SignKey,
			&task.Status,
			&result,
			&credits,
			&lockedAt,
			&createdAt,
		); err != nil {
			return nil, err
		}

		// 处理可能为 NULL 的字段
		task.Result = sql.NullString{String: result.String, Valid: result.Valid}
		task.Credits = int(credits.Int64)

		if lockedAt.Valid {
			task.LockedAt = sql.NullTime{Time: time.Unix(lockedAt.Int64, 0), Valid: true}
		}

		task.CreatedAt = time.Unix(createdAt, 0)

		tasks = append(tasks, task)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return tasks, nil
}

func (d *Database) UpsertClient(client models.Client) error {
	_, err := d.db.Exec(`
		INSERT OR REPLACE INTO client 
		(client_uuid, client_key, client_ip, heart_time, cpu, memory)
		VALUES (?, ?, ?, ?, ?, ?)`,
		client.UUID, client.Key, client.IP, client.HeartTime, client.CPU, client.Memory)
	return err
}

func (d *Database) GetClientStates() ([]models.ClientState, error) {
	query := `
        SELECT 
            main.client_ip,
            main.client_key,
            COUNT(DISTINCT sub.client_uuid) AS client_count,
            MAX(main.heart_time) AS last_updated,
            (SELECT cpu FROM client 
             WHERE client_key = main.client_key 
             ORDER BY heart_time DESC 
             LIMIT 1) AS cpu,
            (SELECT memory FROM client 
             WHERE client_key = main.client_key 
             ORDER BY heart_time DESC 
             LIMIT 1) AS memory
        FROM client AS main
        INNER JOIN client AS sub ON main.client_key = sub.client_key
        GROUP BY main.client_key;
    `

	rows, err := d.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var states []models.ClientState
	for rows.Next() {
		var state models.ClientState
		err := rows.Scan(
			&state.ClientIp,
			&state.ClientKey,
			&state.ClientCount,
			&state.LastUpdated,
			&state.CPU,
			&state.Memory,
		)
		if err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	return states, nil
}
