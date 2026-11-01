use super::*;
use anyhow::{Context, anyhow};
use futures_util::StreamExt;
use serde_json::json;
use std::collections::BTreeMap;

fn completion_url(base: &str) -> String {
    let base = base.trim_end_matches('/');
    let last = base.rsplit('/').next().unwrap_or("");
    if last.contains("completion") {
        return base.into();
    }
    if last
        .strip_prefix('v')
        .is_some_and(|v| !v.is_empty() && v.chars().all(|c| c.is_ascii_digit()))
    {
        format!("{base}/chat/completions")
    } else {
        format!("{base}/v1/chat/completions")
    }
}

fn messages_wire(messages: Vec<Message>) -> Vec<Value> {
    messages.into_iter().map(|m| {
        let mut value = json!({"role":m.role,"content":m.content});
        if let Some(id) = m.tool_call_id { value["tool_call_id"] = json!(id); }
        if !m.tool_calls.is_empty() {
            value["tool_calls"] = json!(m.tool_calls.into_iter().map(|call| json!({
                "id":call.id,"type":"function","function":{"name":call.name,"arguments":if call.arguments.is_empty(){"{}".into()}else{call.arguments}}
            })).collect::<Vec<_>>());
        }
        value
    }).collect()
}

fn request(cfg: &Config, messages: Vec<Message>) -> Result<(String, String, Value)> {
    let name = cfg.string("llm", "deepseek");
    let (prefix, key, base, model) = match name.as_str() {
        "deepseek" => (
            "DEEPSEEK",
            "DEEPSEEK_API_KEY",
            "https://api.deepseek.com",
            "deepseek-chat",
        ),
        "openai-compatible" => (
            "OPENAI",
            "OPENAI_API_KEY",
            "https://api.openai.com",
            "gpt-4o-mini",
        ),
        "cerebras" => (
            "CEREBRAS",
            "CEREBRAS_API_KEY",
            "https://api.cerebras.ai",
            "qwen-3.8-27b",
        ),
        "inception" => (
            "INCEPTION",
            "INCEPTION_API_KEY",
            "https://api.inceptionlabs.ai",
            "mercury-2.5",
        ),
        _ => bail!("unsupported streaming LLM provider {name:?}"),
    };
    let key = required(cfg, key)?;
    let profile_base = cfg.string("backend_base_url", "");
    let profile_model = cfg.string("backend_model", "");
    let default_base = if profile_base.is_empty() {
        base
    } else {
        &profile_base
    };
    let default_model = if profile_model.is_empty() {
        model
    } else {
        &profile_model
    };
    let base = env(
        cfg,
        &format!("GOLIVE_{prefix}_BASE_URL"),
        &env(cfg, "GOLIVE_BACKEND_BASE_URL", default_base),
    );
    let model = env(
        cfg,
        &format!("GOLIVE_{prefix}_MODEL"),
        &env(cfg, "GOLIVE_BACKEND_MODEL", default_model),
    );
    let mut body = json!({"model":model,"messages":messages_wire(messages),"stream":true,
        "temperature":cfg.f64("temperature",0.7),"max_tokens":cfg.usize("max_output_tokens",512),"stream_options":{"include_usage":true}});
    if let Some(tools) = cfg
        .value("tools")
        .filter(|t| t.as_array().is_some_and(|a| !a.is_empty()))
    {
        body["tools"] = json!(
            tools
                .as_array()
                .unwrap()
                .iter()
                .map(|t| {
                    if t.get("function").is_some() {
                        t.clone()
                    } else {
                        let mut function = t.clone();
                        if let Some(obj) = function.as_object_mut() {
                            obj.remove("type");
                        }
                        json!({"type":"function","function":function})
                    }
                })
                .collect::<Vec<_>>()
        );
        if let Some(choice) = cfg.value("tool_choice") {
            body["tool_choice"] = choice.clone();
        }
        if let Some(parallel) = cfg.value("parallel_tool_calls") {
            body["parallel_tool_calls"] = parallel.clone();
        }
    }
    if let Some(extra) = cfg.value("backend_extra").and_then(Value::as_object) {
        for (key, value) in extra {
            if body.get(key).is_none() {
                body[key] = value.clone();
            }
        }
    }
    Ok((completion_url(&base), key, body))
}

pub(super) fn shared_client() -> Result<reqwest::Client> {
    client_for_lane(0)
}

fn client_for_lane(lane: usize) -> Result<reqwest::Client> {
    static CLIENTS: std::sync::OnceLock<std::sync::Mutex<BTreeMap<usize, reqwest::Client>>> =
        std::sync::OnceLock::new();
    let mut clients = CLIENTS
        .get_or_init(|| std::sync::Mutex::new(BTreeMap::new()))
        .lock()
        .unwrap_or_else(|p| p.into_inner());
    let lane = lane.min(15);
    if let Some(client) = clients.get(&lane) {
        return Ok(client.clone());
    }
    let client = reqwest::Client::builder()
        .connect_timeout(Duration::from_secs(10))
        .pool_max_idle_per_host(8)
        .pool_idle_timeout(Duration::from_secs(300))
        .build()?;
    clients.insert(lane, client.clone());
    Ok(client)
}

