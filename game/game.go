package game

import (
	"fmt"
	"slices"
	"time"

	"github.com/mowshon/iterium"
)

type State int

const numOfDigits = 3
const numOfAllHandsPatturn = 720

var numbers = []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}

const (
	InMenu State = iota
	Playing
	Finished
)

type Turn int

const (
	MyTurn Turn = iota
	OpTurn
)

func (t Turn) Reverse() Turn {
	return t ^ 1
}

func NewTurnBySeed(seed int) Turn {
	return Turn(seed % 2)
}

type Hand []int

var allHands [numOfAllHandsPatturn]Hand

func init() {
	// generate all hands
	permutations := iterium.Permutations(numbers, numOfDigits)
	numbersList, _ := permutations.Slice()
	for i, ns := range numbersList {
		allHands[i] = Hand(ns)
	}
}

func NewHand(numbers []int) *Hand {
	hand := Hand(numbers)
	return &hand
}

func NewHandBySeed(seed int) *Hand {
	return &allHands[seed%numOfAllHandsPatturn]
}

type Guess Hand

func NewGuessFromText(numStr string) *Guess {
	var numbers []int
	for _, r := range numStr {
		numbers = append(numbers, int(r-'0'))
	}
	guess := Guess(numbers)
	return &guess
}

func (g *Guess) String() string {
	n := []int(*g)
	return fmt.Sprintf("%d%d%d", n[0], n[1], n[2])
}

func (h *Hand) Answer(guess *Guess) *Answer {
	var hit, blow int
	for i, n := range *guess {
		if n == (*h)[i] {
			hit++
		} else if slices.Contains(*h, n) {
			blow++
		}
	}
	return NewAnswer(hit, blow)
}

func (h *Hand) QA(guess *Guess) *QA {
	return NewQA(guess, h.Answer(guess))
}

type Answer struct {
	hit  int
	blow int
}

func NewAnswer(hit, blow int) *Answer {
	return &Answer{hit, blow}
}

func (a *Answer) Hit() int {
	return a.hit
}

func (a *Answer) Blow() int {
	return a.blow
}

func (a *Answer) IsAllHit() bool {
	return a.hit == numOfDigits && a.blow == 0
}

func (a *Answer) Msg() string {
	return fmt.Sprintf("%d hit, %d blow", a.hit, a.blow)
}

type QA struct {
	guess  *Guess
	answer *Answer
}

func NewQA(guess *Guess, answer *Answer) *QA {
	return &QA{guess, answer}
}

type Board struct {
	state   State
	turn    Turn
	myHands *Hand
	myQA    []*QA
	opQA    []*QA
}

func NewBoard() *Board {
	return &Board{
		state: InMenu,
	}
}

func (b *Board) IsInMenu() bool {
	return b.state == InMenu
}

func (b *Board) IsPlaying() bool {
	return b.state == Playing
}

func (b *Board) IsMyTurn() bool {
	return b.turn == MyTurn
}

func (b *Board) IsOpTurn() bool {
	return b.turn == OpTurn
}

func (b *Board) ToggleTurn() {
	b.turn = b.turn.Reverse()
}

func (b *Board) Start(hand *Hand, initTurn Turn) {
	b.state = Playing
	b.turn = initTurn
	b.myHands = hand
}

func (b *Board) Finish() {
	b.state = Finished
}

func (b *Board) CalcAnswer(guess *Guess) *Answer {
	return b.myHands.Answer(guess)
}

func (b *Board) AddMyQA(qa *QA) {
	b.myQA = append(b.myQA, qa)
}

func (b *Board) AddOpQA(qa *QA) {
	b.opQA = append(b.opQA, qa)
}

func (b *Board) WaitGuess(ch chan *Guess, to time.Duration) (*Guess, bool) {
	select {
	case guess := <-ch:
		return guess, false
	case <-time.After(to):
		return nil, true
	}
}
