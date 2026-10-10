package duplex

import (
	"context"

	"github.com/chuanmingliu/golive/internal/audio"
)

// speakBackchannel bypasses the speech pipe: a backchannel must not disturb the
// pipe that a real answer may already be using.
//
// The audio comes from the phrase cache when it can. These are a handful of
// fixed phrases said over and over, and synthesizing one each time had two
// costs: the phrase arrived a synthesis round trip late, which for an
// acknowledgement is most of the way to being pointless, and it was synthesized
// on the same ordered connection as the answer it was covering for, so the
// filler could be the thing the answer was waiting behind.
func (e *Engine) speakBackchannel(turn *Turn, phrase string) {
	key := e.phraseKey(phrase)
	useCache := e.opts.Cfg.Duplex.PhraseCache

	if useCache {
		if cached, ok := Phrases().Get(key); ok {
			pcm, err := audio.ResamplePCM16(cached.PCM, cached.Rate, e.opts.ClientRate)
			if err == nil && len(pcm) > 0 {
				if !e.player.Enqueue(Segment{Kind: SegBegin, Gen: turn.Generation, TurnID: turn.ID, Text: phrase}) {
					e.log.Debug("backchannel: dropped; the answer took the floor first",
						"turn", turn.ID, "text", phrase)
					return
				}
				e.player.Enqueue(Segment{Kind: SegAudio, Gen: turn.Generation, TurnID: turn.ID, PCM: pcm})
				e.player.Enqueue(Segment{Kind: SegMark, Gen: turn.Generation, TurnID: turn.ID, Text: phrase})
				e.player.Enqueue(Segment{Kind: SegEnd, Gen: turn.Generation, TurnID: turn.ID})
				return
			}
		}
	}

	stream, err := e.ttsSession()
	if err != nil {
		return
	}
	// Own context, for the same reason as speak: the loop below returns the
	// moment the generation moves, and abandoning the channel without
	// cancelling wedges the connection every other turn shares. Backchannels
	// are abandoned often — a filler is superseded whenever the caller speaks
	// again — so this is the call site that did the most damage.
	bcCtx, cancelBC := context.WithCancel(e.ctx)
	defer cancelBC()
	chunks, err := stream.Synthesize(bcCtx, phrase)
	if err != nil {
		return
	}
	// The floor may have been taken while this was synthesizing, in which case
	// the player refuses it and there is nothing to say or to transcribe.
	if !e.player.Enqueue(Segment{Kind: SegBegin, Gen: turn.Generation, TurnID: turn.ID, Text: phrase}) {
		e.log.Debug("backchannel: dropped; the answer took the floor while it was synthesizing",
			"turn", turn.ID, "text", phrase)
		return
	}
	// The caller hears this, so it belongs in the transcript — and it gets
	// there the way every other spoken segment does, from the player, on its
	// first audio. Conversation history is the separate question and keeps the
	// opposite answer: recordTurn drops bc_ turns, because a filler is not
	// something the model should reason from.
	var keep []byte
	for chunk := range chunks {
		if chunk.Err != nil || !e.gen.Valid(turn.Generation) {
			return
		}
		if useCache {
			keep = append(keep, chunk.PCM...)
		}
		pcm, err := audio.ResamplePCM16(chunk.PCM, stream.SampleRate(), e.opts.ClientRate)
		if err != nil {
			continue
		}
		e.player.Enqueue(Segment{Kind: SegAudio, Gen: turn.Generation, TurnID: turn.ID, PCM: pcm})
	}
	e.player.Enqueue(Segment{Kind: SegMark, Gen: turn.Generation, TurnID: turn.ID, Text: phrase})
	e.player.Enqueue(Segment{Kind: SegEnd, Gen: turn.Generation, TurnID: turn.ID})
	if useCache && len(keep) > 0 && bcCtx.Err() == nil {
		Phrases().Put(key, PhraseAudio{PCM: keep, Rate: stream.SampleRate()})
	}
}
