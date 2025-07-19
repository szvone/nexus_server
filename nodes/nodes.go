package nodes

import (
	"nexus_server/gen"
	"sync"
	"sync/atomic"
)

var (
	globalNodes sync.Map                 // map[nodeId]*Node
	nodeList    atomic.Pointer[[]string] // 存储所有节点ID的列表
	mu          sync.RWMutex
	currentIdx  uint64 // 原子计数器，用于循环索引
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

// GetNextNode 安全地循环获取下一个节点（跳过无效节点）
func GetNextNode() *gen.Node {
	// 尝试多次查找有效节点
	for attempt := 0; attempt < 3; attempt++ {
		nodeIDs := nodeList.Load()
		if nodeIDs == nil || len(*nodeIDs) == 0 {
			return nil
		}

		total := uint64(len(*nodeIDs))
		if total == 0 {
			return nil
		}

		// 获取下一个索引位置
		idx := atomic.AddUint64(&currentIdx, 1) % total
		nodeID := (*nodeIDs)[idx]

		// 获取节点
		if value, ok := globalNodes.Load(nodeID); ok {
			if node, ok := value.(*gen.Node); ok && node != nil {
				return node
			}
		}
	}

	return nil // 多次尝试后仍未找到有效节点
}

// 以下是原有的函数...

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
