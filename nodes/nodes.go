package nodes

import (
	"nexus_server/gen"
	"sync"
	"sync/atomic"
	"time"
)

var (
	globalNodes   sync.Map                 // map[nodeId]*Node
	nodeList      atomic.Pointer[[]string] // 存储所有节点ID的列表
	mu            sync.RWMutex
	currentIdx    uint64     // 原子计数器，用于循环索引
	nodeLastTaken sync.Map   // 记录节点最后提取时间 map[nodeId]int64 (Unix时间戳)
	nextNodeLock  sync.Mutex // 新增: 全局互斥锁，确保GetNextNode单线程调用
)

// StoreNodes 安全地更新节点列表（不覆盖现有有效节点）
func StoreNodes(resp *gen.UserResponse) {
	if resp == nil {
		return
	}

	mu.Lock()
	defer mu.Unlock()

	// 创建新的节点ID列表（保留之前的有效节点）
	newNodeIDs := []string{}
	if existingList := nodeList.Load(); existingList != nil {
		// 保留现有的节点ID列表
		newNodeIDs = *existingList
	}

	// 处理当前响应中的节点
	for _, node := range resp.GetNodes() {
		if node == nil || node.GetNodeId() == "" {
			continue
		}

		nodeID := node.GetNodeId()

		// 存储/更新节点
		nodeCopy := &gen.Node{
			NodeId:   node.GetNodeId(),
			NodeType: node.GetNodeType(),
			// 添加其他必要字段...
		}
		globalNodes.Store(nodeID, nodeCopy)

		// 如果节点ID不在列表中则添加
		found := false
		for _, id := range newNodeIDs {
			if id == nodeID {
				found = true
				break
			}
		}
		if !found {
			newNodeIDs = append(newNodeIDs, nodeID)
		}
	}

	// 原子方式更新节点ID列表
	updatedList := make([]string, len(newNodeIDs))
	copy(updatedList, newNodeIDs)
	nodeList.Store(&updatedList)
}
func StoreNodesUseList(list []*gen.Node) {

	mu.Lock()
	defer mu.Unlock()

	// 创建新的节点ID列表（保留之前的有效节点）
	newNodeIDs := []string{}
	if existingList := nodeList.Load(); existingList != nil {
		// 保留现有的节点ID列表
		newNodeIDs = *existingList
	}

	// 处理当前响应中的节点
	for _, node := range list {

		nodeID := node.GetNodeId()

		// 存储/更新节点
		nodeCopy := &gen.Node{
			NodeId:   node.GetNodeId(),
			NodeType: node.GetNodeType(),
			// 添加其他必要字段...
		}
		globalNodes.Store(nodeID, nodeCopy)

		// 如果节点ID不在列表中则添加
		found := false
		for _, id := range newNodeIDs {
			if id == nodeID {
				found = true
				break
			}
		}
		if !found {
			newNodeIDs = append(newNodeIDs, nodeID)
		}
	}

	// 原子方式更新节点ID列表
	updatedList := make([]string, len(newNodeIDs))
	copy(updatedList, newNodeIDs)
	nodeList.Store(&updatedList)
}

// GetNextNode 获取下一个可用的节点（确保单线程调用）
// cooldownSeconds: 冷却时间（秒），0表示无冷却时间
func GetNextNode(cooldownSeconds int) *gen.Node {
	// 获取全局互斥锁，确保函数单线程执行
	nextNodeLock.Lock()
	defer nextNodeLock.Unlock()

	// 尝试多次查找有效节点
	startTime := time.Now().Unix() // 当前Unix时间戳(秒)

	nodeIDs := nodeList.Load()
	if nodeIDs == nil || len(*nodeIDs) == 0 {
		return nil
	}

	total := uint64(len(*nodeIDs))
	if total == 0 {
		return nil
	}

	// 记录原始起始索引
	originalIdx := atomic.LoadUint64(&currentIdx)
	attempts := uint64(0)

	for attempts < total {
		// 计算当前索引
		idx := (originalIdx + attempts) % total
		attempts++

		nodeID := (*nodeIDs)[idx]

		// 检查冷却时间
		if cooldownSeconds > 0 {
			if lastTaken, ok := nodeLastTaken.Load(nodeID); ok {
				if startTime-lastTaken.(int64) < int64(cooldownSeconds) {
					continue // 仍在冷却期，跳过
				}
			}
		}

		// 获取节点实例
		value, ok := globalNodes.Load(nodeID)
		if !ok {
			continue // 节点不存在
		}

		node, valid := value.(*gen.Node)
		if !valid || node == nil {
			continue // 节点无效
		}

		// 成功获取: 更新提取时间和当前索引
		nodeLastTaken.Store(nodeID, startTime)
		atomic.StoreUint64(&currentIdx, (idx+1)%total) // 更新索引到下一个位置
		return node
	}

	// 没有找到可用节点，更新索引到末尾+1
	atomic.StoreUint64(&currentIdx, (originalIdx+attempts)%total)
	return nil
}
func UpdateNodeExtractionTime(nodeId string, addSeconds int) {
	if addSeconds == 0 {
		return // 不需要改变
	}

	now := time.Now().Unix()
	var newTime int64

	if lastTaken, ok := nodeLastTaken.Load(nodeId); ok {
		// 如果已有提取时间，则在原有基础上添加
		newTime = lastTaken.(int64) + int64(addSeconds)
	} else {
		// 如果没有记录，则在当前时间基础上添加
		newTime = now + int64(addSeconds)
	}

	// 原子更新提取时间
	nodeLastTaken.Store(nodeId, newTime)
}

// 从全局变量获取节点
func GetNode(nodeId string) *gen.Node {
	if value, ok := globalNodes.Load(nodeId); ok {
		return value.(*gen.Node)
	}
	return nil
}

// 获取所有节点
func GetAllNodes() []*gen.Node {
	var nodes []*gen.Node
	globalNodes.Range(func(key, value interface{}) bool {
		nodes = append(nodes, value.(*gen.Node))
		return true
	})
	return nodes
}
