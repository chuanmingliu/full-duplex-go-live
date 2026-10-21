package main

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// A scenario is a caller who behaves one particular way, for `episodes`
// question-and-answer rounds in one session. Each maps to a row of the
// panel; the suite together is the benchmark.
type scenario struct {
	Name  string
	About string
	// Tools, when set, are offered to the backend (the mock calls the first
	// one on a question containing 查).
	Tools []json.RawMessage
	Run   func(ctx context.Context, s *session, ep int, opt runOptions)
}

type runOptions struct {
	// WaitToRespond is how long the caller waits after the agent falls silent
	// before speaking again (τ-voice: 1.0 s).
	WaitToRespond time.Duration
	// InterruptAt is how far into the agent's answer an over-the-agent event
	// is said (τ-voice checks every 2 s; 1.5 s lands inside the first
	// sentence of the mock's answer).
	InterruptAt time.Duration
	// AnswerTimeout bounds how long an episode waits for the agent at all.
	AnswerTimeout time.Duration
	// PauseMS is the intra-utterance pause for the pause scenario.
	PauseMS int
}

// questions the plain turns cycle through: each is answered by the mock
// backend with a long sentence that restates it.
var questions = []int{pRest, pFollowUp, pStatement, pFollowUp2, pChange, pLookup, pHello, pOK}

func question(ep int) int { return questions[ep%len(questions)] }

// askAndWait says a question and waits for the answer to end: the base of
// most episodes. It returns false when no answer came.
func askAndWait(ctx context.Context, s *session, k int, opt runOptions) bool {
	<-s.say(k, kindTurn, naturalMS(k), 0, 0)
	if !s.waitAgentSpeaking(ctx, opt.AnswerTimeout) {
		return false
	}
	s.waitAgentSilent(ctx, opt.WaitToRespond, 30*time.Second)
	return true
}

// overAgent says phrase k of the given kind InterruptAt into the agent's
// answer to the question, then lets whatever follows play out.
func overAgent(ctx context.Context, s *session, q, k int, kind segKind, ms int, opt runOptions) {
	<-s.say(q, kindTurn, naturalMS(q), 0, 0)
	if !s.waitAgentSpeaking(ctx, opt.AnswerTimeout) {
		return
	}
	sleepCtx(ctx, opt.InterruptAt)
	<-s.say(k, kind, ms, 0, 0)
	// Give the engine its windows: an answer to the interruption, or the
	// rest of the original answer.
	if kind == kindTurn {
		s.waitAgentSpeaking(ctx, opt.AnswerTimeout)
	}
	s.waitAgentSilent(ctx, opt.WaitToRespond, 30*time.Second)
}

