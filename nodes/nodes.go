package nodes

import (
	"database/sql"
	"errors"
	"log"
	"nexus_server/gen"
	"sync"
	"time"
)

var (
	dbMutex       sync.Mutex // 保护所有数据库写操作
	walletMutexes sync.Map   // 每个钱包的专用互斥锁
	nodeMutexes   sync.Map   // 每个节点的专用互斥锁
)

type Wallet struct {
	Address  string
	LastUsed int64
	LockTime int64
}

type DBNode struct {
	NodeID   string
	NodeType gen.NodeType
	Wallet   string
	LastUsed int64
	LockTime int64
}

// 获取特定钱包的互斥锁
func walletLock(walletAddress string) *sync.Mutex {
	m, _ := walletMutexes.LoadOrStore(walletAddress, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// 获取特定节点的互斥锁
func nodeLock(nodeID string) *sync.Mutex {
	m, _ := nodeMutexes.LoadOrStore(nodeID, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// AcquireNode 安全地获取一个节点（多线程安全）
func AcquireNode(db *sql.DB, cooldownSeconds int, cooldownSeconds2 int) (*gen.Node, string, string, error) {
	now := time.Now().Unix()

	// 第一阶段：查找可用的钱包
	var walletAddress string
	var signkey string
	if err := func() error {
		// 全局锁保护钱包查找
		dbMutex.Lock()
		defer dbMutex.Unlock()

		row := db.QueryRow(`
			SELECT wallet_address , signkey
			FROM wallets 
			WHERE lock_time <= ?
			ORDER BY last_used ASC
			LIMIT 1
		`, now)

		if err := row.Scan(&walletAddress, &signkey); err != nil {
			if err == sql.ErrNoRows {
				return errors.New("no available wallets")
			}
			return err
		}
		return nil
	}(); err != nil {
		return nil, "", "", err
	}
	// 第二阶段：查找该钱包下的可用节点
	var (
		nodeID   string
		nodeType gen.NodeType
	)

	// 获取钱包锁（防止多个线程同时操作同一个钱包）
	walletLock(walletAddress).Lock()
	defer walletLock(walletAddress).Unlock()

	tx, err := db.Begin()
	if err != nil {
		return nil, "", "", err
	}

	// 再次检查钱包状态
	var walletLockTime int64
	if err := tx.QueryRow(`
		SELECT lock_time FROM wallets WHERE wallet_address = ? 
	`, walletAddress).Scan(&walletLockTime); err != nil {
		tx.Rollback()
		return nil, "", "", err
	}

	if walletLockTime > now {
		tx.Rollback()
		return nil, "", "", errors.New("wallet has been locked by another process")
	}

	// 查找节点
	if err := tx.QueryRow(`
		SELECT node_id, node_type
		FROM nodes
		WHERE wallet_address = ? AND lock_time <= ?
		ORDER BY last_used ASC
		LIMIT 1
	`, walletAddress, now).Scan(&nodeID, &nodeType); err != nil {
		tx.Rollback()
		return nil, "", "", err
	}

	// 获取节点锁（防止多个线程同时操作同一个节点）
	nodeLock(nodeID).Lock()
	defer nodeLock(nodeID).Unlock()

	newLockTime := now + int64(cooldownSeconds)

	// 更新节点状态
	if _, err := tx.Exec(`
		UPDATE nodes 
		SET last_used = ?, lock_time = ?
		WHERE node_id = ?
	`, now, newLockTime, nodeID); err != nil {
		tx.Rollback()
		return nil, "", "", err
	}
	newLockTime = now + int64(cooldownSeconds2)

	// 更新钱包状态
	if _, err := tx.Exec(`
		UPDATE wallets 
		SET last_used = ?, lock_time = ?
		WHERE wallet_address = ?
	`, now, newLockTime, walletAddress); err != nil {
		tx.Rollback()
		return nil, "", "", err
	}

	if err := tx.Commit(); err != nil {
		return nil, "", "", err
	}

	return &gen.Node{
		NodeId:   nodeID,
		NodeType: nodeType,
	}, walletAddress, signkey, nil
}

// SetWalletLockTime 安全设置钱包锁定时间
func SetWalletLockTime(db *sql.DB, walletAddress string, seconds int) error {

	// 获取钱包锁
	mutex := walletLock(walletAddress)
	mutex.Lock()
	defer mutex.Unlock()

	lockTime := time.Now().Unix() + int64(seconds)

	// 使用事务确保原子性
	tx, err := db.Begin()
	if err != nil {
		return err
	}

	if _, err := tx.Exec(`
		UPDATE wallets 
		SET lock_time = ?
		WHERE wallet_address = ?
	`, lockTime, walletAddress); err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit()
}

// SetNodeLockTime 安全设置节点锁定时间
func SetNodeLockTime(db *sql.DB, nodeID string, seconds int) error {

	// 获取节点锁
	mutex := nodeLock(nodeID)
	mutex.Lock()
	defer mutex.Unlock()

	lockTime := time.Now().Unix() + int64(seconds)

	// 使用事务确保原子性
	tx, err := db.Begin()
	if err != nil {
		return err
	}

	if _, err := tx.Exec(`
		UPDATE nodes 
		SET lock_time = ?
		WHERE node_id = ?
	`, lockTime, nodeID); err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit()
}

// StoreNodes 安全存储节点列表
func StoreNodes(db *sql.DB, nodes []*gen.Node, walletAddress string) error {
	if len(nodes) == 0 {
		return nil
	}

	// 获取钱包锁（确保整个操作原子性）
	mutex := walletLock(walletAddress)
	mutex.Lock()
	defer mutex.Unlock()

	// 开始事务
	tx, err := db.Begin()
	if err != nil {
		return err
	}

	// 确保钱包存在
	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO wallets (wallet_address) 
		VALUES (?)
	`, walletAddress); err != nil {
		tx.Rollback()
		return err
	}

	// 插入/更新节点
	for _, node := range nodes {
		if _, err := tx.Exec(`
			INSERT OR REPLACE INTO nodes 
			(node_id, node_type, wallet_address) 
			VALUES (?, ?, ?)
		`, node.GetNodeId(), int(node.GetNodeType()), walletAddress); err != nil {
			tx.Rollback()
			return err
		}
	}

	return tx.Commit()
}

// StoreWallet 安全存储钱包地址
func StoreWallet(db *sql.DB, walletAddress string, signkey string) error {

	// 获取钱包锁
	mutex := walletLock(walletAddress)
	mutex.Lock()
	defer mutex.Unlock()

	tx, err := db.Begin()
	if err != nil {
		log.Println(err)
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO wallets (wallet_address, signkey)
		VALUES (?, ?)
		ON CONFLICT(wallet_address) DO UPDATE 
		SET signkey = excluded.signkey
	`, walletAddress, signkey); err != nil {
		tx.Rollback()
		log.Println(err)

		return err
	}

	return tx.Commit()
}

// GetNode 安全获取节点信息
func GetNode(db *sql.DB, nodeID string) (*gen.Node, error) {

	node := &gen.Node{}
	var nodeType int

	// 只读操作不需要全局锁，但需要节点级锁避免写入冲突
	mutex := nodeLock(nodeID)
	mutex.Lock()
	defer mutex.Unlock()

	if err := db.QueryRow(`
		SELECT node_id, node_type 
		FROM nodes 
		WHERE node_id = ?
	`, nodeID).Scan(&node.NodeId, &nodeType); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	node.NodeType = gen.NodeType(nodeType)
	return node, nil
}

// GetAllNodes 安全获取所有节点
func GetAllNodes(db *sql.DB) ([]*gen.Node, error) {

	// 只读操作使用全局锁确保一致性
	dbMutex.Lock()
	defer dbMutex.Unlock()

	rows, err := db.Query(`
		SELECT node_id, node_type
		FROM nodes
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	nodes := []*gen.Node{}
	for rows.Next() {
		var nodeID string
		var nodeType int
		if err := rows.Scan(&nodeID, &nodeType); err != nil {
			return nil, err
		}
		nodes = append(nodes, &gen.Node{
			NodeId:   nodeID,
			NodeType: gen.NodeType(nodeType),
		})
	}
	return nodes, nil
}
