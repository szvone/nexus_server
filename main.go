package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	crand "crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"nexus_server/database"
	"nexus_server/gen"
	"nexus_server/handlers"
	"nexus_server/models"
	"nexus_server/nodes"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gin-gonic/gin"
	"github.com/imroc/req/v3"
	"github.com/pkg/browser"

	"google.golang.org/protobuf/proto"
)

//go:embed templates/*
var htmlFS embed.FS

//go:embed static/*
var assetFS embed.FS

var db *database.Database

// 定义全局信号量（最大并发数）
const maxConcurrent = 20 // 最大并发数
var sem = make(chan struct{}, maxConcurrent)

// 解析单行节点数据
func parseNodeLine(line string) (*gen.Node, error) {
	// 分割为两部分: <NodeId>=<Type>
	parts := strings.Split(line, "=")
	if len(parts) != 2 {
		return nil, fmt.Errorf("无效格式: 等号数量错误")
	}

	nodeId := strings.TrimSpace(parts[0])
	typeStr := strings.TrimSpace(parts[1])

	if nodeId == "" {
		return nil, fmt.Errorf("节点ID不能为空")
	}
	var nodeType gen.NodeType
	switch typeStr {
	case "WEB_PROVER":
		nodeType = 0
	case "CLI_PROVER":
		nodeType = 1
	default:
		return nil, fmt.Errorf("未知节点类型: '%s'", typeStr)
	}

	return &gen.Node{
		NodeId:   nodeId,
		NodeType: nodeType,
	}, nil
}

// 工作协程函数
func getNodes(address string) bool {
	log.Println("读取节点列表...")

	// 清空当前缓存
	var nodeList []*gen.Node

	file, err := os.Open("./" + address + ".txt")
	if err != nil {
		if os.IsNotExist(err) {

			c := req.C()
			c.SetRedirectPolicy(req.NoRedirectPolicy())
			c.SetCookieJar(nil)
			c.ImpersonateChrome()
			c.SetTLSFingerprintRandomized()
			c.SetCommonHeader("Content-Type", "application/octet-stream")
			c.SetCommonHeader("Origin", "https://app.nexus.xyz")
			c.SetCommonHeader("Referer", "https://app.nexus.xyz/")
			c.SetTimeout(time.Duration(120) * time.Second)

			// c.SetProxyURL("http://127.0.0.1:2025")

			r := c.R()
			resp, err := r.Send("GET", "https://beta.orchestrator.nexus.xyz/v3/users/"+address)
			if err != nil {
				log.Println("读取节点失败，重新尝试...")
				resp, err = r.Send("GET", "https://beta.orchestrator.nexus.xyz/v3/users/"+address)
				if err != nil {
					log.Println("读取节点失败：", err)
					return false
				}
			}
			res := &gen.UserResponse{}
			bin := resp.Bytes()
			err = proto.Unmarshal(bin, res)
			if err != nil {
				log.Println("读取节点失败：", string(bin))
				return false
			}
			line := ""
			for i := 0; i < len(res.Nodes); i++ {
				line += res.Nodes[i].GetNodeId() + "=" + res.Nodes[i].GetNodeType().String() + "\n"
			}

			nodes.StoreNodes(res)
			count := len(res.Nodes)
			if len(res.Nodes) == 50 {
				resp2, err := r.Send("GET", "https://beta.orchestrator.nexus.xyz/v3/nodes/"+res.UserId+"/"+res.Nodes[len(res.Nodes)-1].NodeId)
				if err != nil {
					log.Println("读取节点失败，重新尝试...")
					resp2, err = r.Send("GET", "https://beta.orchestrator.nexus.xyz/v3/nodes/"+res.UserId+"/"+res.Nodes[len(res.Nodes)-1].NodeId)
					if err != nil {
						log.Println("读取节点失败：", err)
						return false
					}
				}
				res2 := &gen.UserResponse{}
				bin = resp2.Bytes()
				err = proto.Unmarshal(bin, res2)
				if err != nil {
					log.Println("读取节点失败：", string(bin))
					return false
				}
				nodes.StoreNodes(res2)
				count += len(res2.Nodes)

				for i := 0; i < len(res2.Nodes); i++ {
					line += res2.Nodes[i].GetNodeId() + "=" + res2.Nodes[i].GetNodeType().String() + "\n"
				}

			}
			log.Println("读取到节点节点数量：", count)
			if count <= 10 {
				log.Println("节点节点数量不足10个，请创建节点后再运行！")
				return false
			}
			// 写出缓存
			f, err := os.OpenFile("./"+address+".txt", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
			if err != nil {
				log.Println("写出文件失败，请检查程序权限！", err)
				return false
			}
			defer f.Close()
			if _, err := f.WriteString(line); err != nil {
				log.Println("写出文件失败，请检查程序权限！", err)
				return false
			}
			log.Println("已生成节点缓存文件：", address+".txt")

			return true
		} else {
			log.Println("检查缓存失败，请检查程序权限！", err)
			return false
		}
	}
	defer file.Close()

	log.Println("检测到缓存文件：", address+".txt")

	scanner := bufio.NewScanner(file)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue // 跳过空行
		}

		// 解析每行内容
		node, err := parseNodeLine(line)
		if err != nil {
			log.Printf("缓存文件解析错误：解析错误(第%d行): %v - 内容: '%s'\n", lineNum, err, line)
			continue
		}

		// 添加到缓存
		nodeList = append(nodeList, node)
	}

	if err := scanner.Err(); err != nil {
		log.Println("检查缓存失败，请检查程序权限！", err)
		return false
	}
	nodes.StoreNodesUseList(nodeList)
	log.Printf("成功从缓存文件加载 %d 个节点，如果删除了或者更新了节点，请删除缓存文件以获取最新的节点列表。\n", len(nodeList))
	return true
}

