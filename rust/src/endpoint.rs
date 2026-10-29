//! Transcript endpoint classification ported from internal/endpoint/endpoint.go.
use unicode_categories::UnicodeCategories;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Class {
    Neutral,
    Complete,
    Incomplete,
}

pub fn normalize(text: &str) -> String {
    text.to_lowercase()
        .chars()
        .filter(|c| !c.is_whitespace() && !c.is_punctuation() && !c.is_symbol())
        .collect()
}

pub fn hesitation(text: &str) -> bool {
    let text = normalize(text);
    !text.is_empty()
        && (HESITATION_WORDS.contains(&text.as_str())
            || text.chars().all(|c| matches!(c, '呃' | '额' | '唔')))
}

pub fn classify(text: &str) -> Class {
    let text = text.trim();
    match text.chars().last() {
        None => return Class::Neutral,
        Some('。' | '？' | '！' | '?' | '!') => return Class::Complete,
        Some('，' | ',' | '、' | '；' | ';' | '：' | ':' | '—' | '…') => {
            return Class::Incomplete;
        }
        _ => (),
    }
    let norm = normalize(text);
    if norm.is_empty() || MURMURS.contains(&norm.as_str()) {
        return Class::Neutral;
    }
    if CLOSED_REPLIES.contains(&norm.as_str()) {
        return Class::Complete;
    }
    if INCOMPLETE_WHOLE.contains(&norm.as_str()) {
        return Class::Incomplete;
    }
    if COMPLETE_SUFFIXES
        .iter()
        .any(|suffix| norm.ends_with(suffix))
    {
        return Class::Complete;
    }
    if INCOMPLETE_SUFFIXES
        .iter()
        .any(|suffix| norm.ends_with(suffix))
    {
        return Class::Incomplete;
    }
    let lower = text.to_lowercase();
    let last = lower
        .split(|c: char| c.is_whitespace() || c.is_punctuation())
        .rfind(|w| !w.is_empty())
        .unwrap_or("");
    if !last.is_empty()
        && last.chars().all(|c| c.is_ascii_alphabetic() || c == '\'')
        && INCOMPLETE_WORDS.contains(&last.replace('\'', "").as_str())
    {
        return Class::Incomplete;
    }
    Class::Neutral
}

const CLOSED_REPLIES: &[&str] = &[
    "好",
    "好的",
    "好吧",
    "好啊",
    "行",
    "行吧",
    "可以",
    "可以的",
    "对",
    "对的",
    "对对",
    "对对对",
    "是",
    "是的",
    "是啊",
    "没错",
    "不是",
    "不对",
    "不行",
    "不用",
    "不用了",
    "不要",
    "不需要",
    "没有",
    "没了",
    "没问题",
    "没事",
    "有",
    "有的",
    "要",
    "要的",
    "需要",
    "知道了",
    "明白了",
    "明白",
    "懂了",
    "谢谢",
    "谢谢你",
    "多谢",
    "再见",
    "拜拜",
    "你好",
    "您好",
    "喂",
    "算了",
    "yes",
    "yeah",
    "yep",
    "no",
    "nope",
    "ok",
    "okay",
    "sure",
    "right",
    "correct",
    "thanks",
    "thankyou",
    "bye",
    "goodbye",
    "hello",
    "hi",
    "nevermind",
    "gotit",
    "isee",
];

const COMPLETE_SUFFIXES: &[&str] = &[
    "吗",
    "呢",
    "吧",
    "呀",
    "啦",
    "嘛",
    "么办",
    "怎么样",
    "怎么办",
    "是什么",
    "什么意思",
    "多少钱",
    "多少",
    "几点",
    "几号",
    "几个",
    "哪里",
    "哪儿",
    "哪个",
    "是谁",
    "为什么",
    "可不可以",
    "行不行",
    "好不好",
    "对不对",
    "是不是",
    "有没有",
    "能不能",
    "要不要",
    "谢谢",
    "谢谢你",
    "再见",
    "就这样",
    "就这些",
    "没有了",
    "好了",
    "可以了",
    "行了",
    "please",
    "thanks",
    "thankyou",
];

