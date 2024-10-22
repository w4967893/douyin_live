package main

import (
	"douyinlive"
	"douyinlive/config"
	"douyinlive/database"
	"douyinlive/generated/douyin"
	"douyinlive/utils"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/spf13/pflag"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var (
	agentlist sync.Map
	unknown   bool
)

type LiveParam struct {
	RoomId int    `json:"room_id"`
	LiveId int    `json:"live_id"`
	Ping   string `json:"ping"`
}

func main() {
	var port string
	var room string
	pflag.StringVar(&port, "port", "18080", "WebSocket 服务端口")
	pflag.StringVar(&room, "room", "****", "抖音直播房间号")
	pflag.BoolVar(&unknown, "unknown", false, "是否输出未知源的pb消息")
	pflag.Parse()

	//加载配置配置文件
	config.Init()
	database.InitRMSDB(config.Conf.DbConf)

	// 创建 WebSocket 升级器
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true // 允许所有 CORS 请求，实际应用中应根据需要设置
		},
	}

	// 设置 WebSocket 路由
	http.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("升级 WebSocket 失败: %v\n", err)
			return
		}
		defer conn.Close()

		sec := r.Header.Get("Sec-WebSocket-Key")
		StoreConnection(sec, conn)
		log.Printf("当前连接数: %d\n", GetConnectionCount())

		defer func() {
			log.Printf("客户端 %s 断开连接\n", sec)
			DeleteConnection(sec)
		}()

		// 处理 WebSocket 消息
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				log.Printf("读取消息失败: %v\n", err)
				break
			}

			var liveParam LiveParam
			err = json.Unmarshal(message, &liveParam)
			if err != nil {
				log.Printf("消息解析失败: %v\n", err)
				break
			}

			if liveParam.RoomId != 0 && liveParam.LiveId != 0 {
				go func() {
					// 创建 DouyinLive 实例
					d, err := douyinlive.NewDouyinLive(strconv.Itoa(liveParam.RoomId))
					if err != nil {
						log.Fatalf("抖音链接失败: %v", err)
					}
					// 订阅事件
					d.Subscribe(Subscribe)
					// 开始处理
					d.Start(liveParam.RoomId, liveParam.LiveId)
				}()
			}
		}
	})

	http.HandleFunc("/stop", func(w http.ResponseWriter, r *http.Request) {
		roomIdStr := r.URL.Query().Get("room_id")
		roomId, _ := strconv.Atoi(roomIdStr)
		douyinlive.Close(roomId)
	})

	// 启动 WebSocket 服务器
	http.ListenAndServe(":18080", nil)
	log.Printf("WebSocket 服务启动成功，地址为: ws://127.0.0.1:18080/\n")
}

// Subscribe 处理订阅的更新
func Subscribe(eventData *douyin.Message) {
	//关闭通知
	if eventData.Method == "WebcastOffNotificationMessage" {
		RangeConnections(func(agentID string, conn *websocket.Conn) {
			if err := conn.WriteMessage(websocket.TextMessage, []byte("closed")); err != nil {
				log.Printf("发送消息到客户端 %s 失败: %v\n", agentID, err)
			}
		})
	}

	msg, err := utils.MatchMethod(eventData.Method)
	if err != nil {
		if unknown {
			log.Printf("未知消息，无法处理: %v, %s\n", err, hex.EncodeToString(eventData.Payload))
		}
		return
	}

	if msg != nil {
		if err := proto.Unmarshal(eventData.Payload, msg); err != nil {
			log.Printf("反序列化失败: %v, 方法: %s\n", err, eventData.Method)
			return
		}

		marshal, err := protojson.Marshal(msg)
		if err != nil {
			log.Printf("JSON 序列化失败: %v\n", err)
			return
		}

		RangeConnections(func(agentID string, conn *websocket.Conn) {
			if err := conn.WriteMessage(websocket.TextMessage, marshal); err != nil {
				log.Printf("发送消息到客户端 %s 失败: %v\n", agentID, err)
			}
		})
	}
}

// StoreConnection 储存 WebSocket 客户端连接
func StoreConnection(agentID string, conn *websocket.Conn) {
	agentlist.Store(agentID, conn)
}

// DeleteConnection 删除 WebSocket 客户端连接
func DeleteConnection(agentID string) {
	agentlist.Delete(agentID)
}

// RangeConnections 遍历 WebSocket 客户端连接
func RangeConnections(f func(agentID string, conn *websocket.Conn)) {
	agentlist.Range(func(key, value interface{}) bool {
		agentID, ok := key.(string)
		if !ok {
			return true // 跳过错误的键类型
		}
		conn, ok := value.(*websocket.Conn)
		if !ok {
			return true // 跳过错误的值类型
		}
		f(agentID, conn)
		return true
	})
}

// GetConnectionCount 获取当前连接数
func GetConnectionCount() int {
	count := 0
	agentlist.Range(func(key, value interface{}) bool {
		count++
		return true
	})
	return count
}