func GenKey() string {
	_, privateKey, err := ed25519.GenerateKey(crand.Reader)
	if err != nil {

		return ""
	}
	return hex.EncodeToString(privateKey)
}
func GetProofTaskRequest(node_id string, node_key string, node_type int) []byte {
	var req gen.GetProofTaskRequest
	pk, err := hex.DecodeString(node_key[64:])
	if err != nil {
		return nil
	}
	if node_type == 1 {
		req = gen.GetProofTaskRequest{
			NodeId:           node_id,
			NodeType:         gen.NodeType_CLI_PROVER,
			Ed25519PublicKey: pk,
		}
	} else {
		req = gen.GetProofTaskRequest{
			NodeId:           node_id,
			NodeType:         gen.NodeType_WEB_PROVER,
			Ed25519PublicKey: pk,
		}
	}
	binaryData, err := proto.Marshal(&req)
	if err != nil {
		return nil
	}
	return binaryData

}

// 创建任务的本地调用函数（不通过HTTP）
func CreateTask(programID, publicInputs, taskID, signKey string) error {
	task := models.TaskData{
		ProgramID:    programID,
		PublicInputs: publicInputs,
		TaskID:       taskID,
		SignKey:      signKey,
	}

	if err := db.CreateTask(task); err != nil {
		return fmt.Errorf("failed to create task: %w", err)
	}

	// 在数据库创建成功后输出信息
	// fmt.Printf("Task created successfully! \nTask ID: %s\n", taskID)
	return nil
}

