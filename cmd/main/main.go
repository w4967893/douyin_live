package main

import (
	"douyinlive"
	"douyinlive/config"
	"douyinlive/database"
	"douyinlive/generated/douyin"
	"douyinlive/utils"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/spf13/pflag"
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

type responseData struct {
	RoomId int `json:"room_id"`
	Status int `json:"status"`
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
	http.HandleFunc("/ws/start", func(w http.ResponseWriter, r *http.Request) {
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
			// 与客户端断开连接，持久化消息
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

			if string(message) == "ping" {
				pong, _ := json.Marshal("pong")
				if err := conn.WriteMessage(websocket.TextMessage, pong); err != nil {
					log.Printf("发送心跳回应到客户端失败: %v\n", err)
				}
				continue
			}

			var liveParam LiveParam
			err = json.Unmarshal(message, &liveParam)
			if err != nil {
				log.Printf("消息解析失败: %v\n", err)
				break
			}

			if liveParam.RoomId != 0 && liveParam.LiveId != 0 {
				isLiving := utils.InSlice(douyinlive.LivingRoomIds, liveParam.RoomId)
				// 如果room id没有在抓取弹幕信息，继续执行
				if !isLiving {
					douyinlive.LivingRoomIds = append(douyinlive.LivingRoomIds, liveParam.RoomId)
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
				} else {
					livingNotificationMap := map[string]interface{}{
						"is_ok": true,
						"data": responseData{
							Status: 3,
							RoomId: liveParam.RoomId,
						},
					}
					livingNotification, _ := json.Marshal(livingNotificationMap)
					if err := conn.WriteMessage(websocket.TextMessage, livingNotification); err != nil {
						log.Printf("发送消息到客户端失败: %v\n", err)
					}
					log.Printf("room id %v 已在抓取弹幕信息\n", liveParam.RoomId)
				}
			}
		}
	})

	http.HandleFunc("/api/stop", func(w http.ResponseWriter, r *http.Request) {
		roomIdStr := r.URL.Query().Get("room_id")
		roomId, _ := strconv.Atoi(roomIdStr)

		// 判断该room id是否正在抓取弹幕
		isLiving := utils.InSlice(douyinlive.LivingRoomIds, roomId)

		responseData := map[string]interface{}{
			"is_ok":   false,
			"message": "room id 并未在抓取弹幕信息",
		}
		if isLiving == true {
			if douyinlive.Close(roomId) {
				responseData = map[string]interface{}{
					"is_ok":   true,
					"message": "success",
				}
			} else {
				responseData = map[string]interface{}{
					"is_ok":   false,
					"message": "room id正在执行解析消息",
				}
			}
		}
		// 将数据编码为 JSON 格式
		jsonResponse, _ := json.Marshal(responseData)
		w.Write(jsonResponse)
	})

	// 启动 WebSocket 服务器
	http.ListenAndServe(":18080", corsMiddleware(http.DefaultServeMux))
	log.Printf("WebSocket 服务启动成功，地址为: ws://127.0.0.1:18080/\n")
}

// Subscribe 处理订阅的更新
func Subscribe(eventData *douyin.Message) {
	//关闭通知
	if eventData.Method == "PassiveOffNotification" {
		offNotificationMap := map[string]interface{}{
			"is_ok": true,
			"data": responseData{
				Status: 1,
				RoomId: eventData.RoomId,
			},
		}
		offNotification, _ := json.Marshal(offNotificationMap)
		RangeConnections(func(agentID string, conn *websocket.Conn) {
			if err := conn.WriteMessage(websocket.TextMessage, offNotification); err != nil {
				log.Printf("发送消息到客户端 %s 失败: %v\n", agentID, err)
			}
		})
	}

	if eventData.Method == "ErrNotification" {
		errNotificationMap := map[string]interface{}{
			"is_ok": false,
			"data": responseData{
				Status: 1,
				RoomId: eventData.RoomId,
			},
		}
		errNotification, _ := json.Marshal(errNotificationMap)
		RangeConnections(func(agentID string, conn *websocket.Conn) {
			if err := conn.WriteMessage(websocket.TextMessage, errNotification); err != nil {
				log.Printf("发送消息到客户端 %s 失败: %v\n", agentID, err)
			}
		})
	}

	if eventData.Method == "SuccessNotification" {
		successNotificationMap := map[string]interface{}{
			"is_ok": true,
			"data": responseData{
				Status: 0,
				RoomId: eventData.RoomId,
			},
		}
		successNotification, _ := json.Marshal(successNotificationMap)
		RangeConnections(func(agentID string, conn *websocket.Conn) {
			if err := conn.WriteMessage(websocket.TextMessage, successNotification); err != nil {
				log.Printf("发送消息到客户端 %s 失败: %v\n", agentID, err)
			}
		})
	}

	if eventData.Method == "ActiveOffNotification" {
		activeOffNotificationMap := map[string]interface{}{
			"is_ok": true,
			"data": responseData{
				Status: 5,
				RoomId: eventData.RoomId,
			},
		}
		activeOffNotification, _ := json.Marshal(activeOffNotificationMap)
		RangeConnections(func(agentID string, conn *websocket.Conn) {
			if err := conn.WriteMessage(websocket.TextMessage, activeOffNotification); err != nil {
				log.Printf("发送消息到客户端 %s 失败: %v\n", agentID, err)
			}
		})
	}

	//msg, err := utils.MatchMethod(eventData.Method)
	//if err != nil {
	//	if unknown {
	//		log.Printf("未知消息，无法处理: %v, %s\n", err, hex.EncodeToString(eventData.Payload))
	//	}
	//	return
	//}
	//
	//if msg != nil {
	//	if err := proto.Unmarshal(eventData.Payload, msg); err != nil {
	//		log.Printf("反序列化失败: %v, 方法: %s\n", err, eventData.Method)
	//		return
	//	}
	//
	//	marshal, err := protojson.Marshal(msg)
	//	if err != nil {
	//		log.Printf("JSON 序列化失败: %v\n", err)
	//		return
	//	}
	//
	//	RangeConnections(func(agentID string, conn *websocket.Conn) {
	//		if err := conn.WriteMessage(websocket.TextMessage, marshal); err != nil {
	//			log.Printf("发送消息到客户端 %s 失败: %v\n", agentID, err)
	//		}
	//	})
	//}
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

// corsMiddleware 返回一个处理 CORS 请求的中间件
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 设置允许的源，这里使用通配符 * 表示允许所有源
		w.Header().Set("Access-Control-Allow-Origin", "*")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
