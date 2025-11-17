package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/silenceper/wechat/v2"
	"github.com/silenceper/wechat/v2/cache"
	offConfig "github.com/silenceper/wechat/v2/officialaccount/config"
	"github.com/silenceper/wechat/v2/officialaccount/message"
	_ "github.com/silenceper/wechat/v2/officialaccount/server"
)

func main() {
	// 环境变量或配置读取
	appID := os.Getenv("WECHAT_APPID")         // 公众号 AppID
	appSecret := os.Getenv("WECHAT_APPSECRET") // 公众号 AppSecret
	token := os.Getenv("WECHAT_TOKEN")         // 在后台填写的 Token
	aesKey := os.Getenv("WECHAT_AESKEY")       // 在后台填写的 EncodingAESKey

	wc := wechat.NewWechat()
	memory := cache.NewMemory() // 简单内存缓存，生产中可换 Redis 等

	cfg := &offConfig.Config{
		AppID:          appID,
		AppSecret:      appSecret,
		Token:          token,
		EncodingAESKey: aesKey,
		Cache:          memory,
	}

	officialAccount := wc.GetOfficialAccount(cfg)

	http.HandleFunc("/wechat/callback", func(w http.ResponseWriter, r *http.Request) {
		srv := officialAccount.GetServer(r, w)
		// 设置消息／事件处理函数
		srv.SetMessageHandler(func(msg *message.MixMessage) *message.Reply {
			// 根据不同 msg.MsgType 或 msg.Event 做逻辑
			switch msg.MsgType {
			case message.MsgTypeText:
				// echo 用户文本消息
				text := message.NewText("你发送的是: " + msg.Content)
				return &message.Reply{MsgType: message.MsgTypeText, MsgData: text}
			case message.MsgTypeEvent:
				if msg.Event == "subscribe" {
					text := message.NewText("欢迎关注！")
					return &message.Reply{MsgType: message.MsgTypeText, MsgData: text}
				}
			}
			// 默认不回复
			return nil
		})

		err := srv.Serve()
		if err != nil {
			log.Printf("Serve error: %v", err)
			fmt.Fprintf(w, "error")
			return
		}
		srv.Send()
	})

	addr := ":28083"
	fmt.Println("Server listening on", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("listen and serve error: %v", err)
	}
}