// 工作协程函数
func worker(ctx context.Context, No string) {

	c := req.C()
	c.SetRedirectPolicy(req.NoRedirectPolicy())
	c.SetCookieJar(nil)
	c.ImpersonateChrome()
	c.SetTLSFingerprintRandomized()
	c.SetCommonHeader("Content-Type", "application/octet-stream")
	c.SetCommonHeader("Origin", "https://app.nexus.xyz")
	c.SetCommonHeader("Referer", "https://app.nexus.xyz/")
	c.SetTimeout(time.Duration(120) * time.Second)

	// c.SetProxyURL("http://127.0.0.1:2025")

	for {
		select {
		case <-ctx.Done():
			return // 收到停止信号
		default:
			// 准备请求
			queue := db.GetTaskCount("pending")
			if queue >= config.Queue {
				// log.Println("线程" + No + " 待处理任务：" + strconv.Itoa(queue) + "，队列过多，暂不获取任务！")
				time.Sleep(10 * time.Second)
				continue
			}
			node := nodes.GetNextNode()
			node_key := GenKey()
			log.Println("线程" + No + " 节点" + node.NodeId + " 获取任务...")
			r := c.R()
			r.SetBody(GetProofTaskRequest(node.NodeId, node_key, int(node.NodeType)))
			resp, err := r.Send("POST", "https://beta.orchestrator.nexus.xyz/v3/tasks")
			if err != nil {
				log.Println("线程"+No+" 节点"+node.NodeId+" 获取任务失败：", err)
				continue
			}
			bin := resp.Bytes()

			if strings.Contains(string(bin), "Node has too many tasks") {
				// log.Println("线程"+No+" 节点"+node.NodeId+" 获取任务失败：", string(bin))
				log.Println("线程" + No + " 节点" + node.NodeId + " 获取任务失败：节点任务过多，该节点已无法使用，可进行删除，删除后需重启服务端。")

				continue
			} else if strings.Contains(string(bin), "Node not found") {
				// log.Println("线程"+No+" 节点"+node.NodeId+" 获取任务失败：", string(bin))
				log.Println("线程" + No + " 节点" + node.NodeId + " 获取任务失败：节点未找到，如果进行了节点删除和添加，需要重启服务端。")

				continue
			} else if strings.Contains(string(bin), "Rate limit exceeded for node") {
				log.Println("线程" + No + " 节点" + node.NodeId + " 获取任务失败：429错误，获取任务频繁，尝试增加节点数量、钱包数量、减少配置中的worker和queue数。")
				continue
			}
			task := &gen.GetProofTaskResponse{}
			err = proto.Unmarshal(bin, task)
			if err != nil {
				log.Println("线程"+No+" 节点"+node.NodeId+" 获取任务失败：", string(bin))
				continue
			}
			CreateTask(task.ProgramId, base64.StdEncoding.EncodeToString(task.PublicInputs), task.TaskId, node_key)
			log.Println("线程" + No + " 节点" + node.NodeId + " 获取任务获取成功，已入库！")

		}
	}
}

// Config 定义配置结构
type Config struct {
	Host    string `yaml:"host"`
	Address string `yaml:"address"`
	Port    int    `yaml:"port"`
	Worker  int    `yaml:"worker"`
	Queue   int    `yaml:"queue"`
}

var config Config

func main() {
	file, err := os.ReadFile("./nexus_server.txt")
	if err != nil {
		log.Println("无配置文件或配置文件错误，已使用默认配置，请前往控制面板设置钱包地址！")
		// fmt.Printf("读取配置文件失败: %v\n", err)
		// scanner := bufio.NewScanner(os.Stdin)
		// scanner.Scan()
		config = Config{
			Host:    "127.0.0.1",
			Address: "",
			Port:    8182,
			Worker:  5,
			Queue:   20,
		}

	} else {
		err = yaml.Unmarshal(file, &config)
		if err != nil {
			log.Println("无配置文件或配置文件错误，已使用默认配置，请前往控制面板设置钱包地址！")
			config = Config{
				Host:    "127.0.0.1",
				Address: "",
				Port:    8182,
				Worker:  5,
				Queue:   20,
			}
		}
	}

	// 创建上下文用于关闭所有goroutine
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if config.Address != "" {
		log.Println("已加载钱包地址：" + config.Address)
		// 使用逗号分割字符串
		parts := strings.Split(config.Address, ",")

		// 遍历并打印分割结果
		for _, item := range parts {
			log.Println("----------------", item, "----------------")

			if !getNodes(item) {
				log.Println(item, "读取节点列表失败！")
				scanner := bufio.NewScanner(os.Stdin)
				scanner.Scan()
				return
			}
			log.Println("----------------------------------------------------------------------------")

		}
	}

	// 初始化数据库
	db, err = database.NewDatabase()
	if err != nil {
		log.Println("初始化数据库失败！")
		log.Println("Error:", err)
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Scan()
		return
	}
	defer db.Close()
	// 循环获取节点
	for i := 1; i <= config.Worker; i++ {
		go worker(ctx, strconv.Itoa(i)) // 每行启动一个goroutine
	}
	go func() {
		// 创建请求处理器
		taskHandler := handlers.NewTaskHandler(db)
		// 设置HTTP路由
		router := setupRouter(taskHandler)
		// 启动服务
		// fmt.Println("Server running on :")
		log.Println("节点服务端启动，运行端口：", config.Port)
		go func() {
			time.Sleep(3 * time.Second)
			url := "http://127.0.0.1:" + strconv.Itoa(config.Port)
			browser.OpenURL(url)
		}()

		if err := router.Run(":" + strconv.Itoa(config.Port)); err != nil {
			log.Fatalf("Server failed to start: %v", err)
		}

	}()

	// 等待退出信号
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)
	<-signalChan
	cancel() // 通知所有goroutine停止

}

