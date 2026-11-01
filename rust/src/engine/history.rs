//! Keep tool-call groups intact while auxiliary speech and context arrive.
use super::{Engine, Message};

impl Engine {
    pub(super) fn pending_tool_ids(&self) -> Vec<String> {
        let answered: std::collections::HashSet<_> = self
            .history
            .iter()
            .filter_map(|m| m.tool_call_id.as_deref())
            .collect();
        self.history
            .iter()
            .flat_map(|m| m.tool_calls.iter())
            .filter(|t| !answered.contains(t.id.as_str()))
            .map(|t| t.id.clone())
            .collect()
    }

    pub(super) fn insert_tool_result(&mut self, id: String, output: String) {
        let Some(owner) = self
            .history
            .iter()
            .position(|m| m.tool_calls.iter().any(|t| t.id == id))
        else {
            return;
        };
        let mut at = owner + 1;
        while self.history.get(at).is_some_and(|m| m.role == "tool") {
            at += 1;
        }
        self.history.insert(
            at,
            Message {
                role: "tool".into(),
                content: output,
                tool_call_id: Some(id),
                tool_calls: vec![],
            },
        );
    }

    pub(super) fn resolve_abandoned_tools(&mut self) {
        for id in self.pending_tool_ids() {
            self.insert_tool_result(id, "{\"error\":\"superseded by new user input\"}".into());
        }
    }

    pub(super) fn trim_history(&mut self) {
        let max = self.cfg.usize("history_turns", 16).max(1) * 2;
        let pending = self.pending_tool_ids();
        let owner = self
            .history
            .iter()
            .position(|m| m.tool_calls.iter().any(|t| pending.contains(&t.id)));
        let excess = self.history.len().saturating_sub(max);
        // Never evict an outstanding call: clients still need to answer it.
        let prefix = owner.map_or(excess, |at| excess.min(at));
        self.history.drain(..prefix);
        if let Some(owner) = owner.map(|at| at - prefix) {
            let mut after = owner + 1;
            while self.history.get(after).is_some_and(|m| m.role == "tool") {
                after += 1;
            }
            // Retain the protected call/result block and the newest auxiliary
            // messages. A large block may exceed max, but appended context cannot.
            let excess = self.history.len().saturating_sub(max.max(after));
            self.history.drain(after..after + excess);
        }
        while self.history.first().is_some_and(|m| m.role == "tool") {
            self.history.remove(0);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{config::Config, engine::message, provider::ToolCall};
    use tokio::sync::mpsc;
    use tokio_util::sync::CancellationToken;

    #[tokio::test]
    async fn context_pressure_preserves_pending_calls_and_adjacent_results() {
        let mut cfg = Config::default();
        cfg.set("history_turns", serde_json::json!(2));
        let (events, _rx) = mpsc::channel(128);
        let mut engine = Engine::new(cfg, events, CancellationToken::new());
        engine.history.push(message("user", "question".into()));
        let mut call = message("assistant", String::new());
        call.tool_calls = vec![
            ToolCall {
                id: "a".into(),
                name: "lookup".into(),
                arguments: "{}".into(),
            },
            ToolCall {
                id: "b".into(),
                name: "lookup".into(),
                arguments: "{}".into(),
            },
        ];
        engine.history.push(call);
        engine.insert_tool_result("a".into(), "done".into());
        for i in 0..100 {
            engine
                .history
                .push(message("system", format!("context {i}")));
            engine.trim_history();
        }
        assert_eq!(engine.history.len(), 4);
        assert_eq!(engine.pending_tool_ids(), vec!["b"]);
        engine.resolve_abandoned_tools();
        assert_eq!(engine.history[0].role, "assistant");
        assert_eq!(engine.history[1].tool_call_id.as_deref(), Some("a"));
        assert_eq!(engine.history[2].tool_call_id.as_deref(), Some("b"));
        assert_eq!(engine.history.last().unwrap().content, "context 99");
        assert!(engine.pending_tool_ids().is_empty());
        engine.trim_history();
        assert!(engine.history.len() <= 4);
        assert_ne!(engine.history[0].role, "tool");
        engine.cancel.cancel();
    }
}