pub(super) async fn prewarm(cfg: Config, cancel: CancellationToken) -> Result<()> {
    let (endpoint, _, _) = request(&cfg, vec![])?;
    let mut endpoint = url::Url::parse(&endpoint).context("invalid LLM base URL")?;
    endpoint.set_path("/");
    endpoint.set_query(None);
    endpoint.set_fragment(None);
    let mut jobs = tokio::task::JoinSet::new();
    for lane in 0..cfg.usize("llm_hedge", 1).clamp(1, 16) {
        let client = client_for_lane(lane)?;
        let endpoint = endpoint.clone();
        jobs.spawn(async move {
            tokio::time::timeout(Duration::from_secs(5), client.head(endpoint).send())
                .await
                .context("LLM prewarm timeout")?
                .map_err(|_| anyhow!("LLM prewarm connection failed"))?;
            Ok::<_, anyhow::Error>(())
        });
    }
    let mut error = None;
    loop {
        let next = tokio::select! { _=cancel.cancelled()=>bail!("LLM prewarm cancelled"),next=jobs.join_next()=>next };
        match next {
            Some(Ok(Ok(()))) => {}
            Some(Ok(Err(e))) => error = Some(e),
            Some(Err(e)) => error = Some(e.into()),
            None => break,
        }
    }
    match error {
        Some(error) => Err(error),
        None => Ok(()),
    }
}

pub(super) async fn stream_llm(
    cfg: Config,
    messages: Vec<Message>,
    cancel: CancellationToken,
) -> Result<mpsc::Receiver<Result<LlmDelta>>> {
    let (endpoint, key, body) = request(&cfg, messages)?;
    let client = client_for_lane(cfg.usize("_llm_lane", 0))?;
    let response = tokio::select! {
        _ = cancel.cancelled() => bail!("LLM request cancelled"),
        result = tokio::time::timeout(Duration::from_secs(30), client.post(endpoint).bearer_auth(key).header("Accept","text/event-stream").json(&body).send()) => {
            result.context("LLM response header timeout")?.map_err(|_| anyhow!("LLM HTTP request failed"))?
        }
    };
    if !response.status().is_success() {
        bail!("LLM returned HTTP {}", response.status());
    }
    let (out, rx) = mpsc::channel(64);
    tokio::spawn(async move {
        let run = consume(response, &out);
        tokio::select! {
            _ = cancel.cancelled() => {}, _ = out.closed() => {},
            result = run => if let Err(error) = result {
                tokio::select! { _ = cancel.cancelled() => {}, _ = out.send(Err(error)) => {} }
            }
        }
    });
    Ok(rx)
}

#[derive(Default)]
struct Decoder {
    calls: BTreeMap<u64, ToolCall>,
    finished: bool,
}

impl Decoder {
    fn line(&mut self, line: &[u8]) -> Result<(Vec<LlmDelta>, bool)> {
        let line = std::str::from_utf8(line)
            .context("LLM stream is not UTF-8")?
            .trim();
        let Some(data) = line.strip_prefix("data:").map(str::trim) else {
            return Ok((vec![], false));
        };
        if data == "[DONE]" {
            let result = self.flush_calls()?;
            self.finished = true;
            return Ok((result, true));
        }
        let Ok(chunk) = serde_json::from_str::<Value>(data) else {
            return Ok((vec![], false));
        };
        if chunk.get("error").is_some() {
            bail!("LLM returned a streaming error");
        }
        let mut result = vec![];
        if let Some(usage) = chunk.get("usage").filter(|u| !u.is_null()) {
            let cached = usage["prompt_cache_hit_tokens"].as_u64().unwrap_or(0).max(
                usage["prompt_tokens_details"]["cached_tokens"]
                    .as_u64()
                    .unwrap_or(0),
            );
            result.push(LlmDelta { usage:Some(json!({"input_tokens":usage["prompt_tokens"].as_u64().unwrap_or(0),"output_tokens":usage["completion_tokens"].as_u64().unwrap_or(0),"cached_input_tokens":cached})),..Default::default() });
        }
        for choice in chunk["choices"]
            .as_array()
            .into_iter()
            .flatten()
            .filter(|c| c["index"].as_u64().unwrap_or(0) == 0)
        {
            if let Some(text) = choice["delta"]["content"]
                .as_str()
                .filter(|s| !s.is_empty())
            {
                result.push(LlmDelta {
                    text: text.into(),
                    ..Default::default()
                });
            }
            for call in choice["delta"]["tool_calls"]
                .as_array()
                .into_iter()
                .flatten()
            {
                let entry = self
                    .calls
                    .entry(call["index"].as_u64().unwrap_or(0))
                    .or_default();
                if let Some(id) = call["id"].as_str().filter(|s| !s.is_empty()) {
                    entry.id = id.into();
                }
                if let Some(name) = call["function"]["name"].as_str().filter(|s| !s.is_empty()) {
                    entry.name = name.into();
                }
                if let Some(args) = call["function"]["arguments"].as_str() {
                    if entry.arguments.len() + args.len() > 4 * 1024 * 1024 {
                        bail!("LLM tool arguments exceed 4 MiB");
                    }
                    entry.arguments.push_str(args);
                }
            }
            if choice["finish_reason"]
                .as_str()
                .is_some_and(|s| !s.is_empty())
            {
                result.extend(self.flush_calls()?);
                self.finished = true;
            }
        }
        Ok((result, false))
    }

