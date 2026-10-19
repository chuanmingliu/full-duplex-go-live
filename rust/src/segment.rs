//! Streaming clause segmentation matching internal/segment/sentence.go.
use serde::{Deserialize, Serialize};
use unicode_categories::UnicodeCategories;

#[derive(Debug, Clone, Copy, Deserialize, Serialize)]
#[serde(default)]
pub struct SegmentConfig {
    pub first_chunk_chars: usize,
    pub min_chunk_chars: usize,
    pub max_chunk_chars: usize,
}
impl Default for SegmentConfig {
    fn default() -> Self {
        Self {
            first_chunk_chars: 8,
            min_chunk_chars: 24,
            max_chunk_chars: 120,
        }
    }
}
pub struct Segmenter {
    cfg: SegmentConfig,
    buf: Vec<char>,
    first: bool,
    depth: usize,
    early: Option<usize>,
}
impl Segmenter {
    pub fn new(mut cfg: SegmentConfig) -> Self {
        let default = SegmentConfig::default();
        if cfg.first_chunk_chars == 0 {
            cfg.first_chunk_chars = default.first_chunk_chars;
        }
        if cfg.min_chunk_chars == 0 {
            cfg.min_chunk_chars = default.min_chunk_chars;
        }
        if cfg.max_chunk_chars == 0 {
            cfg.max_chunk_chars = default.max_chunk_chars;
        }
        cfg.max_chunk_chars = cfg.max_chunk_chars.max(cfg.min_chunk_chars);
        Self {
            cfg,
            buf: Vec::new(),
            first: true,
            depth: 0,
            early: None,
        }
    }
    pub fn reset(&mut self) {
        self.buf.clear();
        self.first = true;
        self.depth = 0;
        self.early = None;
    }
    pub fn pending(&self) -> String {
        self.buf.iter().collect()
    }
    pub fn push(&mut self, delta: &str) -> Vec<String> {
        let mut result = Vec::new();
        for c in delta.chars() {
            self.buf.push(c);
            match c {
                '(' | '（' | '[' | '【' | '“' | '「' => self.depth += 1,
                ')' | '）' | ']' | '】' | '”' | '」' => {
                    self.depth = self.depth.saturating_sub(1)
                }
                _ => (),
            }
            if "。！？…，、；：\n".contains(c) {
                if let Some(segment) = self.try_flush_at(self.buf.len() - 1) {
                    result.push(segment);
                    continue;
                }
            }
            if self.buf.len() >= 2 {
                if let Some(segment) = self.try_flush_at(self.buf.len() - 2) {
                    result.push(segment);
                }
            }
        }
        result
    }
    pub fn flush_first(&mut self) -> String {
        let text = normalize(&self.pending());
        self.buf.clear();
        self.depth = 0;
        self.first = false;
        self.early = None;
        text
    }
    pub fn has_early_boundary(&self) -> bool {
        self.first && self.early.is_some()
    }
    pub fn flush_first_at_boundary(&mut self) -> String {
        if !self.first {
            return String::new();
        }
        self.early
            .and_then(|index| self.cut(index, index + 1))
            .unwrap_or_default()
    }
    pub fn flush(&mut self) -> String {
        let text = normalize(&self.pending());
        self.reset();
        text
    }
    fn try_flush_at(&mut self, index: usize) -> Option<String> {
        let n = self.buf.len();
        if index >= n {
            return None;
        }
        let candidate = self.buf[index];
        let length = index + 1;
        let min_chars = if self.first {
            self.cfg.first_chunk_chars
        } else {
            self.cfg.min_chunk_chars
        };
        let strong = "。！？!?…\n".contains(candidate)
            || (candidate == '.' && !self.is_decimal_point(index) && !self.is_abbreviation(index));
        let weak = "，,、；;：:—".contains(candidate);
        if self.depth > 0 && n < self.cfg.max_chunk_chars * 2 {
            return None;
        }
        if self.first && (strong || weak) && length < min_chars && length >= 3 && self.depth == 0 {
            self.early = Some(index);
        }
        if (strong || weak && self.first) && length >= min_chars
            || weak && length >= self.cfg.max_chunk_chars
        {
            self.cut(index, index + 1)
        } else if n >= self.cfg.max_chunk_chars * 2 {
            if let Some(space) = self
                .buf
                .iter()
                .rposition(|c| c.is_whitespace())
                .filter(|&space| space > min_chars)
            {
                return self.cut(space, space + 1);
            }
            self.cut(self.cfg.max_chunk_chars - 1, self.cfg.max_chunk_chars)
        } else {
            None
        }
    }
    fn cut(&mut self, last: usize, from: usize) -> Option<String> {
        if last >= self.buf.len() || from > self.buf.len() {
            return None;
        }
        let segment = normalize(&self.buf[..=last].iter().collect::<String>());
        self.buf.drain(..from);
        self.first = false;
        self.early = None;
        if segment.is_empty() {
            None
        } else {
            Some(segment)
        }
    }
    fn is_decimal_point(&self, index: usize) -> bool {
        index > 0
            && index + 1 < self.buf.len()
            && self.buf[index] == '.'
            && self.buf[index - 1].is_number_decimal_digit()
            && self.buf[index + 1].is_number_decimal_digit()
    }
    fn is_abbreviation(&self, index: usize) -> bool {
        let mut start = index;
        while start > 0 && self.buf[start - 1].is_letter() {
            start -= 1;
        }
        let word = &self.buf[start..index];
        !word.is_empty() && word.len() <= 3 && word[0].is_letter_uppercase()
    }
}

