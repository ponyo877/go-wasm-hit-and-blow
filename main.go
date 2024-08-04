//go:build js && wasm
// +build js,wasm

package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/url"
	"syscall/js"
	"time"

	"github.com/pion/webrtc/v3"
	"github.com/ponyo877/go-wasm-p2p-chat/go-ayame"

	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

var (
	wsScheme          string
	matchmakingOrigin string
	signalingOrigin   string
	recentGuess       *Guess
)

type mmReqMsg struct {
	UserID    string    `json:"user_id"`
	CreatedAt time.Time `json:"created_at"`
}

type mmResMsg struct {
	Type      string    `json:"type"`
	RoomID    string    `json:"room_id"`
	UserID    string    `json:"user_id"`
	CreatedAt time.Time `json:"created_at"`
}

func main() {
	mmURL := url.URL{Scheme: wsScheme, Host: matchmakingOrigin, Path: "/matchmaking"}
	signalingURL := url.URL{Scheme: wsScheme, Host: signalingOrigin, Path: "/signaling"}

	now := time.Now()
	userID, _ := shortHash(now)
	reqMsg, err := json.Marshal(mmReqMsg{
		UserID:    userID,
		CreatedAt: now,
	})
	if err != nil {
		log.Fatal(err)
	}
	var resMsg mmResMsg
	var dc *webrtc.DataChannel
	ch := make(chan *Guess)
	defer func() {
		if dc != nil {
			dc.Close()
		}
	}()
	var conn *ayame.Connection
	connected := make(chan bool)
	board := NewBoard()
	js.Global().Set("startSearchPlayer", js.FuncOf(func(_ js.Value, _ []js.Value) interface{} {
		go func() {
			ws, _, err := websocket.Dial(context.Background(), mmURL.String(), nil)
			if err != nil {
				log.Fatal(err)
			}
			defer ws.Close(websocket.StatusNormalClosure, "close connection")

			if err := ws.Write(context.Background(), websocket.MessageText, reqMsg); err != nil {
				log.Fatal(err)
			}
			logElem("[Sys]: Waiting match...\n")
			for {
				if err := wsjson.Read(context.Background(), ws, &resMsg); err != nil {
					log.Fatal(err)
					break
				}
				if resMsg.Type == "MATCH" {
					break
				}
			}
			ws.Close(websocket.StatusNormalClosure, "close connection")
			if resMsg.Type == "MATCH" {
				conn = ayame.NewConnection(signalingURL.String(), resMsg.RoomID, ayame.DefaultOptions(), false, false)
				conn.OnOpen(func(metadata *interface{}) {
					log.Println("Open")
					var err error
					dc, err = conn.CreateDataChannel("matchmaking-hit&blow", nil)
					if err != nil && err != fmt.Errorf("client does not exist") {
						log.Printf("CreateDataChannel error: %v", err)
						return
					}
					log.Printf("CreateDataChannel: label=%s", dc.Label())
					rand.NewSource(time.Now().UnixNano())
					seed := rand.Int()

					initTurn := NewTurnBySeed(seed)
					myHand := NewHandBySeed(seed)
					board.Start(myHand, initTurn)
					startMsg := Message{Type: "start", Turn: int(initTurn)}
					by, _ := json.Marshal(startMsg)
					if err := dc.SendText(string(by)); err != nil {
						handleError()
						return
					}
					dc.OnMessage(onMessage(dc, ch, board))
				})

				conn.OnConnect(func() {
					logElem("[Sys]: Matching! Start P2P chat not via server\n")
					conn.CloseWebSocketConnection()
					connected <- true
				})

				conn.OnDataChannel(func(c *webrtc.DataChannel) {
					log.Printf("OnDataChannel: label=%s", c.Label())
					if dc == nil {
						dc = c
					}
					dc.OnMessage(onMessage(dc, ch, board))
				})

				if err := conn.Connect(); err != nil {
					log.Fatal("Failed to connect Ayame", err)
				}
				select {
				case <-connected:
					return
				}
			}
		}()
		return js.Undefined()
	}))
	js.Global().Set("sendMessage", js.FuncOf(func(_ js.Value, _ []js.Value) interface{} {
		go func() {
			el := getElementByID("message")
			message := el.Get("value").String()
			if message == "" {
				js.Global().Call("alert", "Message must not be empty")
				return
			}
			if dc == nil {
				return
			}

			ch <- NewGuessFromText(message)
			logElem(fmt.Sprintf("[You]: %s\n", message))
			el.Set("value", "")
		}()
		return js.Undefined()
	}))
	select {}
}