var suite = []scenario{
	{
		Name:  "turns",
		About: "plain question and answer; the caller waits 1 s after each answer (R_R, L_R; FDB turn-taking)",
		Run: func(ctx context.Context, s *session, ep int, opt runOptions) {
			askAndWait(ctx, s, question(ep), opt)
		},
	},
	{
		Name:  "interrupt",
		About: "a follow-up question said 1.5 s into the answer (R_Y, L_Y; FDB user interruption)",
		Run: func(ctx context.Context, s *session, ep int, opt runOptions) {
			overAgent(ctx, s, question(ep), pFollowUp, kindTurn, naturalMS(pFollowUp), opt)
		},
	},
	{
		Name:  "backchannel",
		About: "a murmur (嗯 / 对对 / 好) said over the answer; the answer should carry on (S_BC)",
		Run: func(ctx context.Context, s *session, ep int, opt runOptions) {
			murmurs := []int{pMm, pDuidui, pHao}
			overAgent(ctx, s, question(ep), murmurs[ep%len(murmurs)], kindBackchannel, 450, opt)
		},
	},
	{
		Name:  "vocal-tic",
		About: "a hesitation (呃) over the answer, and another alone into the silence after it (S_VT)",
		Run: func(ctx context.Context, s *session, ep int, opt runOptions) {
			overAgent(ctx, s, question(ep), pEr, kindTic, 400, opt)
			<-s.say(pEr, kindTic, 400, 0, 0)
			sleepCtx(ctx, opt.WaitToRespond+defaultWindows.TicResponse)
			s.waitAgentSilent(ctx, opt.WaitToRespond, 20*time.Second)
		},
	},
	{
		Name:  "non-directed",
		About: "speech to someone else (等一下我接个电话 / 先别说) over the answer and into silence (S_ND)",
		Run: func(ctx context.Context, s *session, ep int, opt runOptions) {
			asides := []int{pAside, pAsideShort}
			k := asides[ep%len(asides)]
			overAgent(ctx, s, question(ep), k, kindNonDirected, naturalMS(k), opt)
			<-s.say(k, kindNonDirected, naturalMS(k), 0, 0)
			sleepCtx(ctx, opt.WaitToRespond+defaultWindows.NonDirectedResp)
			s.waitAgentSilent(ctx, opt.WaitToRespond, 20*time.Second)
		},
	},
	{
		Name:  "pause",
		About: "an opening that is not finished (我想问一下), a pause, then the rest; the agent should hold (FDB pause handling)",
		Run: func(ctx context.Context, s *session, ep int, opt runOptions) {
			<-s.sayTwo(pOpening, pRest, opt.PauseMS)
			if s.waitAgentSpeaking(ctx, opt.AnswerTimeout) {
				s.waitAgentSilent(ctx, opt.WaitToRespond, 30*time.Second)
			}
		},
	},
	{
		Name:  "rapid",
		About: "two questions 300 ms apart; the second supersedes the first (R_R counts the first as unanswered)",
		Run: func(ctx context.Context, s *session, ep int, opt runOptions) {
			<-s.say(pRest, kindTurn, naturalMS(pRest), 0, 0)
			sleepCtx(ctx, 300*time.Millisecond)
			askAndWait(ctx, s, pFollowUp2, opt)
		},
	},
	{
		Name:  "tool",
		About: "a lookup (帮我查一下…) the backend answers with a tool call; pass = the call came and its result was read out (pass^k)",
		Tools: []json.RawMessage{json.RawMessage(`{"type":"function","name":"quote_total","description":"计算总保费","parameters":{"type":"object","properties":{"request":{"type":"string"}}}}`)},
		Run: func(ctx context.Context, s *session, ep int, opt runOptions) {
			before := len(s.tl.FunctionCalls)
			<-s.say(pLookup, kindTurn, naturalMS(pLookup), 0, 0)
			s.waitAgentSpeaking(ctx, opt.AnswerTimeout)
			// The preface, the tool round trip, the read-out: a longer quiet
			// than between two sentences says the answer is over.
			s.waitAgentSilent(ctx, 3500*time.Millisecond, 30*time.Second)
			judgeTool(s, before)
		},
	},
	{
		Name:  "tool-interrupt",
		About: "the same lookup, interrupted with a follow-up during the preface; the result must still be read out once (pass^k)",
		Tools: []json.RawMessage{json.RawMessage(`{"type":"function","name":"quote_total","description":"计算总保费","parameters":{"type":"object","properties":{"request":{"type":"string"}}}}`)},
		Run: func(ctx context.Context, s *session, ep int, opt runOptions) {
			before := len(s.tl.FunctionCalls)
			<-s.say(pLookup, kindTurn, naturalMS(pLookup), 0, 0)
			if s.waitAgentSpeaking(ctx, opt.AnswerTimeout) {
				sleepCtx(ctx, 300*time.Millisecond)
				<-s.say(pFollowUp, kindTurn, naturalMS(pFollowUp), 0, 0)
			}
			s.waitAgentSpeaking(ctx, opt.AnswerTimeout)
			s.waitAgentSilent(ctx, 3500*time.Millisecond, 30*time.Second)
			judgeTool(s, before)
		},
	},
}

// judgeTool scores a lookup episode: a function call for the request, and
// its result spoken back exactly once.
func judgeTool(s *session, before int) {
	s.tl.mu.Lock()
	defer s.tl.mu.Unlock()
	s.tl.ToolAttempts++
	called := false
	for _, fc := range s.tl.FunctionCalls[before:] {
		if fc.Name == "quote_total" && strings.Contains(fc.Arguments, "查") {
			called = true
		}
	}
	readouts := strings.Count(strings.Join(s.tl.Replies, ""), "查到了")
	s.tl.Replies = nil
	if called && readouts == 1 {
		s.tl.ToolPasses++
	}
}

func scenarioByName(name string) (scenario, bool) {
	for _, sc := range suite {
		if sc.Name == name {
			return sc, true
		}
	}
	return scenario{}, false
}
