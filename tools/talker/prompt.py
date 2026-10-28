"""The one prompt and clause contract shared by training and CPU evaluation."""

from __future__ import annotations

import re
import unicodedata

END_TOKEN = "<|im_end|>"
FIRST_CLAUSE_LIMIT = 12
CLAUSE_BOUNDARIES = "，。！？；：,.!?;:\n"
ACKS = {"嗯", "嗯嗯", "好", "好的", "好呀", "好啊", "明白", "了解", "收到", "行", "行的"}
SYSTEM_PROMPT = (
    "你是电话对话助手。根据对话和来电者刚说的话，紧接已经说出的开场词，"
    "只写回答的第一个自然短分句，正文不超过12个字，以标点结束。"
    "不要重复开场词，不要解释，不要编造未提供的信息。"
)
ASSISTANT_PREFILL = "<|im_start|>assistant\n<think>\n\n</think>\n\n"
_SPECIAL = re.compile(r"<\|[^\n]*?\|>|</?think>")


def visible_length(text: str) -> int:
    """Count content characters; punctuation and whitespace do not spend the cap."""
    return sum(not ch.isspace() and not unicodedata.category(ch).startswith("P") for ch in text)


def first_clause(text: str, max_chars: int = FIRST_CLAUSE_LIMIT) -> str | None:
    """Return a complete short clause, never a length-truncated fragment.

    A caller must only treat an unpunctuated result as complete after EOS. This
    function handles finished answers; a streaming consumer knows whether EOS
    has arrived and must wait for it or a clause boundary.
    """
    text = text.strip()
    if not text or _SPECIAL.search(text):
        return None
    end = next((i for i, ch in enumerate(text) if ch in CLAUSE_BOUNDARIES), len(text))
    if end < len(text):
        end += 1
        while end < len(text) and text[end] in CLAUSE_BOUNDARIES:
            end += 1
    clause = text[:end].strip()
    count = visible_length(clause)
    normalized = "".join(ch for ch in clause if not ch.isspace() and not unicodedata.category(ch).startswith("P"))
    if not 0 < count <= max_chars or normalized in ACKS:
        return None
    return clause


def strip_opener(answer: str, opener: str) -> str:
    """Journal answers normally exclude the opener; strip only an exact prefix."""
    answer = answer.strip()
    if opener and answer.startswith(opener):
        return answer[len(opener):].lstrip()
    return answer


def _message(role: str, text: str) -> str:
    # Literal template delimiters in speech must not create another role.
    text = _SPECIAL.sub(lambda match: match.group().replace("<", "＜").replace(">", "＞"), text)
    return f"<|im_start|>{role}\n{text}{END_TOKEN}\n"


def render_prompt(
    context: list[dict], caller: str, opener: str,
    max_context_turns: int = 6, max_prompt_chars: int = 768,
) -> str:
    """Render no-thinking Qwen ChatML, ending in the already-spoken opener.

    Context is prior completed user/assistant exchanges (an initial assistant
    greeting is accepted). Old exchanges are dropped whole. The current caller
    and opener are never shortened to fit: an oversized current turn raises.
    """
    if max_context_turns < 0 or max_prompt_chars < 1:
        raise ValueError("invalid prompt budget")
    groups: list[list[dict]] = []
    pending: dict | None = None
    for message in context:
        role, content = message.get("role"), message.get("content")
        if not isinstance(content, str) or not content.strip():
            continue
        if role == "user":
            pending = {"role": role, "content": content}
        elif role == "assistant":
            if pending is not None:
                groups.append([pending, {"role": role, "content": content}])
                pending = None
            elif not groups:
                groups.append([{"role": role, "content": content}])
    groups = groups[-max_context_turns:] if max_context_turns else []
    head = _message("system", SYSTEM_PROMPT)
    # A formatting newline prevents tokenizer merges across the prompt/label
    # boundary. It is not spoken and is identical in training and serving.
    tail = _message("user", caller) + ASSISTANT_PREFILL + _SPECIAL.sub("", opener) + "\n"
    if len(head + tail) > max_prompt_chars:
        raise ValueError("current caller and opener exceed prompt character budget")
    while groups:
        middle = "".join(_message(m["role"], m["content"]) for group in groups for m in group)
        if len(head + middle + tail) <= max_prompt_chars:
            return head + middle + tail
        groups.pop(0)
    return head + tail


def prompt_for_pair(pair: dict, **kwargs) -> str:
    options = dict(pair.get("prompt_options", {}))
    if set(options) - {"max_context_turns", "max_prompt_chars"}:
        raise ValueError("unknown saved prompt options")
    options.update(kwargs)
    return render_prompt(pair["context"], pair["caller"], pair["opener"], **options)