func shortHash(now time.Time) (string, error) {
	h := sha256.New()
	if _, err := h.Write([]byte(now.String())); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:7], nil
}

type Message struct {
	Type  string `json:"type"`
	Turn  int    `json:"turn,omitempty"`
	Hit   int    `json:"hit,omitempty"`
	Blow  int    `json:"blow,omitempty"`
	Guess string `json:"guess,omitempty"`
}

func onMessage(dc *webrtc.DataChannel, ch chan *Guess, board *Board) func(webrtc.DataChannelMessage) {
	return func(msg webrtc.DataChannelMessage) {
		if !msg.IsString {
			return
		}
		var message Message
		if err := json.Unmarshal(msg.Data, &message); err != nil {
			log.Printf("Failed to unmarshal: %v", err)
			return
		}
		logElem(fmt.Sprintf("[Any]: %s\n", msg.Data))
		switch message.Type {
		case "start":
			// 非開室者Only: GameStart処理
			if board.IsInMenu() {
				initTurn := Turn(message.Turn).Reverse()
				rand.NewSource(time.Now().UnixNano())
				seed := rand.Int()
				myHand := NewHandBySeed(seed)
				board.Start(myHand, initTurn)
			}
			// 非開室者Only: 初回が後攻のときに開室者を初回guess処理に誘導
			if board.IsOpTurn() {
				startMsg := Message{Type: "start"}
				by, _ := json.Marshal(startMsg)
				if err := dc.SendText(string(by)); err != nil {
					handleError()
					return
				}
				return
			}
			// guess送信処理に続く
		case "guess":
			if board.IsMyTurn() {
				return
			}
			// 自分ターンへ遷移
			board.ToggleTurn()
			guess := NewGuessFromText(message.Guess)
			ans := board.CalcAnswer(guess)
			ansMsg := Message{Type: "answer", Hit: ans.Hit(), Blow: ans.Blow()}
			by, _ := json.Marshal(ansMsg)
			if err := dc.SendText(string(by)); err != nil {
				handleError()
				return
			}
			if ans.IsAllHit() {
				logElem("[Sys]: You Lose!\n")
				board.Finish()
				return
			}
			// guess送信処理に続く
		case "answer":
			if board.IsMyTurn() {
				return
			}
			ans := NewAnswer(message.Hit, message.Blow)
			qa := NewQA(recentGuess, ans)
			board.AddMyQA(qa)
			if ans.IsAllHit() {
				logElem("[Sys]: You Win!\n")
				board.Finish()
				return
			}
			return
		case "timeout":
			logElem("[Sys]: Opponent Timeout! You Win!\n")
			return
		default:
			return
		}
		if board.IsOpTurn() {
			return
		}

		// 60sの間にguessを送信する処理
		timeout := 60 * time.Second
		myGuess, isTO := board.WaitGuess(ch, timeout)
		recentGuess = myGuess
		if isTO {
			toMsg := Message{Type: "timeout"}
			by, _ := json.Marshal(toMsg)
			if err := dc.SendText(string(by)); err != nil {
				handleError()
				return
			}
			logElem("[Sys]: You Timeout! You Lose!\n")
			board.Finish()
			return
		}
		guessMsg := Message{Type: "guess", Guess: myGuess.String()}
		by, _ := json.Marshal(guessMsg)
		// 相手ターンへ遷移
		board.ToggleTurn()
		if err := dc.SendText(string(by)); err != nil {
			handleError()
			return
		}
	}
}

func logElem(msg string) {
	el := getElementByID("logs")
	el.Set("innerHTML", el.Get("innerHTML").String()+msg)
}

func handleError() {
	logElem("[Sys]: Maybe Any left, Please restart\n")
}

func getElementByID(id string) js.Value {
	return js.Global().Get("document").Call("getElementById", id)
}