// 并发限制中间件
func concurrencyLimitMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 在超时时间内等待获取信号量（这里使用10秒超时）
		select {
		case sem <- struct{}{}: // 获取信号量槽位
			defer func() { <-sem }() // 请求完成后释放槽位
			c.Next()                 // 继续处理请求
		case <-time.After(10 * time.Second): // 等待超时
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error": "系统繁忙，请稍后再试",
			})
			c.Abort()
		}
	}
}
func setupRouter(handler *handlers.TaskHandler) *gin.Engine {
	gin.DisableConsoleColor()
	gin.DefaultWriter = io.Discard // 所有日志输出到 io.Discard
	gin.SetMode(gin.ReleaseMode)

	router := gin.Default()
	router.Use(gin.LoggerWithConfig(gin.LoggerConfig{
		Output: nil, // 禁用日志输出
	}))
	// 加载模板
	// 注册并发限制中间件
	router.Use(concurrencyLimitMiddleware())

	router.SetHTMLTemplate(template.Must(template.New("").ParseFS(htmlFS, "templates/*")))
	// 推荐：引入js css等  例如j.js  访问地址为 localhost:8080/asset/j.js
	router.Any("/static/*filepath", func(c *gin.Context) {
		staticServer := http.FileServer(http.FS(assetFS))
		staticServer.ServeHTTP(c.Writer, c.Request)
	})

	// router.LoadHTMLGlob("./templates/*")
	// router.Static("/static", "./static")
	router.GET("/", func(c *gin.Context) {
		c.HTML(200, "index.html", nil)
	})

	router.GET("/getConfig", func(c *gin.Context) {
		c.JSON(http.StatusOK, config)
	})
	router.POST("/setConfig", func(c *gin.Context) {
		var _config Config
		if err := c.ShouldBindJSON(&_config); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid JSON: " + err.Error()})
			return
		}

		if _config.Host == "" || _config.Address == "" || _config.Port == 0 || _config.Queue == 0 || _config.Worker == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "需要填写完整"})
			return
		}
		yamlData, err := yaml.Marshal(&_config)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "YAML编码失败: " + err.Error()})
			return
		}
		err = os.WriteFile("./nexus_server.txt", yamlData, 0644)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "写入文件失败: " + err.Error()})
			return
		}

		type ClientConfig struct {
			Host string `yaml:"host"`
			Port int    `yaml:"port"`
		}
		clientConfig := ClientConfig{
			Host: _config.Host,
			Port: _config.Port,
		}

		yamlDataClient, err := yaml.Marshal(&clientConfig)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "YAML编码失败: " + err.Error()})
			return
		}
		err = os.WriteFile("./nexus_client.txt", yamlDataClient, 0644)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "写入文件失败: " + err.Error()})
			return
		}
		log.Println("配置文件已更新，请重启服务端！")

		c.JSON(http.StatusOK, config)
	})

	// 任务管理API
	taskGroup := router.Group("/tasks")
	{
		// taskGroup.POST("", handler.CreateTask)
		taskGroup.DELETE("", handler.DeleteTask)

		// 新增API端点
		taskGroup.GET("/getTaskStats", handler.GetTaskStats) // 获取任务状态
		taskGroup.GET("/pick", handler.PickTask)             // 提取任务
		taskGroup.POST("/submit", handler.SubmitResult)      // 提交任务结果

		taskGroup.POST("/clientHeart", handler.ClientHeart)   // 客户端心跳
		taskGroup.GET("/clientList", handler.GetClientStates) // 获取客户端状态

	}

	return router
}
