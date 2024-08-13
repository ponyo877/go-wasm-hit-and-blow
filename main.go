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
	"github.com/ponyo877/go-wasm-hit-and-blow/game"
	"github.com/ponyo877/go-wasm-hit-and-blow/go-ayame"

	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

var (
	wsScheme          string
	matchmakingOrigin string
	signalingOrigin   string
	recentGuess       *game.Guess
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
	ch := make(chan *game.Guess)
	defer func() {
		if dc != nil {
			dc.Close()
		}
	}()
	var conn *ayame.Connection
	connected := make(chan bool)
	board := game.NewBoard()

	js.Global().Set("Search", js.FuncOf(func(_ js.Value, _ []js.Value) interface{} {
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
					var err error
					dc, err = conn.CreateDataChannel("matchmaking-example", nil)
					if err != nil && err != fmt.Errorf("client does not exist") {
						log.Printf("CreateDataChannel error: %v", err)
						return
					}
					log.Printf("CreateDataChannel: label=%s", dc.Label())
					go func() {
						rand.NewSource(time.Now().UnixNano())
						seed := rand.Int()

						initTurn := game.NewTurnBySeed(seed)
						myHand := game.NewHandBySeed(seed)
						log.Printf("myHand(opener): %v", myHand)
						board.Start(myHand, initTurn)
						if board.IsMyTurnInit() {
							log.Printf("YOU FIRST!!!")
						}
						turn := int(initTurn)
						startMsg := Message{Type: "start", Turn: &turn}
						by, _ := json.Marshal(startMsg)
						time.Sleep(3 * time.Second)
						log.Printf("startMsg(opener): %v", string(by))
						if err := dc.SendText(string(by)); err != nil {
							log.Printf("failed to send startMsg: %v", err)
							return
						}
					}()

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
					log.Println("ready to recieve")
					dc.OnMessage(onMessage(dc, ch, board))
				})

				if err := conn.Connect(); err != nil {
					log.Fatal("failed to connect Ayame", err)
				}
				select {
				case <-connected:
					return
				}
			}
		}()
		return js.Undefined()
	}))
	js.Global().Set("SendGuess", js.FuncOf(func(_ js.Value, _ []js.Value) interface{} {
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

			ch <- game.NewGuessFromText(message)
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
	Turn  *int   `json:"turn,omitempty"`
	Hit   *int   `json:"hit,omitempty"`
	Blow  *int   `json:"blow,omitempty"`
	Guess string `json:"guess,omitempty"`
}

func onMessage(dc *webrtc.DataChannel, ch chan *game.Guess, board *game.Board) func(webrtc.DataChannelMessage) {
	return func(msg webrtc.DataChannelMessage) {
		log.Printf("recieve msg.Data: %s", string(msg.Data))
		if !msg.IsString {
			return
		}
		var message Message
		if err := json.Unmarshal(msg.Data, &message); err != nil {
			log.Printf("failed to unmarshal: %v", err)
			return
		}
		// logElem(fmt.Sprintf("[Any]: %s\n", msg.Data))
		switch message.Type {
		case "start":
			// 非開室者Only: GameStart処理
			if board.IsInMenu() {
				log.Printf("message.Turn: %v", *message.Turn)
				initTurn := game.Turn(*message.Turn).Reverse()
				rand.NewSource(time.Now().UnixNano())
				seed := rand.Int()
				myHand := game.NewHandBySeed(seed)
				log.Printf("myHand(unopener): %v", myHand)
				board.Start(myHand, initTurn)
				if board.IsMyTurnInit() {
					log.Printf("YOU FIRST!!!")
				}
			}
			// 非開室者Only: 初回が後攻のときに開室者を初回guess処理に誘導
			if board.IsOpTurn() {
				startMsg := Message{Type: "start"}
				by, _ := json.Marshal(startMsg)
				if err := dc.SendText(string(by)); err != nil {
					log.Printf("failed to send startMsg: %v", err)
					return
				}
				log.Printf("startMsg(unopener): %v", string(by))
				return
			}
			// guess送信処理に続く
		case "guess":
			if board.IsMyTurn() {
				return
			}
			// 自分ターンへ遷移
			board.ToggleTurn()
			guess := game.NewGuessFromText(message.Guess)
			ans := board.CalcAnswer(guess)
			hit, blow := ans.Hit(), ans.Blow()
			ansMsg := Message{Type: "answer", Hit: &hit, Blow: &blow}
			by, _ := json.Marshal(ansMsg)
			board.CountTurn()
			board.AddOpQA(game.NewQA(guess, ans))
			setScore(board, guess.View(), hit, blow)
			j := board.Judge()
			setJudge(j)
			log.Printf("ansMsg: %v", string(by))
			if err := dc.SendText(string(by)); err != nil {
				log.Printf("failed to send ansMsg: %v", err)
				return
			}
			if j != game.NotYet {
				board.Finish()
				return
			}
			// guess送信処理に続く
		case "answer":
			if board.IsMyTurn() {
				return
			}
			ans := game.NewAnswer(*message.Hit, *message.Blow)
			board.CountTurn()
			board.AddMyQA(game.NewQA(recentGuess, ans))
			setScore(board, recentGuess.View(), ans.Hit(), ans.Blow())
			j := board.Judge()
			setJudge(j)
			if j != game.NotYet {
				board.Finish()
				return
			}
			return
		case "timeout":
			setJudge(game.Win)
			board.Finish()
			return
		default:
			return
		}
		if board.IsOpTurn() {
			return
		}

		// 60sの間にguessを送信する処理
		timeout := 60
		gracePeriod := 1
		toChan := make(chan struct{})
		go func(to int, ch chan struct{}) {
			for {
				select {
				case <-ch:
					log.Printf("catch guess!!!!")
					return
				default:
					to--
					setTimer(to)
					if to <= 0 {
						return
					}
					time.Sleep(1 * time.Second)
				}
			}
		}(timeout, toChan)
		myGuess, isTO := board.WaitGuess(ch, toChan, time.Duration(timeout+gracePeriod)*time.Second)
		recentGuess = myGuess
		if isTO {
			toMsg := Message{Type: "timeout"}
			by, _ := json.Marshal(toMsg)
			if err := dc.SendText(string(by)); err != nil {
				log.Printf("failed to send toMsg: %v", err)
				return
			}
			logElem("[Sys]: You Timeout! You Lose!\n")
			setJudge(game.Lose)
			board.Finish()
			return
		}
		guessMsg := Message{Type: "guess", Guess: myGuess.Msg()}
		by, _ := json.Marshal(guessMsg)
		// 相手ターンへ遷移
		board.ToggleTurn()
		if err := dc.SendText(string(by)); err != nil {
			log.Printf("failed to send guessMsg: %v", err)
			return
		}
	}
}

func logElem(msg string) {
	log.Printf(msg)
	// el := getElementByID("logs")
	// el.Set("innerHTML", el.Get("innerHTML").String()+msg)
}

func handleError() {
	logElem("[Sys]: Maybe Any left, Please restart\n")
}

func getElementByID(id string) js.Value {
	return js.Global().Get("document").Call("getElementById", id)
}

func setJudge(judge game.JudgeStatus) {
	myJudge := js.Global().Get("document").Call("getElementById", "my-judge")
	opJudge := js.Global().Get("document").Call("getElementById", "op-judge")
	switch judge {
	case game.Win:
		myJudge.Set("id", "win")
		opJudge.Set("id", "lose")
		myJudge.Set("innerHTML", "WIN")
		opJudge.Set("innerHTML", "LOSE")
	case game.Lose:
		myJudge.Set("id", "lose")
		opJudge.Set("id", "win")
		myJudge.Set("innerHTML", "LOSE")
		opJudge.Set("innerHTML", "WIN")
	case game.Draw:
		myJudge.Set("innerHTML", "DRAW")
		opJudge.Set("innerHTML", "DRAW")
	default:
		return
	}
}

func setScore(board *game.Board, guess string, hit int, blow int) {
	var scores js.Value
	doc := js.Global().Get("document").Call("getElementsByClassName", "board")
	scores = doc.Index(0).Call("querySelector", "table").Get("tBodies").Index(0).Get("rows")
	if board.IsMyTurn() {
		scores = doc.Index(1).Call("querySelector", "table").Get("tBodies").Index(0).Get("rows")
	}
	turnCount := board.TurnCount()
	log.Printf("board.TurnCount() %d", board.TurnCount())
	guessCell := scores.Index(turnCount).Get("cells").Index(0)
	hitCell := scores.Index(turnCount).Get("cells").Index(1)
	blowCell := scores.Index(turnCount).Get("cells").Index(2)
	guessCell.Set("innerHTML", guess)
	hitCell.Set("innerHTML", hit)
	blowCell.Set("innerHTML", blow)
}

func setTimer(second int) {
	timer := js.Global().Get("document").Call("getElementById", "timer")
	timer.Set("innerHTML", second)
}