const INCOMPLETE_SUFFIXES: &[&str] = &[
    "然后",
    "然后就",
    "但是",
    "可是",
    "不过",
    "因为",
    "所以",
    "如果",
    "要是",
    "假如",
    "虽然",
    "而且",
    "并且",
    "或者",
    "还是说",
    "还有",
    "以及",
    "另外",
    "比如",
    "比如说",
    "就是说",
    "也就是",
    "那就是",
    "就是",
    "还有就是",
    "的话",
    "的时候",
    "一下那个",
    "就是那个",
    "跟",
    "与",
    "把",
    "被",
    "对于",
    "关于",
    "除了",
    "我想",
    "我想要",
    "我想问",
    "我想问一下",
    "我想说",
    "我觉得",
    "我是说",
    "帮我",
    "请问",
    "想问一下",
    "问一下",
    "问你一下",
    "问您一下",
    "想问你",
    "想问您",
    "问一下啊",
    "问你一下啊",
    "问您一下啊",
    "问一下哈",
    "问你一下哈",
    "请教一下",
    "咨询一下",
    "呃",
    "额",
    "唔",
];

const INCOMPLETE_WHOLE: &[&str] = &[
    "那个",
    "这个",
    "我要",
    "麻烦",
    "麻烦你",
    "那",
    "还有",
    "然后",
];

const MURMURS: &[&str] = &[
    "嗯",
    "嗯嗯",
    "嗯嗯嗯",
    "哦",
    "噢",
    "啊",
    "mm",
    "mhm",
    "uhhuh",
];

const INCOMPLETE_WORDS: &[&str] = &[
    "and", "but", "so", "because", "or", "if", "when", "that", "which", "the", "a", "an", "to",
    "of", "for", "with", "my", "your", "i", "um", "uh", "er", "like", "then", "also", "about",
    "is", "are", "was", "can", "could", "would", "should", "want", "need", "its",
];

const HESITATION_WORDS: &[&str] = &["um", "uh", "er", "erm", "uhm", "umm", "uhh"];

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn go_classifier_cases() {
        for (text, want) in [
            ("", Class::Neutral),
            ("   ", Class::Neutral),
            ("明天上海的天气", Class::Neutral),
            ("the weather in Shanghai tomorrow", Class::Neutral),
            ("明天会下雨吗", Class::Complete),
            ("这个多少钱", Class::Complete),
            ("你觉得怎么样", Class::Complete),
            ("好的", Class::Complete),
            ("不用了", Class::Complete),
            ("谢谢", Class::Complete),
            ("那就这样", Class::Complete),
            ("然后呢", Class::Complete),
            ("okay", Class::Complete),
            ("Thank you", Class::Complete),
            ("yes please", Class::Complete),
            ("帮我查一下明天的天气。", Class::Complete),
            ("what time is it?", Class::Complete),
            ("帮我查一下，", Class::Incomplete),
            ("I was wondering,", Class::Incomplete),
            ("我想问一下那个", Class::Incomplete),
            ("诶，我想问你一下啊", Class::Incomplete),
            ("我想问您一下", Class::Incomplete),
            ("就是", Class::Incomplete),
            ("帮我查一下明天的天气然后", Class::Incomplete),
            ("我昨天买的那个保险但是", Class::Incomplete),
            ("请问", Class::Incomplete),
            ("这个保单的话", Class::Incomplete),
            ("I want to book a flight to", Class::Incomplete),
            ("it was on Tuesday and", Class::Incomplete),
            ("um", Class::Incomplete),
            ("那个", Class::Incomplete),
            ("我要这个", Class::Neutral),
            ("我要一个", Class::Neutral),
            ("这是我的", Class::Neutral),
            ("嗯", Class::Neutral),
            ("I think so also", Class::Incomplete),
            ("that is the pianist", Class::Neutral),
            ("we went to Oslo", Class::Neutral),
        ] {
            assert_eq!(classify(text), want, "{text:?}");
        }
    }
    #[test]
    fn hesitation_excludes_backchannel_answers() {
        for text in ["呃", "呃。", "额额", "呃……", "Um.", "uh"] {
            assert!(hesitation(text), "{text:?}");
        }
        for text in ["嗯", "啊", "呃，多少钱", "好的", "", "。", "umbrella"] {
            assert!(!hesitation(text), "{text:?}");
        }
    }
    #[test]
    fn normalization_removes_unicode_symbols_and_punctuation() {
        assert_eq!(normalize(" 嗯，✦Ａ！ Aa. + "), "嗯ａaa");
    }
}