/// Remove Markdown decoration without removing spoken words.
pub fn normalize(text: &str) -> String {
    let chars: Vec<char> = text.chars().collect();
    let mut out = String::with_capacity(text.len());
    let mut index = 0;
    while index < chars.len() {
        let c = chars[index];
        index += 1;
        match c {
            '*' | '_' | '`' | '#' | '~' => continue,
            '\n' | '\r' | '\t' => {
                out.push(' ');
                continue;
            }
            _ => (),
        }
        if (c == '-' || c == '+')
            && chars.get(index) == Some(&' ')
            && is_line_start(&chars, index - 1)
        {
            index += 1;
            continue;
        }
        out.push(c);
    }
    out.split_whitespace().collect::<Vec<_>>().join(" ")
}
fn is_line_start(chars: &[char], index: usize) -> bool {
    for &c in chars[..index].iter().rev() {
        if c == '\n' {
            return true;
        }
        if !c.is_whitespace() {
            return false;
        }
    }
    true
}
pub fn visible_length(text: &str) -> usize {
    text.chars().count()
}
pub fn truncate_runes(text: &str, n: usize) -> String {
    text.chars().take(n).collect()
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn full_width_terminators_emit_without_lookahead() {
        let mut s = Segmenter::new(SegmentConfig {
            first_chunk_chars: 3,
            ..Default::default()
        });
        assert_eq!(s.push("好的，"), ["好的，"]);
    }
    #[test]
    fn deadline_can_use_short_early_clause_without_cutting_a_word() {
        let mut s = Segmenter::new(SegmentConfig::default());
        assert!(s.push("好的，我听到你说帮").is_empty());
        assert!(s.has_early_boundary());
        assert_eq!(s.flush_first_at_boundary(), "好的，");
        assert_eq!(s.pending(), "我听到你说帮");
    }
    #[test]
    fn numbers_and_titles_do_not_split() {
        let mut s = Segmenter::new(SegmentConfig {
            first_chunk_chars: 2,
            min_chunk_chars: 2,
            max_chunk_chars: 120,
        });
        let mut segments = s.push("Dr. Chen paid 3.5 yuan. Next.");
        segments.push(s.flush());
        assert_eq!(segments, ["Dr. Chen paid 3.5 yuan.", "Next."]);
    }
    #[test]
    fn incremental_deltas_produce_the_same_segments() {
        let text = "您好，您可以说一下具体情况吗？我会帮您看看。（这一段不会在括号中间，截断。）最后一句话。";
        let mut one = Segmenter::new(SegmentConfig::default());
        let mut all = one.push(text);
        all.push(one.flush());
        let mut stream = Segmenter::new(SegmentConfig::default());
        let mut pieces = Vec::new();
        for c in text.chars() {
            pieces.extend(stream.push(&c.to_string()));
        }
        pieces.push(stream.flush());
        assert_eq!(all, pieces);
        assert_eq!(all.join(""), text);
    }
    #[test]
    fn markup_is_removed_but_words_and_arithmetic_remain() {
        assert_eq!(
            normalize("## Hello\n- **world**\n+ `voice`\n3 - 2"),
            "Hello world voice 3 - 2"
        );
        assert_eq!(visible_length("你好"), 2);
    }
    #[test]
    fn unpunctuated_input_is_bounded() {
        let mut s = Segmenter::new(SegmentConfig {
            first_chunk_chars: 4,
            min_chunk_chars: 8,
            max_chunk_chars: 16,
        });
        let text = "字".repeat(100);
        let mut emitted = s.push(&text);
        emitted.push(s.flush());
        assert!(emitted.iter().all(|x| x.chars().count() <= 32));
        assert_eq!(emitted.join(""), text);
    }
}
