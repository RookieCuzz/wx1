package main

import (
    "crypto/rand"
    "encoding/base64"
    "encoding/hex"
    "encoding/json"
    "fmt"
    "log"
    "net/http"
    "os"
    "strings"
    "sync"
    "time"

    "github.com/silenceper/wechat/v2"
    "github.com/silenceper/wechat/v2/cache"
    offConfig "github.com/silenceper/wechat/v2/officialaccount/config"
    "github.com/silenceper/wechat/v2/officialaccount/message"
    _ "github.com/silenceper/wechat/v2/officialaccount/server"
    qrcode "github.com/skip2/go-qrcode"
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

	// 简单的登录会话状态存储
	type loginState struct {
		OpenID    string    `json:"openid"`
		UnionID   string    `json:"unionid,omitempty"`
		ScannedAt time.Time `json:"scanned_at"`
	}
	var (
		loginMu       sync.RWMutex
		loginSessions = map[string]loginState{}
	)

	// 生成随机会话ID
	newSID := func() string {
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		return hex.EncodeToString(b)
	}

	// 从事件键提取会话ID
	extractSID := func(eventKey string) string {
		// subscribe 的场景值形如：qrscene_login:<sid>
		if strings.HasPrefix(eventKey, "qrscene_") {
			eventKey = strings.TrimPrefix(eventKey, "qrscene_")
		}
		if strings.HasPrefix(eventKey, "login:") {
			return strings.TrimPrefix(eventKey, "login:")
		}
		return ""
	}

    // 登录二维码接口：返回 sid 与二维码图片（data URL），用于在PC页面引导用户在微信内打开 /wechat/loginU
    http.HandleFunc("/wechat/login_qr", func(w http.ResponseWriter, r *http.Request) {
        sid := r.URL.Query().Get("sid")
        if sid == "" {
            sid = newSID()
        }
        scheme := r.Header.Get("X-Forwarded-Proto")
        if scheme == "" { scheme = "http" }
        loginURL := scheme + "://" + r.Host + "/wechat/loginU?sid=" + sid
        png, err := qrcode.Encode(loginURL, qrcode.Medium, 240)
        if err != nil {
            w.WriteHeader(http.StatusInternalServerError)
            _ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
            return
        }
        dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
        w.Header().Set("Content-Type", "application/json")
        _ = json.NewEncoder(w).Encode(map[string]interface{}{
            "sid":     sid,
            "qr_data": dataURL,
        })
    })

	// 登录状态轮询接口
	http.HandleFunc("/wechat/login_status", func(w http.ResponseWriter, r *http.Request) {
		sid := r.URL.Query().Get("sid")
		if sid == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "missing sid"})
			return
		}
		loginMu.RLock()
		st, ok := loginSessions[sid]
		loginMu.RUnlock()
		if !ok {
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "waiting"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  "scanned",
			"openid":  st.OpenID,
			"unionid": st.UnionID,
			"scanned": st.ScannedAt.Format(time.RFC3339),
		})
	})

	// 简单前端页面
	http.HandleFunc("/wechat/login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
        fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><title>微信扫码登录</title></head><body>
			<h3>微信扫码登录</h3>
			<div id="qr"></div>
			<div id="status">等待扫码...</div>
			<p id="wx-auth" style="display:none"><a href="/wechat/loginU">或点击在微信客户端内进行网页授权获取UnionID</a></p>
			<script>
			(async function(){
			  const res = await fetch('/wechat/login_qr');
			  const data = await res.json();
			  const img = document.createElement('img');
			  img.src = data.qr_data;
			  img.style.width = '240px';
			  document.getElementById('qr').appendChild(img);
			  const sid = data.sid;
			  async function poll(){
			    const r = await fetch('/wechat/login_status?sid='+sid);
			    const s = await r.json();
			    if(s.status === 'scanned'){
			      document.getElementById('status').innerText = '登录成功，OpenID: '+s.openid+(s.unionid ? ('，UnionID: '+s.unionid) : '');
			    }else{
			      setTimeout(poll, 2000);
			    }
			  }
			  poll();
			  if(navigator.userAgent.indexOf('MicroMessenger') !== -1){
			    document.getElementById('wx-auth').style.display = 'block';
			  }
			})();
			</script>
			</body></html>`)
    })

    // 在微信内打开的预制登录页，点击按钮后再发起网页授权
    http.HandleFunc("/wechat/loginU", func(w http.ResponseWriter, r *http.Request) {
        sid := r.URL.Query().Get("sid")
        w.Header().Set("Content-Type", "text/html; charset=utf-8")
        fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><title>授权登录</title></head><body>
        <h3>授权登录</h3>
        <p>此页面需在微信客户端内打开。</p>
        <p>会话ID: %s</p>
        <button id="go">同意授权</button>
        <script>
        (function(){
          document.getElementById('go').onclick = function(){
            location.href = '/wechat/oauth_go?sid=%s';
          };
        })();
        </script>
        </body></html>`, sid, sid)
    })

    // 发起网页授权跳转（将 sid 通过 state 传递到回调）
    http.HandleFunc("/wechat/oauth_go", func(w http.ResponseWriter, r *http.Request) {
        sid := r.URL.Query().Get("sid")
        scheme := r.Header.Get("X-Forwarded-Proto")
        if scheme == "" { scheme = "http" }
        cb := scheme + "://" + r.Host + "/wechat/callbackU"
        oauth := officialAccount.GetOauth()
        url, err := oauth.GetRedirectURL(cb, "snsapi_userinfo", sid)
        if err != nil {
            w.WriteHeader(http.StatusInternalServerError)
            _ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
            return
        }
        http.Redirect(w, r, url, http.StatusFound)
    })

    http.HandleFunc("/wechat/callbackU", func(w http.ResponseWriter, r *http.Request) {
        code := r.URL.Query().Get("code")
        sid := r.URL.Query().Get("state")
        if code == "" {
            w.WriteHeader(http.StatusBadRequest)
            _ = json.NewEncoder(w).Encode(map[string]string{"error": "missing code"})
            return
        }
        oauth := officialAccount.GetOauth()
        tok, err := oauth.GetUserAccessToken(code)
        if err != nil {
            w.WriteHeader(http.StatusBadRequest)
            _ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
            return
        }
        info, err := oauth.GetUserInfo(tok.AccessToken, tok.OpenID, "zh_CN")
        if err != nil {
            w.WriteHeader(http.StatusBadRequest)
            _ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
            return
        }
        if sid != "" {
            loginMu.Lock()
            loginSessions[sid] = loginState{OpenID: info.OpenID, UnionID: info.Unionid, ScannedAt: time.Now()}
            loginMu.Unlock()
        }
        w.Header().Set("Content-Type", "text/html; charset=utf-8")
        fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><title>授权完成</title></head><body>
        <p>授权完成</p>
        <p>OpenID: %s</p>
        <p>UnionID: %s</p>
        </body></html>`, info.OpenID, info.Unionid)
    })

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
				// 处理扫码登录事件
				if msg.Event == "subscribe" || msg.Event == "SCAN" {
					sid := extractSID(msg.EventKey)
					if sid != "" {
						openid := string(msg.FromUserName)
						var union string
						if openid != "" {
							userSvc := officialAccount.GetUser()
							if info, err := userSvc.GetUserInfo(openid); err == nil {
								union = info.UnionID
							}
						}
						loginMu.Lock()
						loginSessions[sid] = loginState{OpenID: openid, UnionID: union, ScannedAt: time.Now()}
						loginMu.Unlock()
					}
					if msg.Event == "subscribe" {
						text := message.NewText("欢迎关注！")
						return &message.Reply{MsgType: message.MsgTypeText, MsgData: text}
					}
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