    fn flush_calls(&mut self) -> Result<Vec<LlmDelta>> {
        let mut result = vec![];
        for (_, mut call) in std::mem::take(&mut self.calls) {
            if call.name.is_empty() {
                continue;
            }
            if call.arguments.trim().is_empty() {
                call.arguments = "{}".into();
            }
            serde_json::from_str::<Value>(&call.arguments)
                .context("LLM function call has incomplete JSON arguments")?;
            if call.id.is_empty() {
                call.id = format!("call_{}", uuid::Uuid::new_v4());
            }
            result.push(LlmDelta {
                tool_call: Some(call),
                ..Default::default()
            });
        }
        Ok(result)
    }
}

async fn consume(response: reqwest::Response, out: &mpsc::Sender<Result<LlmDelta>>) -> Result<()> {
    let mut stream = response.bytes_stream();
    let mut pending = Vec::new();
    let mut decoder = Decoder::default();
    while let Some(chunk) = tokio::time::timeout(Duration::from_secs(30), stream.next())
        .await
        .context("LLM stream stalled")?
    {
        pending.extend_from_slice(&chunk.context("LLM stream read failed")?);
        while let Some(end) = pending.iter().position(|b| *b == b'\n') {
            let line: Vec<u8> = pending.drain(..=end).collect();
            let (deltas, done) = decoder.line(&line)?;
            for delta in deltas {
                if out.send(Ok(delta)).await.is_err() {
                    return Ok(());
                }
            }
            if done {
                return Ok(());
            }
        }
        if pending.len() > 4 * 1024 * 1024 {
            bail!("LLM stream line exceeds 4 MiB");
        }
    }
    if !pending.is_empty() {
        let (deltas, _) = decoder.line(&pending)?;
        for delta in deltas {
            if out.send(Ok(delta)).await.is_err() {
                return Ok(());
            }
        }
    }
    if !decoder.finished {
        bail!("LLM stream ended before a completion marker");
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::super::tests::config;
    use super::*;
    use tokio::io::{AsyncReadExt, AsyncWriteExt};

    #[test]
    fn tool_fragments_are_emitted_in_index_order_and_validated() {
        let mut decoder = Decoder::default();
        decoder.line(br#"data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"b","function":{"name":"second","arguments":"{\"b\":"}},{"index":0,"id":"a","function":{"name":"first","arguments":"{}"}}]}}]}"#).unwrap();
        let (deltas,_)=decoder.line(br#"data: {"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"2}"}}]},"finish_reason":"tool_calls"}]}"#).unwrap();
        assert_eq!(deltas[0].tool_call.as_ref().unwrap().name, "first");
        assert_eq!(
            deltas[1].tool_call.as_ref().unwrap().arguments,
            r#"{"b":2}"#
        );
        let mut broken = Decoder::default();
        assert!(broken.line(br#"data: {"choices":[{"delta":{"tool_calls":[{"function":{"name":"unsafe","arguments":"{"}}]},"finish_reason":"tool_calls"}]}"#).is_err());
    }

    #[test]
    fn completion_paths_and_tool_history_match_wire_contract() {
        assert_eq!(
            completion_url("https://example.com/v3/"),
            "https://example.com/v3/chat/completions"
        );
        let wire = messages_wire(vec![Message {
            role: "assistant".into(),
            tool_calls: vec![ToolCall {
                id: "x".into(),
                name: "lookup".into(),
                arguments: String::new(),
            }],
            ..Default::default()
        }]);
        assert_eq!(wire[0]["tool_calls"][0]["function"]["arguments"], "{}");
    }

    #[tokio::test]
    async fn streams_split_utf8_from_local_http_server() {
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let endpoint = format!("http://{}", listener.local_addr().unwrap());
        let server = tokio::spawn(async move {
            let (mut stream, _) = listener.accept().await.unwrap();
            let mut request = vec![0; 8192];
            let _ = stream.read(&mut request).await.unwrap();
            let body =
                "data: {\"choices\":[{\"delta\":{\"content\":\"你好\"}}]}\n\ndata: [DONE]\n\n";
            stream.write_all(format!("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: {}\r\n\r\n",body.len()).as_bytes()).await.unwrap();
            for byte in body.as_bytes() {
                stream.write_all(&[*byte]).await.unwrap();
                tokio::task::yield_now().await;
            }
        });
        let mut cfg = config(json!({"llm":"deepseek","backend_base_url":endpoint}));
        cfg.env.insert("DEEPSEEK_API_KEY".into(), "test-key".into());
        let mut rx = stream_llm(cfg, vec![], CancellationToken::new())
            .await
            .unwrap();
        assert_eq!(rx.recv().await.unwrap().unwrap().text, "你好");
        assert!(rx.recv().await.is_none());
        server.await.unwrap();
    }
    #[tokio::test]
    async fn prewarm_separates_hedge_connections_and_reuses_winning_lane() {
        use tokio::io::{AsyncBufReadExt, BufReader};
        async fn request(reader: &mut BufReader<tokio::net::TcpStream>) -> String {
            let mut headers = String::new();
            let mut length = 0;
            loop {
                let mut line = String::new();
                if reader.read_line(&mut line).await.unwrap() == 0 {
                    return headers;
                }
                if line == "\r\n" {
                    break;
                }
                if let Some(raw) = line.to_ascii_lowercase().strip_prefix("content-length:") {
                    length = raw.trim().parse::<usize>().unwrap();
                }
                headers.push_str(&line);
            }
            let mut body = vec![0; length];
            reader.read_exact(&mut body).await.unwrap();
            headers
        }
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let endpoint = format!("http://{}", listener.local_addr().unwrap());
        let server = tokio::spawn(async move {
            let mut clients = tokio::task::JoinSet::new();
            for _ in 0..2 {
                let (stream, _) = listener.accept().await.unwrap();
                clients.spawn(async move {
                    let mut reader=BufReader::new(stream);
                    let head=request(&mut reader).await;
                    assert!(head.starts_with("HEAD / HTTP/1.1"));
                    assert!(!head.to_ascii_lowercase().contains("authorization"));
                    reader.get_mut().write_all(b"HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n").await.unwrap();
                    if let Ok(post)=tokio::time::timeout(Duration::from_millis(500),request(&mut reader)).await {
                        if post.is_empty(){return false;}
                        assert!(post.starts_with("POST /v1/chat/completions HTTP/1.1"));
                        let body=b"data: {\"choices\":[{\"delta\":{\"content\":\"warm\"}}]}\n\ndata: [DONE]\n\n";
                        reader.get_mut().write_all(format!("HTTP/1.1 200 OK\r\nContent-Length: {}\r\nContent-Type: text/event-stream\r\n\r\n",body.len()).as_bytes()).await.unwrap();
                        reader.get_mut().write_all(body).await.unwrap();return true;
                    }
                    false
                });
            }
            let mut reused = 0;
            while let Some(result) = clients.join_next().await {
                if result.unwrap() {
                    reused += 1;
                }
            }
            assert_eq!(reused, 1);
        });
        let mut cfg = config(
            json!({"llm":"deepseek","llm_hedge":2,"backend_base_url":endpoint,"_llm_lane":0}),
        );
        cfg.env.insert("DEEPSEEK_API_KEY".into(), "fake".into());
        prewarm(cfg.clone(), CancellationToken::new())
            .await
            .unwrap();
        let mut stream = stream_llm(cfg, vec![], CancellationToken::new())
            .await
            .unwrap();
        assert_eq!(stream.recv().await.unwrap().unwrap().text, "warm");
        assert!(stream.recv().await.is_none());
        server.await.unwrap();
    }
    #[test]
    fn blank_shared_settings_use_selected_provider_defaults() {
        let mut cfg =
            config(json!({"llm":"openai-compatible","backend_base_url":"","backend_model":""}));
        cfg.env.insert("OPENAI_API_KEY".into(), "fake".into());
        let (endpoint, _, body) = request(&cfg, vec![]).unwrap();
        assert_eq!(endpoint, "https://api.openai.com/v1/chat/completions");
        assert_eq!(body["model"], "gpt-4o-mini");
        cfg.env.insert(
            "GOLIVE_OPENAI_BASE_URL".into(),
            "http://localhost:9999/v2".into(),
        );
        cfg.env
            .insert("GOLIVE_OPENAI_MODEL".into(), "custom-model".into());
        let (endpoint, _, body) = request(&cfg, vec![]).unwrap();
        assert_eq!(endpoint, "http://localhost:9999/v2/chat/completions");
        assert_eq!(body["model"], "custom-model");
    }
}
