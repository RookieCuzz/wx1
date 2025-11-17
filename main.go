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
	//test
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
		if scheme == "" {
			scheme = "http"
		}
		h := r.Header.Get("X-Forwarded-Host")
		if h == "" { h = r.Host }
		loginURL := scheme + "://" + h + "/wechat/loginU?sid=" + sid
		log.Printf("login_qr scheme=%s host=%s url=%s", scheme, h, loginURL)
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
				  var auth = document.querySelector('#wx-auth a');
				  if (auth) { auth.href = '/wechat/loginU?sid=' + encodeURIComponent(sid); }
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
		if sid == "" {
			http.Redirect(w, r, "/wechat/login", http.StatusFound)
			return
		}
        
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover"><title>授权绑定</title>
		<style>
		  html,body{height:100%}
		  body{margin:0; background:#1a1a1a; color:#fff; font-family: monospace; display:flex; align-items:center; justify-content:center; image-rendering: pixelated; padding-top: env(safe-area-inset-top); padding-bottom: env(safe-area-inset-bottom);}
		  .bg{position:fixed; inset:0; background:
			linear-gradient(#222 1px, transparent 1px),
			linear-gradient(90deg, #222 1px, transparent 1px);
			background-size: 16px 16px, 16px 16px; pointer-events:none; opacity:.4}
		  .card{position:relative; width:94%; max-width:600px; padding:28px; background:#2b2b2b; border:4px solid #000; box-shadow: 0 0 0 4px #3f3f3f, 0 0 0 8px #000; text-align:center}
		  .title{font-size:24px; letter-spacing:2px; text-shadow:2px 2px #000}
		  .desc{margin:10px 0 18px; color:#bfbfbf}
		  .badge{display:block; width:max-content; margin:0 auto 22px; padding:10px 14px; background:#3a3a3a; border:3px solid #000; box-shadow:2px 2px #000; font-size:14px}
		  .btn{display:block; width:max-content; margin:16px auto 0; padding:16px 24px; font-size:20px; color:#000; background:#6cc24a; border:4px solid #000; box-shadow:0 6px #000, 0 0 0 6px #2e5f23; text-transform:uppercase; letter-spacing:3px; cursor:pointer}
		  .btn:hover{filter:brightness(1.05)}
		  .btn:active{transform:translateY(4px); box-shadow:0 4px #000, 0 0 0 6px #2e5f23}
		  .btn[disabled]{opacity:.7; filter:saturate(.8) brightness(.95); cursor:not-allowed; box-shadow:0 6px #000, 0 0 0 8px #2e5f23}
		  @keyframes bob{0%,100%{transform:translateY(0)}50%{transform:translateY(-6px)}}
		  .btn.locked{animation:bob 1.2s ease-in-out infinite; will-change: transform}
		  .npc{width:96px; height:96px; margin:0 auto 12px; background:
			radial-gradient(circle at 50% 35%, #ffec9a 0 18%, transparent 19%),
			radial-gradient(circle at 35% 50%, #000 0 6%, transparent 7%),
			radial-gradient(circle at 65% 50%, #000 0 6%, transparent 7%),
			linear-gradient(#ff9f43 0 0) center/60% 20% no-repeat,
			linear-gradient(#ff9f43 0 0) center 80%/100% 22% no-repeat;
			image-rendering: pixelated; border:4px solid #000; box-shadow:2px 2px #000; background-color:#ffcf6b}
		  @media (max-width: 480px){
			.card{width:94%; max-width:480px; padding:24px}
			.title{font-size:22px}
			.npc{width:88px; height:88px}
			.badge{font-size:13px}
			.btn{width:100%; font-size:17px; padding:14px 10px; box-shadow:0 5px #000, 0 0 0 6px #2e5f23}
		  }
		  @media (max-width: 360px){
			.card{width:96%; max-width:360px; padding:20px}
			.title{font-size:20px}
			.npc{width:80px; height:80px}
			.btn{width:100%; font-size:15px; padding:12px 8px; background:#6cc24a}
		  }
		</style></head><body>
		<div class="bg"></div>
		<div class="card">
		  <div class="npc"></div>
		  <div class="title">像素授权绑定</div>
		  <div class="desc">此页面需在微信客户端内打开</div>
		  <div id="sid" class="badge"></div>
		  <button id="go" class="btn">同意授权</button>
		</div>
		<script>
		  (function(){
			var btn = document.getElementById('go');
			var sidText = document.getElementById('sid');
			var SID = '`)
		fmt.Fprint(w, sid)
		fmt.Fprint(w, `';
			sidText.textContent = '会话ID: ' + SID;
			var locked = false;
			btn.onclick = function(){
			  if(locked) return;
			  locked = true;
			  btn.classList.add('locked');
			  btn.disabled = true;
			  btn.innerText = '处理中...';
			  setTimeout(function(){ location.href = '/wechat/oauth_go?sid=' + encodeURIComponent(SID); }, 30);
			};
		  })();
		</script>
		</body></html>`)
	})

	// 发起网页授权跳转（将 sid 通过 state 传递到回调）
	http.HandleFunc("/wechat/oauth_go", func(w http.ResponseWriter, r *http.Request) {
        sid := r.URL.Query().Get("sid")
		if sid == "" {
			http.Redirect(w, r, "/wechat/login", http.StatusFound)
			return
		}
		scheme := r.Header.Get("X-Forwarded-Proto")
		if scheme == "" { scheme = "http" }
		h := r.Header.Get("X-Forwarded-Host")
		if h == "" { h = r.Host }
		cb := scheme + "://" + h + "/wechat/callback"
		oauth := officialAccount.GetOauth()
		url, err := oauth.GetRedirectURL(cb, "snsapi_userinfo", sid)
		log.Printf("oauth_go rawQuery=%s sid=%s ua=%s", r.URL.RawQuery, sid, r.Header.Get("User-Agent"))
		log.Printf("oauth_go scheme=%s host=%s cb=%s sid=%s url=%s", scheme, h, cb, sid, url)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		http.Redirect(w, r, url, http.StatusFound)
	})

	// 合并 OAuth 回调与消息回调为同一路径
	http.HandleFunc("/wechat/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code != "" {
			sid := r.URL.Query().Get("state")
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
			return
		}

		srv := officialAccount.GetServer(r, w)
		srv.SetMessageHandler(func(msg *message.MixMessage) *message.Reply {
			switch msg.MsgType {
			case message.MsgTypeText:
				text := message.NewText("你发送的是: " + msg.Content)
				return &message.Reply{MsgType: message.MsgTypeText, MsgData: text}
			case message.MsgTypeEvent:
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
