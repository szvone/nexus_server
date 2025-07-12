package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	crand "crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"nexserver/database"
	"nexserver/gen"
	"nexserver/handlers"
	"nexserver/models"
	"nexserver/nodes"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gin-gonic/gin"
	"github.com/imroc/req/v3"

	"google.golang.org/protobuf/proto"
)

var db *database.Database

// 工作协程函数
func getNodes() bool {

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
	resp, err := r.Send("GET", "https://beta.orchestrator.nexus.xyz/v3/users/"+config.Address)
	if err != nil {
		fmt.Println("读取节点失败：{}", err)
		return false
	}
	res := &gen.UserResponse{}

	proto.Unmarshal(resp.Bytes(), res)
	nodes.StoreNodes(res)
	if len(res.Nodes) == 50 {
		resp2, err := r.Send("GET", "https://beta.orchestrator.nexus.xyz/v3/nodes/"+res.UserId+"/"+res.Nodes[len(res.Nodes)-1].NodeId)
		if err != nil {
			fmt.Println("读取节点失败：{}", err)
			return false
		}
		res2 := &gen.UserResponse{}
		proto.Unmarshal(resp2.Bytes(), res2)
		nodes.StoreNodes(res2)
	}
	log.Println("读取到节点节点数量：", len(nodes.GetAllNodes()))
	if len(nodes.GetAllNodes()) == 0 {
		log.Println("节点节点数量不足，请创建节点后再运行！")
		return false
	}
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
				log.Println("线程"+No+" 节点"+node.NodeId+" 获取任务失败：", string(bin))
				continue
			} else if strings.Contains(string(bin), "Node not found") {
				log.Println("线程"+No+" 节点"+node.NodeId+" 获取任务失败：", string(bin))
				continue
			} else if strings.Contains(string(bin), "Rate limit exceeded for node") {
				log.Println("线程"+No+" 节点"+node.NodeId+" 获取任务失败：", string(bin))
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
	Address string `yaml:"address"`
	Port    int    `yaml:"port"`
	Worker  int    `yaml:"worker"`
	Queue   int    `yaml:"queue"`
}

var config Config

func main() {
	file, err := os.ReadFile("./nexus_server.txt")
	if err != nil {
		fmt.Printf("读取配置文件失败: %v\n", err)
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Scan()
		return
	}
	err = yaml.Unmarshal(file, &config)
	if err != nil {
		fmt.Printf("配置文件读取失败: %v\n", err)
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Scan()
		return
	}
	log.Println("钱包地址：" + config.Address)
	log.Println("读取节点列表...")
	if !getNodes() {
		log.Println("读取节点列表失败！")
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Scan()
		return
	}
	// 初始化数据库
	db, _ = database.NewDatabase()

	defer db.Close()

	// 创建上下文用于关闭所有goroutine
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
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

func setupRouter(handler *handlers.TaskHandler) *gin.Engine {
	gin.DisableConsoleColor()
	gin.DefaultWriter = io.Discard // 所有日志输出到 io.Discard
	gin.SetMode(gin.ReleaseMode)

	router := gin.Default()
	router.Use(gin.LoggerWithConfig(gin.LoggerConfig{
		Output: nil, // 禁用日志输出
	}))
	// 加载模板
	router.LoadHTMLGlob("./templates/*")
	router.Static("/static", "./static")
	router.GET("/", func(c *gin.Context) {
		c.HTML(200, "index.html", nil)
	})
	// 任务管理API
	taskGroup := router.Group("/tasks")
	{
		taskGroup.POST("", handler.CreateTask)
		taskGroup.GET("/:task_id", handler.GetTask)
		taskGroup.PUT("/:task_id", handler.UpdateTask)
		taskGroup.DELETE("/:task_id", handler.DeleteTask)
		taskGroup.GET("", handler.ListTasks)

		// 新增API端点
		taskGroup.GET("/getTaskStats", handler.GetTaskStats) // 提取任务
		taskGroup.GET("/pick", handler.PickTask)             // 提取任务
		taskGroup.POST("/submit", handler.SubmitResult)      // 提交任务结果
	}

	return router
}
