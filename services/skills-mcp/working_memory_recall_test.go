package main

import (
	"context"
	"slices"
	"strconv"
	"testing"
)

func TestWorkingMemoryRecallPublishedSelection(t *testing.T) {
	f := newFixture(t, nil)
	c := f.s.catalog
	names := []string{c.Skills[0].Name, c.Skills[1].Name, "tool:quote_total"}
	c.WorkingMemoryRecall = &workingMemoryRecall{true, recallFunction, "Choose candidates.", names}
	c, err := f.s.publish(c)
	if err != nil {
		t.Fatal(err)
	}
	discovery := f.result("prompts/get", obj{"name": recallPromptName})
	meta := discovery["_meta"].(obj)[slotKey].(obj)
	if meta["function"] != recallFunction || meta["selector_prompt"] != "Choose candidates." || len(meta["candidates"].(obj)) != len(names) {
		t.Fatal("published selector registration is incomplete")
	}

	args := map[string]string{"event": "auto", "recall_revision": strconv.Itoa(c.Revision), "recall_ids": `["tool:quote_total","wechat-service-notification"]`}
	if _, err := c.recallSelection(args, false); err == nil {
		t.Fatal("accepted selection without negotiated preparation")
	}
	stale := map[string]string{"event": "auto", "recall_revision": strconv.Itoa(c.Revision - 1), "recall_ids": args["recall_ids"]}
	if _, err := c.recallSelection(stale, true); err == nil {
		t.Fatal("accepted selection from the previous revision")
	}
	ids, err := c.recallSelection(args, true)
	if err != nil {
		t.Fatal(err)
	}

	index := newSkillIndex(c)
	for _, tc := range []struct {
		state string
		want  []string
	}{
		{"consented", []string{c.Skills[0].uri(), c.Skills[1].uri()}},
		{"declined", []string{c.Skills[0].uri()}},
		{"added", []string{c.Skills[0].uri()}},
	} {
		input := turnInput{Event: "auto", Utterance: "接下来这两件事怎么处理", State: map[string]string{"wechat.status": tc.state}}
		prepared := &preparedTurn{Version: 1}
		if err := index.mergeWorkingMemoryRecall(context.Background(), c.WorkingMemoryRecall, input, prepared, ids); err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, resource := range prepared.Resources {
			got = append(got, resource.Manifest.URI)
		}
		if !slices.Equal(got, tc.want) {
			t.Fatalf("state %s: got %v, want %v", tc.state, got, tc.want)
		}
	}
}
